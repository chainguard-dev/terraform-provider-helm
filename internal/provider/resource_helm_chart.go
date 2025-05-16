/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package provider

import (
	"context"
	"fmt"

	"github.com/chainguard-dev/terraform-provider-helm/internal/pkg/chart"
	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/hashicorp/terraform-plugin-framework/diag"
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
	ID             types.String `tfsdk:"id"`
	Repo           types.String `tfsdk:"repo"`
	PackageName    types.String `tfsdk:"package_name"`
	PackageVersion types.String `tfsdk:"package_version"`
	PackageArch    types.String `tfsdk:"package_arch"`
	Digest         types.String `tfsdk:"digest"`
	Name           types.String `tfsdk:"name"`
	ChartVersion   types.String `tfsdk:"chart_version"`
	JSONPatches    types.Map    `tfsdk:"json_patches"`
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
			"repo": schema.StringAttribute{
				Required:    true,
				Description: "The repo in the OCI registry where the Helm chart will be pushed.",
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
				Description: "The architecture of the package to fetch. If not specified, uses the provider default_arch or falls back to system defaults.",
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
			"chart_version": schema.StringAttribute{
				Computed:    true,
				Description: "The chart version of the Helm chart extracted from the chart metadata.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"json_patches": schema.MapAttribute{
				Optional:    true,
				Description: "JSON RFC6902 patches to apply to the Helm chart, organized by the file to which the patch should be applied. Each file must contain the json representation of the JSON patch array to apply. It's easiest to use the jsonencode function to generate the JSON string.",
				ElementType: types.StringType,
			},
		},
	}
}

// Create is called when the provider must create a new resource.
func (r *helmChartResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data helmChartResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(r.do(ctx, &data)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
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
	var data helmChartResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(r.do(ctx, &data)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *helmChartResource) do(ctx context.Context, data *helmChartResourceModel) (ds diag.Diagnostics) {
	arch := data.PackageArch.ValueString()
	if arch == "" {
		// Pull from the provider scoped default arch, if arch is still empty, the pkg default will be used
		arch = r.client.defaultArch
	}

	patches, diags := toJsonPatch(ctx, data.JSONPatches)
	if diags != nil {
		return diags
	}

	ocichart, err := chart.Build(ctx, data.PackageName.ValueString(), &chart.BuildConfig{
		Keys:               r.client.extraKeyrings,
		RuntimeRepos:       r.client.extraRepositories,
		Arch:               arch,
		JSONRFC6902Patches: patches,
	})
	if err != nil {
		ds = append(ds, diag.NewErrorDiagnostic("building chart", err.Error()))
		return ds
	}

	metadata, err := ocichart.Metadata()
	if err != nil {
		ds = append(ds, diag.NewErrorDiagnostic("getting chart metadata", err.Error()))
		return ds
	}
	data.Name = types.StringValue(metadata.Name)
	data.ChartVersion = types.StringValue(metadata.Version)

	ref, err := name.ParseReference(data.Repo.ValueString())
	if err != nil {
		ds = append(ds, diag.NewErrorDiagnostic("parsing repository reference", err.Error()))
		return ds
	}

	if err := remote.Write(ref, ocichart, r.client.ropts...); err != nil {
		ds = append(ds, diag.NewErrorDiagnostic("pushing chart to registry", err.Error()))
		return ds
	}

	digest, err := ocichart.Digest()
	if err != nil {
		ds = append(ds, diag.NewErrorDiagnostic("getting chart digest", err.Error()))
		return ds
	}
	data.Digest = types.StringValue(digest.String())

	data.ID = types.StringValue(ref.Context().Digest(digest.String()).String())
	return ds
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

func toJsonPatch(ctx context.Context, tpatches types.Map) (map[string]jsonpatch.Patch, diag.Diagnostics) {
	var diags diag.Diagnostics

	patches := make(map[string]jsonpatch.Patch)

	if tpatches.IsNull() || tpatches.IsUnknown() {
		return patches, diags
	}

	patchOps := make(map[string]string)
	if diag := tpatches.ElementsAs(ctx, &patchOps, false); diag != nil {
		return patches, diag
	}

	for filename, patchOps := range patchOps {
		jp, err := jsonpatch.DecodePatch([]byte(patchOps))
		if err != nil {
			diags = append(diags, diag.NewErrorDiagnostic("error decoding patch", err.Error()))
			continue
		}
		patches[filename] = jp
	}

	return patches, diags
}
