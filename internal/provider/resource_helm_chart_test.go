/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package provider_test

import (
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	helmprovider "github.com/chainguard-dev/terraform-provider-helm/internal/provider"
	"github.com/chainguard-dev/terraform-provider-helm/internal/testutil"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-containerregistry/pkg/name"
	registry "github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"helm": providerserver.NewProtocol6WithError(helmprovider.New("dev")()),
}

func TestAccHelmChartResource(t *testing.T) {
	registryServer := httptest.NewServer(registry.New())
	defer registryServer.Close()

	resourceName := "helm_chart.test"
	serverURL := strings.TrimPrefix(registryServer.URL, "http://")
	portPart := strings.Split(serverURL, ":")[1]
	repoURL := fmt.Sprintf("localhost:%s/test-repo", portPart)

	cgJSON, err := filepath.Abs("../../testdata/charts/withimages/cg.json")
	if err != nil {
		t.Fatal(err)
	}
	mainDigest := "sha256:" + strings.Repeat("a", 64)
	sidecarDigest := "sha256:" + strings.Repeat("b", 64)
	chartImages := func(refs string) string {
		return fmt.Sprintf(`
provider "helm" {
  extra_repositories = ["../../testdata/packages"]
  extra_keyrings     = ["../../testdata/packages/melange.rsa.pub"]
}

resource "helm_chart" "test" {
  repo         = %q
  package_name = "chart-withimages"
  package_arch = "x86_64"

  chart_images = jsonencode({
    refs     = {%s}
    template = jsondecode(file(%q))
  })
}
`, repoURL, refs, cgJSON)
	}
	validRefs := fmt.Sprintf(`
      main    = { repoName = "cgr.dev/chainguard-private/nginx", tag = "1.27", digest = %q }
      sidecar = { repoName = "cgr.dev/chainguard-private/redis-fips", tag = "7.4", digest = %q }
`, mainDigest, sidecarDigest)

	testCases := map[string]resource.TestCase{
		"package with chart images": {
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: chartImages(validRefs),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttrSet(resourceName, "digest"),
						func(s *terraform.State) error {
							rs, ok := s.RootModule().Resources[resourceName]
							if !ok {
								return fmt.Errorf("Not found: %s", resourceName)
							}
							ociRef := fmt.Sprintf("oci://%s@%s", rs.Primary.Attributes["repo"], rs.Primary.Attributes["digest"])
							helmChart, _, err := testutil.TestPullAndTemplateChart(ociRef, "withimages", false)
							if err != nil {
								return err
							}

							want := map[string]any{
								"image": map[string]any{
									"registry":   "cgr.dev",
									"repository": "chainguard-private/nginx",
									"tag":        "1.27",
									"digest":     mainDigest,
								},
								"sidecar": map[string]any{
									"image": map[string]any{
										"registry":   "cgr.dev",
										"repository": "chainguard-private/redis-fips",
										"tag":        "7.4@" + sidecarDigest,
									},
								},
							}
							if diff := cmp.Diff(want, helmChart.Values); diff != "" {
								return fmt.Errorf("packaged values mismatch (-want +got):\n%s", diff)
							}
							return nil
						},
					),
				},
			},
		},
		"chart images missing a mapped image": {
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: chartImages(fmt.Sprintf(`
      main = { repoName = "cgr.dev/chainguard-private/nginx", tag = "1.27", digest = %q }
`, mainDigest)),
					ExpectError: regexp.MustCompile(`image "sidecar" has no ref`),
				},
			},
		},
		"chart images with an extra image": {
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: chartImages(validRefs + fmt.Sprintf(`
      extra = { repoName = "cgr.dev/chainguard-private/extra", tag = "1", digest = %q }
`, mainDigest)),
					ExpectError: regexp.MustCompile(`image "extra" is not in the packaged mapping`),
				},
			},
		},
		"malformed chart images": {
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: chartImages(`
      main = { repoName = "chainguard-private/nginx:1.27", digest = "sha256:abc" }
`),
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(`Invalid chart images`),
				},
			},
		},
		"chart images conflict with images": {
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: fmt.Sprintf(`
provider "helm" {
  extra_repositories = ["../../testdata/packages"]
  extra_keyrings     = ["../../testdata/packages/melange.rsa.pub"]
}

resource "helm_chart" "test" {
  repo         = %q
  package_name = "chart-withimages"
  package_arch = "x86_64"

  images = {
    main = "cgr.dev/chainguard-private/nginx:1.27@%s"
  }
  chart_images = jsonencode({
    refs     = {}
    template = { images = {} }
  })
}
`, repoURL, mainDigest),
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(`Conflicting image inputs`),
				},
			},
		},
		"basic package": {
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: testAccHelmChartConfig(repoURL, "chart-basic"),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr(resourceName, "package_name", "chart-basic"),
						resource.TestCheckResourceAttr(resourceName, "repo", repoURL),
						resource.TestCheckResourceAttrSet(resourceName, "digest"),
						resource.TestCheckResourceAttrSet(resourceName, "name"),
						resource.TestCheckResourceAttrSet(resourceName, "chart_version"),
						testAccCheckHelmChartExists(resourceName, "basic"),
					),
				},
			},
		},
		"basic library package": {
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: testAccHelmChartConfig(repoURL, "chart-basiclibrary"),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr(resourceName, "package_name", "chart-basiclibrary"),
						resource.TestCheckResourceAttr(resourceName, "repo", repoURL),
						resource.TestCheckResourceAttrSet(resourceName, "digest"),
						resource.TestCheckResourceAttrSet(resourceName, "name"),
						resource.TestCheckResourceAttrSet(resourceName, "chart_version"),
						testAccCheckHelmChartExists(resourceName, "basiclib"),
					),
				},
			},
		},
		"basic package with json patch": {
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: fmt.Sprintf(`
provider "helm" {
  extra_repositories = ["../../testdata/packages"]
  extra_keyrings = ["../../testdata/packages/melange.rsa.pub"]
}

resource "helm_chart" "test" {
  repo         = %q
  package_name = %q

	json_patches = {
		"values.yaml" = jsonencode([
			{
				op    = "replace"
				path  = "/image/tag"
				value = "notadonkey"
			},
			{
				op    = "add"
				path  = "/image/digest"
				value = "deadbeef"
			}
		])
	}
}
`, repoURL, "chart-basic"),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr(resourceName, "package_name", "chart-basic"),
						resource.TestCheckResourceAttr(resourceName, "repo", repoURL),
						resource.TestCheckResourceAttrSet(resourceName, "digest"),
						resource.TestCheckResourceAttrSet(resourceName, "name"),
						resource.TestCheckResourceAttrSet(resourceName, "chart_version"),
						func(s *terraform.State) error {
							rs, ok := s.RootModule().Resources[resourceName]
							if !ok {
								return fmt.Errorf("Not found: %s", resourceName)
							}

							if rs.Primary.ID == "" {
								return fmt.Errorf("No chart ID is set")
							}

							// Extract chart reference from state
							repo := rs.Primary.Attributes["repo"]
							digest := rs.Primary.Attributes["digest"]

							// Construct OCI reference
							ociRef := fmt.Sprintf("oci://%s@%s", repo, digest)

							// Use shared test utility to pull and template the chart
							helmChart, _, err := testutil.TestPullAndTemplateChart(ociRef, "basic", false)
							if err != nil {
								return err
							}

							lref, err := name.ParseReference(fmt.Sprintf("%s:latest", repo))
							if err != nil {
								return fmt.Errorf("Failed to parse latest ref: %v", err)
							}
							_, err = remote.Head(lref)
							if err == nil {
								return fmt.Errorf("Expected 'latest' tag to not exist, but it was found")
							}

							imageMap, ok := helmChart.Values["image"].(map[string]any)
							if !ok {
								return fmt.Errorf("Expected image to be a map, but got %T", helmChart.Values["image"])
							}

							vtag, ok := imageMap["tag"].(string)
							if !ok {
								return fmt.Errorf("Expected image.tag to be a string, but got %T", imageMap["tag"])
							}

							if vtag != "notadonkey" {
								return fmt.Errorf("Expected image.tag to be notadonkey but got %s", vtag)
							}

							vdigest, ok := imageMap["digest"].(string)
							if !ok {
								return fmt.Errorf("Expected image.digest to be a string, but got %T", imageMap["digest"])
							}

							if vdigest != "deadbeef" {
								return fmt.Errorf("Expected image.digest to be deadbeef but got %s", digest)
							}

							return nil
						},
					),
				},
			},
		},
		"package with images": {
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: fmt.Sprintf(`
provider "helm" {
  extra_repositories = ["../../testdata/packages"]
  extra_keyrings = ["../../testdata/packages/melange.rsa.pub"]
}

resource "helm_chart" "test" {
  repo         = %q
  package_name = %q

  images = {
    "main"    = "cgr.dev/chainguard/nginx:v1.0@sha256:abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234"
    "sidecar" = "cgr.dev/chainguard/redis:v2.0@sha256:beef5678beef5678beef5678beef5678beef5678beef5678beef5678beef5678"
  }
}
`, repoURL, "chart-withimages"),
					Check: resource.ComposeAggregateTestCheckFunc(
						resource.TestCheckResourceAttr(resourceName, "package_name", "chart-withimages"),
						resource.TestCheckResourceAttr(resourceName, "repo", repoURL),
						resource.TestCheckResourceAttrSet(resourceName, "digest"),
						func(s *terraform.State) error {
							rs, ok := s.RootModule().Resources[resourceName]
							if !ok {
								return fmt.Errorf("Not found: %s", resourceName)
							}

							repo := rs.Primary.Attributes["repo"]
							digest := rs.Primary.Attributes["digest"]
							ociRef := fmt.Sprintf("oci://%s@%s", repo, digest)

							helmChart, _, err := testutil.TestPullAndTemplateChart(ociRef, "withimages", false)
							if err != nil {
								return err
							}

							// Check main image values were resolved
							imageMap, ok := helmChart.Values["image"].(map[string]any)
							if !ok {
								return fmt.Errorf("Expected image to be a map, got %T", helmChart.Values["image"])
							}

							if imageMap["registry"] != "cgr.dev" {
								return fmt.Errorf("Expected image.registry=cgr.dev, got %v", imageMap["registry"])
							}
							if imageMap["repository"] != "chainguard/nginx" {
								return fmt.Errorf("Expected image.repository=chainguard/nginx, got %v", imageMap["repository"])
							}
							if imageMap["tag"] != "v1.0" {
								return fmt.Errorf("Expected image.tag=v1.0, got %v", imageMap["tag"])
							}
							if imageMap["digest"] != "sha256:abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234" {
								return fmt.Errorf("Expected image.digest=sha256:abcd..., got %v", imageMap["digest"])
							}

							// Check sidecar image values were resolved
							sidecarMap, ok := helmChart.Values["sidecar"].(map[string]any)
							if !ok {
								return fmt.Errorf("Expected sidecar to be a map, got %T", helmChart.Values["sidecar"])
							}
							sidecarImage, ok := sidecarMap["image"].(map[string]any)
							if !ok {
								return fmt.Errorf("Expected sidecar.image to be a map, got %T", sidecarMap["image"])
							}

							if sidecarImage["registry"] != "cgr.dev" {
								return fmt.Errorf("Expected sidecar.image.registry=cgr.dev, got %v", sidecarImage["registry"])
							}
							if sidecarImage["repository"] != "chainguard/redis" {
								return fmt.Errorf("Expected sidecar.image.repository=chainguard/redis, got %v", sidecarImage["repository"])
							}
							// pseudo_tag should be "v2.0@sha256:beef..."
							expectedPseudoTag := "v2.0@sha256:beef5678beef5678beef5678beef5678beef5678beef5678beef5678beef5678"
							if sidecarImage["tag"] != expectedPseudoTag {
								return fmt.Errorf("Expected sidecar.image.tag=%s, got %v", expectedPseudoTag, sidecarImage["tag"])
							}

							return nil
						},
					),
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			resource.Test(t, tc)
		})
	}
}

