/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package provider

import (
	"context"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/v1/google"
	"github.com/google/go-containerregistry/pkg/v1/remote"
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
			version: version,
		}
	}
}

// helmProvider is the provider implementation.
type helmProvider struct {
	version string
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
			"extra_repositories": schema.ListAttribute{
				Description: "A list of URLs for package repositories to use for fetching APK packages.",
				Optional:    true,
				ElementType: types.StringType,
			},
			"extra_keyrings": schema.ListAttribute{
				Description: "A list of paths to package repository public keys for signature verification.",
				Optional:    true,
				ElementType: types.StringType,
			},
			"default_arch": schema.StringAttribute{
				Description: "The default architecture to use for package fetching. Can be overridden at the resource level.",
				Optional:    true,
			},
		},
	}
}

// providerData can be used to store data from the Terraform configuration.
type providerData struct {
	ExtraRepositories types.List   `tfsdk:"extra_repositories"`
	ExtraKeyrings     types.List   `tfsdk:"extra_keyrings"`
	DefaultArch       types.String `tfsdk:"default_arch"`
}

func (p *helmProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerData
	diags := req.Config.Get(ctx, &config)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	extraRepositories := []string{}
	extraKeyrings := []string{}
	// Default arch is empty by default, will use chart.DefaultArch if not specified
	defaultArch := ""

	if !config.ExtraRepositories.IsNull() {
		var repos []string
		diags = config.ExtraRepositories.ElementsAs(ctx, &repos, false)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		extraRepositories = append(extraRepositories, repos...)
	}

	// Parse the keys from the list
	if !config.ExtraKeyrings.IsNull() {
		var keys []string
		diags = config.ExtraKeyrings.ElementsAs(ctx, &keys, false)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
		extraKeyrings = append(extraKeyrings, keys...)
	}

	// Get default architecture if specified
	if !config.DefaultArch.IsNull() {
		defaultArch = config.DefaultArch.ValueString()
	}

	kc := authn.NewMultiKeychain(google.Keychain, authn.RefreshingKeychain(authn.DefaultKeychain, 30*time.Minute))
	ropts := []remote.Option{
		remote.WithAuthFromKeychain(kc),
		remote.WithUserAgent("terraform-provider-helm/" + p.version),
	}

	puller, err := remote.NewPuller(ropts...)
	if err != nil {
		resp.Diagnostics.AddError("Configure []remote.Option", err.Error())
		return
	}
	pusher, err := remote.NewPusher(ropts...)
	if err != nil {
		resp.Diagnostics.AddError("Configure []remote.Option", err.Error())
		return
	}
	ropts = append(ropts, remote.Reuse(puller), remote.Reuse(pusher))

	// Make the OCI client available during Resource and DataSource Configure methods
	client := &helmClient{
		extraRepositories: extraRepositories,
		extraKeyrings:     extraKeyrings,
		defaultArch:       defaultArch,
		ropts:             ropts,
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
	extraRepositories []string
	extraKeyrings     []string
	defaultArch       string
	ropts             []remote.Option
}
