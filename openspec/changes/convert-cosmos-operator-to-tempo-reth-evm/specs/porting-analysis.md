# Porting Analysis: cosmos-operator → Tempo (reth + Commonware) operator

Phase 0 deliverable for `convert-cosmos-operator-to-tempo-reth-evm`. No source code was
modified for this phase. Everything below was produced by reading the working tree at
`d67dcdc` (upstream `strangelove-ventures/cosmos-operator` head + openspec commit) and by
verifying the proposal's ground-truth claims against the public Tempo docs
(`tempo.xyz/developers/docs`, current as of 2026-07-27, i.e. the T8/v1.11.0 era).

**Verification caveat, stated up front:** this session has no Docker daemon and no access to
the `tempoxyz/tempo` source tree, so `tempo node --help` / `tempo download --help` could not
be run in a container. Every claim below is tagged with its verification status. Items
tagged **UNVERIFIED** must be confirmed against the real binary before Phase 1 relies on
them. Section 0 lists the places where the docs **contradict** the proposal's ground truth.

---

## 0. Ground-truth verification status

The proposal asked that wrong items be flagged rather than silently adapted. Two of its
claims are contradicted by the current docs; the rest check out or could not be verified.

| # | Proposal claim | Status | What the docs actually say |
|---|---|---|---|
| 0.1 | "Validator keys: a static signing key (**BLS12-381**)" | **CONTRADICTED** | The static signing key is **ed25519** — "Identify your validator in the consensus protocol. Used for DKG participation, block proposals, and voting." Generated via `tempo consensus generate-signing-key --output <path> --secret <fifo>`. **BLS12-381 is the algorithm of the DKG-derived *signing share***, not the static key. |
| 0.2 | "dynamic signing share … **must survive restarts**" | **PARTIALLY CONTRADICTED** | The share (stored in `<datadir>/consensus`) is "Managed automatically — updated every DKG ceremony (~3 hours)", and "**Lost shares are recovered from the network on restart**" — if the directory is deleted, "the node will recover a new share in the following epochs". So survival is a *liveness* optimization (avoid missed epochs), not a hard *correctness* requirement. This materially softens the storage constraint for the consensus volume. |
| 0.3 | Single process, no CL/EL pair, no Engine API, no JWT | Consistent with docs (single `tempo` binary, `--consensus.*` flags on the same command) | **UNVERIFIED at source level** — confirm no `--authrpc.*` / JWT flags exist in `tempo node --help`. |
| 0.4 | Node command shape incl. `--port 30303 --discovery.addr --discovery.port` | **UNVERIFIED** | The docs' CLI page lists `--datadir`, `--chain`, `--follow`, `--http*`, `--consensus.*`, `--metrics`, `--telemetry-url`, `--telemetry-metrics-interval` — it does **not** document `--port` or `--discovery.*`. As a reth derivative these flags almost certainly exist, but names/defaults must come from `tempo node --help`. |
| 0.5 | Chains: `mainnet \| moderato \| devnet` | **PARTIAL** | Docs consistently show `mainnet` and `moderato` only. `devnet` is UNVERIFIED. |
| 0.6 | `--follow` for RPC nodes; `--follow.experimental.certify` trustless mode | **PARTIAL** | `--follow` confirmed ("Run as a full node following a trusted RPC endpoint"). But the RPC guide now says "All RPC nodes are trustless by default" and "The upstream must provide consensus finalization certificates" — suggesting certify-style verification may have been promoted to default since the proposal was written. `--follow.experimental.certify` as a distinct flag is UNVERIFIED. |
| 0.7 | `tempo download --chain <c> --datadir <p> [--minimal] [--force]` preserving `discovery-secret`, `known-peers.json` | **CONFIRMED + extended** | Full flag set: `--chain`, `--datadir`, `-u/--url`, `--manifest-url`, `--list`, `--resumable` (default on), `--minimal` (validators), `--full`, `--archive`/`--all`, `-y/--non-interactive` (defaults to minimal), `--force` ("Overwrite existing snapshot data while preserving `discovery-secret` and `known-peers.json`"). Snapshots browsable at `snapshots.tempo.xyz`. |
| 0.8 | Execution state requires local NVMe; network-attached storage unsupported; consensus state may live on network storage | **CONFIRMED** | "Validators must run execution state on local NVMe / direct-attached storage. Network-attached volumes (EBS, GCP Persistent Disk, Azure Managed Disk, NAS, SAN) are **not supported** for execution." Consensus: "Can reside on lower-performance volumes like EBS", split via `--datadir` + `--consensus.datadir`. |
| 0.9 | DKG ceremony roughly every 3 hours, share written to disk | **CONFIRMED** | "updated every DKG ceremony (~3 hours)"; shares live in `<datadir>/consensus`. Monitoring metrics exist: `consensus_engine_dkg_manager_ceremony_successes_total`, `..._failures_total`, `..._how_often_dealer`, `..._how_often_player`; recommended alert: DKG successes unchanged for 12h. |
| 0.10 | Validator set permissioned, onboarding out of band | **CONFIRMED** | "The active validator set is currently permissioned" — registration is address + pubkey + ingress/egress IPs + fee recipient + signature, via the Tempo team. |
| 0.11 | Config is CLI flags + env vars, no config.toml/genesis.json/addrbook.json/priv_validator_key.json | Consistent with all docs pages | **UNVERIFIED at source level** (reth supports `reth.toml`; whether tempo exposes/uses it is unknown — confirm via `--help`). |
| 0.12 | Upgrades (not in ground truth but load-bearing for Phase 2.7) | **CONFIRMED: timestamp-activated hardforks** | T-series upgrades activate at fixed unix timestamps per network (e.g. T8: testnet 1785160800, mainnet 1785420000). "Node operators should run v1.11.0 before the activation timestamp… Nodes that are not updated will fall out of sync at the T8 activation timestamp." There is **no halt-height mechanism**: the old binary keeps running and simply forks off. Upgrade = rolling image swap **before** a wall-clock deadline. |
| 0.13 | Metrics (not in ground truth) | **Docs finding** | Three scrape endpoints are documented: `:9000/metrics` (execution, set by `--metrics 9000`), `:8002/metrics` (consensus — port provenance UNVERIFIED, likely fixed or flag-set), `:6060/metrics` (sync checkpoint / `reth_sync_checkpoint`). Key health metrics: `consensus_engine_marshal_finalized_height`, `consensus_engine_marshal_processed_height`, `consensus_engine_peer_manager_peers`, `reth_payloads_resolved_block`. |
| 0.14 | Key encryption delivery | **Docs finding** | `--consensus.secret` is documented to take a **FIFO / process substitution** ("a named pipe (FIFO) or shell process substitution for this path"), so the decryption secret is never a file at rest. Whether a plain file path is *accepted* (which a k8s Secret mount would produce) is UNVERIFIED — this affects the pod design (§4, §7). |

Hardware baselines from the docs (for §3): RPC 16→32+ cores / 32→64 GB / **1000→2000 GB NVMe** / 1→10 Gbps; validator 8→16+ cores / 16→32 GB / **100 GB→1 TB NVMe** / 1 Gbps. `chrony`/`ntpd` required; TCP BBR recommended.

---

## 1. Inventory of the operator

Layout: kubebuilder v3, module `github.com/strangelove-ventures/cosmos-operator`, domain
`strange.love`, group `cosmos.strange.love`, three namespaced CRDs. Controller-runtime
v0.13.1 / k8s API v0.25.5 (old — see §7 Q12).

Legend: **agnostic** = pure k8s mechanics, ports as-is · **coupled** = assumes
Cosmos/CometBFT semantics, rewrite · **dead** = no Tempo analogue, delete.

