/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package provider

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/stretchr/testify/assert"
)

func TestFetchAPKPackage(t *testing.T) {
	// Setup common test paths
	repoPath, err := filepath.Abs("testdata/local-apk-repo/packages")
	assert.NoError(t, err)
	
	keyPath, err := filepath.Abs("testdata/local-apk-repo/local-melange.rsa.pub")
	assert.NoError(t, err)
	
	tests := []struct {
		name           string
		packageName    string
		packageVersion *string
		arch           string
		expectError    bool
		errorContains  string
	}{
		{
			name:           "fetch package with specific version",
			packageName:    "istio-charts-base",
			packageVersion: stringPtr("1.20.3-r0"),
			arch:           "aarch64",
			expectError:    false,
		},
		{
			name:           "fetch latest package version",
			packageName:    "istio-charts-base",
			packageVersion: nil, // No specific version - should fetch latest
			arch:           "aarch64",
			expectError:    false,
		},
		{
			name:           "fetch non-existent package",
			packageName:    "non-existent-package",
			packageVersion: nil,
			arch:           "aarch64",
			expectError:    true,
			errorContains:  "nothing provides",
		},
		{
			name:           "fetch package with non-existent version",
			packageName:    "istio-charts-base",
			packageVersion: stringPtr("9999.9.9-r0"),
			arch:           "aarch64",
			expectError:    true,
			errorContains:  "does not satisfy",
		},
		{
			name:           "fetch package with x86_64 architecture",
			packageName:    "istio-charts-base",
			packageVersion: stringPtr("1.20.3-r0"),
			arch:           "x86_64",
			expectError:    false,
		},
	}
	
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			diags := diag.Diagnostics{}
			
			// Fetch the package
			tempApkFile, cleanup, err := fetchAPKPackage(
				ctx,
				tc.packageName,
				tc.packageVersion,
				tc.arch,
				repoPath,
				[]string{keyPath},
				&diags,
			)
			
			// Ensure cleanup happens
			defer func() {
				if cleanup != nil {
					cleanup()
				}
			}()
			
			if tc.expectError {
				assert.Error(t, err)
				if tc.errorContains != "" {
					assert.Contains(t, err.Error(), tc.errorContains)
				}
			} else {
				assert.NoError(t, err)
				assert.NotEmpty(t, tempApkFile)
				assert.FileExists(t, tempApkFile)
			}
		})
	}
}

// Helper function to create string pointers
func stringPtr(s string) *string {
	return &s
}