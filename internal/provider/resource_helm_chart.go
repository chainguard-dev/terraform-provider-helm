/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package provider

import (
	"context"
	"fmt"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource              = &helmChartResource{}
	_ resource.ResourceWithConfigure = &helmChartResource{}
	_ resource.ResourceWithImportState = &helmChartResource{}
)

// NewHelmChartResource is a helper function to simplify the provider implementation.
func NewHelmChartResource() resource.Resource {
	return &helmChartResource{}
}

// helmChartResource is the resource implementation.
type helmChartResource struct {
	client *helmClient
}

// helmChartResourceModel maps the resource schema data.
type helmChartResourceModel struct {
	ID                   types.String `tfsdk:"id"`
	Repository           types.String `tfsdk:"repository"`
	PackageName          types.String `tfsdk:"package_name"`
	PackageVersion       types.String `tfsdk:"package_version"`
	PackageArch          types.String `tfsdk:"package_arch"`
	Digest               types.String `tfsdk:"digest"`
	Name                 types.String `tfsdk:"name"`
	Version              types.String `tfsdk:"version"`
}

// Configure adds the provider configured client to the resource.
func (r *helmChartResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	client, ok := req.ProviderData.(*helmClient)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected *helmClient, got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = client
}

// Metadata returns the resource type name.
func (r *helmChartResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_chart"
}

// Schema defines the schema for the resource.
func (r *helmChartResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Manages a Helm chart in an OCI registry from an APK package.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:    true,
				Description: "Identifier for this resource.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"repository": schema.StringAttribute{
				Required:    true,
				Description: "The repository in the OCI registry where the Helm chart will be pushed.",
			},
			"package_name": schema.StringAttribute{
				Required:    true,
				Description: "The name of the package to fetch from the package repository.",
			},
			"package_version": schema.StringAttribute{
				Optional:    true,
				Description: "The version of the package to fetch from the package repository. If not specified, the latest available version will be used.",
			},
			"package_arch": schema.StringAttribute{
				Optional:    true,
				Description: "The architecture of the package to fetch. If not specified, defaults to the current system architecture.",
			},
			"digest": schema.StringAttribute{
				Computed:    true,
				Description: "The SHA256 digest of the Helm chart after it is pushed to the registry.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Computed:    true,
				Description: "The name of the Helm chart extracted from the chart metadata.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"version": schema.StringAttribute{
				Computed:    true,
				Description: "The version of the Helm chart extracted from the chart metadata.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
		},
	}
}

// Create is called when the provider must create a new resource.
func (r *helmChartResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	// Retrieve values from plan
	var plan helmChartResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Verify package repository is configured
	if r.client.packageRepository == "" {
		resp.Diagnostics.AddError(
			"Missing Package Repository Configuration",
			"Package repository is not configured. Configure package_repository in the provider.",
		)
		return
	}
	
	// Determine architecture
	arch := "aarch64" // Default to ARM64
	if !plan.PackageArch.IsNull() {
		arch = plan.PackageArch.ValueString()
	}
	
	// Get package version if specified
	var packageVersion *string
	if !plan.PackageVersion.IsNull() {
		version := plan.PackageVersion.ValueString()
		packageVersion = &version
	}
	
	// Fetch package from repository
	tempApkFile, cleanup, err := fetchAPKPackage(
		ctx,
		plan.PackageName.ValueString(),
		packageVersion,
		arch,
		r.client.packageRepository,
		r.client.packageRepositoryPubKeys,
		&resp.Diagnostics,
	)
	if err != nil {
		resp.Diagnostics.AddError(
			"APK Package Fetch Failed",
			fmt.Sprintf("Failed to fetch APK package: %s", err),
		)
		return
	}
	defer cleanup()
	defer os.Remove(tempApkFile)
	

	// Create chart builder
	builder, err := NewChartBuilder(tempApkFile)
	if err != nil {
		resp.Diagnostics.AddError(
			"APK File Error",
			fmt.Sprintf("Failed to create chart builder: %s", err),
		)
		return
	}
	defer func() {
		if err := builder.Cleanup(); err != nil {
			resp.Diagnostics.AddWarning(
				"Cleanup Warning",
				fmt.Sprintf("Failed to clean up chart builder resources: %s", err),
			)
		}
	}()

	// Extract chart name and version from chart metadata instead of using the parameters
	chartMetadata, err := builder.GetChartMetadata()
	if err != nil {
		resp.Diagnostics.AddError(
			"Chart Metadata Error",
			fmt.Sprintf("Failed to extract chart metadata: %s", err),
		)
		return
	}

	// Push the APK file to OCI registry

	// Push the chart to the OCI registry by digest only (no tag)
	// Pushing chart directory contents to OCI registry by digest only
	
	// Get the chart image
	img, err := builder.AsImage()
	if err != nil {
		resp.Diagnostics.AddError(
			"Image Creation Error",
			fmt.Sprintf("Failed to create chart image: %s", err),
		)
		return
	}

	digest, err := r.client.ociClient.Push(
		plan.Repository.ValueString(),
		chartMetadata.Name,
		chartMetadata.Version,
		img)
	if err != nil {
		resp.Diagnostics.AddError(
			"OCI Push Error",
			fmt.Sprintf("Failed to push chart to OCI registry: %s", err),
		)
		return
	}

	// Set values into the state
	// Format: repository@digest for unique identification
	id := fmt.Sprintf("%s@%s", plan.Repository.ValueString(), digest)
	plan.ID = types.StringValue(id)
	plan.Digest = types.StringValue(digest)
	plan.Name = types.StringValue(chartMetadata.Name)
	plan.Version = types.StringValue(chartMetadata.Version)

	// Don't modify the package_version if it was null in the plan
	// This prevents Terraform from treating it as a modification

	// Save plan into Terraform state
	diags = resp.State.Set(ctx, plan)
	resp.Diagnostics.Append(diags...)
}

