#!/usr/bin/env bash
# Local development loop: kind cluster + locally-built operator image + Helm install.
#
# Idempotent — re-run after code changes to rebuild the image and roll the operator.
# Requirements: docker, kind, kubectl, helm (all provided by the devcontainer).
set -euo pipefail

CLUSTER_NAME=${KIND_CLUSTER_NAME:-tempo-operator-dev}
IMG=${IMG:-tempo-operator:dev}
NAMESPACE=${NAMESPACE:-tempo-operator-system}
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

cd "$REPO_ROOT"

if ! kind get clusters 2>/dev/null | grep -qx "$CLUSTER_NAME"; then
  echo ">> Creating kind cluster $CLUSTER_NAME..."
  kind create cluster --name "$CLUSTER_NAME" --wait 120s
else
  echo ">> Reusing kind cluster $CLUSTER_NAME"
fi
kubectl config use-context "kind-$CLUSTER_NAME" >/dev/null

echo ">> Building operator image $IMG..."
docker build -t "$IMG" --build-arg VERSION=dev .

echo ">> Loading image into kind..."
kind load docker-image "$IMG" --name "$CLUSTER_NAME"

echo ">> Installing CRD + operator via Helm..."
IMG_REPO=${IMG%%:*}
IMG_TAG=${IMG##*:}
helm upgrade --install tempo-operator ./charts/tempo-operator \
  --namespace "$NAMESPACE" --create-namespace \
  --set image.repository="$IMG_REPO" \
  --set image.tag="$IMG_TAG" \
  --set image.pullPolicy=Never \
  --set logLevel=debug

# helm install applies crds/ on first install but never upgrades them; keep them fresh in dev.
kubectl apply -f charts/tempo-operator/crds/

echo ">> Waiting for the operator to become ready..."
kubectl -n "$NAMESPACE" rollout status deploy/tempo-operator --timeout=120s

cat <<EOF

Operator is running in namespace $NAMESPACE.

Next steps:
  make dev-sample      # deploy a TempoFullNode syncing against the moderato testnet
  kubectl get tempofullnodes -A -w
  kubectl -n $NAMESPACE logs deploy/tempo-operator -f
  make dev-down        # tear everything down
EOF
