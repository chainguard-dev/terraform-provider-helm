/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package provider

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestChartBuilder(t *testing.T) {
	// Test cases for ChartBuilder
	tests := []struct {
		name              string
		apkFilePath       string
		expectError       bool
		errorContains     string
		expectedChartName string
		expectedChartVer  string
		validateAsImage   bool
	}{
		{
			name:              "valid aarch64 APK file",
			apkFilePath:       "testdata/local-apk-repo/packages/aarch64/istio-charts-base-1.20.3-r0.apk",
			expectError:       false,
			expectedChartName: "base",
			expectedChartVer:  "1.25.2",
			validateAsImage:   true,
		},
		{
			name:              "valid x86_64 APK file",
			apkFilePath:       "testdata/local-apk-repo/packages/x86_64/istio-charts-base-1.20.3-r0.apk",
			expectError:       false,
			expectedChartName: "base",
			expectedChartVer:  "1.25.2",
			validateAsImage:   true,
		},
		{
			name:          "non-existent APK file",
			apkFilePath:   "testdata/local-apk-repo/packages/non-existent.apk",
			expectError:   true,
			errorContains: "invalid APK file path",
		},
		{
			name:          "directory instead of APK file",
			apkFilePath:   "testdata/local-apk-repo/packages",
			expectError:   true,
			errorContains: "is a directory",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Get absolute path
			apkPath, err := filepath.Abs(tc.apkFilePath)
			if !tc.expectError {
				require.NoError(t, err, "Failed to resolve absolute path")
			} else if err != nil {
				// If we expect an error and the file doesn't exist, just use the relative path
				apkPath = tc.apkFilePath
			}

			// Create ChartBuilder
			builder, err := NewChartBuilder(apkPath)

			// Check initial error
			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
				return // Skip rest of test for error cases
			}

			// For success cases
			assert.NoError(t, err)
			assert.NotNil(t, builder)

			// Ensure cleanup happens
			defer func() {
				if builder != nil {
					assert.NoError(t, builder.Cleanup())
				}
			}()

			// Test GetChartMetadata
			metadata, err := builder.GetChartMetadata()
			assert.NoError(t, err)
			assert.NotNil(t, metadata)
			assert.Equal(t, tc.expectedChartName, metadata.Name)
			assert.Equal(t, tc.expectedChartVer, metadata.Version)

			// Test AsImage if needed
			if tc.validateAsImage {
				img, err := builder.AsImage()
				assert.NoError(t, err)
				assert.NotNil(t, img)

				// Validate the image has layers
				layers, err := img.Layers()
				assert.NoError(t, err)
				assert.Equal(t, 1, len(layers), "Image should have exactly one layer")

				// Validate the config
				config, err := img.ConfigFile()
				assert.NoError(t, err)
				assert.NotNil(t, config)

				// Just verify config exists
				assert.NotNil(t, config.Config)
			}
		})
	}
}
