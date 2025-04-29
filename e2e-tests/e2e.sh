#!/bin/bash
#
# Copyright 2025 Chainguard, Inc.
# SPDX-License-Identifier: Apache-2.0
#

set -euo pipefail

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"
REPO_ROOT="$( cd "$SCRIPT_DIR/.." &> /dev/null && pwd )"
TEMP_DIR=$(mktemp -d)
REGISTRY=${REGISTRY:-registry.local:5000}

# Cleanup function
cleanup() {
  echo "Cleaning up temporary files..."
  rm -rf "$TEMP_DIR"
}

# Set trap to ensure cleanup runs on exit
trap cleanup EXIT

# Use existing registry
echo "Using registry at $REGISTRY..."

# Build the binary in the current directory
echo "Building terraform-provider-helm binary..."
(
  cd "$REPO_ROOT" &&
  go build -o "terraform-provider-helm"
)

# Create dev.tfrc file in TEMP_DIR
cat > "$TEMP_DIR/dev.tfrc" << EOF
provider_installation {
  dev_overrides {
    "chainguard-dev/helm" = "$REPO_ROOT"
  }
}
EOF

# Run terraform
cd "$SCRIPT_DIR"
echo "Running terraform apply..."
TF_CLI_CONFIG_FILE="$TEMP_DIR/dev.tfrc" terraform apply -var="registry=$REGISTRY/e2e" -auto-approve
if [ $? -eq 0 ]; then
  echo "✅ Terraform apply completed successfully"
else
  echo "❌ Terraform apply failed"
  exit 1
fi

# Extract reference from terraform output
echo "Extracting image reference..."
ISTIO_REF=$(TF_CLI_CONFIG_FILE="$TEMP_DIR/dev.tfrc" terraform output -raw istio_base_ref)

if [ -n "$ISTIO_REF" ]; then
  echo "✅ Istio base reference: $ISTIO_REF"

  kubectl create ns istio-system || true
  echo "✅ Namespace istio-system created or already exists"
  
  # Install the Helm chart from the OCI reference
  echo "Installing Helm chart from $ISTIO_REF..."
  helm install --plain-http istio-base oci://$ISTIO_REF --wait
  if [ $? -eq 0 ]; then
    echo "✅ Helm chart installed successfully"
  else
    echo "❌ Helm chart installation failed"
    exit 1
  fi
  
  # Verify Istio CRDs are present in the cluster
  echo "Verifying Istio CRDs in the cluster..."
  ISTIO_CRDS=$(kubectl get crds | grep istio.io || true)
  
  if [ -n "$ISTIO_CRDS" ]; then
    echo "✅ Istio CRDs found in the cluster:"
    echo "$ISTIO_CRDS"
  else
    echo "❌ No Istio CRDs found in the cluster"
    exit 1
  fi
else
  echo "Failed to extract istio base reference from terraform output"
  exit 1
fi
