package chart

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"chainguard.dev/apko/pkg/apk/apk"
	"chainguard.dev/apko/pkg/apk/expandapk"
	"chainguard.dev/apko/pkg/build"
	apkotypes "chainguard.dev/apko/pkg/build/types"
	"chainguard.dev/apko/pkg/tarfs"
	jsonpatch "github.com/evanphx/json-patch/v5"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	helmchart "helm.sh/helm/v3/pkg/chart"
	helmregistry "helm.sh/helm/v3/pkg/registry"
	"sigs.k8s.io/yaml"
)

type BuildConfig struct {
	Version            string
	Keys               []string
	RuntimeRepos       []string
	Arch               string
	JSONRFC6902Patches map[string]jsonpatch.Patch
}

func Build(ctx context.Context, name string, config *BuildConfig) (Chart, error) {
	dr, metadata, err := config.fetch(ctx, name)
	if err != nil {
		return nil, err
	}

	chartl, err := chartify(metadata, dr, config.JSONRFC6902Patches)
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
// It returns a new layer.
func chartify(metadata *helmchart.Metadata, r io.Reader, patches map[string]jsonpatch.Patch) (v1.Layer, error) {
	gr, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	// create a new tar writer in mem, we never really expect a chart to be large
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("error reading tar: %w", err)
		}

		// if the file is rooted in /<chart-name>, copy it to the new layer in /
		if !strings.HasPrefix(hdr.Name, metadata.Name+"/") {
			continue
		}

		rel, err := filepath.Rel(metadata.Name, hdr.Name)
		if err != nil {
			return nil, fmt.Errorf("error getting relative path: %w", err)
		}

		if p, ok := patches[rel]; ok {
			// apply the patches before copying the file, unfortunately we have to
			// buffer the whole file
			raw, err := io.ReadAll(tr)
			if err != nil {
				return nil, fmt.Errorf("error reading file: %w", err)
			}

			var patched []byte
			if strings.HasSuffix(rel, ".yaml") || strings.HasSuffix(rel, ".yml") {
				jsonBytes, err := yaml.YAMLToJSON(raw)
				if err != nil {
					return nil, fmt.Errorf("error converting YAML to JSON: %w", err)
				}

				patchedJSON, err := p.Apply(jsonBytes)
				if err != nil {
					return nil, fmt.Errorf("error applying patch to JSON: %w", err)
				}

				patched, err = yaml.JSONToYAML(patchedJSON)
				if err != nil {
					return nil, fmt.Errorf("error converting JSON to YAML: %w", err)
				}
			} else {
				// For non-YAML files, apply the patch directly
				patched, err = p.Apply(raw)
				if err != nil {
					return nil, fmt.Errorf("error applying patch: %w", err)
				}
			}

			hdr.Size = int64(len(patched))
			if err := tw.WriteHeader(hdr); err != nil {
				return nil, fmt.Errorf("error writing header for patched file: %w", err)
			}

			if _, err := io.CopyN(tw, bytes.NewReader(patched), hdr.Size); err != nil {
				return nil, fmt.Errorf("error copying patched file: %w", err)
			}
		} else {
			// Simnply copy the file as-is
			if err := tw.WriteHeader(hdr); err != nil {
				return nil, fmt.Errorf("error writing header: %w", err)
			}

			if _, err := io.CopyN(tw, tr, hdr.Size); err != nil {
				return nil, fmt.Errorf("error copying file: %w", err)
			}
		}
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("error closing tar: %w", err)
	}

	l, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
	}, tarball.WithMediaType(helmregistry.ChartLayerMediaType))
	return l, err
}

// fetch will find the chart and return a reader for the APK (the data section), along with its parsed Chart.yaml.
func (c *BuildConfig) fetch(ctx context.Context, name string) (io.Reader, *helmchart.Metadata, error) {
	bc, err := c.bc(ctx, name)
	if err != nil {
		return nil, nil, err
	}

	pkgs, conflicts, err := bc.APK().ResolveWorld(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to resolve package: %w for arch %q", err, c.Arch)
	}

	if len(conflicts) > 0 {
		return nil, nil, fmt.Errorf("package conflicts detected: %v", conflicts)
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
		return nil, nil, fmt.Errorf("failed to download package: %w", err)
	}
	defer rc.Close()

	parts, err := expandapk.Split(rc)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to split APK: %w", err)
	}

	datar := parts[len(parts)-1]

	var databuf bytes.Buffer
	if _, err := io.Copy(&databuf, datar); err != nil {
		return nil, nil, fmt.Errorf("failed to buffer data section: %w", err)
	}

	metadatar := bytes.NewReader(databuf.Bytes())

	// crack it open and find the Chart.yaml
	gr, err := gzip.NewReader(metadatar)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	var metadata *helmchart.Metadata
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("error reading tar: %w", err)
		}

		if !strings.HasSuffix(hdr.Name, "/Chart.yaml") {
			continue
		}

		raw, err := io.ReadAll(tr)
		if err != nil {
			return nil, nil, fmt.Errorf("error reading Chart.yaml: %w", err)
		}

		if err := yaml.Unmarshal(raw, &metadata); err != nil {
			return nil, nil, fmt.Errorf("error marshalling Chart.yaml: %w", err)
		}

		break
	}

	return bytes.NewReader(databuf.Bytes()), metadata, nil
}

func (c *BuildConfig) bc(ctx context.Context, name string) (*build.Context, error) {
	if c.Arch == "" {
		c.Arch = DefaultArch
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
		ic.Contents.RuntimeRepositories = c.RuntimeRepos
	}

	opts := []build.Option{
		build.WithArch(apkotypes.ParseArchitecture(c.Arch)),
		build.WithImageConfiguration(ic),
	}

	return build.New(ctx, tarfs.New(), opts...)
}
