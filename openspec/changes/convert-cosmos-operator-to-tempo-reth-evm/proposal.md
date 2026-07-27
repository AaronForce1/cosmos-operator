# Proposal: Port `cosmos-operator` to a Tempo (reth + Commonware) fork

---

## CONTEXT

You are working in my fork of `strangelove-ventures/cosmos-operator`, a Kubernetes operator
(kubebuilder / controller-runtime, Go) that runs Cosmos SDK + CometBFT full nodes and validators.

**Goal:** re-target this operator to run nodes for Tempo and compatible forks of Tempo
(`https://github.com/tempoxyz/tempo`) — a reth-SDK execution layer plus Commonware Threshold Simplex
consensus, compiled into a **single `tempo` binary and a single process**.

Repos / references:
- Operator fork: this working tree (`https://github.com/AaronForce1/cosmos-operator`)
- Upstream operator: `https://github.com/strangelove-ventures/cosmos-operator`
- Chain: `https://github.com/tempoxyz/tempo/releases/tag/v1.11.0` 
- Upstream chain: `https://github.com/tempoxyz/tempo`, docs `https://docs.tempo.xyz/guide/node`, CLI ref `https://docs.tempo.xyz/cli/node`
- Target platform: GKE (Autopilot: ideally, yes. / Standard: yes), Prometheus + Grafana.

Target CRD identity (change from upstream `tempo.aaron.force`):
- API group: `tempo.aaronforce.io`, version `v1alpha1`, kind `TempoFullNode`
- Container image repo: `docker pull ghcr.io/tempoxyz/tempo`

---

## GROUND TRUTH ABOUT THE CHAIN (verify each against the fork's source and `--help` before relying on it)

Everything below came from public Tempo docs and should be considered baseline for delivery. That said, **Verify by running `tempo node --help` / `tempo download --help`
in a container. Flag any item that is wrong rather than silently adapting.**

- Single process: reth execution layer + Commonware consensus launched as a task in the same binary.
  There is **no separate CL/EL pair, no Engine API, no JWT secret**.
- Node command shape:
  `tempo node --datadir <path> --chain <mainnet|moderato|devnet> --consensus.datadir <path>
   --consensus.signing-key <path> --consensus.secret <path> --port 30303 --discovery.addr 0.0.0.0
   --discovery.port 30303 --http --http.addr 0.0.0.0 --http.port 8545
   --http.api eth,net,web3,txpool,trace --metrics 9000 --telemetry-url <url>`
- RPC (non-validator) nodes run with `--follow`; there is an experimental
  `--follow.experimental.certify` trustless mode requiring finalization certificates.
- Initial sync is snapshot-based: `tempo download --chain <c> --datadir <p> [--minimal] [--force]`.
  It preserves node identity files in the datadir — `discovery-secret`, `known-peers.json`.
- **Storage constraint that breaks the upstream design:** execution state requires local NVMe.
  Network-attached block storage (GCP PD / EBS) is documented as unsupported for the execution
  datadir. Consensus state (`--consensus.datadir`) may live on network-attached storage.
- Validator keys: a static signing key (BLS12-381), plus a **dynamic signing share re-derived by an
  on-chain DKG ceremony roughly every 3 hours and written to disk**. The share must survive restarts.
- Validator set is permissioned/whitelisted on-chain; onboarding is out of band.
- Config is **CLI flags and env vars** — there is no `config.toml` / `app.toml` / `genesis.json` /
  `addrbook.json` / `priv_validator_key.json`.

---

## PHASE 0 — RECON AND DESIGN (no implementation code in this phase)

Do not modify any Go source in Phase 0. Produce `openspec/changes/convert-cosmos-operator-to-tempo-reth-evm/specs/porting-analysis.md` containing:

1. **Inventory of the operator.** Every controller, CRD, builder, and package under `internal/`
   and `api/`, with one line on what each does and whether it is chain-agnostic, chain-coupled,
   or dead weight for Tempo. Cover at minimum: the fullnode controller and its pod/PVC/service/
   configmap builders and init-container chain, the healthcheck sidecar and CometBFT client, PVC
   auto-scaling / self-healing, `ScheduledVolumeSnapshot`, `StatefulJob`, and the version/upgrade
   (halt-height) machinery. Correct my list if the tree differs.

2. **Assumption mapping table.** For each Cosmos assumption, the Tempo equivalent or "no analogue":
   | Cosmos assumption | Tempo reality | Disposition (keep / rewrite / delete) |
   Must include: genesis + addrbook init, TOML config merge, `node_key.json` / `priv_validator_key.json`,
   CometBFT `/status` health and `catching_up`, P2P/RPC/gRPC/API port set, state-sync,
   PVC-backed data dir, VolumeSnapshot-based restore, halt-height upgrades.

