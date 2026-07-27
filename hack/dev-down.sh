#!/usr/bin/env bash
# Tears down the local development kind cluster created by hack/dev-up.sh.
set -euo pipefail

CLUSTER_NAME=${KIND_CLUSTER_NAME:-tempo-operator-dev}

if kind get clusters 2>/dev/null | grep -qx "$CLUSTER_NAME"; then
  kind delete cluster --name "$CLUSTER_NAME"
else
  echo "kind cluster $CLUSTER_NAME not found; nothing to do."
fi
