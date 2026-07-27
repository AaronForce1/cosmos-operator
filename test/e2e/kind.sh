#!/usr/bin/env bash
# Spins up a kind cluster, installs the CRD, runs the operator locally, and executes the e2e
# suite against the Tempo moderato testnet.
#
# Requirements: kind, kubectl, go, and outbound network access from the kind nodes.
# NOTE: kind is a smoke test. Execution state on kind lives on the host's disk via emptyDir and
# will not meet Tempo's NVMe guidance; use small replica counts and the moderato chain only.
set -euo pipefail

CLUSTER_NAME=${CLUSTER_NAME:-tempo-operator-e2e}
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

cd "$REPO_ROOT"

if ! kind get clusters | grep -qx "$CLUSTER_NAME"; then
  kind create cluster --name "$CLUSTER_NAME" --wait 120s
fi
trap 'kind delete cluster --name "$CLUSTER_NAME"' EXIT

kubectl config use-context "kind-$CLUSTER_NAME"

# Install CRDs.
make install

# Run the operator locally against the kind cluster, in the background.
go run . --log-level=debug &
OPERATOR_PID=$!
trap 'kill "$OPERATOR_PID" 2>/dev/null || true; kind delete cluster --name "$CLUSTER_NAME"' EXIT

# kind's default storage class is "standard" (local-path).
E2E_CHAIN=${E2E_CHAIN:-moderato} \
E2E_REPLICAS=${E2E_REPLICAS:-1} \
E2E_STORAGE_CLASS=${E2E_STORAGE_CLASS:-standard} \
  go test -tags e2e -count=1 -timeout=60m -v ./test/e2e/...
