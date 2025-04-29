/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package provider

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"chainguard.dev/apko/pkg/apk/expandapk"
	"github.com/chainguard-dev/terraform-oci-helm/internal/pkg/image"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"gopkg.in/yaml.v2"
	"helm.sh/helm/v3/pkg/chart"
)

// ChartBuilder manages APK file operations to build Helm charts.
type ChartBuilder struct {
	APKFilePath   string
	RepositoryDir string
	chartYaml     []byte          // Cached content of Chart.yaml
	chartDir      string          // Directory containing the chart in the APK
	metadata      *chart.Metadata // Cached chart metadata
	once          sync.Once       // For initializing chartYaml once
	chartYamlErr  error           // Error from initialization
}

// NewChartBuilder creates a new ChartBuilder.
func NewChartBuilder(apkFilePath string) (*ChartBuilder, error) {
	// Validate APK file
	fi, err := os.Stat(apkFilePath)
	if err != nil {
		return nil, fmt.Errorf("invalid APK file path: %w", err)
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("APK file path is a directory, not a file")
	}

	// Create a temporary directory for extraction
	tempDir, err := os.MkdirTemp("", "apk-extract-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}

	return &ChartBuilder{
		APKFilePath:   apkFilePath,
		RepositoryDir: tempDir,
	}, nil
}

// Cleanup removes the temporary extraction directory.
func (c *ChartBuilder) Cleanup() error {
	return os.RemoveAll(c.RepositoryDir)
}

// findChartYaml locates and reads the Chart.yaml file from the APK.
func (c *ChartBuilder) findChartYaml() error {
	// Use sync.Once to ensure we only do this initialization once
	c.once.Do(func() {
		// Open the APK file
		apkFile, err := os.Open(c.APKFilePath)
		if err != nil {
			c.chartYamlErr = fmt.Errorf("failed to open APK file: %w", err)
			return
		}
		defer apkFile.Close()

		// Create a context for the operation
		ctx := context.Background()

		// Use chainguard-dev/apko's ExpandApk function to extract the APK
		expanded, err := expandapk.ExpandApk(ctx, apkFile, c.RepositoryDir)
		if err != nil {
			c.chartYamlErr = fmt.Errorf("failed to expand APK file: %w", err)
			return
		}
		defer expanded.Close()

		// Get the package data as uncompressed tar
		packageData, err := expanded.PackageData()
		if err != nil {
			c.chartYamlErr = fmt.Errorf("failed to get package data from APK: %w", err)
			return
		}
		defer packageData.Close()

		// Create a tar reader for the package data
		tr := tar.NewReader(packageData)

		// Process each entry in the tar
		found := false
		for {
			header, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				c.chartYamlErr = fmt.Errorf("error reading tar: %w", err)
				return
			}

			// Check if this is a Chart.yaml file
			if header.Typeflag == tar.TypeReg && strings.HasSuffix(header.Name, "/Chart.yaml") {
				// Extract the chart directory from the path
				c.chartDir = strings.TrimSuffix(header.Name, "/Chart.yaml")

				// Read the Chart.yaml content
				contentBytes, err := io.ReadAll(tr)
				if err != nil {
					c.chartYamlErr = fmt.Errorf("failed to read Chart.yaml content: %w", err)
					return
				}

				// Save the Chart.yaml content for later use
				c.chartYaml = contentBytes
				found = true
				break
			}
		}

		if !found {
			c.chartYamlErr = fmt.Errorf("failed: Chart.yaml not found in APK package")
			return
		}

		// Parse the Chart.yaml content
		c.metadata = &chart.Metadata{}
		if err := yaml.Unmarshal(c.chartYaml, c.metadata); err != nil {
			c.chartYamlErr = fmt.Errorf("failed to parse Chart.yaml: %w", err)
			return
		}
	})

	return c.chartYamlErr
}

// GetChartMetadata extracts and returns the metadata from Chart.yaml.
func (c *ChartBuilder) GetChartMetadata() (*chart.Metadata, error) {
	if c.metadata == nil {
		if err := c.findChartYaml(); err != nil {
			return nil, err
		}
	}
	return c.metadata, nil
}

