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
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	helmchart "helm.sh/helm/v3/pkg/chart"
)

func TestBuild(t *testing.T) {
	s := httptest.NewServer(registry.New())
	defer s.Close()

	registryAddr := strings.TrimPrefix(s.URL, "http://")
	t.Logf("temporary registry started at %s", registryAddr)

	tests := []struct {
		name        string
		packageName string
		patches     map[string][]byte
		validate    func(t *testing.T, artifact v1.Image, md *helmchart.Metadata)
	}{
		{
			name:        "basic",
			packageName: "chart-basic",
			validate: func(t *testing.T, artifact v1.Image, md *helmchart.Metadata) {
				m, err := artifact.Manifest()
				if err != nil {
					t.Fatalf("failed to get chart manifest: %v", err)
				}

				if m.Annotations["thisshould"] != "bepreserved" {
					t.Fatalf("unexpected annotation value: %s", m.Annotations["thisshould"])
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
		{
			name:        "Chart.yaml annotation patch",
			packageName: "chart-basic",
			patches: map[string][]byte{
				"Chart.yaml": []byte(`[{"op": "add", "path": "/annotations/patched", "value": "patched-value"}]`),
			},
			validate: func(t *testing.T, artifact v1.Image, md *helmchart.Metadata) {
				m, err := artifact.Manifest()
				if err != nil {
					t.Fatalf("failed to get chart manifest: %v", err)
				}

				// Verify existing annotation is preserved
				if m.Annotations["thisshould"] != "bepreserved" {
					t.Errorf("existing annotation not preserved: %s", m.Annotations["thisshould"])
				}

				// Verify patched annotation appears in OCI manifest
				if m.Annotations["patched"] != "patched-value" {
					t.Errorf("patched annotation not in manifest, got %q", m.Annotations["patched"])
				}

				// Verify patched annotation appears in metadata
				if md.Annotations["patched"] != "patched-value" {
					t.Errorf("patched annotation not in metadata, got %q", md.Annotations["patched"])
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()

			// Build the chart
			artifact, metadata, err := chart.Build(ctx, tc.packageName, &chart.BuildConfig{
				RuntimeRepos:       []string{"testdata/packages"},
				Keys:               []string{"testdata/packages/melange.rsa.pub"},
				JSONRFC6902Patches: tc.patches,
			})
			if err != nil {
				t.Fatalf("failed to build chart: %v", err)
			}

			if tc.validate != nil {
				tc.validate(t, artifact, metadata)
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

// TestBuildSubchartImages verifies that image references are resolved into a
// subchart's slots — not just the top-level chart's — by reading the subchart's
// own cg.json from the chart APK. The leaf subchart's override must appear in
// the rendered manifest.
func TestBuildSubchartImages(t *testing.T) {
	s := httptest.NewServer(registry.New())
	defer s.Close()
	registryAddr := strings.TrimPrefix(s.URL, "http://")

	const mainRef = "example.com/over-main:9.9"
	const dbRef = "example.com/over-db:8.8"

	artifact, metadata, err := chart.Build(t.Context(), "chart-withsubchart", &chart.BuildConfig{
		RuntimeRepos: []string{"testdata/packages"},
		Keys:         []string{"testdata/packages/melange.rsa.pub"},
		Images: map[string]string{
			"main": mainRef, // top-level chart image
			"db":   dbRef,   // leaf subchart image
		},
	})
	if err != nil {
		t.Fatalf("failed to build chart: %v", err)
	}

	chartRef := fmt.Sprintf("%s/%s:%s", registryAddr, metadata.Name, metadata.Version)
	ref, err := name.ParseReference(chartRef)
	if err != nil {
		t.Fatalf("failed to parse reference %q: %v", chartRef, err)
	}
	if err := remote.Write(ref, artifact); err != nil {
		t.Fatalf("failed to push chart: %v", err)
	}

	ociref := fmt.Sprintf("oci://%s/%s:%s", registryAddr, metadata.Name, metadata.Version)
	_, rel, err := testutil.TestPullAndTemplateChart(ociref, metadata.Name, false)
	if err != nil {
		t.Fatalf("failed to pull and template chart: %v", err)
	}
	if rel == nil {
		t.Fatal("expected rendered release output, got nil")
	}

	for _, want := range []string{mainRef, dbRef} {
		if !strings.Contains(rel.Manifest, want) {
			t.Errorf("rendered manifest missing %q; subchart override not applied\n%s", want, rel.Manifest)
		}
	}
}
