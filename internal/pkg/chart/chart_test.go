package chart_test

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/chainguard-dev/terraform-provider-helm/internal/pkg/chart"
	"github.com/chainguard-dev/terraform-provider-helm/internal/testutil"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func TestBuild(t *testing.T) {
	s := httptest.NewServer(registry.New())
	defer s.Close()

	registryAddr := strings.TrimPrefix(s.URL, "http://")
	t.Logf("temporary registry started at %s", registryAddr)

	tests := []struct {
		name        string
		packageName string
		validate    func(t *testing.T, artifact chart.Chart)
	}{
		{
			name:        "basic",
			packageName: "chart-basic",
			validate: func(t *testing.T, artifact chart.Chart) {
				m, err := artifact.Manifest()
				if err != nil {
					t.Fatalf("failed to get chart manifest: %v", err)
				}

				if m.Annotations["thisshould"] != "bepreserved" {
					t.Fatalf("unexpected annotation value: %s", m.Annotations["thisshould"])
				}

				md, err := artifact.Metadata()
				if err != nil {
					t.Fatalf("failed to get chart metadata: %v", err)
				}

				if md.Annotations["thisshould"] != "bepreserved" {
					t.Fatalf("unexpected annotation value: %s", md.Annotations["thisshould"])
				}
			},
		},
		{
			name:        "basic library",
			packageName: "chart-basiclibrary",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()

			// Build the chart
			artifact, err := chart.Build(ctx, tc.packageName, &chart.BuildConfig{
				RuntimeRepos: []string{"testdata/packages"},
				Keys:         []string{"testdata/packages/melange.rsa.pub"},
			})
			if err != nil {
				t.Fatalf("failed to build chart: %v", err)
			}

			if tc.validate != nil {
				tc.validate(t, artifact)
			}

			// Get metadata for constructing a proper OCI reference
			metadata, err := artifact.Metadata()
			if err != nil {
				t.Fatalf("failed to get chart metadata: %v", err)
			}

			chartName := metadata.Name
			chartVersion := metadata.Version
			if chartVersion == "" {
				chartVersion = "0.1.0" // Use a default if not set
			}

			// Push to temporary registry
			chartRef := fmt.Sprintf("%s/%s:%s", registryAddr, chartName, chartVersion)
			ref, err := name.ParseReference(chartRef)
			if err != nil {
				t.Fatalf("failed to parse reference %q: %v", chartRef, err)
			}

			t.Logf("pushing chart to %q", chartRef)
			if err := remote.Write(ref, artifact); err != nil {
				t.Fatalf("failed to push chart to registry: %v", err)
			}
			t.Logf("successfully pushed chart to registry")

			// Use Helm to template the chart from OCI registry
			t.Run("helm-template-from-registry", func(t *testing.T) {
				// Use a direct OCI reference with the full URL including tag
				ociref := fmt.Sprintf("oci://%s/%s:%s", registryAddr, chartName, chartVersion)

				// Pull and template the chart using shared test utility
				helmChart, rel, err := testutil.TestPullAndTemplateChart(ociref, chartName, false)
				if err != nil {
					t.Fatalf("Failed to pull and template chart: %v", err)
				}

				// Print chart debug info
				t.Logf("Chart loaded: %s version %s", helmChart.Name(), helmChart.Metadata.Version)
				t.Logf("Chart contains %d templates", len(helmChart.Templates))
				for _, tmpl := range helmChart.Templates {
					t.Logf("Template: %s (size: %d bytes)", tmpl.Name, len(tmpl.Data))
				}
				t.Logf("Chart has %d values entries", len(helmChart.Values))
				t.Logf("Chart has %d dependencies", len(helmChart.Dependencies()))

				// if its not a library chart, then make sure we have release output
				if helmChart.Metadata.Type != "library" && rel == nil {
					t.Fatalf("Expected release output for non-library chart but got nil")
				}

				// Log rendered manifests for non-library charts
				if rel != nil {
					t.Logf("Rendered manifests:\n%s\n", rel.Manifest)
				}
			})
		})
	}
}
