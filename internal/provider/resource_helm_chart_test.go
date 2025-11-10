/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package provider_test

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	helmprovider "github.com/chainguard-dev/terraform-provider-helm/internal/provider"
	"github.com/chainguard-dev/terraform-provider-helm/internal/testutil"
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

	testCases := map[string]resource.TestCase{
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