### 1.1 Controllers (`controllers/`)

| File | Reconciles | What it does | Verdict |
|---|---|---|---|
| `cosmosfullnode_controller.go` | `CosmosFullNode` (v1) | The main loop, order: Services → node keys → peers → ConfigMaps → SA/Role/RoleBinding → Pods → PVCs; status written via deferred `updateStatus`; requeues 3s (work pending) / 30s (waiting for LB IPs) / 60s (steady-state poll). Watches owned Pod/PVC/ConfigMap/Service **delete events only**; steady progress is requeue-driven. | keep shape; rename; drop node-key→peers→ConfigMap ordering (identity moves out of ConfigMaps) |
| `selfhealing_controller.go` | `CosmosFullNode` (2nd controller, same CRD) | PVC auto-scale (writes `status.selfHealing`) + height-drift mitigation (deletes lagging pods directly). 60s loop; no-op unless `spec.selfHeal` set. | keep; retarget height source; route validator deletes through the double-sign gate (§4) |
| `scheduledvolumesnapshot_controller.go` | `ScheduledVolumeSnapshot` (v1alpha1) | Cron-driven phase machine; picks an in-sync pod, optionally signals the fullnode controller to delete it (via `status.scheduledSnapshotStatus[..].podCandidate`), takes a CSI VolumeSnapshot, restores the pod, prunes old snapshots. | **delete** under the §3 storage decision |
| `statefuljob_controller.go` | `StatefulJob` (v1alpha1) | Periodically restores the newest matching VolumeSnapshot into a fresh PVC and runs a user Job against it (snapshot-to-object-storage publishing). Chain-agnostic Go. | **delete** (depends on VolumeSnapshots of chain data, which no longer exist) |
| `ptr.go` | — | generic pointer helper | keep |

### 1.2 API (`api/`)

| Item | What it does | Verdict |
|---|---|---|
| `v1/cosmosfullnode_types.go` — `FullNodeSpec` core: `replicas`, `ordinals`, `podTemplate`, `strategy` (maxUnavailable), `volumeClaimTemplate`, `volumeRetentionPolicy`, `instanceOverrides`, `additionalVersionedPods`, `serviceAccountName` | Replica/identity/rollout/storage plumbing | agnostic — carries over nearly field-for-field |
| `v1/cosmosfullnode_types.go` — `type: FullNode\|Sentry` + `privvalSleepSeconds` | Sentry mode opens privval port 1234 for an external remote signer (Horcrux/TMKMS), sleeps before start so the signer can connect | **dead** — Tempo signs in-process |
| `v1/cosmosfullnode_types.go` — `ChainSpec` | `chainID`, `binary`, `homeDir`, `CometConfig` (p2p/rpc laddrs, peers, seeds, TOML overrides), `SDKAppConfig` (minGasPrice, pruning, haltHeight, TOML overrides), genesis URL/script, addrbook URL/script, snapshot URL/script, `skipInvariants`, `databaseBackend`, `versions []ChainVersion` (height-gated images) | **coupled/dead** — the single largest block of deleted surface; replaced by a flag-oriented `TempoChainSpec` (§6) |
| `v1/cosmosfullnode_types.go` — `FullNodeStatus` | `phase`, `observedGeneration`, `statusMessage`, `peers`, `sync` (per-pod height/inSync/error), `height` map, `scheduledSnapshotStatus`, `selfHealing` | mechanism agnostic; sources coupled (CometBFT `/status`); `peers`/`scheduledSnapshotStatus` dead |
| `v1/self_healing_types.go` | `pvcAutoScale` (usedSpacePercentage/increaseQuantity/maxSize) + `heightDriftMitigation` (threshold) | agnostic mechanism; keep both (drift detection retargeted to `eth_blockNumber`) |
| `v1alpha1/scheduledvolumesnapshot_types.go` | Recurring VolumeSnapshots of one replica's PVC, `minAvailable` in-sync gate, retention `limit`, optional pod deletion for quiesce | delete (§3) |
| `v1alpha1/statefuljob_types.go` | Snapshot-restore-plus-Job runner | delete (§3) |
| `groupversion_info.go` ×2 | `cosmos.strange.love` v1 / v1alpha1 | rename → `tempo.aaronforce.io/v1alpha1`. The group string is also hardcoded in `internal/kube/labels.go` (`BelongsToLabel`), `internal/volsnapshot/vol_snapshot_control.go` (`cosmosSourceLabel`), `internal/fullnode/labels.go` (`networkLabel`, `typeLabel`), `internal/test/assertions.go`, and `internal/kube/indexer.go` (compares `owner.APIVersion`) |

### 1.3 `internal/fullnode` — builders and sub-reconcilers

Pod construction:

| File | What it does | Verdict |
|---|---|---|
| `pod_builder.go` (641 ln) | Builds one pod per ordinal: containers `node` (chain, `<binary> start --home …`), `healthcheck` sidecar (operator image, `/manager healthcheck --rpc-host localhost:26657`), `version-check-interval` sidecar; 6–7 init containers (below); volumes `vol-chain-home` (PVC), `vol-tmp`, `vol-config` (ConfigMap incl. `node_key.json`), `vol-system-tmp` (exists for CometBFT state-sync); env `CHAIN_HOME/GENESIS_FILE/ADDRBOOK_FILE/CONFIG_DIR/DATA_DIR`; readiness probes keyed by `probes.strategy` (main: CometBFT `GET /health` on 26657; sidecar: `GET /` on 1251); strategic-merge `podPatch` escape hatch; per-height image selection from `ChainSpec.Versions`. | rewrite — keep the `PodBuilder`/`WithOrdinal`/`podPatch`/instance-override scaffolding; replace containers, init chain, ports, env, probes |
| — init chain | 1 `clean-init` (wipe tmp) → 2 `chain-init` (`<binary> init`, also into `.tmp` for pristine TOML) → 3 `genesis-init` (script/URL/init-genesis) → 4 `addrbook-init` → 5 `config-merge` (TOML merge + copy node_key) → 6 `snapshot-restore` (wget\|tar, skip if `*.db` present) → 7 `version-check` (opens Cosmos DB, patches `status.height`, panics on wrong image). Steps use `ghcr.io/strangelove-ventures/infra-toolkit`. | steps 2–5,7 **dead**; step 6 rewritten as `tempo download` (§6); Tempo chain: ~2 init containers total |
| `build_pods.go` | Ordinal loop, config-checksum annotation (ConfigMap change ⇒ pod roll), skips VolumeSnapshot pod candidates, additional-pod synthesis | keep; drop candidate skipping with volsnapshot |
| `pod_control.go` (302 ln) | The rollout engine — see §4 for its exact semantics and gaps | keep structure; re-define readiness; add termination barrier (§4) |
| `ports.go` | 1317 api / 9090 grpc / 9091 grpc-web / 1234 privval / 26660 prom / 8080 rosetta (+26656/26657 from CometConfig) | replace wholesale: 30303 tcp+udp p2p, 8545 http-rpc, 9000 metrics (+8002/6060 pending 0.13 verification) |

Config and init payloads:

| File | What it does | Verdict |
|---|---|---|
| `configmap_builder.go` (281 ln) | Per-ordinal ConfigMap: `config-overlay.toml` + `app-overlay.toml` (deep-merged defaults ← spec ← user TOML overrides, snake_case + kebab-case dual-emit) + `node_key.json`; computes **halt-height** for coordinated upgrades | **dead** as a body of code. Only the "one small per-ordinal config object" shape survives, and identity material must move to Secrets |
| `configmap_control.go` | Generic diff-driven ConfigMap reconcile returning per-pod checksums | keep verbatim |
| `genesis.go`, `addrbook.go` + `script/download-{genesis,addrbook}.sh`, `script/use-init-genesis.sh` | Genesis / addrbook acquisition (URL or script) | **dead** |
| `snapshot.go` + `script/download-snapshot.sh` | wget\|tar snapshot restore, `*.db` sentinel for idempotence | rewrite as `tempo download` wrapper (its `--force`-preserves-identity + populated-datadir skip replaces the sentinel) |
| `toml/*.toml`, `testdata/*.toml` | Embedded CometBFT/SDK default configs + golden files | **dead** |

