# tempo-operator Helm chart

Installs the `TempoFullNode` CRD and the Tempo operator.

```sh
helm install tempo-operator ./charts/tempo-operator \
  --namespace tempo-operator-system --create-namespace
```

The CRD lives in `crds/` and is installed by Helm on first install. Per Helm's CRD policy it is
**not upgraded** by `helm upgrade`; apply CRD changes explicitly when upgrading the operator:

```sh
kubectl apply -f charts/tempo-operator/crds/
```

`crds/` is kept in sync with `config/crd/bases/` by `make manifests` (CI verifies there is no
drift).

## Values

| Key | Default | Description |
|---|---|---|
| `image.repository` | `ghcr.io/aaronforce1/tempo-operator` | Operator image |
| `image.tag` | chart `appVersion` | Operator image tag |
| `image.pullPolicy` | `IfNotPresent` | |
| `imagePullSecrets` | `[]` | |
| `replicaCount` | `1` | Requires `leaderElection` when > 1 |
| `leaderElection` | `true` | `--leader-elect` plus the namespace Role/RoleBinding |
| `logLevel` / `logFormat` | `info` / `console` | Operator logging |
| `serviceAccount.create` / `.name` / `.annotations` | `true` / `""` / `{}` | |
| `rbac.create` | `true` | ClusterRole/Binding + leader-election Role/Binding |
| `metricsService.enabled` | `false` | Expose controller metrics (`:8080`) as a Service |
| `resources`, `nodeSelector`, `tolerations`, `affinity`, `priorityClassName`, `podAnnotations`, `podLabels` | | Standard scheduling/spec passthroughs |

After installation, deploy nodes with the samples in
[`config/samples/`](../../config/samples/) — see the repo [README](../../README.md) and
[`docs/`](../../docs/) for storage prerequisites (node-local NVMe for the execution datadir)
and validator key handling.