3. **The storage decision, argued explicitly.** The upstream operator's entire state model is
   PVC-per-pod plus VolumeSnapshot. Tempo forbids network-attached storage for execution state.
   Present the options — local SSD ephemeral + re-download on reschedule; local PersistentVolumes
   with node affinity pinning; PD-SSD anyway with measured degradation; split exec-local /
   consensus-PVC — with the recovery-time, cost, and node-pool implications of each on GKE, and a
   recommendation. Note what this does to `ScheduledVolumeSnapshot` and PVC auto-scaling.

4. **Double-signing safety analysis.** The upstream operator has no equivalence guard. Enumerate
   every path by which two pods could hold the same signing key simultaneously (rolling update,
   node NotReady + reschedule, manual scale, controller restart, PVC re-attach) and specify the
   guard for each. Also specify how the DKG-derived signing share survives pod restart and
   rescheduling given the storage decision above.

5. **Fork strategy recommendation.** Choose and justify one:
   (a) hard fork — strip Cosmos entirely, single-purpose Tempo operator;
   (b) parallel CRD + controller alongside the Cosmos ones in the same binary;
   (c) abstract a `ChainRuntime` interface with Cosmos and Tempo implementations.
   Weigh: ability to pull upstream fixes, blast radius, test surface, and the fact that I only need
   Tempo. State the merge-conflict cost of each honestly.

6. **Proposed CRD spec** for `TempoFullNode` as commented Go types or YAML — including a node `role`
   enum (validator / rpc / archive), image + version, exec and consensus volume specs, signing-key
   secret reference, snapshot-init policy, flag overrides / `additionalArgs` escape hatch,
   telemetry, service exposure, and per-instance overrides. Note every field dropped from
   `CosmosFullNode` and why.

7. **Open questions for me** — anything you had to guess. Do not resolve ambiguity by inventing a
   design; list it.

**STOP after Phase 0 and wait for my review.** Do not begin Phase 1 unprompted.

---

## PHASE 1 — API AND SCAFFOLDING (after I approve Phase 0)

- Re-group / rename the API. Regenerate deepcopy and CRD manifests with `controller-gen`; update
  kustomize bases, RBAC, sample manifests, and the operator image name. `make manifests generate`
  must be clean and `git status` must show no stray generated diffs.
- Land the CRD types with full kubebuilder validation markers (enums, defaults, required fields,
  immutability where appropriate) and doc comments on every field.
- Delete or quarantine what Phase 0 marked for deletion in one clearly-labelled commit, separate
  from feature work.

## PHASE 2 — CONTROLLER IMPLEMENTATION

Implement in this order, each as its own commit with tests:

1. **Pod builder** — flag construction from spec (not TOML merge), volume mounts for exec and
   consensus datadirs, signing-key secret mount at mode `0400`, probes, resources, security context,
   ports 30303/TCP+UDP, 8545, 9000, node affinity / topology for the storage decision.
2. **Snapshot-init** — init container running `tempo download` with the right `--chain`, idempotent
   (skip when the datadir is already populated), preserving `discovery-secret` and `known-peers.json`.
3. **Health** — replace the CometBFT client with a Tempo client: `eth_syncing`, `eth_blockNumber`,
   `net_peerCount`, block age against wall clock, plus any consensus-layer signal exposed on the
   metrics port. Wire it into the existing healthcheck sidecar subcommand and into service
   endpoint gating so unhealthy pods leave the RPC service.
4. **Services** — per-pod P2P exposure and the aggregate RPC service; preserve upstream's
   per-instance service semantics.
5. **Rollout strategy** — ordered updates honouring the double-sign guards from Phase 0.
6. **Volumes** — implement the Phase 0 storage decision, including whatever remains of PVC
   auto-scaling and scheduled snapshots for the consensus volume.
7. **Upgrades** — replace halt-height version switching with Tempo's actual upgrade mechanism
   (research it in the fork: activation by block height, timestamp, or chainspec hardfork config)
   and reconcile the image accordingly.

## PHASE 3 — VALIDATION

- Unit tests for every builder using table-driven golden objects, in the style already present in
  the repo. Do not lower existing coverage.
- `envtest` controller tests for create / scale / update / delete.
- A kind or GKE e2e that brings up N nodes against `moderato` tempo testnet and asserts sync.
- Update `README.md` and add `docs/` for: node roles, storage prerequisites and node-pool setup,
  key management and rotation, upgrade runbook, and the double-sign safety model.

---

## WORKING RULES

- Read before writing. Never assume an upstream file's contents from its name.
- Small, reviewable commits with conventional-commit messages. No mega-commits.
- Add `upstream` as a git remote and keep the port rebaseable if Phase 0 chose (b) or (c).
- Never commit keys, secrets, or real endpoints. Placeholders only. Never log key material or
  key file paths' contents.
- If a documented Tempo flag or behaviour does not match the fork's source, stop and tell me —
  do not paper over it.
- When a decision has real trade-offs, present them and recommend; do not silently pick.
- Prefer deleting Cosmos code over leaving it dormant behind unused config.
- Run `make fmt vet lint test` before declaring any phase done, and report actual output.