Identity and peering:

| File | What it does | Verdict |
|---|---|---|
| `node_key_collector.go` | Mints per-ordinal CometBFT `node_key.json` (ed25519, tendermint node-ID = `sha256(pub)[:20]`), **persisted in the ConfigMap** (a security smell), read-before-create to stay deterministic | rewrite: Tempo network identity is the datadir `discovery-secret`; if stable per-ordinal identity is wanted it must be generated/persisted (Secret-backed) and injected — see §7 Q4 |
| `peer_collector.go` | Builds `<nodeID>@<host>:<port>` peer strings from per-ordinal p2p Service DNS/LB IPs; drives `WaitingForP2PServices` phase | rewrite string format (enode/ENR-style, UNVERIFIED); keep the LB-address plumbing and phase gate |
| `service_builder.go` | Per-pod p2p Services (`<app>-p2p-<n>`, first `maxP2PExternalAddresses` become LoadBalancer + `externalTrafficPolicy: Local`), per-pod privval Services (Sentry), one shared `<app>-rpc` ClusterIP (api/rosetta/grpc/rpc/grpc-web) | keep per-pod p2p topology (it maps well onto discovery reachability); delete privval; RPC service becomes 8545 (+8546 ws if verified) |
| `service_control.go` | Create/update only, never deletes (public p2p addresses must not be released) | keep verbatim |

Storage, health, status:

| File | What it does | Verdict |
|---|---|---|
| `pvc_builder.go` / `pvc_control.go` | One RWO PVC per ordinal (`pvc-<app>-<n>`), grow-only sizing (incl. autoscale × 1.02), snapshot/PVC `dataSource` + `autoDataSource` seeding, retain/delete on scale-down, resize-only patches | keep — retargeted to the **consensus** volume (§3); dataSource paths restricted for validators (§4 G6) |
| `pvc_auto_scaler.go` | Writes `status.selfHealing.pvcAutoScale` (percent or quantity increase, capped) | keep for consensus volume |
| `pvc_disk_usage.go` | Fans out to each pod's healthcheck sidecar `/disk?dir=…`, joins to PVC capacity | keep; point at consensus dir; also reuse to surface **exec NVMe** usage as status/alert (resize impossible there) |
| `status.go` | `ResetStatus` + `SyncInfoStatus` (CometBFT `catching_up` → `InSync`, `latest_block_height` → height) | rewrite sources (`eth_syncing` / `eth_blockNumber`) |
| `status_client.go` | Per-CRD-key mutex serializing status writes from 4 writers | keep verbatim |
| `drift_detection.go` | max-height minus per-pod height ≥ threshold ⇒ delete pod, capped by rollout budget | keep, retargeted |
| `rbac_builder.go` + `service_account_control.go`, `role_control.go`, `role_binding_control.go` | `-vc-*` SA/Role/RoleBinding so the in-pod version-check container can patch `status.height` | builders **dead** with versioncheck; keep the three generic `*_control.go` reconcilers if any in-pod API access survives (likely none — then delete all) |
| `labels.go`, `ptr.go`, `client.go` | Standard labels (+`cosmos.strange.love/{network,type}`), pointer helpers, narrow client ifaces | keep; rename label domains |

### 1.4 `internal/cosmos` — CometBFT client + status cache

