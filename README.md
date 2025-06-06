# Terraform Provider for OCI Helm and APK

This provider allows you to manage OCI Helm charts from APK packages in OCI registries. It supports both direct APK file paths and fetching packages from APK repositories.

## Requirements

- [Terraform](https://www.terraform.io/downloads.html) >= 1.0
- [Go](https://golang.org/doc/install) >= 1.21

## Building The Provider

1. Clone the repository
2. Enter the repository directory
3. Build the provider using the Go `install` command:

```shell
go install
```

## Using the provider

### Basic Usage

```terraform
terraform {
  required_providers {
    helm = {
      source = "chainguard-dev/helm"
    }
  }
}

provider "helm" {
  extra_repositories = ["https://packages.wolfi.dev/os"]
  extra_keyrings = [
    "/path/to/wolfi-signing1.rsa.pub",
    "/path/to/wolfi-signing2.rsa.pub"
  ]
}

resource "helm_chart" "istio_base" {
  repository      = "my-repo/charts"
  package_name    = "istio-charts-base"
  package_version = "1.20.3-r0"  # Optional, latest will be used if not specified
  package_arch    = "aarch64"    # Optional, defaults to current system architecture
}
```

## Example with Wolfi APK Files

For using Wolfi APK files containing Helm charts:

```terraform
provider "helm" {
  extra_repositories = ["https://packages.wolfi.dev/os"]
  extra_keyrings = [
    "/path/to/wolfi-signing1.rsa.pub",
    "/path/to/wolfi-signing2.rsa.pub"
  ]
}

resource "helm_chart" "istio_base" {
  repository      = "my-repo/charts"
  package_name    = "istio-charts-base"
  package_version = "1.20.3-r0"
  package_arch    = "x86_64"
}
```

## Provider Configuration

The provider supports ambient credential helpers for OCI registries, including:

1. Docker credential helpers
2. Docker config.json files
3. Environment variables

Configuration options:

```terraform
provider "helm" {
  # Optional: For package repository support
  extra_repositories = ["https://packages.wolfi.dev/os"]  # List of URLs for APK repositories
  extra_keyrings = [
    "/path/to/wolfi-signing1.rsa.pub",
    "/path/to/wolfi-signing2.rsa.pub"
  ]  # Paths to public keys for verification
  default_arch = "aarch64"  # Optional default architecture for package fetching
}
```

You can also configure the provider directly in your Terraform code, as shown above.

## How It Works

The provider extracts APK files, which are essentially tar.gz archives, and finds the Helm chart within the extracted contents. It reads the Chart.yaml to determine chart name and version, and then pushes the chart to the specified OCI registry.

### Architecture Selection

The provider has a hierarchy for determining which architecture to use when fetching packages:

1. If a resource specifies `package_arch`, that value is used
2. Otherwise, if the provider specifies `default_arch`, that value is used
3. Otherwise, it falls back to the system default (currently "x86_64")

Example of setting provider-level default architecture:

```terraform
provider "helm" {
  default_arch = "aarch64"
}
```

Example of overriding architecture at the resource level:

```terraform
resource "helm_chart" "example" {
  repo         = "example/charts"
  package_name = "example-chart"
  package_arch = "arm64"  # This takes precedence over provider default_arch
}
```

### Package Repository Support

When using package references instead of direct file paths, the provider:

1. Creates a minimal build context using chainguard-dev/apko
2. Resolves the package dependencies using the specified repository
3. Downloads the package to a temporary file
4. Extracts the APK and processes it the same way as the direct file path

## Developing the Provider

If you wish to work on the provider, you'll first need [Go](http://www.golang.org) installed on your machine (see [Requirements](#requirements) above).

To compile the provider, run `go install`. This will build the provider and put the provider binary in the `$GOPATH/bin` directory.

To generate or update documentation, run `go generate`.

### Local Development Setup

1. Build the provider:
   ```shell
   go build -o terraform-provider-helm
   ```

2. Create a dev.tfrc file to point Terraform to your local provider:
   ```hcl
   # dev.tfrc
   provider_installation {
     dev_overrides {
       "chainguard-dev/helm" = "/path/to/terraform-provider-helm"
     }
     direct {}
   }
   ```

3. Set up a local APK repository for testing:
   - The examples/local-apk-repo directory contains a minimal APK repository for testing
   - It includes a public key for verification (local-melange.rsa.pub) and a packages directory

4. Use the local provider for testing:
   ```shell
   cd examples
   TF_CLI_CONFIG_FILE=dev.tfrc terraform apply
   ```

5. Alternative setup with local plugin directory:
   ```shell
   mkdir -p ~/.terraform.d/plugins/registry.terraform.io/chainguard-dev/helm/0.0.1/$(go env GOOS)_$(go env GOARCH)
   cp terraform-provider-helm ~/.terraform.d/plugins/registry.terraform.io/chainguard-dev/helm/0.0.1/$(go env GOOS)_$(go env GOARCH)/
   ```

