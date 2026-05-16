#!/usr/bin/env bash
# Create the local k3d cluster.
# k3d wraps k3s in Docker containers — works identically on macOS (incl. Apple Silicon) and Linux.
# Cluster shape is defined in bootstrap/k3d-config.yaml so it stays reproducible.

set -euo pipefail

CLUSTER_NAME="k3d-argocd-template"
CONFIG="$(dirname "$0")/k3d-config.yaml"

# 1. Verify Docker is available
if ! command -v docker >/dev/null 2>&1; then
  echo "❌ Docker is required (k3d runs k3s inside Docker)."
  echo "   Install Docker Desktop (macOS) or docker-engine (Linux) first."
  exit 1
fi

if ! docker info >/dev/null 2>&1; then
  echo "❌ Docker daemon isn't running. Start Docker Desktop / dockerd and retry."
  exit 1
fi

# 2. Install k3d if missing
if ! command -v k3d >/dev/null 2>&1; then
  echo "📦 Installing k3d..."
  curl -s https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash
fi

# 3. Create the cluster (idempotent)
if k3d cluster list | awk '{print $1}' | grep -qx "$CLUSTER_NAME"; then
  echo "✅ k3d cluster '$CLUSTER_NAME' already exists — skipping create"
else
  echo "🚀 Creating k3d cluster '$CLUSTER_NAME'..."
  k3d cluster create --config "$CONFIG"
fi

# 4. Sanity check
echo ""
echo "Cluster info:"
kubectl cluster-info --context "k3d-${CLUSTER_NAME}"
echo ""
echo "✅ Cluster ready. kubectl context is k3d-${CLUSTER_NAME}."