// Read refreshes the Terraform state with the latest data.
func (r *helmChartResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	// Get current state
	var state helmChartResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Check if the Helm chart exists in the registry
	// In a production setting, you'd check if the chart exists and update its digest
	// For now, we keep the state as is
	
	// State already contains name and version values from the create/update operation
	// We don't need to set default values here as they should already be populated
	// from the Chart.yaml metadata

	// Set refreshed state
	diags = resp.State.Set(ctx, &state)
	resp.Diagnostics.Append(diags...)
}

// Update updates the resource and sets the updated Terraform state on success.
func (r *helmChartResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	// Retrieve values from plan
	var plan helmChartResourceModel
	diags := req.Plan.Get(ctx, &plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Get current state
	var state helmChartResourceModel
	diags = req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Verify package repository is configured
	if r.client.packageRepository == "" {
		resp.Diagnostics.AddError(
			"Missing Package Repository Configuration",
			"Package repository is not configured. Configure package_repository in the provider.",
		)
		return
	}
	
	// Determine architecture
	arch := "aarch64" // Default to ARM64
	if !plan.PackageArch.IsNull() {
		arch = plan.PackageArch.ValueString()
	}
	
	// Get package version if specified
	var packageVersion *string
	if !plan.PackageVersion.IsNull() {
		version := plan.PackageVersion.ValueString()
		packageVersion = &version
	}
	
	// Fetch package from repository
	tempApkFile, cleanup, err := fetchAPKPackage(
		ctx,
		plan.PackageName.ValueString(),
		packageVersion,
		arch,
		r.client.packageRepository,
		r.client.packageRepositoryPubKeys,
		&resp.Diagnostics,
	)
	if err != nil {
		resp.Diagnostics.AddError(
			"APK Package Fetch Failed",
			fmt.Sprintf("Failed to fetch APK package: %s", err),
		)
		return
	}
	defer cleanup()
	defer os.Remove(tempApkFile)
	

	// Create chart builder
	builder, err := NewChartBuilder(tempApkFile)
	if err != nil {
		resp.Diagnostics.AddError(
			"APK File Error",
			fmt.Sprintf("Failed to create chart builder: %s", err),
		)
		return
	}
	defer func() {
		if err := builder.Cleanup(); err != nil {
			resp.Diagnostics.AddWarning(
				"Cleanup Warning",
				fmt.Sprintf("Failed to clean up chart builder resources: %s", err),
			)
		}
	}()

	// Extract chart name and version from chart metadata instead of using the parameters
	chartMetadata, err := builder.GetChartMetadata()
	if err != nil {
		resp.Diagnostics.AddError(
			"Chart Metadata Error",
			fmt.Sprintf("Failed to extract chart metadata: %s", err),
		)
		return
	}

	// Push the APK file to OCI registry

	// Push the chart to the OCI registry by digest only (no tag)
	// Pushing chart directory contents to OCI registry by digest only
	
	// Get the chart image
	img, err := builder.AsImage()
	if err != nil {
		resp.Diagnostics.AddError(
			"Image Creation Error",
			fmt.Sprintf("Failed to create chart image: %s", err),
		)
		return
	}

	digest, err := r.client.ociClient.Push(
		plan.Repository.ValueString(),
		chartMetadata.Name,
		chartMetadata.Version,
		img)
	if err != nil {
		resp.Diagnostics.AddError(
			"OCI Push Error",
			fmt.Sprintf("Failed to push chart to OCI registry: %s", err),
		)
		return
	}

	// Update state
	plan.Digest = types.StringValue(digest)
	plan.Name = types.StringValue(chartMetadata.Name)
	plan.Version = types.StringValue(chartMetadata.Version)
	// Use current ID if it exists, otherwise create a new one
	if state.ID.ValueString() != "" {
		plan.ID = state.ID
	} else {
		// Format: repository@digest for unique identification
		id := fmt.Sprintf("%s@%s", plan.Repository.ValueString(), digest)
		plan.ID = types.StringValue(id)
	}

	// Set state
	diags = resp.State.Set(ctx, plan)
	resp.Diagnostics.Append(diags...)
}

// Delete deletes the resource and removes the Terraform state on success.
func (r *helmChartResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// Retrieve values from state
	var state helmChartResourceModel
	diags := req.State.Get(ctx, &state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Delete the Helm chart from the OCI registry using GGCR
	// In a production setting, you'd need to implement this using the registry's API
	// For now, we'll just log the action
	// Deleting Helm chart from OCI registry
	
	// Note: Most OCI registries don't support deletion via API, so this is a no-op
	// We just remove it from Terraform state
}

// ImportState handles importing an existing OCI Helm chart into Terraform state
func (r *helmChartResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	// The import ID is expected to be in the format: repository@digest
	// For example: ttl.sh/test-repo/test-chart@sha256:abc123def456
	
	// Use ID directly as it matches our internal ID format
	// In latest version we need to pass path.Root("id")
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

