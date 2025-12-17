package chart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"strings"

	"chainguard.dev/apko/pkg/apk/apk"
	"chainguard.dev/apko/pkg/apk/expandapk"
	"chainguard.dev/apko/pkg/build"
	apkotypes "chainguard.dev/apko/pkg/build/types"
	"chainguard.dev/apko/pkg/tarfs"
	jsonpatch "github.com/evanphx/json-patch/v5"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	yamlpatch "github.com/palantir/pkg/yamlpatch"
	helmchart "helm.sh/helm/v3/pkg/chart"
	helmregistry "helm.sh/helm/v3/pkg/registry"
	"sigs.k8s.io/yaml"
)

type BuildConfig struct {
	Version            string
	Keys               []string
	RuntimeRepos       []string
	Arch               string
	JSONRFC6902Patches map[string][]byte
}

func Build(ctx context.Context, name string, config *BuildConfig) (Chart, error) {
	dr, chartName, err := config.fetch(ctx, name)
	if err != nil {
		return nil, err
	}

	chartl, metadata, err := chartify(chartName, dr, config.JSONRFC6902Patches)
	if err != nil {
		return nil, fmt.Errorf("failed to build chart layer: %w", err)
	}

	chart := &chart{
		metadata:  metadata,
		content:   chartl,
		diffIDs:   make(map[v1.Hash]v1.Layer),
		digestIDs: make(map[v1.Hash]v1.Layer),
	}

	return chart, nil
}

// chartify takes a standard "apko" layer and mutates it to the format required by the Helm OCI format.
// This essentially just "re-roots" the filesystem to the root where Chart.yaml is located.
// It returns a new layer and the (possibly patched) chart metadata.
func chartify(chartName string, r io.Reader, patches map[string][]byte) (v1.Layer, *helmchart.Metadata, error) {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	// create a new tar writer in mem, we never really expect a chart to be large
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	var metadata *helmchart.Metadata

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("error reading tar: %w", err)
		}

		// if the file is rooted in /<chart-name>, copy it to the new layer in /
		if !strings.HasPrefix(hdr.Name, chartName+"/") {
			continue
		}

		rel, err := filepath.Rel(chartName, hdr.Name)
		if err != nil {
			return nil, nil, fmt.Errorf("error getting relative path: %w", err)
		}

		p, needsPatch := patches[rel]

		// For files that need patching or Chart.yaml, we need to buffer the content
		if needsPatch || rel == "Chart.yaml" {
			raw, err := io.ReadAll(tr)
			if err != nil {
				return nil, nil, fmt.Errorf("error reading file: %w", err)
			}

			content := raw
			if needsPatch {
				content, err = patchedWith(rel, raw, p)
				if err != nil {
					return nil, nil, fmt.Errorf("error applying patch to file %s: %w", rel, err)
				}
			}

			if rel == "Chart.yaml" {
				if err := yaml.Unmarshal(content, &metadata); err != nil {
					return nil, nil, fmt.Errorf("error parsing Chart.yaml: %w", err)
				}
			}

			hdr.Size = int64(len(content))
			if err := tw.WriteHeader(hdr); err != nil {
				return nil, nil, fmt.Errorf("error writing header: %w", err)
			}

			if _, err := io.CopyN(tw, bytes.NewReader(content), hdr.Size); err != nil {
				return nil, nil, fmt.Errorf("error copying file: %w", err)
			}
		} else {
			// Simply copy the file as-is
			if err := tw.WriteHeader(hdr); err != nil {
				return nil, nil, fmt.Errorf("error writing header: %w", err)
			}

			if _, err := io.CopyN(tw, tr, hdr.Size); err != nil {
				return nil, nil, fmt.Errorf("error copying file: %w", err)
			}
		}
	}

	if err := tw.Close(); err != nil {
		return nil, nil, fmt.Errorf("error closing tar: %w", err)
	}

	if metadata == nil {
		return nil, nil, fmt.Errorf("could not find Chart.yaml")
	}

	l, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
	}, tarball.WithMediaType(helmregistry.ChartLayerMediaType))
	return l, metadata, err
}

