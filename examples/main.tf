# Copyright 2025 Chainguard, Inc.
# SPDX-License-Identifier: Apache-2.0

variable "registry" {
  description = "Registry URL prefix for chart repositories"
  type        = string
}

terraform {
  required_providers {
    helm = {
      source  = "chainguard-dev/helm"
      version = "0.0.1"
    }
  }
}

provider "helm" {
  extra_repositories = ["./local-apk-repo/packages"]
  extra_keyrings = [
    "./local-apk-repo/local-melange.rsa.pub"
  ]
  default_arch = "amd64" # Set default architecture for all resources
}

# Using package repository for istio-charts-base with specific version
resource "helm_chart" "istio_base_exact_version" {
  repo            = "${var.registry}/test1"
  package_name    = "istio-charts-base"
  package_version = "1.20.3-r0"
}

# Using package repository for istio-charts-base with latest version and overriding architecture
resource "helm_chart" "istio_base" {
  repo         = "${var.registry}/test3"
  package_name = "istio-charts-base"
  package_arch = "x86_64" # Overrides the provider's default_arch
}

// These digests are useful to tag, after testing.
output "istio_base_exact_version_digest" {
  value = helm_chart.istio_base_exact_version.digest
}

output "istio_base_digest" {
  value = helm_chart.istio_base.digest
}

output "istio_base_ref" {
  value = resource.helm_chart.istio_base.id
}
