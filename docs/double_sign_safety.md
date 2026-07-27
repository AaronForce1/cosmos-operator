# Double-Sign Safety Model

Two live `tempo` processes holding the same current DKG signing share and voting is equivocation.
This operator is designed so that, within one cluster, no automated path can produce two
concurrent holders of a validator's key material. This document is the contract: what the
operator guarantees, what it deliberately refuses to do, and what remains your responsibility.

## What signs

The BLS12-381 signing *share* in the consensus datadir (rotated by on-chain DKG ~every 3 h),
unlocked by the static ed25519 signing key + encryption secret mounted from Secrets. The share
lives on a per-ordinal ReadWriteOnce PVC managed by the operator.

## Guards

| # | Threat path | Guard |
|---|---|---|
| G1 | Rolling update overlap: old pod still terminating while the replacement starts elsewhere | Pod creates derive purely from API state: a pod is created only when no pod object with that name exists in any phase, including `Terminating`. For validators, no *new* delete is issued while a previous pod is still terminating. |
| G2 | Node NotReady / network partition: kubelet unreachable, pod `Unknown`, but the container may still be running | The consensus RWO PVC is a hard fence — the replacement cannot attach the volume while the old node holds it, so it sticks in `ContainerCreating` (Multi-Attach). The operator **never force-deletes** validator pods and reports phase `WaitingForFence` instead of escalating. Availability is sacrificed for key safety. |
| G3 | Manual scale-out: `replicas: 2` on a validator | Rejected at the API server by CEL validation; the controller additionally clamps pod/PVC construction to one instance. |
| G4 | Controller restart mid-rollout | There is no in-memory rollout bookkeeping that influences creates; a restarted controller re-derives everything from API state and re-enters the same gates. |
| G5 | Cross-controller deletes (self-healing) | Height-drift mitigation never deletes validator pods; a lagging validator surfaces as an event. All validator pod deletion flows through the single gated PodControl path. |
| G6 | Cloned consensus state | The CRD offers **no** `dataSource`/`autoDataSource` for the consensus volume, and no VolumeSnapshot machinery exists in this operator. There is nothing to clone a share from. |

The rollout budget (`maxUnavailable`) is clamped to 1 for validators regardless of
`spec.strategy`.

## WaitingForFence: what to do

`status.phase: WaitingForFence` means a validator pod has been terminating past its grace period
— the classic signature of a failed or partitioned node. The old container may still be running
and signing on the isolated node. The operator is *correctly* refusing to start a replacement.

Do **not**:

- `kubectl delete pod --force --grace-period=0` — this removes the API object while the container
  may still run, and unblocks a second signer. This is the exact accident the guard exists for.

Do:

1. Determine whether the node is actually dead (cloud console, serial output) or partitioned.
2. If dead: delete/repair the node via your cloud provider so kubelet deregistration (or the
   cloud's volume fencing) confirms the workload is gone. On Kubernetes ≥1.26 you may use the
   non-graceful node shutdown taint (`node.kubernetes.io/out-of-service`) **only after** you have
   confirmed the machine is powered off.
3. Once the pod object is garbage-collected and the PVC detaches, the operator creates the
   replacement automatically.

The trade is explicit: a fenced validator is *down* until a human (or cloud automation) confirms
the old machine is dead. Downtime is recoverable; equivocation may not be.

## What the operator cannot protect

- **The same signing-key Secret referenced by two TempoFullNodes, two namespaces, or two
  clusters.** Kubernetes gives the operator no visibility into other clusters and no authority
  over Secret reuse. Operational invariant: one signing-key Secret → exactly one TempoFullNode →
  exactly one cluster.
- **A node run outside Kubernetes with the same key** (bare-metal failover, blue/green cluster
  migration). If custody of a key is ever ambiguous, rotate it
  (see [key_management.md](key_management.md)) before starting any new node with it.
- **Standby validators.** There is deliberately no `standby` role: a hot standby holding live key
  material is a double-sign machine waiting for a split-brain. If Tempo ships a sanctioned
  standby/failover mechanism, it can be modeled then.

## Consequences of equivocation

The Tempo validator set is permissioned; whether equivocation is slashed or "only" a protocol
fault at the threshold-signature layer is not documented publicly. This operator assumes it is
unacceptable either way and does not treat the answer as a tunable.