| File | What it does | Verdict |
|---|---|---|
| `comet_client.go` | Hand-rolled `GET /status` client; parses `catching_up`, `latest_block_height` (string), node/validator info. (Latent bug: nil-deref on unexpected 200 bodies — don't port the pattern.) | **replace** with a Tempo JSON-RPC client: `eth_syncing`, `eth_blockNumber`, `net_peerCount`, `eth_getBlockByNumber("latest")` for block age |
| `status_collector.go` | errgroup fan-out per pod, 5s timeout, per-item errors, RPC port discovered from container `node` port `rpc` (default 26657) | keep skeleton; port lookup → `http-rpc`/8545 |
| `status_collection.go` | `Synced()` ≡ reachable ∧ `!catching_up`; upsert/intersect cache plumbing | keep; `Synced()` becomes `eth_syncing == false` (+ block-age guard, §7 Q7) |
| `cache_controller.go` | Second reconciler watching the CRD purely to run a 5s poll goroutine per CRD; exposes `Collect`/`SyncedPods`/`Invalidate` consumed by fullnode rollout, drift detection, and volsnapshot candidate selection | keep nearly verbatim |

### 1.5 `internal/healthcheck` — sidecar server

| File | What it does | Verdict |
|---|---|---|
| `comet.go` | Sidecar `GET /`: 200 in-sync / 422 catching-up / 503 unreachable; log-on-state-change | rewrite probe body against `eth_syncing`; keep the 200/422/503 contract — kubelet's view and the operator's cached view must stay in agreement (rollout math assumes it) |
| `disk_usage.go`, `client.go` | `GET /disk?dir=` via `statfs`; operator-side client (port 1251) | keep verbatim |
| `healtchcheck.go` (sic) | `Port = 1251` | keep (fix filename typo while touching it) |

### 1.6 `cmd/`, `main.go`, `internal/version`

| Item | What it does | Verdict |
|---|---|---|
| `main.go` | Cobra root = manager; wires httpClient → CometClient → CacheController → FullNode/SelfHealing/StatefulJob/ScheduledVolumeSnapshot controllers; VolumeSnapshot-CRD presence probe; leader-election ID `16e1bc09.strange.love` | keep scaffolding; swap client construction; drop two controllers; rename election ID |
| `cmd/healtcheck.go` (sic) | Sidecar entrypoint `/manager healthcheck --rpc-host http://localhost:26657` | keep; retarget default to `http://localhost:8545` |
| `cmd/versioncheck.go` | Opens the Cosmos application DB (`cosmos-db` + `rootmulti`) to read height **while the node is stopped**, patches `status.height`, panics on image mismatch. Exists solely for halt-height upgrades. | **dead** — with it goes `cosmossdk.io/log`, `cosmossdk.io/store`, `cosmos-db`, the `rocksdb pebbledb` build tags, the RocksDB CGO Dockerfile stages, and `rocksdb/`. Timestamp upgrades don't need offline height reads. |
| `cmd/logger.go` | zap setup | keep |
| `internal/version` | Operator's own build stamp (ldflags); pins the sidecar image tag | keep (rename image repo) |

### 1.7 `internal/volsnapshot`, `internal/statefuljob`

Both are well-built and almost chain-agnostic (the only couplings: `SyncedPods` candidate
filter, the `cosmos.strange.love/source` label, and the `CosmosFullNode` status handshake) —
but under the §3 storage decision their *object* (CSI VolumeSnapshots of chain data)
disappears, so both packages, both CRDs, and their two controllers are **dead weight for
Tempo**. Deleting them is the honest move per the working rules; resurrecting the pattern
later for consensus-volume backups would be a small, isolated re-add.

### 1.8 `internal/kube`, `internal/diff`, `internal/test`

- `internal/kube` — **agnostic** utility belt: `ReconcileError`/transience classification,
  recommended labels + `OrdinalAnnotation` + `.metadata.controller` field index,
  `ComputeRollout` (max-unavailable math), pod availability vendoring, strategic-merge
  patch, VolumeSnapshot helpers, name/label normalization. Two Cosmos strings to rename
  (`BelongsToLabel`, the `cosmosv1` import in `indexer.go`); `volume_snapshot.go` deletes
  with §1.7.
- `internal/diff` — **agnostic** three-way differ (`Creates/Updates/Deletes` by
  FNV-1-hashed revision label, ordinal-ordered). The backbone of every `*Control`. Port
  verbatim.
- `internal/test` — `RequireValidMetadata` (agnostic, keep), `NopReporter` (keep),
  `assertions.go` (`cosmos.strange.love/type` FullNode/Sentry assertion — dead).

### 1.9 Build & deploy artifacts

| Item | Notes | Verdict |
|---|---|---|
| `Dockerfile` | Multi-stage; RocksDB v7.10.2 stage + musl cross toolchains + `CGO_ENABLED=1 -tags 'rocksdb pebbledb'` static build — all of it exists **only** for `versioncheck` | collapses to a plain `CGO_ENABLED=0` build once versioncheck dies |
| `Makefile` | `manifests` pipes the CosmosFullNode CRD through `tools/minify-crd.go` (CRD > annotation size limits); `gen-api` hardcodes `--group cosmos`; `latest-snapshot` curls polkachu | keep targets; re-point minify filename; delete `latest-snapshot` |
| `config/` | Group-named CRD bases, `cosmos-operator-` prefixes/namespace, kube-rbac-proxy patch set, samples (the `_full.yaml` sample is the best field-reference doc); stale `hostedsnapshots` patch files | full rename sweep; delete stale patches; new samples |
| `PROJECT` | domain `strange.love`, group `cosmos` | rewrite |
| go.mod | k8s v0.25.5 / controller-runtime v0.13.1 (2022-era), Cosmos SDK deps only via versioncheck | Cosmos deps drop for free; k8s bump is a separate decision (§7 Q12) |

---

## 2. Assumption mapping table

| Cosmos assumption (where it lives) | Tempo reality | Disposition |
|---|---|---|
| **Genesis init** — `genesisURL`/`genesisScript`, `GENESIS_FILE`, `<binary> init` chain (`genesis.go`, init containers 2–3) | No `genesis.json`; the chain is selected by `--chain <name>` and state arrives via snapshot | **delete** |
| **Addrbook init** — `addrbookURL`/`addrbookScript`, `ADDRBOOK_FILE` (`addrbook.go`, init container 4) | No `addrbook.json`; `known-peers.json` is node-managed inside the datadir and preserved by `tempo download --force` | **delete** (nothing to manage) |
| **TOML config merge** — `CometConfig` + `SDKAppConfig` → ConfigMap overlays → `config-merge` init container | Config is CLI flags/env only (0.11) | **rewrite**: flags are constructed directly in the pod builder from spec; the `additionalArgs` escape hatch replaces `TomlOverrides` |
| **`node_key.json`** — operator-minted, ConfigMap-persisted, drives peer IDs | `discovery-secret` in the exec datadir, node-generated, preserved by `tempo download` | **rewrite**: default is "let the node own it" (ephemeral identity per exec volume); stable identity is an open design point (§7 Q4) |
| **`priv_validator_key.json` / remote signer / Sentry type** — privval port 1234, privval Services, `privvalSleepSeconds` | In-process signing: static ed25519 key (Secret-mounted) + auto-managed BLS12-381 DKG share in `<datadir>/consensus`. No remote-signer protocol. | **delete** Sentry/privval; **add** signing-key Secret mount + consensus-volume persistence + double-sign guards (§4) |
| **CometBFT `/status`, `catching_up`** — comet_client, sidecar probe, `Synced()`, rollout readiness, snapshot candidate gate | JSON-RPC: `eth_syncing` (false ⇒ synced), `eth_blockNumber`, `net_peerCount`; consensus metrics (`marshal_finalized/processed_height`) on the metrics ports for validator-grade signals | **rewrite** both the operator-side client and the sidecar handler; keep the 200/422/503 sidecar contract and the cache-collector architecture |
| **Port set** — 26656 p2p, 26657 rpc, 1317 api, 9090/9091 grpc, 8080 rosetta, 26660 prom, 1234 privval | 30303 tcp+udp p2p/discovery (0.4 UNVERIFIED), 8545 http (+ws port UNVERIFIED), 9000 execution metrics, 8002 consensus metrics + 6060 sync metrics (0.13) | **rewrite** `ports.go` wholesale |
| **State-sync** — `/tmp` emptyDir exists for chunk queues; state-sync configured via TOML overrides | No CometBFT state-sync; initial sync = `tempo download` snapshots (modular manifests, resumable) | **delete** the state-sync accommodations; **rewrite** snapshot-restore init container to `tempo download` |
| **PVC-backed data dir** — one RWO PVC per pod holds *all* chain state | Execution state **must not** be network-attached (0.8); consensus state may be | **rewrite** into split storage (§3): exec = local NVMe ephemeral, consensus = small PVC. PVC machinery survives pointed at the consensus volume |
| **VolumeSnapshot restore + ScheduledVolumeSnapshot + StatefulJob + autoDataSource** | Exec state lives on ephemeral local disk — CSI snapshots are impossible there and pointless for consensus (share is network-recoverable, 0.2) | **delete** (both CRDs, both controllers, `internal/volsnapshot`, `internal/statefuljob`, `autoDataSource`) |
| **Halt-height upgrades** — `ChainSpec.Versions` (height-gated images), `SetHaltHeight` → `app.toml`, versioncheck DB reads, crash-loop-until-image-matches dance | Timestamp-activated hardforks (0.12): run the new binary *before* the activation instant; old binaries fork off, they don't halt | **delete** the entire mechanism; **rewrite** as an optional `scheduledUpgrades: [{activatesAt, image}]` list — the operator flips the image and rolls pods ahead of the deadline (§6) |
| **PVC auto-scaling / self-healing** — disk-usage sidecar + status-driven resize; height-drift pod deletion | Consensus PVC still benefits; exec NVMe is fixed-size per node shape (resize impossible — monitor + alert only); drift detection maps cleanly onto `eth_blockNumber` | **keep**, rescoped |
| **`minGasPrice`, pruning, `skipInvariants`, `databaseBackend`** | No SDK app config; RPC nodes run archive-by-default; pruning model is reth's (role-driven, §6); no invariants; no DB backend choice needed by the operator | **delete** |

---

## 3. The storage decision

**Constraint (0.8, CONFIRMED):** execution datadir on local NVMe / direct-attached only;
consensus datadir may be network-attached. **Softener (0.2):** the DKG share is recoverable
from the network after loss — consensus-volume persistence buys reduced downtime (no missed
epochs waiting for recovery, no re-download), not correctness. **Sizes:** exec needs 1–2 TB
(RPC/archive) or 0.1–1 TB (validator); consensus state is small (order of GBs — exact figure
UNVERIFIED, §7 Q6).

### Options

**A. Local NVMe ephemeral + re-download on reschedule.**
Exec datadir on `emptyDir` backed by local-SSD ephemeral storage (GKE Standard:
`--ephemeral-storage-local-ssd`; 375 GiB units on N2, larger fixed units on C3/C4 Titanium).
Any pod deletion/eviction/node-drain loses the datadir; the init container re-runs
`tempo download`.
- *Recovery time:* download-bound. At the documented 1 Gbps floor, ~2.3 h/TB; at 10 Gbps,
  ~15 min/TB plus extraction (mitigated by `--resumable` and modular manifests). Validators
  with `--minimal` (≤100 GB) recover in minutes; a 2 TB archive RPC node is a multi-hour
  rebuild at 1 Gbps.
- *Cost:* local SSD ≈ $0.08/GiB·mo — cheaper than pd-ssd (~$0.17) and comparable to
  pd-balanced (~$0.10) while being the only *supported* option anyway.
- *Node pools:* per-role pools sized to the docs' hardware table; local SSD count fixed at
  pool creation; GKE node auto-upgrade recreates VMs and wipes local SSDs, so **every node
  upgrade is a re-download event** — surge upgrades + PDBs must be tuned so only one node's
  worth of pods rebuilds at a time.

**B. Local PersistentVolumes with node-affinity pinning.**
Static local PVs (or local-volume-provisioner) over the same NVMe, giving PVC semantics and
pod→node pinning.
- On GKE this buys almost nothing over A: node upgrades/repairs *recreate the VM and wipe
  local SSDs anyway*, so the data the pinning protects dies with the node it's pinned to; in
  exchange you gain a Pending-forever failure mode when the node goes away and manual PV
  cleanup toil. Worth it only on clusters where nodes are long-lived pets, which GKE nodes
  deliberately are not.

**C. PD-SSD / Hyperdisk anyway, accept degradation.**
Explicitly documented unsupported (0.8). Latency (~ms vs ~100 µs) and IOPS ceilings risk
falling behind head, missed DKG participation windows, and un-diagnosable consensus
flakiness — and puts you outside the chain team's support envelope on day one. Reject; not
worth "measuring" on mainnet infrastructure. (A moderato-only experiment could quantify it
if anyone insists — §7 Q6.)

**D. Split: exec on local NVMe ephemeral (as A) + consensus/identity on a small PVC.**
`--datadir` → emptyDir on local SSD; `--consensus.datadir` → RWO PVC (pd-balanced, tens of
GiB).
- *Recovery:* same as A for exec, but the BLS share survives reschedules → validator resumes
  signing immediately instead of idling "the following epochs" through share recovery; no
  window of reduced committee participation.
- *Cost:* A + a few dollars/month per node for the PVC.
- *Bonus:* the RWO PVC restores — deliberately, this time — the multi-attach fence that the
  upstream design got by accident, which §4 leans on as the structural double-sign guard.
- *Node pools:* identical to A.

### Recommendation

**D for validators; A-with-optional-consensus-PVC for RPC/archive, defaulted to D uniformly
for operational symmetry.** Concretely: exec datadir on local-SSD-backed `emptyDir`
(re-download on reschedule, `--minimal` for validators, `--full`/`--archive` for RPC roles),
consensus datadir on a per-ordinal RWO PVC. GKE **Standard** with per-role node pools is the
primary target. **Autopilot** is *conditionally* viable — recent Autopilot supports
local-SSD-backed ephemeral storage only through specific compute classes/machine series —
and must be validated as a Phase 3 item, not assumed (§7 Q10).

**Consequences (as the proposal demands):**
- `ScheduledVolumeSnapshot` and `StatefulJob` **die**. CSI snapshots can't capture local-SSD
  emptyDirs; snapshotting the consensus PVC protects state that is already
  network-recoverable. `tempo download` *is* the restore path now.
- `autoDataSource`/`dataSource` PVC seeding loses its purpose (and is a double-sign hazard
  for consensus volumes — §4 G6). Removed from the new CRD except as an explicitly
  non-validator escape hatch, or dropped entirely (§6 drops it).
- PVC auto-scaling survives, rescoped to the consensus PVC. The disk-usage sidecar
  additionally reports exec-volume fullness as status + events (it cannot be resized; the
  remedy is a bigger node shape or `--minimal`).

---

## 4. Double-signing safety analysis

**What signs:** the BLS12-381 share in `<consensus.datadir>` (rotates ~3 h via DKG), unlocked
by the static ed25519 key + encryption secret. Two *live* processes holding the same current
share and voting is equivocation within the threshold scheme. The validator set is
permissioned; whether equivocation is slashed or "only" a protocol fault is UNVERIFIED
(§7 Q5) — the guards below assume it is unacceptable either way.

**Upstream's posture, verified in source:** there is **no guard**. The operator never touches
signing keys (remote-signer delegation for Sentry; `priv_validator_key.json` appears nowhere
in the code). Rollout is delete-then-recreate with no wait-for-termination: `Delete()`
returns on API acceptance, the 3s requeue recreates. Several paths delete pods *outside* the
rollout budget (image-change-and-RPC-unreachable in `pod_control.go`, drift mitigation in a
different controller). The **only** mutual exclusion is accidental: the RWO PVC can't
multi-attach across nodes. The §3 decision removes that PVC from the exec path — so the port
must build the guard the upstream never had.

### Enumerated paths and guards

| # | Path to two concurrent key-holders | Guard |
|---|---|---|
| P1 | **Rolling update:** old pod `Terminating` on node A (grace period, slow unmount) while the recreate lands on node B | **G1 — termination barrier:** for `role: validator`, the create step is gated on the predecessor being *fully gone*: no pod object with the same instance name/UID in any phase (incl. `Terminating`) may exist before a replacement is posted. Delete → requeue → verify-absent → create, as distinct reconcile passes. `maxUnavailable` is forced to 1 for validators regardless of spec (CEL + controller clamp). |
| P2 | **Node NotReady / network partition:** kubelet unreachable, pod `Unknown`; controller (or GC, or a human with `--force`) replaces it while the isolated kubelet keeps the container running | **G2 — attach fencing + no force-delete:** the consensus RWO PVC (§3 D) is the hard fence — GCE PD refuses multi-attach, so the replacement sticks in `ContainerCreating` (Multi-Attach error) until the old node is actually fenced/repaired. The controller must (a) never force-delete validator pods, (b) treat a stuck-Pending replacement as *correct* behavior, surfacing phase `WaitingForFence` rather than escalating. Availability is sacrificed for safety; that is the right trade for a signer. Runbook (Phase 3) covers manual node fencing / non-graceful shutdown taints. |
| P3 | **Manual scale:** `replicas: 2` on a validator CRD ⇒ two pods mount the same signing-key Secret | **G3 — API-level invariant:** CEL validation rule `role != 'validator' \|\| replicas <= 1` on the CRD (plus the controller refusing to build ordinal >0 for validators, defense in depth). Horizontal validator scale-out is meaningless anyway — one validator identity = one node. |
| P4 | **Controller restart mid-rollout:** in-flight delete forgotten, restart reconciles from scratch and creates eagerly | **G4 — statelessness done right:** G1's verify-absent gate derives from API state only, so a restart re-enters the same gate. No in-memory rollout bookkeeping may influence validator creates. |
| P5 | **Cross-controller deletes:** drift mitigation (self-healing) deletes a validator pod outside `PodControl`'s view; upstream also had snapshot-candidate deletion | **G5 — single choke point:** all validator pod deletion/creation flows through the same gated code path; drift mitigation excludes `role: validator` pods (a lagging validator gets an event + phase, not an automated restart), and the snapshot controller is deleted anyway. |
| P6 | **PVC re-attach / restore:** consensus PVC cloned from a snapshot / `dataSource`-seeded into a second CRD or a restored instance while the original lives | **G6 — no seeded consensus volumes:** the TempoFullNode CRD carries no `dataSource`/`autoDataSource` for the consensus volume (§6 drops them); the docs' own model (share auto-recovers from the network) makes cloning worthless. Cross-CRD duplication of the *static key Secret* cannot be prevented by the operator — documented as an operational invariant: one signing-key Secret ⇒ referenced by exactly one TempoFullNode in one cluster. |
| P7 | **Blue/green cluster migration or a node run outside k8s with the same key** | Out of the operator's authority — runbook material: rotate the ed25519 key (docs: rotation preserves validator index/committee slot) when custody of a key is ambiguous. |

### DKG share survival across restart/reschedule

Under §3-D the share lives on the consensus PVC:

- **Pod restart, same node:** volume stays attached; share intact; node resumes signing
  immediately. Rotation cadence (~3 h) is irrelevant to restarts — the share on disk is the
  current one until the next ceremony replaces it.
- **Reschedule to another node:** PVC detaches/re-attaches (single-digit minutes); share
  intact. The exec datadir re-downloads in parallel (`--minimal`, minutes); the node misses
  at most the blocks during the restart window, and DKG-metric alerting (0.9: successes flat
  for 12 h = critical) catches pathological cases.
- **Consensus PVC lost entirely:** not fatal (0.2) — the node recovers a share "in the
  following epochs"; expect participation gap on the order of one-to-few DKG cycles
  (~3 h each). This is the accepted disaster case, documented, not designed around.
- **Static ed25519 key:** k8s Secret, mounted read-only at mode `0400`. The encryption
  secret (`--consensus.secret`) is FIFO-oriented (0.14): the pod design likely needs an
  entrypoint shim (`--consensus.secret <(cat /secrets/consensus-secret)` or a mkfifo
  sidecar) unless a plain file path is accepted — **must be resolved by container
  verification** (§7 Q3).

---

## 5. Fork strategy recommendation

**Recommendation: (a) hard fork — strip Cosmos entirely, single-purpose Tempo operator.**

Why, weighed against the stated criteria:

- **What you actually need:** one chain family (Tempo + its forks), stated explicitly in the
  proposal. Optionality toward Cosmos is worth nothing here; keeping it dormant violates the
  proposal's own working rule ("prefer deleting Cosmos code over leaving it dormant").
- **Blast radius / dead weight:** the Cosmos coupling is not a thin layer — it's `ChainSpec`
  + the TOML machinery + init chain + versioncheck, and versioncheck alone drags in the
  Cosmos SDK store/db dependency tree and the RocksDB CGO Dockerfile apparatus. Options (b)
  and (c) keep all of that compiling, building, and shipping in an image that runs your
  validators. A hard fork deletes ~40% of the coupled surface in one labelled commit and
  collapses the build to `CGO_ENABLED=0`.
- **Test surface:** (a) keeps only the tests for code that runs. (b) doubles CRD/controller
  test surface permanently. (c) is worst: the `ChainRuntime` interface itself needs contract
  tests plus two implementations' worth of suites, for one real user of the abstraction.
  Abstractions extracted with a single live implementation are guesses; this one would be a
  guess with a consensus-safety component.
- **Ability to pull upstream fixes — the honest merge-conflict accounting:**
  - *(a)* `git merge upstream` becomes impossible almost immediately (renamed API group,
    deleted packages). Upstream fixes arrive by **manual cherry-pick with conflicts**. This
    is a real, permanent cost — but bounded: the packages worth stealing fixes from
    (`internal/diff`, `internal/kube`, PVC machinery, `pod_control` rollout math) are the
    stable, slow-moving ones; the fast-moving upstream surface (TOML/config/versions
    handling — e.g. both of the two most recent upstream commits, #500 dynamic Comet ports
    and #499 musl toolchain) is precisely what gets deleted.
  - *(b)* merges stay *technically* possible but conflict constantly in the shared spine —
    `main.go`, `Makefile`, `Dockerfile`, `go.mod`, `config/` — the exact files both variants
    must touch. Every upstream Cosmos-side change also re-enters your CI/image whether you
    want it or not.
  - *(c)* the refactor to thread a `ChainRuntime` interface through pod/config/health/rollout
    code rewrites the very lines upstream keeps editing; merges conflict *worse* than (b)
    while also carrying (b)'s dead weight. It is the most expensive option on every axis
    except a hypothetical future third chain.
- **Practical rider:** keep `strangelove-ventures/cosmos-operator` configured as an
  `upstream` remote for cherry-pick archaeology (read-only; no rebase intent), and keep the
  deletions in dedicated commits so `git log --follow` survives for the files that remain.
  (The working-rules item about staying rebaseable applies only if (b)/(c) were chosen.)

---

## 6. Proposed `TempoFullNode` CRD

Group `tempo.aaronforce.io`, version `v1alpha1`, kind `TempoFullNode`. Presented as
commented Go types (kubebuilder markers abbreviated; full markers land in Phase 1).
Flag names marked ⚠ depend on §0 UNVERIFIED items.

```go
// TempoFullNodeSpec defines the desired state of a set of Tempo nodes.
type TempoFullNodeSpec struct {
	// Role selects the node's operating mode.
	// - validator: participates in consensus. Requires signingKey. Forces replicas <= 1,
	//   maxUnavailable = 1, and the double-sign guards (termination barrier, attach fence).
	//   Runs `tempo node` WITHOUT --follow. Snapshot profile defaults to "minimal".
	// - rpc: follower serving JSON-RPC (`--follow`). Snapshot profile defaults to "full".
	// - archive: follower retaining full history. Snapshot profile defaults to "archive".
	//   (reth-side pruning flags for rpc-vs-archive: open question Q8.)
	// +kubebuilder:validation:Enum=validator;rpc;archive
	// +kubebuilder:validation:Required
	Role NodeRole `json:"role"`

	// Replicas is the number of node instances (pods). Each gets a stable ordinal
	// identity, its own consensus PVC, and its own p2p service.
	// CEL: role != 'validator' || replicas <= 1   (double-sign guard G3)
	Replicas int32 `json:"replicas"`

	// Ordinals sets the starting ordinal (carried over from CosmosFullNode).
	// +optional
	Ordinals Ordinals `json:"ordinals,omitempty"`

	// ChainSpec identifies the chain and how the node is launched.
	ChainSpec TempoChainSpec `json:"chain"`

	// PodTemplate applies to all pods: image, resources, metadata, nodeSelector,
	// affinity, tolerations, priorityClass, probes, imagePullSecrets, and the
	// strategic-merge escape hatches (volumes, initContainers, containers).
	// Carried over from CosmosFullNode.PodSpec minus nothing; Probes.Strategy keeps
	// None|Reachable|InSync with Tempo-native semantics (InSync = eth_syncing false).
	PodTemplate PodSpec `json:"podTemplate"`

	// ScheduledUpgrades replaces halt-height version switching. Tempo hardforks
	// activate at wall-clock timestamps (network upgrades T1..Tn); the operator
	// rolls pods onto Image once now >= ActivatesAt - LeadTime, honoring the
	// rollout strategy and validator guards. Entries must be time-ascending.
	// +optional
	ScheduledUpgrades []ScheduledUpgrade `json:"scheduledUpgrades,omitempty"`

	// ExecVolume configures the execution datadir (`--datadir`).
	// MUST be node-local storage (Tempo forbids network-attached volumes for
	// execution state). Contents are disposable: re-populated by `tempo download`.
	ExecVolume ExecVolumeSpec `json:"execVolume"`

	// ConsensusVolume configures the consensus datadir (`--consensus.datadir`) as a
	// per-ordinal RWO PVC. Holds the DKG signing share; persistence avoids missed
	// epochs after reschedules and provides the multi-attach fence (guard G2).
	// No dataSource seeding is offered (guard G6).
	ConsensusVolume ConsensusVolumeSpec `json:"consensusVolume"`

	// SigningKey references the validator's key material. Required iff role=validator.
	// +optional
	SigningKey *SigningKeySpec `json:"signingKey,omitempty"`

	// SnapshotInit controls the `tempo download` init container.
	// +optional
	SnapshotInit SnapshotInitSpec `json:"snapshotInit,omitempty"`

	// RPC configures the JSON-RPC server flags.
	// +optional
	RPC RPCSpec `json:"rpc,omitempty"`

	// P2P configures networking/discovery. ⚠ flag names pending `tempo node --help`.
	// +optional
	P2P P2PSpec `json:"p2p,omitempty"`

	// Telemetry configures metrics and the unified telemetry exporter.
	// +optional
	Telemetry TelemetrySpec `json:"telemetry,omitempty"`

	// Service configures per-pod p2p exposure and the aggregate RPC Service
	// (carried over: maxP2PExternalAddresses, p2pTemplate, rpcTemplate, clusterDomain).
	// +optional
	Service ServiceSpec `json:"service,omitempty"`

	// Strategy is the rolling-update budget (maxUnavailable, default 25%).
	// Clamped to 1 for role=validator.
	// +optional
	Strategy RolloutStrategy `json:"strategy,omitempty"`

	// SelfHeal carries over pvcAutoScale (consensus PVC only) and
	// heightDriftMitigation (eth_blockNumber-based; never deletes validator pods).
	// +optional
	SelfHeal *SelfHealSpec `json:"selfHeal,omitempty"`

	// InstanceOverrides: per-instance disable/image/nodeSelector/consensusVolume
	// overrides, keyed by pod name (carried over; volumeClaimTemplate override now
	// targets the consensus volume; adds externalAddress override for p2p).
	// +optional
	InstanceOverrides map[string]InstanceOverridesSpec `json:"instanceOverrides,omitempty"`

	// VolumeRetentionPolicy: Retain|Delete for consensus PVCs on scale-down. Default Delete.
	// +optional
	VolumeRetentionPolicy *RetentionPolicy `json:"volumeRetentionPolicy,omitempty"`

	// ServiceAccountName (carried over). NOTE: with versioncheck deleted, the operator
	// may no longer need per-CRD RBAC at all; kept as escape hatch pending Phase 1.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
}

type TempoChainSpec struct {
	// Chain is the target network, passed to `--chain` on both `tempo node` and
	// `tempo download`. Known values: mainnet, moderato. ("devnet": unverified, Q2.)
	// Free-form string (not enum) so Tempo forks can pass their own chain names/paths.
	// +kubebuilder:validation:MinLength=1
	Chain string `json:"chain"`

	// AdditionalArgs are appended verbatim to `tempo node` — THE escape hatch
	// replacing TomlOverrides. Also the interim path for any flag not yet modeled.
	// +optional
	AdditionalArgs []string `json:"additionalArgs,omitempty"`

	// AdditionalDownloadArgs are appended to `tempo download` (e.g. --manifest-url).
	// +optional
	AdditionalDownloadArgs []string `json:"additionalDownloadArgs,omitempty"`

	// Env sets extra environment variables on the node container (config is
	// "CLI flags and env vars"; e.g. RUST_LOG — verbosity flag shape is Q9).
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Follow overrides the role-derived default for `--follow` (validator=false,
	// rpc/archive=true). Exists for standby validators (Q11). ⚠
	// +optional
	Follow *bool `json:"follow,omitempty"`
}

type ScheduledUpgrade struct {
	// ActivatesAt is the network activation time (from Tempo release notes).
	// The operator begins rolling Image at ActivatesAt - LeadTime.
	ActivatesAt metav1.Time `json:"activatesAt"`
	// Image is the full image ref to run from that point on.
	Image string `json:"image"`
	// LeadTime is how long before activation the roll starts. Default 6h.
	// +optional
	LeadTime *metav1.Duration `json:"leadTime,omitempty"`
}

type ExecVolumeSpec struct {
	// Source of the node-local volume:
	// - EmptyDir (default): local-SSD-backed ephemeral storage; SizeLimit recommended.
	// - HostPath / generic ephemeral PVC with a local-storage class: escape hatches
	//   for non-GKE topologies.
	// Validation rejects network-attached storage classes it can recognize; ultimately
	// the storage constraint is the operator of the cluster's responsibility.
	// +optional
	EmptyDir *corev1.EmptyDirVolumeSource `json:"emptyDir,omitempty"`
	// +optional
	Ephemeral *corev1.EphemeralVolumeSource `json:"ephemeral,omitempty"`
}

type ConsensusVolumeSpec struct {
	// PVC template: storageClassName (required), resources (grow-only),
	// accessModes (default+enforced RWO — the multi-attach fence), metadata.
	// Deliberately NO dataSource/autoDataSource (guard G6).
	StorageClassName string                      `json:"storageClassName"`
	Resources        corev1.ResourceRequirements `json:"resources"`
	// +optional
	Metadata Metadata `json:"metadata,omitempty"`
}

type SigningKeySpec struct {
	// SigningKeySecret names a Secret whose `signing-key` entry is the encrypted
	// ed25519 consensus signing key (output of `tempo consensus generate-signing-key`).
	// Mounted read-only, file mode 0400, and passed via --consensus.signing-key.
	SigningKeySecret corev1.SecretKeySelector `json:"signingKeySecret"`
	// EncryptionSecret names the Secret entry holding the key-encryption secret,
	// delivered to --consensus.secret. Delivery mechanism (plain file vs FIFO shim)
	// is decided by Q3; either way the value never lands in a ConfigMap, env var,
	// image, or log.
	EncryptionSecret corev1.SecretKeySelector `json:"encryptionSecret"`
	// FeeRecipient, if Tempo still honors it (flag documented as deprecated). ⚠
	// +optional
	FeeRecipient *string `json:"feeRecipient,omitempty"`
}

type SnapshotInitSpec struct {
	// Policy: Auto (default; run `tempo download` only when the exec datadir is
	// empty/unpopulated), Always (--force: refresh data, preserving discovery-secret
	// and known-peers.json), Never (operator provides no init sync).
	// +kubebuilder:validation:Enum=Auto;Always;Never
	// +optional
	Policy SnapshotInitPolicy `json:"policy,omitempty"`
	// Profile: minimal|full|archive → the corresponding tempo download flag.
	// Defaults by role (validator→minimal, rpc→full, archive→archive).
	// +optional
	Profile *SnapshotProfile `json:"profile,omitempty"`
	// URL / ManifestURL override the default snapshot source (-u / --manifest-url).
	// +optional
	URL *string `json:"url,omitempty"`
	// +optional
	ManifestURL *string `json:"manifestURL,omitempty"`
}

type RPCSpec struct {
	// Enabled controls --http. Default: true for rpc/archive, false for validator.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
	// Port for --http.port. Default 8545. Addr is always 0.0.0.0 in-pod.
	// +optional
	Port *int32 `json:"port,omitempty"`
	// APIs for --http.api. Default: eth,net,web3,txpool,trace.
	// +optional
	APIs []string `json:"apis,omitempty"`
	// WS: --ws / --ws.port if verified to exist. ⚠ (Q1)
	// +optional
	WS *WSSpec `json:"ws,omitempty"`
}

type P2PSpec struct {
	// Port for p2p listen + discovery (--port / --discovery.port). Default 30303. ⚠
	// +optional
	Port *int32 `json:"port,omitempty"`
	// MaxPeers etc. deferred to additionalArgs until flags are verified. ⚠
}

type TelemetrySpec struct {
	// MetricsPort: --metrics <port>. Default 9000. The consensus (8002) and sync
	// (6060) metric endpoints are exposed as container ports if their provenance
	// is confirmed (Q7).
	// +optional
	MetricsPort *int32 `json:"metricsPort,omitempty"`
	// TelemetryURL: --telemetry-url. Optional; secret-shaped values go through
	// TelemetryURLSecret instead so tokens never appear in the pod spec.
	// +optional
	TelemetryURL *string `json:"telemetryURL,omitempty"`
	// +optional
	TelemetryURLSecret *corev1.SecretKeySelector `json:"telemetryURLSecret,omitempty"`
	// MetricsInterval: --telemetry-metrics-interval (default 10s upstream).
	// +optional
	MetricsInterval *metav1.Duration `json:"metricsInterval,omitempty"`
}

// TempoFullNodeStatus carries over: observedGeneration, phase
// (adds WaitingForFence for guard G2), statusMessage, per-pod sync
// {height, inSync, error, timestamp} sourced from eth_syncing/eth_blockNumber,
// selfHealing.pvcAutoScale, and adds upgrade tracking
// {currentImage, nextUpgrade activatesAt/image}.
// Dropped from FullNodeStatus: peers (CometBFT strings), scheduledSnapshotStatus,
// the separate height map (folded into sync).
```

### Every dropped `CosmosFullNode` field and why

| Dropped field | Why |
|---|---|
| `type` (FullNode/Sentry) | replaced by `role`; sentry/remote-signer architecture doesn't exist on Tempo |
| `chain.chainID`, `chain.network` | `--chain <name>` carries both meanings; no genesis chain-id to configure |
| `chain.binary`, `chain.homeDir` | single known binary `tempo`; datadirs are explicit volumes/flags, not `--home` |
| `chain.config.*` (CometConfig: laddrs, peers, seeds, private/unconditional peer IDs, max in/outbound, CORS, `overrides` TOML) | no config.toml; peers/discovery are node-managed (`known-peers.json`); modelled flags + `additionalArgs` cover the rest |
| `chain.app.*` (minGasPrice, pruning, haltHeight, CORS toggles, `overrides` TOML) | no app.toml; no SDK gas floor; pruning is role/profile-driven (Q8); halt-height has no analogue (0.12) |
| `chain.genesisURL/genesisScript` | no genesis.json |
| `chain.addrbookURL/addrbookScript` | no addrbook.json |
| `chain.snapshotURL/snapshotScript` | replaced by structured `snapshotInit` (`tempo download`) |
| `chain.logLevel/logFormat` | CometBFT flag shapes; Tempo/reth verbosity handled via env/args pending Q9 |
| `chain.skipInvariants` | x/crisis only |
| `chain.privvalSleepSeconds` | privval only |
| `chain.databaseBackend` | existed to open the Cosmos DB offline (versioncheck); dead with it |
| `chain.versions[]` (+`setHaltHeight`) | height-gated upgrades replaced by `scheduledUpgrades` (timestamp-gated, 0.12) |
| `chain.additionalInitArgs` | no `<binary> init`; `additionalDownloadArgs` is the analogous knob |
| `volumeClaimTemplate` (top-level, exec state on PVC) | split into `execVolume` (local, disposable) + `consensusVolume` (PVC); the single-PVC state model is the thing Tempo forbids |
| `volumeClaimTemplate.dataSource`/`autoDataSource` | VolumeSnapshot seeding dead (§3); consensus seeding forbidden (G6) |
| `service.maxP2PExternalAddresses` semantics tied to `p2p.external_address` TOML | the *k8s* half (per-pod LB services) is kept; the config.toml write-back is dead; how an external address is told to tempo (if at all) is Q4 |
| `status.peers` | CometBFT persistent-peer strings; nothing consumes an equivalent |
| `status.scheduledSnapshotStatus` | volsnapshot handshake, dead with the CRD |
| `instanceOverrides[].externalAddress` | kept, but re-pointed at p2p service annotation rather than TOML |

---

## 7. Open questions

Things I had to leave undecided rather than invent. Q1–Q5 block Phase 1 CRD details;
the rest can resolve during Phase 1–2.

1. **Full `tempo node --help` flag inventory (0.4).** Docs don't list p2p/discovery
   (`--port`, `--discovery.addr/.port`), WS (`--ws*`), or verbosity flags. Needs one run of
   the container (`ghcr.io/tempoxyz/tempo:v1.11.0`) in an environment with a container
   runtime — this session had none. Everything marked ⚠ in §6 hangs on this.
2. **Does `--chain devnet` exist (0.5)?** Docs show only mainnet/moderato. Also: can
   `--chain` take a path/spec file for private Tempo forks (affects how "compatible forks"
   are onboarded — §6 keeps the field free-form on this bet)?
3. **`--consensus.secret` delivery (0.14):** does it accept a plain file path (a normal
   Secret mount), or FIFO-only (requiring an entrypoint shim / process substitution in the
   container command)? Determines the signing-key mount design and whether the node container
   needs a shell.
4. **Stable network identity:** `discovery-secret` lives in the (now ephemeral) exec
   datadir. Is per-pod identity loss on reschedule acceptable (peers re-learn via
   discovery), or should the operator persist `discovery-secret` (e.g. seed it from the
   consensus PVC or a Secret via init container)? Related: does Tempo need/support an
   advertised external address for NAT'd k8s pods (upstream wrote `p2p.external_address`;
   the per-pod LB Services currently reserve addresses with no way to announce them)?
5. **Double-sign consequences:** is equivocation slashed / does it eject the validator from
   the permissioned set, or is it tolerated by threshold aggregation? Doesn't change the §4
   guards, but sets how loudly the runbook screams and whether G2's availability sacrifice
   is negotiable.
6. **Real snapshot sizes and consensus-state size** for mainnet/moderato (minimal vs full vs
   archive) — needed to size node pools, the consensus PVC default, and to state §3 recovery
   times as numbers instead of bandwidth math. (`snapshots.tempo.xyz` has the data.)
7. **Health signal of record:** confirm `eth_syncing` behaves like stock reth under
   Commonware consensus (returns `false` at head), whether `--follow` nodes expose the
   consensus metrics endpoint at all, and where port 8002 (consensus metrics) comes from —
   fixed, or a flag. Decides whether the sidecar readiness for validators should combine
   `eth_syncing` + block-age + `consensus_engine_marshal_processed_height` progress.
8. **rpc vs archive at the node level:** docs say RPC nodes are archive-by-default with no
   pruning. Are there pruning flags that make a leaner `rpc` role real, or do rpc/archive
   differ only in snapshot profile? If the latter, the role enum may collapse to
   validator|follower + profile.
9. **Log level/format control:** reth uses `-v/-vvv` and `RUST_LOG`; does tempo expose
   `--log.*` flags? (Currently punted to `env` + `additionalArgs`.)
10. **GKE Autopilot viability:** confirm current Autopilot support for local-SSD-backed
    ephemeral storage (compute classes / machine-series constraints, max local SSD per pod)
    and for the 30303/UDP LoadBalancer exposure. Until verified, GKE Standard is the target
    and Autopilot is "ideally yes, unproven".
11. **Standby validators:** docs mention "RPC and Standby Nodes". What makes a node
    "standby" — a follower with the signing key staged but not loaded? If Tempo has a
    sanctioned standby/failover pattern, it belongs in the CRD (`role: standby`?) and
    interacts directly with the §4 guards. Not modelled yet.
12. **Dependency baseline:** upstream pins k8s v0.25.5 / controller-runtime v0.13.1
    (2022-era). Hard-forking is the natural moment to bump — but that's a scope decision
    (touches every controller signature) that you should make explicitly, not one I'm
    smuggling into a port.
13. **`known-peers.json` bootstrap:** for private forks with no public bootnodes, how are
    initial peers supplied (flag? file the operator must place?) — upstream's
    persistent-peers machinery is deleted, and nothing replaces it yet.

---

*End of Phase 0 analysis. Per the proposal: stopping here for review; no Phase 1 work has
been started.*
