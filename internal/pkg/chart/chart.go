package chart

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/partial"
	"github.com/google/go-containerregistry/pkg/v1/static"
	ggcrtypes "github.com/google/go-containerregistry/pkg/v1/types"
	helmchart "helm.sh/helm/v3/pkg/chart"
	helmregistry "helm.sh/helm/v3/pkg/registry"
)

// Chart defines a compatbile Helm OCI artifact.
type Chart interface {
	v1.Image
	Metadata() (*helmchart.Metadata, error)
}

type chart struct {
	metadata *helmchart.Metadata
	content  v1.Layer

	diffIDs   map[v1.Hash]v1.Layer
	digestIDs map[v1.Hash]v1.Layer
}

func (c *chart) ConfigFile() (*v1.ConfigFile, error) {
	return partial.ConfigFile(c)
}

func (c *chart) ConfigName() (v1.Hash, error) {
	return partial.ConfigName(c)
}

func (c *chart) Digest() (v1.Hash, error) {
	return partial.Digest(c)
}

// TODO: This isn't actually implemented, but I don't think it needs to be?
func (c *chart) LayerByDiffID(hash v1.Hash) (v1.Layer, error) {
	if l, ok := c.diffIDs[hash]; ok {
		return l, nil
	}
	return nil, fmt.Errorf("layer with diff ID %v not found", hash)
}

// TODO: This isn't actually implemented, but I don't think it needs to be?
func (c *chart) LayerByDigest(hash v1.Hash) (v1.Layer, error) {
	if l, ok := c.digestIDs[hash]; ok {
		return l, nil
	}
	return nil, fmt.Errorf("layer with digest %v not found", hash)
}

func (c *chart) Layers() ([]v1.Layer, error) {
	return []v1.Layer{c.content}, nil
}

func (c *chart) MediaType() (ggcrtypes.MediaType, error) {
	return ggcrtypes.OCIManifestSchema1, nil
}

func (c *chart) RawManifest() ([]byte, error) {
	return partial.RawManifest(c)
}

func (c *chart) Size() (int64, error) {
	return partial.Size(c)
}

func (c *chart) Manifest() (*v1.Manifest, error) {
	cfgDesc, err := partial.Descriptor(c.config())
	if err != nil {
		return nil, err
	}

	contentDesc, err := partial.Descriptor(c.content)
	if err != nil {
		return nil, err
	}

	m := &v1.Manifest{
		SchemaVersion: 2,
		MediaType:     ggcrtypes.OCIManifestSchema1,
		Config:        *cfgDesc,
		Layers:        []v1.Descriptor{*contentDesc},
		Annotations: map[string]string{
			"org.opencontainers.image.title":       c.metadata.Name,
			"org.opencontainers.image.version":     c.metadata.Version,
			"org.opencontainers.image.description": c.metadata.Description,
		},
	}

	if len(c.metadata.Sources) > 0 {
		m.Annotations["org.opencontainers.image.source"] = strings.Join(c.metadata.Sources, ",")
	}

	maps.Copy(m.Annotations, c.metadata.Annotations)

	return m, nil
}

func (c *chart) RawConfigFile() ([]byte, error) {
	return json.Marshal(c.metadata)
}

func (c *chart) Metadata() (*helmchart.Metadata, error) {
	return c.metadata, nil
}

func (c *chart) config() v1.Layer {
	raw, err := json.Marshal(c.metadata)
	if err != nil {
		raw = []byte("{}")
	}
	return static.NewLayer(raw, helmregistry.ConfigMediaType)
}
