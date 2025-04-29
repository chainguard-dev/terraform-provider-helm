package image

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	"gopkg.in/yaml.v2"
	"helm.sh/helm/v3/pkg/chart"
)

// Helm OCI media types.
const (
	// ConfigMediaType is the media type for Helm chart config.
	ConfigMediaType = "application/vnd.cncf.helm.config.v1+json"
	// ChartLayerMediaType is the media type for Helm chart content.
	ChartLayerMediaType = "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
	// OCIManifestMediaType is the OCI manifest media type for Helm charts.
	OCIManifestMediaType = "application/vnd.oci.image.manifest.v1+json"
)

// ChartImage implements v1.Image by forwarding all calls to an inner implementation.
// This is useful for cases where you want to wrap an existing image implementation
// and possibly intercept or modify some of its behaviors.
type ChartImage struct {
	manifest v1.Manifest
	layer    v1.Layer
	config   v1.Layer

	byDiffIDs map[v1.Hash]v1.Layer
	byDigests map[v1.Hash]v1.Layer
}

// NewChartImage creates a new image wrapper around the provided inner image with the specified config.
// The config must not be nil or empty.
func NewChartImage(layer v1.Layer, config []byte) *ChartImage {
	// Create config layer using static.NewLayer
	configLayer := static.NewLayer(config, ConfigMediaType)

	// Create maps for layer lookups
	byDiffIDs := make(map[v1.Hash]v1.Layer)
	byDigests := make(map[v1.Hash]v1.Layer)

	// Add chart layer
	layerDigest, _ := layer.Digest()
	layerDiffID, _ := layer.DiffID()
	byDigests[layerDigest] = layer
	byDiffIDs[layerDiffID] = layer

	// Add config layer
	configDigest, _ := configLayer.Digest()
	configDiffID, _ := configLayer.DiffID()
	byDigests[configDigest] = configLayer
	byDiffIDs[configDiffID] = configLayer

	// Parse chart metadata from config to extract annotations
	var chartMeta chart.Metadata
	if err := json.Unmarshal(config, &chartMeta); err != nil {
		// If we can't parse the chart metadata, continue with empty annotations
		chartMeta = chart.Metadata{}
	}

	// Create manifest annotations from chart metadata
	annotations := make(map[string]string)

	// Add standard OCI annotations
	if chartMeta.Name != "" {
		annotations["org.opencontainers.image.title"] = chartMeta.Name
	}
	if chartMeta.Version != "" {
		annotations["org.opencontainers.image.version"] = chartMeta.Version
	}
	if chartMeta.Description != "" {
		annotations["org.opencontainers.image.description"] = chartMeta.Description
	}
	if len(chartMeta.Sources) > 0 && chartMeta.Sources[0] != "" {
		annotations["org.opencontainers.image.source"] = chartMeta.Sources[0]
	}

	// Add any custom annotations from the chart
	if chartMeta.Annotations != nil {
		for k, v := range chartMeta.Annotations {
			if _, exists := annotations[k]; !exists {
				annotations[k] = v
			}
		}
	}

	// Construct the manifest
	configSize, _ := configLayer.Size()
	chartSize, _ := layer.Size()
	manifest := v1.Manifest{
		SchemaVersion: 2,
		MediaType:     OCIManifestMediaType,
		Config: v1.Descriptor{
			MediaType: ConfigMediaType,
			Size:      configSize,
			Digest:    configDigest,
		},
		Layers: []v1.Descriptor{
			{
				MediaType: ChartLayerMediaType,
				Size:      chartSize,
				Digest:    layerDigest,
			},
		},
		Annotations: annotations,
	}

	return &ChartImage{
		manifest:  manifest,
		layer:     layer,
		config:    configLayer,
		byDiffIDs: byDiffIDs,
		byDigests: byDigests,
	}
}

// Layers returns the ordered list of filesystem layers that comprise this image.
// The order of the list is oldest/base layer first, newest/top layer last.
func (w *ChartImage) Layers() ([]v1.Layer, error) {
	return []v1.Layer{w.layer}, nil
}

// / MANIFEST STUFFS
// MediaType of this image's manifest.
// Always returns the OCI manifest media type for Helm charts.
func (w *ChartImage) MediaType() (types.MediaType, error) {
	return OCIManifestMediaType, nil
}

// Size returns the size of the manifest.
func (w *ChartImage) Size() (int64, error) {
	rawManifest, err := json.Marshal(w.manifest)
	if err != nil {
		return 0, err
	}
	return int64(len(rawManifest)), nil
}

// Digest returns the sha256 of this image's manifest.
func (w *ChartImage) Digest() (v1.Hash, error) {
	rawManifest, err := json.Marshal(w.manifest)
	if err != nil {
		return v1.Hash{}, err
	}
	h, _, err := v1.SHA256(bytes.NewReader(rawManifest))
	return h, err
}

// Manifest returns this image's Manifest object.
func (w *ChartImage) Manifest() (*v1.Manifest, error) {
	return &w.manifest, nil
}

// RawManifest returns the serialized bytes of Manifest().
func (w *ChartImage) RawManifest() ([]byte, error) {
	return json.Marshal(w.manifest)
}

// CONFIG STUFFS
// ConfigName returns the hash of the image's config file.
func (w *ChartImage) ConfigName() (v1.Hash, error) {
	return w.config.Digest()
}

// ConfigFile returns this image's config file.
// For Helm charts, we return an empty ConfigFile as Helm charts don't use this.
func (w *ChartImage) ConfigFile() (*v1.ConfigFile, error) {
	return &v1.ConfigFile{}, nil
}

// RawConfigFile returns the serialized bytes of ConfigFile().
// Always returns the raw config data.
func (w *ChartImage) RawConfigFile() ([]byte, error) {
	rc, err := w.config.Uncompressed()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

// LayerByDigest returns a Layer for interacting with a particular layer of
// the image, looking it up by "digest" (the compressed hash).
func (w *ChartImage) LayerByDigest(hash v1.Hash) (v1.Layer, error) {
	if layer, ok := w.byDigests[hash]; ok {
		return layer, nil
	}
	return nil, fmt.Errorf("layer with digest %v not found", hash)
}

// LayerByDiffID returns a Layer for interacting with a particular layer of
// the image, looking it up by "diff id" (the uncompressed hash).
func (w *ChartImage) LayerByDiffID(diffID v1.Hash) (v1.Layer, error) {
	if layer, ok := w.byDiffIDs[diffID]; ok {
		return layer, nil
	}
	return nil, fmt.Errorf("layer with diff ID %v not found", diffID)
}

// ChartYAMLToConfig converts Helm Chart.yaml content to a JSON config.
// This function should be called with the raw content of a Chart.yaml file.
func ChartYAMLToConfig(chartYAML []byte) ([]byte, error) {
	// Parse the YAML using the Helm chart metadata structure
	metadata := &chart.Metadata{}
	if err := yaml.Unmarshal(chartYAML, metadata); err != nil {
		return nil, fmt.Errorf("failed to parse Chart.yaml: %w", err)
	}

	// Convert the parsed metadata to JSON
	jsonData, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to convert Chart.yaml to JSON: %w", err)
	}

	return jsonData, nil
}
