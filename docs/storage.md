# Storage Prerequisites and Node-Pool Setup

Tempo splits node state into two datadirs with very different requirements, and the operator's
volume model follows that split:

| | Execution datadir (`--datadir`) | Consensus datadir (`--consensus.datadir`) |
|---|---|---|
| CRD field | `spec.execVolume` | `spec.consensusVolume` |
| Volume type | node-local `emptyDir` (default) or generic ephemeral volume | per-ordinal ReadWriteOnce PVC |
| Storage medium | **local NVMe / direct-attached only** | network-attached is fine (pd-balanced etc.) |
| Size | 100 GB–1 TB (validator), 1–2 TB+ (rpc/archive) | tens of GiB |
| On loss | re-seeded by `tempo download` (init container) | DKG share recovered from the network over the following epochs |
| Snapshots/backup | none — `tempo download` *is* the restore path | none needed (state is network-recoverable) |

**Tempo does not support network-attached storage (GCP PD, EBS, Azure Managed Disk, NAS, SAN)
for the execution datadir.** This is why the operator deliberately has no PVC option for it, and
why the VolumeSnapshot machinery from the Cosmos lineage of this operator was removed.

## The trade the operator makes

Execution state is treated as **disposable**: any pod reschedule, node upgrade, or eviction wipes
it, and the `snapshot-init` init container re-downloads a snapshot on the next start
(`--resumable` downloads; identity files `discovery-secret` and `known-peers.json` are preserved
by `tempo download`). Recovery time is download-bound:

- validator (`minimal` profile, ≤ ~100 GB): minutes at 10 Gbps.
- rpc/archive (1–2 TB): tens of minutes at 10 Gbps; hours at 1 Gbps. Plan `maxUnavailable` and
  node-upgrade surge settings so only one node's worth of pods rebuilds at a time.

Consensus state is kept on a small PVC so the DKG signing share survives reschedules — a
validator resumes signing immediately instead of idling one-to-few DKG cycles (~3 h each) while
a new share is recovered. The RWO attach semantics of that PVC also serve as a double-sign fence
(see [double_sign_safety.md](double_sign_safety.md)).

## GKE Standard node pools

Create per-role node pools with local NVMe SSD ephemeral storage:

```sh
gcloud container node-pools create tempo-rpc \
  --cluster <cluster> \
  --machine-type n2-standard-32 \
  --ephemeral-storage-local-ssd count=6 \   # 6 x 375 GiB = 2.25 TiB
  --node-labels tempo.aaronforce.io/pool=rpc
```

- Local SSD count is fixed at pool creation; size the pool for the role's dataset plus headroom.
- With `--ephemeral-storage-local-ssd`, pod `emptyDir` volumes and ephemeral-storage requests are
  served from the local NVMe. Set both `spec.execVolume.emptyDir.sizeLimit` and
  `spec.podTemplate.resources.requests.ephemeral-storage` so the scheduler places pods correctly.
- Pin pods with `spec.podTemplate.nodeSelector` (e.g. the label above or
  `cloud.google.com/gke-ephemeral-storage-local-ssd: "true"`).
- **GKE node auto-upgrades recreate VMs and wipe local SSDs.** Every node upgrade is a
  re-download event. Use surge upgrades (`maxSurge=1, maxUnavailable=0`) and a PodDisruptionBudget
  sized to your replica count.
- Run `chrony`/`ntpd`-equivalent time sync (GKE nodes do by default) — consensus requires
  accurate clocks.

For the consensus PVC, any RWO storage class works; `standard-rwo` (pd-balanced) is a fine
default. Prefer a storage class with `volumeBindingMode: WaitForFirstConsumer` so the volume
lands in the same zone the scheduler picks for the pod.

### GKE Autopilot

Autopilot supports local-SSD-backed ephemeral storage only through specific compute classes and
machine series, with per-pod maximums. Treat Autopilot as unvalidated for Tempo workloads until
you have confirmed (a) ephemeral storage on local NVMe for your required size, and (b)
LoadBalancer exposure for 30303/UDP. GKE Standard is the supported target.

## Other clouds / bare metal

Use `spec.execVolume.ephemeral` with a node-local storage class (e.g. a local-volume
provisioner or OpenEBS LocalPV) when `emptyDir` is unsuitable. The operator cannot verify that a
storage class is actually node-local — keeping execution state off network-attached volumes is
the cluster operator's responsibility.

## Sizing and monitoring

- The consensus PVC supports grow-only resizing and optional auto-scaling
  (`spec.selfHeal.pvcAutoScale`), using the healthcheck sidecar's disk-usage endpoint.
- The execution volume **cannot** be resized: when it approaches capacity, move to a bigger node
  shape or a smaller snapshot profile. Watch node ephemeral-storage metrics and the node's own
  metrics endpoints (`:9000/metrics`).
