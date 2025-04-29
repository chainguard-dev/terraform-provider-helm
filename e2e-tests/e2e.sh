#!/bin/bash
#
# Copyright 2025 Chainguard, Inc.
# SPDX-License-Identifier: Apache-2.0
#

set -euo pipefail

SCRIPT_DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )"
REPO_ROOT="$( cd "$SCRIPT_DIR/.." &> /dev/null && pwd )"
TEMP_DIR=$(mktemp -d)

# Cleanup function
cleanup() {
  echo "Cleaning up temporary files..."
  rm -rf "$TEMP_DIR"
  k3d registry delete registry.localhost || true
}

# Set trap to ensure cleanup runs on exit
trap cleanup EXIT

# Start a local k3d registry
echo "Starting k3d registry on port 12733..."
k3d registry create registry.localhost --port 12733 || true

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
TF_CLI_CONFIG_FILE="$TEMP_DIR/dev.tfrc" terraform apply -var="registry=registry.localhost:12733/e2e" -auto-approve
