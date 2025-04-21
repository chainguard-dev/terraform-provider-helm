/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package oci

import (
	"fmt"

	"github.com/chainguard-dev/terraform-oci-helm/internal/pkg/image"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// Client defines the interface for OCI operations
type Client interface {
	Push(repository, chartName, chartVersion string, img *image.ChartImage) (string, error)
}

// DefaultClient is the default implementation of Client
type DefaultClient struct{}

// NewClient creates a new default OCI client
func NewClient() Client {
	return &DefaultClient{}
}

// Push pushes the chart to the OCI registry using go-containerregistry
func (c *DefaultClient) Push(repository, chartName, chartVersion string, img *image.ChartImage) (string, error) {
	// Create the reference for the image without a tag
	// Use just the repository which already includes the registry
	ref, err := name.ParseReference(repository)
	if err != nil {
		return "", fmt.Errorf("failed to parse reference: %w", err)
	}

	// Push the image to the registry without a tag (will use digest only)
	if err := remote.Write(ref, img); err != nil {
		return "", fmt.Errorf("failed to push to registry: %w", err)
	}

	// Get the digest of the image
	digest, err := img.Digest()
	if err != nil {
		return "", fmt.Errorf("failed to compute digest: %w", err)
	}

	return digest.String(), nil
}