// buildChartLayer creates a layer containing the Helm chart files.
func (c *ChartBuilder) buildChartLayer() ([]byte, error) {
	// First, make sure we have the Chart.yaml and chart directory
	if c.metadata == nil {
		if err := c.findChartYaml(); err != nil {
			return nil, err
		}
	}

	// Open the APK file
	apkFile, err := os.Open(c.APKFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open APK file: %w", err)
	}
	defer apkFile.Close()

	// Create a context for the operation
	ctx := context.Background()

	// Use chainguard-dev/apko's ExpandApk function to extract the APK
	expanded, err := expandapk.ExpandApk(ctx, apkFile, c.RepositoryDir)
	if err != nil {
		return nil, fmt.Errorf("failed to expand APK file: %w", err)
	}
	defer expanded.Close()

	// Get the package data as uncompressed tar
	packageData, err := expanded.PackageData()
	if err != nil {
		return nil, fmt.Errorf("failed to get package data from APK: %w", err)
	}
	defer packageData.Close()

	// Create an in-memory tar file for the chart content
	var chartTarBuffer bytes.Buffer
	chartWriter := tar.NewWriter(&chartTarBuffer)

	// Create a tar reader for the package data
	tr := tar.NewReader(packageData)

	// Process each entry in the tar
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("error reading tar: %w", err)
		}

		// If it's Chart.yaml, use our cached version
		if header.Typeflag == tar.TypeReg && strings.HasSuffix(header.Name, "/Chart.yaml") {
			// Create a new header
			newHeader := *header

			// Write the header to our chart tar
			if err := chartWriter.WriteHeader(&newHeader); err != nil {
				return nil, fmt.Errorf("failed to write header: %w", err)
			}

			// Write our cached Chart.yaml content to the chart tar
			if _, err := io.Copy(chartWriter, bytes.NewReader(c.chartYaml)); err != nil {
				return nil, fmt.Errorf("failed to copy Chart.yaml content: %w", err)
			}
			continue
		}

		// If the file is in "var/", ignore it
		if header.Name == "var" || strings.HasPrefix(header.Name, "var/") {
			continue
		}

		// Create a new header with the correct name
		newHeader := *header

		// Write the header to our chart tar
		if err := chartWriter.WriteHeader(&newHeader); err != nil {
			return nil, fmt.Errorf("failed to write header: %w", err)
		}

		// If it's a regular file, copy the content
		if header.Typeflag == tar.TypeReg {
			if _, err := io.Copy(chartWriter, tr); err != nil {
				return nil, fmt.Errorf("failed to copy file content: %w", err)
			}
		}
	}

	// Close the chart writer to flush the data
	if err := chartWriter.Close(); err != nil {
		return nil, fmt.Errorf("failed to close chart writer: %w", err)
	}

	return chartTarBuffer.Bytes(), nil
}

// AsImage converts the APK file to a Helm chart image.
func (c *ChartBuilder) AsImage() (*image.ChartImage, error) {
	// Make sure we have chart metadata
	if c.metadata == nil {
		if err := c.findChartYaml(); err != nil {
			return nil, err
		}
	}

	// Build the chart layer
	tarBytes, err := c.buildChartLayer()
	if err != nil {
		return nil, fmt.Errorf("failed to build chart layer: %w", err)
	}

	// Create a layer from the chart tar buffer
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewBuffer(tarBytes)), nil
	}, tarball.WithMediaType(image.ChartLayerMediaType))
	if err != nil {
		return nil, fmt.Errorf("failed to create layer from tar: %w", err)
	}

	// Convert Chart.yaml to JSON for the config
	chartConfig, err := image.ChartYAMLToConfig(c.chartYaml)
	if err != nil {
		return nil, fmt.Errorf("failed to process Chart.yaml: %w", err)
	}

	// Create a Helm chart image directly with the chart layer and config
	img := image.NewChartImage(layer, chartConfig)

	return img, nil
}
