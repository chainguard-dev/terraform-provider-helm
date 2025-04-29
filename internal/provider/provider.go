/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package provider

import (
	"context"
	"os"

	"github.com/chainguard-dev/terraform-oci-helm/internal/pkg/oci"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ provider.Provider = &helmProvider{}
)

// New is a helper function to simplify provider server and testing implementation.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &helmProvider{
			version:   version,
			ociClient: oci.NewClient(),
		}
	}
}

// helmProvider is the provider implementation.
type helmProvider struct {
	version   string
	ociClient oci.Client
}

// Metadata returns the provider type name.
func (p *helmProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "helm"
	resp.Version = p.version
}

// Schema defines the provider-level schema for configuration data.
func (p *helmProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The Helm provider offers resources to work with OCI Helm charts and APK repositories.",
		Attributes: map[string]schema.Attribute{
			"package_repository": schema.StringAttribute{
				Description: "The URL of the package repository to use for fetching APK packages.",
				Optional:    true,
			},
			"package_repository_pub_keys": schema.ListAttribute{
				Description: "A list of paths to package repository public keys for signature verification.",
				Optional:    true,
				ElementType: types.StringType,
			},
		},
	}
}

// providerData can be used to store data from the Terraform configuration.
type providerData struct {
	PackageRepository        types.String `tfsdk:"package_repository"`
	PackageRepositoryPubKeys types.List   `tfsdk:"package_repository_pub_keys"`
}

func (p *helmProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerData
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Check environment variables for configuration
	packageRepository := os.Getenv("PACKAGE_REPOSITORY")

	// Initialize empty keys slice
	var packageRepositoryPubKeys []string

	// Check for environment variable with keys
	if envKey := os.Getenv("PACKAGE_REPOSITORY_PUB_KEY"); envKey != "" {
		packageRepositoryPubKeys = append(packageRepositoryPubKeys, envKey)
	}

	if !config.PackageRepository.IsNull() {
		packageRepository = config.PackageRepository.ValueString()
	}

	// Parse the keys from the list
	if !config.PackageRepositoryPubKeys.IsNull() {
		var keys []string
		diags = config.PackageRepositoryPubKeys.ElementsAs(ctx, &keys, false)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		// Append keys from config to any from environment
		packageRepositoryPubKeys = append(packageRepositoryPubKeys, keys...)
	}

	// Configuration is complete, no logging needed

	// Make the OCI client available during Resource and DataSource Configure methods
	client := &helmClient{
		packageRepository:        packageRepository,
		packageRepositoryPubKeys: packageRepositoryPubKeys,
		ociClient:                p.ociClient,
	}

	resp.DataSourceData = client
	resp.ResourceData = client
}

// DataSources defines the data sources implemented in the provider.
func (p *helmProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{}
}

// Resources defines the resources implemented in the provider.
func (p *helmProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewHelmChartResource,
	}
}

// helmClient is a client to interact with OCI Helm charts.
type helmClient struct {
	packageRepository        string
	packageRepositoryPubKeys []string
	ociClient                oci.Client
}
