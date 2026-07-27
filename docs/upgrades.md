# Upgrade Runbook

Tempo upgrades are **timestamp-activated hardforks**: each T-series release activates at a fixed
unix timestamp per network. There is no halt-height, no on-chain upgrade module, and no
crash-and-swap dance — a node must simply be running the new binary *before* the activation
instant. Nodes still on the old binary do not halt; they keep running and fork off the network.

## Scheduling an upgrade

When a release is announced (image + activation timestamps per network), add an entry to
`spec.scheduledUpgrades`:

```yaml
spec:
  podTemplate:
    image: ghcr.io/tempoxyz/tempo:v1.11.0
  scheduledUpgrades:
    - activatesAt: "2026-08-30T14:00:00Z"   # from the release notes, for YOUR network
      image: ghcr.io/tempoxyz/tempo:v1.12.0
      leadTime: 6h                          # optional; default 6h
```

Behavior:

- At `activatesAt - leadTime` the operator starts a normal rolling update onto the new image,
  honoring `spec.strategy.maxUnavailable` (clamped to 1 for validators) and all double-sign
  guards. The snapshot-init container uses the same image.
- `status.upgrade` reports `currentImage` and the next pending upgrade.
- After activation has passed you can fold the image into `spec.podTemplate.image` and drop the
  entry; keeping past entries is harmless (the latest applicable one wins).

Pick a `leadTime` comfortably larger than (replicas × per-pod restart-and-resync time), and
avoid lead times so large that the new binary would misbehave on the old rules — follow the
release notes' guidance ("run vX before the activation timestamp").

## Manual / emergency upgrade

Setting `spec.podTemplate.image` directly triggers an immediate rolling update under the same
guards. Use this for non-fork patch releases.

## Checklist per network upgrade

1. Read the release notes; note the activation timestamp for each network you run.
2. Add `scheduledUpgrades` entries to every `TempoFullNode` (validators first in staging,
   then production RPC, then production validators — or your preferred order).
3. Confirm `status.upgrade.nextActivatesAt`/`nextImage` reflect the plan.
4. Before the roll window opens, check all nodes are in sync (`status.sync`); a rollout cannot
   make progress if no pods are ready.
5. During the roll, watch pod events and `status.phase`. For validators, one pod restarts; expect
   a signing gap of one restart (seconds to minutes with a `minimal` re-download, none if the pod
   restarts on the same node).
6. After activation, verify heights keep advancing and consensus metrics
   (`consensus_engine_marshal_finalized_height`) progress. A node that forked off will show
   `eth_syncing=false` with a stale latest block — the operator's health checks mark it not
   in-sync (block-age guard) and it drops out of the RPC service.

## Failure modes

- **Missed the deadline:** the node forks off at activation. Fix: update the image (the operator
  rolls it), let snapshot-init refresh state if the node cannot reorg back — set
  `spec.snapshotInit.policy: Always` temporarily to force a refresh, then revert to `Auto`.
- **Roll stuck because no pods are in sync:** the rollout budget computes from in-sync counts
  with a reachable fallback; if the whole fleet is unhealthy, fix health first — do not
  force-delete validator pods (see [double_sign_safety.md](double_sign_safety.md)).
