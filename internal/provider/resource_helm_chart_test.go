/*
Copyright 2025 Chainguard, Inc.
SPDX-License-Identifier: Apache-2.0
*/

package provider_test

import (
	"fmt"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	helmprovider "github.com/chainguard-dev/terraform-provider-helm/internal/provider"
	"github.com/chainguard-dev/terraform-provider-helm/internal/testutil"
	"github.com/google/go-containerregistry/pkg/name"
	registry "github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
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

	// Images referenced by a chart must exist, so push them to the same registry.
	registryHost := fmt.Sprintf("localhost:%s", portPart)
	mainDigest := pushRandomImage(t, registryHost+"/chainguard/nginx:v1.0")
	sidecarDigest := pushRandomImage(t, registryHost+"/chainguard/redis:v2.0")
	mainImage := fmt.Sprintf("%s/chainguard/nginx:v1.0@%s", registryHost, mainDigest)
	sidecarImage := fmt.Sprintf("%s/chainguard/redis:v2.0@%s", registryHost, sidecarDigest)

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
		"package with images": {
			ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
			Steps: []resource.TestStep{
				{
					Config: testAccHelmChartImagesConfig(repoURL, mainImage, sidecarImage),
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

							if imageMap["registry"] != registryHost {
								return fmt.Errorf("Expected image.registry=%s, got %v", registryHost, imageMap["registry"])
							}
							if imageMap["repository"] != "chainguard/nginx" {
								return fmt.Errorf("Expected image.repository=chainguard/nginx, got %v", imageMap["repository"])
							}
							if imageMap["tag"] != "v1.0" {
								return fmt.Errorf("Expected image.tag=v1.0, got %v", imageMap["tag"])
							}
							if imageMap["digest"] != mainDigest {
								return fmt.Errorf("Expected image.digest=%s, got %v", mainDigest, imageMap["digest"])
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

							if sidecarImage["registry"] != registryHost {
								return fmt.Errorf("Expected sidecar.image.registry=%s, got %v", registryHost, sidecarImage["registry"])
							}
							if sidecarImage["repository"] != "chainguard/redis" {
								return fmt.Errorf("Expected sidecar.image.repository=chainguard/redis, got %v", sidecarImage["repository"])
							}
							// pseudo_tag should be "v2.0@sha256:..."
							expectedPseudoTag := "v2.0@" + sidecarDigest
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

// TestAccHelmChartResource_missingImage verifies that a chart whose images do
// not resolve is refused before anything is pushed.
func TestAccHelmChartResource_missingImage(t *testing.T) {
	registryServer := httptest.NewServer(registry.New())
	defer registryServer.Close()

	portPart := strings.Split(strings.TrimPrefix(registryServer.URL, "http://"), ":")[1]
	registryHost := fmt.Sprintf("localhost:%s", portPart)
	mainDigest := pushRandomImage(t, registryHost+"/chainguard/nginx:v1.0")

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: testAccHelmChartImagesConfig(registryHost+"/test-repo",
				fmt.Sprintf("%s/chainguard/nginx:v1.0@%s", registryHost, mainDigest),
				fmt.Sprintf("%s/chainguard/redis:v2.0@sha256:%064x", registryHost, 0)),
			// Terraform wraps diagnostic text, so the match must span lines.
			ExpectError: regexp.MustCompile(`(?s)image "sidecar":.*does\s+not\s+resolve`),
		}},
	})
}

// TestAccHelmChartResource_skipImageVerify verifies that HELM_SKIP_IMAGE_VERIFY
// lets a chart publish with image references that do not resolve.
func TestAccHelmChartResource_skipImageVerify(t *testing.T) {
	t.Setenv("HELM_SKIP_IMAGE_VERIFY", "true")

	registryServer := httptest.NewServer(registry.New())
	defer registryServer.Close()

	portPart := strings.Split(strings.TrimPrefix(registryServer.URL, "http://"), ":")[1]
	resourceName := "helm_chart.test"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: testAccHelmChartImagesConfig(fmt.Sprintf("localhost:%s/test-repo", portPart),
				"cgr.dev/chainguard/nginx:v1.0@sha256:abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234abcd1234",
				"cgr.dev/chainguard/redis:v2.0@sha256:beef5678beef5678beef5678beef5678beef5678beef5678beef5678beef5678"),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr(resourceName, "package_name", "chart-withimages"),
				resource.TestCheckResourceAttrSet(resourceName, "digest"),
			),
		}},
	})
}

// testAccHelmChartImagesConfig renders a chart-withimages resource. The test
// packages are only built for x86_64, so the arch is pinned rather than left to
// the host default.
func testAccHelmChartImagesConfig(repo, mainImage, sidecarImage string) string {
	return fmt.Sprintf(`
provider "helm" {
  extra_repositories = ["../../testdata/packages"]
  extra_keyrings = ["../../testdata/packages/melange.rsa.pub"]
  default_arch = "x86_64"
}

resource "helm_chart" "test" {
  repo         = %q
  package_name = "chart-withimages"

  images = {
    "main"    = %q
    "sidecar" = %q
  }
}
`, repo, mainImage, sidecarImage)
}

// pushRandomImage pushes a small random image to ref and returns its digest.
func pushRandomImage(t *testing.T, ref string) string {
	t.Helper()
	img, err := random.Image(256, 1)
	if err != nil {
		t.Fatalf("random.Image: %v", err)
	}
	r, err := name.ParseReference(ref)
	if err != nil {
		t.Fatalf("parsing %q: %v", ref, err)
	}
	if err := remote.Write(r, img); err != nil {
		t.Fatalf("pushing %s: %v", ref, err)
	}
	digest, err := img.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	return digest.String()
}
