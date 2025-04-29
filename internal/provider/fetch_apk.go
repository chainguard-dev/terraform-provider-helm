/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package provider

import (
	"context"
	"fmt"
	"io"
	"os"

	"chainguard.dev/apko/pkg/apk/apk"
	apkfs "chainguard.dev/apko/pkg/apk/fs"
	"chainguard.dev/apko/pkg/build"
	apkotypes "chainguard.dev/apko/pkg/build/types"
	"github.com/hashicorp/terraform-plugin-framework/diag"
)

// fetchAPKPackage fetches a package from an APK repository and returns the path to a temporary APK file.
func fetchAPKPackage(
	ctx context.Context,
	packageName string,
	packageVersion *string,
	arch string,
	packageRepo string,
	packageKeys []string,
	_ *diag.Diagnostics,
) (string, func(), error) {
	// Create a temporary directory for the build context
	tempDir, err := os.MkdirTemp("", "apko-build-*")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp directory: %w", err)
	}

	cleanup := func() {
		os.RemoveAll(tempDir)
	}

	// Create a minimal image configuration for the build context
	ic := apkotypes.ImageConfiguration{
		Contents: apkotypes.ImageContents{
			RuntimeRepositories: []string{
				packageRepo,
			},
		},
		Archs: []apkotypes.Architecture{apkotypes.ParseArchitecture(arch)},
	}

	// Add package with version constraint if specified, otherwise use just the package name
	if packageVersion != nil {
		ic.Contents.Packages = []string{
			packageName + "=" + *packageVersion,
		}
	} else {
		ic.Contents.Packages = []string{
			packageName,
		}
	}

	// Create the build context with the desired architecture
	fs := apkfs.DirFS(tempDir)

	// Create build context
	bc, err := build.New(ctx, fs,
		build.WithArch(apkotypes.ParseArchitecture(arch)),
		build.WithImageConfiguration(ic),
		build.WithTempDir(tempDir),
		build.WithExtraKeys(packageKeys),
	)
	if err != nil {
		return "", cleanup, fmt.Errorf("failed to create build context: %w", err)
	}

	// Resolve dependencies
	pkgs, conflicts, err := bc.APK().ResolveWorld(ctx)
	if err != nil {
		return "", cleanup, fmt.Errorf("failed to resolve package: %w", err)
	}

	if len(conflicts) > 0 {
		return "", cleanup, fmt.Errorf("package conflicts detected: %v", conflicts)
	}

	// Find the target package - ResolveWorld returns exactly one version per package
	var targetPkg *apk.RepositoryPackage
	for _, pkg := range pkgs {
		if pkg.Name == packageName {
			targetPkg = pkg
			break
		}
	}

	if targetPkg == nil {
		// Construct appropriate error message based on whether version was specified
		if packageVersion != nil {
			return "", cleanup, fmt.Errorf("package %s with version constraint %s not found in repository for arch %s",
				packageName, *packageVersion, arch)
		} else {
			return "", cleanup, fmt.Errorf("package %s not found in repository for arch %s",
				packageName, arch)
		}
	}

	// Download the package
	rc, err := bc.APK().FetchPackage(ctx, targetPkg)
	if err != nil {
		return "", cleanup, fmt.Errorf("failed to download package: %w", err)
	}
	defer rc.Close()

	// Create a temporary file for the APK
	tf, err := os.CreateTemp("", "apk-*.apk")
	if err != nil {
		return "", cleanup, fmt.Errorf("failed to create temporary file: %w", err)
	}
	tempApkFile := tf.Name()

	// Copy the package content to the temporary file
	_, err = io.Copy(tf, rc)
	if err != nil {
		tf.Close()
		os.Remove(tempApkFile)
		return "", cleanup, fmt.Errorf("failed to save package to temporary file: %w", err)
	}
	tf.Close()

	return tempApkFile, cleanup, nil
}
