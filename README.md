# Tempo Operator

A [Kubernetes Operator](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/) for
[Tempo](https://github.com/tempoxyz/tempo) nodes — a reth-SDK execution layer plus Commonware
Threshold Simplex consensus in a single `tempo` binary — and compatible Tempo forks.

This project is a hard fork of
[strangelove-ventures/cosmos-operator](https://github.com/strangelove-ventures/cosmos-operator),
re-targeted from Cosmos SDK / CometBFT chains to Tempo. The Kubernetes machinery (per-ordinal
identity, diff-driven reconciliation, rolling updates, self-healing) is inherited; everything
chain-facing was rewritten. See `openspec/changes/convert-cosmos-operator-to-tempo-reth-evm/`
for the full porting analysis.

## What it does

One CRD: **`TempoFullNode`** (`tempo.aaronforce.io/v1alpha1`).

- **Roles:** `validator`, `rpc`, `archive` — role-driven defaults for `--follow`, snapshot
  profiles, RPC exposure, and safety guards. [docs/node_roles.md](docs/node_roles.md)
- **Storage that matches Tempo's rules:** execution datadir on node-local (NVMe) ephemeral
  storage, re-seeded by `tempo download` on loss; consensus datadir (DKG share) on a small
  per-ordinal RWO PVC with auto-scaling. Network-attached storage for execution state is not
  offered, on purpose. [docs/storage.md](docs/storage.md)
- **Snapshot bootstrap:** an idempotent `tempo download` init container with `Auto`/`Always`/
  `Never` policies and role-derived `minimal`/`full`/`archive` profiles, preserving node
  identity files (`discovery-secret`, `known-peers.json`).
- **Validator key handling:** signing key + encryption secret mounted read-only at mode 0400
  from Secrets you control; the DKG share persists on the consensus PVC across reschedules.
  The operator never reads or logs key material. [docs/key_management.md](docs/key_management.md)
- **Double-sign guards:** replicas ≤ 1 for validators (CEL-enforced), creates gated on full
  termination of the predecessor, an RWO attach fence, a `WaitingForFence` phase instead of
  force-deletes, and drift mitigation that never touches validators.
  [docs/double_sign_safety.md](docs/double_sign_safety.md)
- **Timestamp-based upgrades:** `spec.scheduledUpgrades` rolls the fleet onto a new image ahead
  of a hardfork's activation timestamp — Tempo has no halt-height mechanism.
  [docs/upgrades.md](docs/upgrades.md)
- **Health that matches reth:** a JSON-RPC health model (`eth_syncing`, `eth_blockNumber`,
  `net_peerCount`, block-age guard) driving readiness probes, service endpoint membership, and
  rollout budgets; unhealthy pods leave the RPC service automatically.
- **Services:** per-pod p2p Services (30303 TCP+UDP, optionally LoadBalancer) and one aggregate
  RPC Service (8545, optional 8546 websocket).
- **Self-healing:** consensus-PVC auto-scaling and height-drift mitigation.

## Quick start

Requires a cluster with node-local SSD ephemeral storage for the execution datadir — read
[docs/storage.md](docs/storage.md) first; on GKE that means Standard node pools with
`--ephemeral-storage-local-ssd`.

```sh
# Install the CRD and deploy the operator
make deploy IMG=ghcr.io/aaronforce1/tempo-operator:<version>

# Deploy two RPC nodes on mainnet
kubectl apply -f config/samples/tempo_v1alpha1_tempofullnode.yaml
```

Sample manifests:

- [`tempo_v1alpha1_tempofullnode.yaml`](config/samples/tempo_v1alpha1_tempofullnode.yaml) — minimal RPC nodes
- [`tempo_v1alpha1_tempofullnode_validator.yaml`](config/samples/tempo_v1alpha1_tempofullnode_validator.yaml) — validator with key prerequisites
- [`tempo_v1alpha1_tempofullnode_full.yaml`](config/samples/tempo_v1alpha1_tempofullnode_full.yaml) — every field, commented

Any flag the CRD does not model can be passed verbatim via `spec.chain.additionalArgs`
(and `spec.chain.additionalDownloadArgs` for `tempo download`).

## Development

```sh
make manifests generate   # controller-gen: CRDs + deepcopy
make test                 # unit tests (-short)
make test-envtest         # controller tests against a real kube-apiserver
make test-e2e-kind        # kind smoke test syncing against the moderato testnet
make lint                 # golangci-lint
```

Layout:

- `api/v1alpha1` — the `TempoFullNode` types.
- `controllers` — the `TempoFullNode` reconciler (services → pods → PVCs), the self-healing
  reconciler, and the status cache controller.
- `internal/fullnode` — builders (pod, PVC, service), rollout control with the double-sign
  guards, scheduled-upgrade image resolution.
- `internal/tempo` — JSON-RPC client and pod status collection/caching.
- `internal/healthcheck` — the sidecar server (`/` sync probe with the 200/422/503 contract,
  `/disk` usage) run from the operator image inside each pod.

## Caveats

- Flag names for p2p/discovery (`--port`, `--discovery.*`), websockets, and the exact
  `--consensus.secret` file-vs-FIFO semantics come from public Tempo docs and reth conventions;
  verify against `tempo node --help` for the release you run, and use `additionalArgs` to adapt.
  If a modeled flag is wrong for your release, file an issue.
- GKE Autopilot is unvalidated; target GKE Standard.

## License

Apache 2.0 — see [LICENSE](LICENSE). Original work © Strangelove Ventures LLC.