func patchedWith(filename string, original []byte, patchOps []byte) ([]byte, error) {
	if strings.HasSuffix(filename, ".yaml") || strings.HasSuffix(filename, ".yml") {
		var patch yamlpatch.Patch
		if err := json.Unmarshal(patchOps, &patch); err != nil {
			return nil, fmt.Errorf("error unmarshalling JSON patch to YAML patch: %w", err)
		}

		patched, err := yamlpatch.Apply(original, patch)
		if err != nil {
			return nil, fmt.Errorf("error applying YAML patch: %w", err)
		}
		return patched, nil
	}

	// For non-YAML files, use jsonpatch directly.
	jp, err := jsonpatch.DecodePatch(patchOps)
	if err != nil {
		return nil, fmt.Errorf("error decoding JSON patch: %w", err)
	}
	patched, err := jp.Apply(original)
	if err != nil {
		return nil, fmt.Errorf("error applying JSON patch: %w", err)
	}
	return patched, nil
}

// fetch will find the chart and return a reader for the APK (the data section), along with the chart name.
func (c *BuildConfig) fetch(ctx context.Context, name string) (io.Reader, string, error) {
	bc, err := c.bc(ctx, name)
	if err != nil {
		return nil, "", err
	}

	pkgs, conflicts, err := bc.APK().ResolveWorld(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("failed to resolve package: %w for arch %q", err, c.Arch)
	}

	if len(conflicts) > 0 {
		return nil, "", fmt.Errorf("package conflicts detected: %v", conflicts)
	}

	var chartPkg *apk.RepositoryPackage
	for _, pkg := range pkgs {
		if pkg.Name == name {
			chartPkg = pkg
			break
		}
	}

	rc, err := bc.APK().FetchPackage(ctx, chartPkg)
	if err != nil {
		return nil, "", fmt.Errorf("failed to download package: %w", err)
	}
	defer rc.Close()

	parts, err := expandapk.Split(rc)
	if err != nil {
		return nil, "", fmt.Errorf("failed to split APK: %w", err)
	}

	datar := parts[len(parts)-1]

	var databuf bytes.Buffer
	if _, err := io.Copy(&databuf, datar); err != nil {
		return nil, "", fmt.Errorf("failed to buffer data section: %w", err)
	}

	// Find the chart name by looking for <name>/Chart.yaml
	gr, err := gzip.NewReader(bytes.NewReader(databuf.Bytes()))
	if err != nil {
		return nil, "", fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	var chartName string
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, "", fmt.Errorf("error reading tar: %w", err)
		}

		if !strings.HasSuffix(hdr.Name, "/Chart.yaml") {
			continue
		}

		// There may be multiple Chart.yaml files in the APK, but we only want the top-level one.
		dir := strings.TrimSuffix(hdr.Name, "/Chart.yaml")
		if !strings.Contains(dir, "/") {
			chartName = dir
			break
		}
	}

	if chartName == "" {
		return nil, "", errors.New("package is missing Chart.yaml")
	}

	return bytes.NewReader(databuf.Bytes()), chartName, nil
}

func (c *BuildConfig) bc(ctx context.Context, name string) (*build.Context, error) {
	if c.Arch == "" {
		c.Arch = apkotypes.ParseArchitecture(runtime.GOARCH).ToAPK()
	}

	ic := apkotypes.ImageConfiguration{
		Contents: apkotypes.ImageContents{
			Packages: []string{name},
		},
		Archs: []apkotypes.Architecture{apkotypes.ParseArchitecture(c.Arch)},
	}

	if c.Keys != nil {
		ic.Contents.Keyring = c.Keys
	}

	if c.RuntimeRepos != nil {
		ic.Contents.Repositories = c.RuntimeRepos
	}

	opts := []build.Option{
		build.WithArch(apkotypes.ParseArchitecture(c.Arch)),
		build.WithImageConfiguration(ic),
	}

	return build.New(ctx, tarfs.New(), opts...)
}
