terraform {
  required_providers {
    helm = {
      source = "chainguard-dev/helm"
      version = "0.0.1"
    }
  }
}

provider "helm" {
  package_repository = "./local-apk-repo/packages"
  package_repository_pub_keys = [
    "./local-apk-repo/local-melange.rsa.pub"
  ]
}

# Using package repository for istio-charts-base with specific version
resource "helm_chart" "istio_base_exact_version" {
  repository      = "ttl.sh/tcnghia/test1"
  package_name    = "istio-charts-base"
  package_version = "1.20.3-r0"
}

# Using package repository for istio-charts-base with latest version
resource "helm_chart" "istio_base" {
  repository      = "ttl.sh/tcnghia/test3"
  package_name    = "istio-charts-base"
}

// These digests are useful to tag, after testing.
output "istio_base_exact_version_digest" {
  value = helm_chart.istio_base_exact_version.digest
}

output "istio_base_digest" {
  value = helm_chart.istio_base.digest
}
