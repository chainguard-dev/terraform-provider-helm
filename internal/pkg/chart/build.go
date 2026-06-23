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
	"testing/fstest"

	"chainguard.dev/apko/pkg/apk/apk"
	"chainguard.dev/apko/pkg/apk/expandapk"
	"chainguard.dev/apko/pkg/build"
	apkotypes "chainguard.dev/apko/pkg/build/types"
	"chainguard.dev/apko/pkg/tarfs"
	sdkchart "chainguard.dev/sdk/helm/chart"
	"chainguard.dev/sdk/helm/images"
	helmv1 "chainguard.dev/sdk/helm/v1"
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
	// Images maps image IDs to full OCI references. Every ID present in the
	// chart's image tree (own images and subcharts) that also appears here is
	// resolved into values.yaml; IDs absent here are left untouched.
	Images map[string]string
}

func Build(ctx context.Context, name string, config *BuildConfig) (v1.Image, *helmchart.Metadata, error) {
	cd, err := config.fetch(ctx, name)
	if err != nil {
		return nil, nil, err
	}

	chartl, metadata, err := chartify(cd, config.JSONRFC6902Patches)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build chart layer: %w", err)
	}

	base := &chart{
		metadata:  metadata,
		content:   chartl,
		diffIDs:   make(map[v1.Hash]v1.Layer),
		digestIDs: make(map[v1.Hash]v1.Layer),
	}

	if cd.ci == nil || len(config.Images) == 0 {
		return base, metadata, nil
	}

	// Resolve every image in the tree into values.yaml using the same SDK
	// patcher catalog-syncer uses, so the published chart's subchart slots are
	// patched identically to synced charts. The tree (own images plus
	// subcharts) comes from the chart's own cg.json files; Images supplies the
	// refs. The returned digest feeds only the (unused) attestation tree.
	patched, _, err := sdkchart.PatchChartImages(base, cd.ci, func(_ *helmv1.ChartImages, id string, _ *helmv1.ChartImage) (string, string, error) {
		full, ok := config.Images[id]
		if !ok {
			return "", "", sdkchart.ErrSkipImage
		}
		return full, "", nil
	})
	if err != nil {
		return nil, nil, fmt.Errorf("patching chart images: %w", err)
	}
	return patched, metadata, nil
}

// toChartImages converts the cg.json tree read from a chart into the image
// tree the SDK patcher consumes. Refs carry only the image IDs at each level —
// the caller's resolver supplies the actual references — and Template carries
// the values-path mappings parsed from each cg.json.
func toChartImages(c *sdkchart.Chart) *helmv1.ChartImages {
	ci := &helmv1.ChartImages{Template: c.Mapping}
	if c.Mapping != nil {
		ci.Refs = make(map[string]*helmv1.ChartImage, len(c.Mapping.Images))
		for id := range c.Mapping.Images {
			ci.Refs[id] = &helmv1.ChartImage{}
		}
	}
	for dep, sub := range c.Subcharts {
		if ci.Subcharts == nil {
			ci.Subcharts = make(map[string]*helmv1.ChartImages, len(c.Subcharts))
		}
		ci.Subcharts[dep] = toChartImages(sub)
	}
	return ci
}

// chartify takes a standard "apko" layer and mutates it to the format required by the Helm OCI format.
// This essentially just "re-roots" the filesystem to the root where Chart.yaml is located,
// applying any JSON/YAML patches. Image resolution into values.yaml is handled separately.
func chartify(cd *chartData, patches map[string][]byte) (v1.Layer, *helmchart.Metadata, error) {
	gr, err := gzip.NewReader(cd.data)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

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

		if !strings.HasPrefix(hdr.Name, cd.name+"/") {
			continue
		}

		rel, err := filepath.Rel(cd.name, hdr.Name)
		if err != nil {
			return nil, nil, fmt.Errorf("error getting relative path: %w", err)
		}

		p, needsPatch := patches[rel]

		if needsPatch || rel == "Chart.yaml" {
			content, err := io.ReadAll(tr)
			if err != nil {
				return nil, nil, fmt.Errorf("error reading file: %w", err)
			}

			if needsPatch {
				content, err = patchedWith(rel, content, p)
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

type chartData struct {
	name string
	// ci is the image tree read from the chart's own cg.json files (top-level
	// and per-subchart). Nil when the chart carries no cg.json.
	ci   *helmv1.ChartImages
	data *bytes.Buffer
}

// fetch fetches the chart APK and parses its metadata.
func (c *BuildConfig) fetch(ctx context.Context, name string) (*chartData, error) {
	bc, err := c.bc(ctx, name)
	if err != nil {
		return nil, err
	}

	pkgs, conflicts, err := bc.APK().ResolveWorld(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve package: %w for arch %q", err, c.Arch)
	}

	if len(conflicts) > 0 {
		return nil, fmt.Errorf("package conflicts detected: %v", conflicts)
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
		return nil, fmt.Errorf("failed to download package: %w", err)
	}
	defer rc.Close()

	parts, err := expandapk.Split(rc)
	if err != nil {
		return nil, fmt.Errorf("failed to split APK: %w", err)
	}

	datar := parts[len(parts)-1]

	var databuf bytes.Buffer
	if _, err := io.Copy(&databuf, datar); err != nil {
		return nil, fmt.Errorf("failed to buffer data section: %w", err)
	}

	gr, err := gzip.NewReader(bytes.NewReader(databuf.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("failed to create gzip reader: %w", err)
	}
	defer gr.Close()

	tr := tar.NewReader(gr)

	var chartName string
	files := fstest.MapFS{}

	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("error reading tar: %w", err)
		}

		// The top-level chart's directory (one deep) names the chart.
		if dir, ok := strings.CutSuffix(hdr.Name, "/Chart.yaml"); ok && !strings.Contains(dir, "/") {
			chartName = dir
		}

		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		content, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("error reading %s: %w", hdr.Name, err)
		}
		files[strings.TrimPrefix(hdr.Name, "./")] = &fstest.MapFile{Data: content}
	}

	if chartName == "" {
		return nil, errors.New("package is missing Chart.yaml")
	}

	// Read the chart's own cg.json tree (top-level plus every subchart) so
	// subchart image slots can be patched, not just the top-level chart's.
	// Charts without a cg.json carry no image mappings and are left as-is.
	var ci *helmv1.ChartImages
	if _, ok := files[chartName+"/"+images.ChainguardChartMetadataFilename]; ok {
		c, err := sdkchart.Read(files)
		if err != nil {
			return nil, fmt.Errorf("reading chart image tree: %w", err)
		}
		ci = toChartImages(c)
	}

	return &chartData{
		name: chartName,
		ci:   ci,
		data: &databuf,
	}, nil
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
