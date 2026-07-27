# Key Management and Rotation

A Tempo validator has three pieces of key material with different lifecycles:

| Key | Algorithm | Where it lives | Managed by |
|---|---|---|---|
| Static consensus signing key | ed25519 (encrypted at rest) | Kubernetes Secret, mounted read-only | you |
| Key-encryption secret | opaque | Kubernetes Secret, mounted read-only | you |
| DKG signing share | BLS12-381 | `<consensus datadir>/consensus` on the consensus PVC | the node (rotated by on-chain DKG ~every 3 h) |

The operator never reads, copies, or logs key material. It only mounts the Secrets you reference.

## Generating and installing the static key

Generate out of band (never inside a manifest or CI log):

```sh
tempo consensus generate-signing-key --output signing-key --secret <(cat encryption-secret)
```

Create one Secret holding both entries (separate Secrets are also supported):

```sh
kubectl create secret generic tempo-validator-keys \
  --from-file=signing-key=./signing-key \
  --from-file=consensus-secret=./encryption-secret
```

Reference it from the CRD:

```yaml
spec:
  role: validator
  signingKey:
    signingKeySecret: {name: tempo-validator-keys, key: signing-key}
    encryptionSecret: {name: tempo-validator-keys, key: consensus-secret}
```

The operator mounts both entries via a projected volume at `/etc/tempo-keys` with file mode
0400 (kubelet widens group permissions per `fsGroup`), read-only, into the node container only —
not into init containers or the healthcheck sidecar — and passes
`--consensus.signing-key /etc/tempo-keys/signing-key --consensus.secret /etc/tempo-keys/consensus-secret`.

> **Verify against your Tempo release:** the docs describe `--consensus.secret` accepting a FIFO /
> process substitution. This operator passes a regular (read-only, tmpfs-backed) file path. If
> your `tempo` build rejects plain file paths for `--consensus.secret`, stop and file an issue —
> the mount design needs an entrypoint shim in that case.

**Invariant: one signing-key Secret is referenced by exactly one TempoFullNode in exactly one
cluster.** The operator cannot detect cross-cluster duplication; violating this invariant is a
double-signing risk (see [double_sign_safety.md](double_sign_safety.md)).

Prefer sourcing the Secret from an external manager (GCP Secret Manager + External Secrets,
Vault, sealed-secrets) rather than `kubectl create secret` from a workstation.

## The DKG signing share

The share is derived by an on-chain DKG ceremony roughly every 3 hours and written by the node to
the consensus datadir, which the operator keeps on a per-ordinal RWO PVC:

- **Pod restart / reschedule:** the PVC re-attaches; the share survives; signing resumes
  immediately.
- **PVC lost entirely:** not fatal. The node recovers a share from the network "in the following
  epochs" — expect a participation gap on the order of one-to-few DKG cycles (~3 h each). This is
  the accepted disaster case; do not build backup tooling that copies the share elsewhere.

Monitor DKG health via the consensus metrics
(`consensus_engine_dkg_manager_ceremony_successes_total` etc.); alert if successes are flat for
12 hours.

## Rotating the static key

Rotate when custody of the key is ambiguous (personnel change, suspected leak, migration between
clusters). Per Tempo docs, rotation preserves the validator's index/committee slot.

1. Generate a new signing key + encryption secret (as above).
2. Follow the chain's rotation procedure to register the new key.
3. Update the Kubernetes Secret (`kubectl apply` a new version or update via your secret manager).
4. Restart the pod so the node picks up the new files: `kubectl delete pod <validator-pod>`.
   The operator recreates it; secret volumes are re-projected on pod start.
5. Decommission the old key material everywhere.

## Telemetry tokens

If `--telemetry-url` embeds a token, use `spec.telemetry.telemetryURLSecret` instead of
`spec.telemetry.telemetryURL`; the value is injected via an environment variable sourced from the
Secret and referenced as `$(TEMPO_TELEMETRY_URL)` in the args, so the literal never appears in
the pod spec.
