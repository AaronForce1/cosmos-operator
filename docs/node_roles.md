# Node Roles

Every `TempoFullNode` declares exactly one `spec.role`. The role drives defaults across the whole
resource: the `--follow` flag, snapshot profile, JSON-RPC exposure, and the safety guards.

| | `validator` | `rpc` | `archive` |
|---|---|---|---|
| Consensus participation | Signs blocks (in-process) | Follower | Follower |
| `tempo node --follow` | no | yes | yes |
| `spec.signingKey` | **required** | forbidden by convention | forbidden by convention |
| Replicas | **max 1** (CRD-enforced) | any | any |
| Snapshot profile default | `minimal` | `full` | `archive` |
| JSON-RPC service | not created | created | created |
| Rollout budget | clamped to 1 | `spec.strategy.maxUnavailable` | `spec.strategy.maxUnavailable` |
| Height-drift mitigation | never deletes pods | may delete lagging pods | may delete lagging pods |
| Double-sign guards | active (see [double_sign_safety.md](double_sign_safety.md)) | n/a | n/a |

## validator

Runs `tempo node` without `--follow`, with `--consensus.signing-key` and `--consensus.secret`
pointed at the mounted Secret (see [key_management.md](key_management.md)).

One validator identity maps to exactly one node. The CRD rejects `replicas > 1` (CEL validation)
and the controller additionally clamps pod/PVC construction to one instance. Horizontal scaling
of a validator is meaningless and dangerous; to run several validators, create several
`TempoFullNode` resources, each with its own key material.

The Tempo validator set is permissioned; registering the validator (address, pubkey, ingress and
egress IPs, fee recipient) happens out of band with the Tempo team.

The JSON-RPC server still runs inside the pod — the healthcheck sidecar and the operator's status
collection depend on `eth_syncing`/`eth_blockNumber` — but no RPC Service is created and the port
is not meant to be exposed. Use NetworkPolicy to restrict pod-IP access if your threat model
requires it.

## rpc

A follower serving JSON-RPC (`--follow`). Per current Tempo docs, RPC nodes are trustless by
default and require the upstream to provide consensus finalization certificates.

Note: Tempo RPC nodes currently retain full history (archive-by-default, no pruning), so `rpc`
vs `archive` today differs only in the snapshot profile used for bootstrap. The roles are kept
separate so a leaner rpc profile can be adopted when/if pruning flags land.

## archive

A follower retaining full history, bootstrapped from the `archive` snapshot profile. Size the
execution volume for the full archive dataset (2 TB+ NVMe recommended; see
[storage.md](storage.md)).

## Role immutability

`spec.role` is immutable (CEL-enforced). Changing a node set's role means new storage semantics,
new guard behavior, and possibly key material — create a new `TempoFullNode` instead.