func testAccHelmChartConfig(repo, packageName string) string {
	return fmt.Sprintf(`
provider "helm" {
  extra_repositories = ["../../testdata/packages"]
  extra_keyrings = ["../../testdata/packages/melange.rsa.pub"]
}

resource "helm_chart" "test" {
  repo         = %q
  package_name = %q
}
`, repo, packageName)
}

// testAccCheckHelmChartExists verifies the chart was pushed correctly by using
// helm libraries to pull and template the chart.
func testAccCheckHelmChartExists(resourceName, expectedChartName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("Not found: %s", resourceName)
		}

		if rs.Primary.ID == "" {
			return fmt.Errorf("No chart ID is set")
		}

		// Extract chart reference from state
		repo := rs.Primary.Attributes["repo"]
		digest := rs.Primary.Attributes["digest"]

		// Construct OCI reference
		ociRef := fmt.Sprintf("oci://%s@%s", repo, digest)

		// Use shared test utility to pull and template the chart
		helmChart, rel, err := testutil.TestPullAndTemplateChart(ociRef, expectedChartName, false)
		if err != nil {
			return err
		}

		// Verify that no "latest" tag exists using go-containerregistry
		lref, err := name.ParseReference(fmt.Sprintf("%s:latest", repo))
		if err != nil {
			return fmt.Errorf("Failed to parse latest ref: %v", err)
		}
		_, err = remote.Head(lref)
		if err == nil {
			return fmt.Errorf("Expected 'latest' tag to not exist, but it was found")
		}

		// For library charts, we only validate the chart was loaded properly
		if helmChart.Metadata.Type == "library" {
			return nil
		}

		// Validate the templating result
		if rel.Info.Status != "pending-install" {
			return fmt.Errorf("Expected pending-install status but got %s", rel.Info.Status)
		}

		return nil
	}
}
