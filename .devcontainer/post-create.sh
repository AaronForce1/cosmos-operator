#!/usr/bin/env bash
# Devcontainer bootstrap: installs kind and the repo's Go tooling.
# kubectl and helm come from the kubectl-helm-minikube devcontainer feature;
# the docker daemon comes from the docker-in-docker feature.
set -euo pipefail

KIND_VERSION=${KIND_VERSION:-v0.24.0}

if ! command -v kind >/dev/null 2>&1; then
  echo "Installing kind ${KIND_VERSION}..."
  curl -fsSLo /tmp/kind "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-$(dpkg --print-architecture)"
  chmod +x /tmp/kind
  sudo mv /tmp/kind /usr/local/bin/kind
fi

# Pre-fetch Go module dependencies and repo-local tools so first builds are fast.
go mod download
make controller-gen

cat <<'EOF'

tempo-operator devcontainer ready. Common workflows:

  make dev-up        # kind cluster + operator image + helm install (CRD + operator)
  make dev-sample    # deploy a TempoFullNode syncing against the moderato testnet
  make dev-down      # tear the kind cluster down

  make test          # unit tests
  make test-envtest  # controller tests against a real kube-apiserver
EOF
