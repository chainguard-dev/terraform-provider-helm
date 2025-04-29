/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package oci

import (
	"fmt"

	"github.com/chainguard-dev/terraform-oci-helm/internal/pkg/image"
)

// MockClient implements Client interface for testing.
type MockClient struct{}

// NewMockClient creates a new mock client for testing.
func NewMockClient() Client {
	return &MockClient{}
}

// Push implements the Push method of the Client interface with test behavior.
func (m *MockClient) Push(repository, chartName, chartVersion string, img *image.ChartImage) (string, error) {
	// Generate a deterministic mock digest based on inputs
	digest := fmt.Sprintf("sha256:%s-%s-%s-mock", repository, chartName, chartVersion)
	return digest, nil
}
