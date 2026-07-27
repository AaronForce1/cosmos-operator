/*
Copyright 2022 Strangelove Ventures LLC.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func init() {
	SchemeBuilder.Register(&TempoFullNode{}, &TempoFullNodeList{})
}

// TempoFullNodeController is the canonical controller name.
const TempoFullNodeController = "TempoFullNode"

// Well-known default ports for a Tempo node.
//
// The p2p/discovery flag names and defaults come from the reth-derived CLI and
// must be re-verified against `tempo node --help` for each new chain release.
const (
	// DefaultP2PPort is the default p2p listen and discovery port (TCP + UDP).
	DefaultP2PPort int32 = 30303
	// DefaultRPCPort is the default JSON-RPC HTTP port (--http.port).
	DefaultRPCPort int32 = 8545
	// DefaultWSPort is the default JSON-RPC websocket port (--ws.port).
	DefaultWSPort int32 = 8546
	// DefaultMetricsPort is the default execution-layer metrics port (--metrics).
	DefaultMetricsPort int32 = 9000
)

// NodeRole selects the operating mode of every node created by a TempoFullNode.
type NodeRole string

const (
	// NodeRoleValidator participates in consensus. It requires a signing key and
	// activates the operator's double-sign guards.
	NodeRoleValidator NodeRole = "validator"
	// NodeRoleRPC is a follower node serving JSON-RPC (`tempo node --follow`).
	NodeRoleRPC NodeRole = "rpc"
	// NodeRoleArchive is a follower node retaining full history.
	NodeRoleArchive NodeRole = "archive"
)

type Ordinals struct {
	// Start is the number representing the first replica's index. It may be used to number replicas from an
	// alternate index (eg: 1-indexed) over the default 0-indexed names, or to orchestrate progressive movement
	// of replicas from one TempoFullNode spec to another.
	// If set, replica indices will be in the range:
	// [.spec.ordinals.start, .spec.ordinals.start + .spec.replicas).
	// If unset, defaults to 0. Replica indices will be in the range:
	// [0, .spec.replicas).
	// +kubebuilder:validation:Minimum:=0
	Start int32 `json:"start,omitempty"`
}

// TempoFullNodeSpec defines the desired state of a set of Tempo nodes.
// +kubebuilder:validation:XValidation:rule="self.role != 'validator' || self.replicas <= 1",message="validator role requires replicas <= 1 (double-sign guard)"
// +kubebuilder:validation:XValidation:rule="self.role != 'validator' || has(self.signingKey)",message="validator role requires spec.signingKey"
type TempoFullNodeSpec struct {
	// Role selects the node's operating mode.
	// - validator: participates in consensus. Requires signingKey. Replicas is limited to 1 and
	//   maxUnavailable is clamped to 1 (double-sign guards). Runs `tempo node` without --follow.
	//   Snapshot profile defaults to "minimal".
	// - rpc: follower serving JSON-RPC (`--follow`). Snapshot profile defaults to "full".
	// - archive: follower retaining full history. Snapshot profile defaults to "archive".
	// This field is immutable.
	// +kubebuilder:validation:Enum:=validator;rpc;archive
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="role is immutable"
	// +kubebuilder:validation:Required
	Role NodeRole `json:"role"`

	// Number of replicas to create.
	// Individual replicas have a consistent identity.
	// Validators are limited to at most 1 replica; one validator identity maps to exactly one node.
	// +kubebuilder:validation:Minimum:=0
	Replicas int32 `json:"replicas"`

	// Ordinals controls the numbering of replica indices in a TempoFullNode spec.
	// The default ordinals behavior assigns a "0" index to the first replica and increments the index by one
	// for each additional replica requested.
	// +optional
	Ordinals Ordinals `json:"ordinals,omitempty"`

	// ChainSpec identifies the chain and how the node is launched.
	ChainSpec TempoChainSpec `json:"chain"`

	// Template applied to all pods.
	// Creates 1 pod per replica.
	PodTemplate PodSpec `json:"podTemplate"`

	// ScheduledUpgrades replaces halt-height version switching from the Cosmos lineage of this operator.
	// Tempo hardforks activate at wall-clock timestamps; the operator rolls pods onto Image once
	// now >= activatesAt - leadTime, honoring the rollout strategy and validator guards.
	// Entries must be ascending by activatesAt.
	// +optional
	ScheduledUpgrades []ScheduledUpgrade `json:"scheduledUpgrades,omitempty"`

	// ExecVolume configures the execution datadir (`--datadir`).
	// It MUST be node-local storage: Tempo does not support network-attached volumes (EBS, GCP PD, ...)
	// for execution state. Contents are disposable and re-populated by `tempo download` on loss.
	// If not set, defaults to an EmptyDir with no size limit; on GKE use ephemeral storage backed by
	// local NVMe SSD (see docs/storage.md).
	// +optional
	ExecVolume ExecVolumeSpec `json:"execVolume,omitempty"`

	// ConsensusVolume configures the consensus datadir (`--consensus.datadir`) as a per-ordinal
	// ReadWriteOnce PVC. It holds the DKG signing share; persistence avoids missed epochs after
	// reschedules, and the RWO attach semantics double as a fence against two pods holding the same
	// share (double-sign guard). No dataSource seeding is offered, deliberately.
	ConsensusVolume ConsensusVolumeSpec `json:"consensusVolume"`

	// SigningKey references the validator's key material. Required iff role is validator.
	// +optional
	SigningKey *SigningKeySpec `json:"signingKey,omitempty"`

	// SnapshotInit controls the `tempo download` init container that seeds the execution datadir.
	// +optional
	SnapshotInit SnapshotInitSpec `json:"snapshotInit,omitempty"`

	// RPC configures the JSON-RPC server flags.
	// +optional
	RPC RPCSpec `json:"rpc,omitempty"`

	// P2P configures networking and peer discovery.
	// +optional
	P2P P2PSpec `json:"p2p,omitempty"`

	// Telemetry configures metrics and the telemetry exporter.
	// +optional
	Telemetry TelemetrySpec `json:"telemetry,omitempty"`

	// Configure Operator created services. A single rpc service is created for load balancing
	// JSON-RPC requests. Additionally, multiple p2p services are created for peer exchange.
	// +optional
	Service ServiceSpec `json:"service,omitempty"`

	// How to scale pods when performing an update.
	// Clamped to 1 unavailable pod for role validator.
	// +optional
	RolloutStrategy RolloutStrategy `json:"strategy,omitempty"`

	// Strategies for automatic recovery of faults and errors.
	// Managed by a separate controller, SelfHealingController, in an effort to reduce
	// complexity of the TempoFullNodeController.
	// +optional
	SelfHeal *SelfHealSpec `json:"selfHeal,omitempty"`

	// Allows overriding an instance on a case-by-case basis. An instance is a pod/pvc combo with an ordinal.
	// Key must be the name of the pod including the ordinal suffix.
	// Example: tempo-1
	// Used for debugging.
	// +optional
	InstanceOverrides map[string]InstanceOverridesSpec `json:"instanceOverrides,omitempty"`

	// Determines how to handle consensus PVCs when pods are scaled down.
	// One of 'Retain' or 'Delete'.
	// If 'Delete', PVCs are deleted if pods are scaled down.
	// If 'Retain', PVCs are not deleted. The admin must delete manually or are deleted if the CRD is deleted.
	// If not set, defaults to 'Delete'.
	// +kubebuilder:validation:Enum:=Retain;Delete
	// +optional
	RetentionPolicy *RetentionPolicy `json:"volumeRetentionPolicy,omitempty"`

	// If set, the pods use the specified service account.
	// The operator does not create service accounts; the default service account of the namespace is
	// used when empty.
	// +optional
	ServiceAccountName string `json:"serviceAccountName,omitempty"`
}

// TempoChainSpec identifies the chain and how the node process is launched.
type TempoChainSpec struct {
	// Chain is the target network, passed as `--chain` to both `tempo node` and `tempo download`.
	// Known public values: mainnet, moderato.
	// Deliberately a free-form string (not an enum) so Tempo forks can pass their own chain names
	// or chainspec paths.
	// +kubebuilder:validation:MinLength:=1
	Chain string `json:"chain"`

	// AdditionalArgs are appended verbatim to the `tempo node` command line.
	// This is the escape hatch for any flag not modeled by this CRD (the equivalent of the Cosmos
	// operator's TOML overrides).
	// +optional
	AdditionalArgs []string `json:"additionalArgs,omitempty"`

	// AdditionalDownloadArgs are appended verbatim to the `tempo download` command line of the
	// snapshot init container.
	// +optional
	AdditionalDownloadArgs []string `json:"additionalDownloadArgs,omitempty"`

	// Env sets extra environment variables on the node container (e.g. RUST_LOG).
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// Follow overrides the role-derived default for `--follow`
	// (validator=false, rpc/archive=true). Exists for advanced setups such as standby validators.
	// +optional
	Follow *bool `json:"follow,omitempty"`
}

// ScheduledUpgrade describes a timestamp-activated network upgrade (hardfork).
type ScheduledUpgrade struct {
	// ActivatesAt is the network activation time, from the chain's release notes.
	// The operator begins rolling pods onto Image at ActivatesAt - LeadTime.
	// +kubebuilder:validation:Required
	ActivatesAt metav1.Time `json:"activatesAt"`

	// Image is the full image ref in "repository:tag" format to run from that point on.
	// +kubebuilder:validation:MinLength:=1
	Image string `json:"image"`

	// LeadTime is how long before activation the roll starts. Nodes must run the new binary
	// BEFORE the activation instant or they fork off the network.
	// If not set, defaults to 6h.
	// +optional
	LeadTime *metav1.Duration `json:"leadTime,omitempty"`
}

// ExecVolumeSpec configures the node-local volume backing the execution datadir.
// Exactly one of the fields may be set. If none is set, an EmptyDir with no size limit is used;
// the pod then draws from the node's ephemeral storage, which must be local-SSD backed for
// production use.
type ExecVolumeSpec struct {
	// EmptyDir provisions the execution datadir from node ephemeral storage.
	// On GKE, schedule onto node pools with local NVMe SSD ephemeral storage
	// (--ephemeral-storage-local-ssd) and set a SizeLimit.
	// +optional
	EmptyDir *corev1.EmptyDirVolumeSource `json:"emptyDir,omitempty"`

	// Ephemeral provisions the execution datadir as a generic ephemeral volume. Use with a
	// node-local storage class (e.g. a local-volume provisioner). Escape hatch for non-GKE
	// topologies. The storage constraint (no network-attached volumes for execution state)
	// is the cluster operator's responsibility.
	// +optional
	Ephemeral *corev1.EphemeralVolumeSource `json:"ephemeral,omitempty"`
}

// ConsensusVolumeSpec configures the per-ordinal PVC for the consensus datadir.
// Deliberately offers no dataSource/autoDataSource: cloning a consensus volume risks two nodes
// holding the same DKG signing share, and shares are network-recoverable anyway.
type ConsensusVolumeSpec struct {
	// Applied to all PVCs.
	// +optional
	Metadata Metadata `json:"metadata,omitempty"`

	// storageClassName is the name of the StorageClass required by the claim.
	// For proper pod scheduling, it's highly recommended to set "volumeBindingMode: WaitForFirstConsumer"
	// in the StorageClass.
	// More info: https://kubernetes.io/docs/concepts/storage/persistent-volumes#class-1
	// Network-attached storage is fine here (unlike the exec volume); on GKE "standard-rwo" or
	// "premium-rwo" are reasonable choices.
	// This field is immutable. Updating this field requires manually deleting the PVC.
	// This field is required.
	StorageClassName string `json:"storageClassName"`

	// resources represents the minimum resources the volume should have.
	// Consensus state is small (order of GiB); tens of GiB is a comfortable default.
	// Updating the storage size is allowed but the StorageClass must support file system resizing.
	// Only increasing storage is permitted.
	// This field is required.
	Resources corev1.ResourceRequirements `json:"resources"`

	// volumeMode defines what type of volume is required by the claim.
	// Value of Filesystem is implied when not included in claim spec.
	// This field is immutable.
	// +optional
	VolumeMode *corev1.PersistentVolumeMode `json:"volumeMode,omitempty"`
}

// SigningKeySpec references the validator's key material.
// The operator never reads or logs the referenced values; it only mounts them into the node
// container. One signing-key Secret must be referenced by exactly one TempoFullNode in one cluster;
// the operator cannot detect cross-cluster duplication.
type SigningKeySpec struct {
	// SigningKeySecret selects the Secret key holding the encrypted ed25519 consensus signing key
	// (the output of `tempo consensus generate-signing-key`).
	// Mounted read-only at file mode 0400 and passed via --consensus.signing-key.
	SigningKeySecret corev1.SecretKeySelector `json:"signingKeySecret"`

	// EncryptionSecret selects the Secret key holding the key-encryption secret, delivered to
	// --consensus.secret as a mounted file. The value never lands in a ConfigMap, env var, or log.
	EncryptionSecret corev1.SecretKeySelector `json:"encryptionSecret"`

	// FeeRecipient sets the fee recipient address, if the chain honors it.
	// +optional
	FeeRecipient *string `json:"feeRecipient,omitempty"`
}

// SnapshotInitPolicy controls whether the snapshot init container runs.
type SnapshotInitPolicy string

const (
	// SnapshotInitAuto runs `tempo download` only when the exec datadir is unpopulated.
	SnapshotInitAuto SnapshotInitPolicy = "Auto"
	// SnapshotInitAlways runs `tempo download --force`, refreshing data while preserving
	// the node identity files (discovery-secret, known-peers.json).
	SnapshotInitAlways SnapshotInitPolicy = "Always"
	// SnapshotInitNever disables the snapshot init container; the node syncs from genesis
	// or the operator of the cluster provides data out of band.
	SnapshotInitNever SnapshotInitPolicy = "Never"
)

// SnapshotProfile selects which snapshot flavor `tempo download` fetches.
type SnapshotProfile string

const (
	SnapshotProfileMinimal SnapshotProfile = "minimal"
	SnapshotProfileFull    SnapshotProfile = "full"
	SnapshotProfileArchive SnapshotProfile = "archive"
)

// SnapshotInitSpec controls the `tempo download` init container.
type SnapshotInitSpec struct {
	// Policy controls when the init container downloads a snapshot:
	// Auto (default): only when the exec datadir is empty or unpopulated.
	// Always: always refresh via --force, preserving discovery-secret and known-peers.json.
	// Never: the operator provides no init sync.
	// +kubebuilder:validation:Enum:=Auto;Always;Never
	// +optional
	Policy SnapshotInitPolicy `json:"policy,omitempty"`

	// Profile selects minimal|full|archive, mapping to the corresponding `tempo download` flag.
	// Defaults by role: validator=minimal, rpc=full, archive=archive.
	// +kubebuilder:validation:Enum:=minimal;full;archive
	// +optional
	Profile *SnapshotProfile `json:"profile,omitempty"`

	// URL overrides the default snapshot source (-u/--url).
	// +optional
	URL *string `json:"url,omitempty"`

	// ManifestURL overrides the snapshot manifest source (--manifest-url).
	// +optional
	ManifestURL *string `json:"manifestURL,omitempty"`
}

// RPCSpec configures the JSON-RPC HTTP (and websocket) server.
type RPCSpec struct {
	// Enabled controls whether the JSON-RPC server is exposed outside the pod.
	// Default: true for rpc/archive roles, false for validator.
	// Note: the node always serves JSON-RPC at least on localhost so the healthcheck sidecar can
	// probe sync state; Enabled only controls binding to the pod IP and the container port.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Port for --http.port. Default 8545.
	// +optional
	Port *int32 `json:"port,omitempty"`

	// APIs for --http.api. Default: eth,net,web3,txpool,trace.
	// +optional
	APIs []string `json:"apis,omitempty"`

	// WS configures the websocket server (--ws), if desired.
	// +optional
	WS *WSSpec `json:"ws,omitempty"`
}

// WSSpec configures the JSON-RPC websocket server.
type WSSpec struct {
	// Enabled controls --ws.
	Enabled bool `json:"enabled"`

	// Port for --ws.port. Default 8546.
	// +optional
	Port *int32 `json:"port,omitempty"`
}

// P2PSpec configures p2p networking and discovery.
type P2PSpec struct {
	// Port is the p2p listen and discovery port (--port, --discovery.port). Default 30303.
	// +optional
	Port *int32 `json:"port,omitempty"`
}

// TelemetrySpec configures metrics and the telemetry exporter.
type TelemetrySpec struct {
	// MetricsPort sets the execution metrics listen port (--metrics <port>). Default 9000.
	// +optional
	MetricsPort *int32 `json:"metricsPort,omitempty"`

	// TelemetryURL is passed to --telemetry-url. Prefer TelemetryURLSecret when the URL embeds
	// a token so it never appears in the pod spec.
	// +optional
	TelemetryURL *string `json:"telemetryURL,omitempty"`

	// TelemetryURLSecret selects a Secret key whose value is passed to --telemetry-url via an
	// environment variable, keeping tokens out of the pod spec's literal args.
	// Takes precedence over TelemetryURL.
	// +optional
	TelemetryURLSecret *corev1.SecretKeySelector `json:"telemetryURLSecret,omitempty"`

	// MetricsInterval is passed to --telemetry-metrics-interval.
	// +optional
	MetricsInterval *metav1.Duration `json:"metricsInterval,omitempty"`
}

// TempoFullNodeStatus defines the observed state of TempoFullNode
type TempoFullNodeStatus struct {
	// The most recent generation observed by the controller.
	ObservedGeneration int64 `json:"observedGeneration"`

	// The current phase of the fullnode deployment.
	// "Progressing" means the deployment is under way.
	// "Complete" means the deployment is complete and reconciliation is finished.
	// "WaitingForFence" means a validator pod replacement is gated on the previous pod fully
	// terminating and releasing its consensus volume (double-sign guard). This is expected
	// behavior during node failures; see docs/double_sign_safety.md before intervening.
	// "Error" means an unrecoverable error occurred, which needs human intervention.
	Phase TempoFullNodePhase `json:"phase"`

	// A generic message for the user. May contain errors.
	// +optional
	StatusMessage *string `json:"status,omitempty"`

	// Status set by the SelfHealing controller.
	// +optional
	SelfHealing SelfHealingStatus `json:"selfHealing,omitempty"`

	// Current sync information, collected from each pod's JSON-RPC endpoint.
	// +optional
	SyncInfo map[string]*SyncInfoPodStatus `json:"sync,omitempty"`

	// Upgrade tracking for spec.scheduledUpgrades.
	// +optional
	Upgrade *UpgradeStatus `json:"upgrade,omitempty"`
}

type SyncInfoPodStatus struct {
	// When sync information was fetched.
	Timestamp metav1.Time `json:"timestamp"`
	// Latest block height (eth_blockNumber) if no error encountered.
	// +optional
	Height *uint64 `json:"height,omitempty"`
	// If the pod reports itself as in sync with chain tip (eth_syncing == false and the latest
	// block is recent).
	// +optional
	InSync *bool `json:"inSync,omitempty"`
	// Number of connected peers (net_peerCount) if no error encountered.
	// +optional
	PeerCount *uint64 `json:"peerCount,omitempty"`
	// Error message if unable to fetch sync state.
	// +optional
	Error *string `json:"error,omitempty"`
}

// UpgradeStatus reports scheduled-upgrade progress.
type UpgradeStatus struct {
	// The image the operator currently wants all pods to run, after applying scheduled upgrades.
	CurrentImage string `json:"currentImage"`
	// The next pending scheduled upgrade, if any.
	// +optional
	NextActivatesAt *metav1.Time `json:"nextActivatesAt,omitempty"`
	// +optional
	NextImage *string `json:"nextImage,omitempty"`
}

type TempoFullNodePhase string

const (
	TempoFullNodePhaseComplete        TempoFullNodePhase = "Complete"
	TempoFullNodePhaseError           TempoFullNodePhase = "Error"
	TempoFullNodePhaseProgressing     TempoFullNodePhase = "Progressing"
	TempoFullNodePhaseTransientError  TempoFullNodePhase = "TransientError"
	TempoFullNodePhaseWaitingForFence TempoFullNodePhase = "WaitingForFence"
)

// Metadata is a subset of k8s object metadata.
type Metadata struct {
	// Labels are added to a resource. If there is a collision between labels the Operator creates, the Operator
	// labels take precedence.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// Annotations are added to a resource. If there is a collision between annotations the Operator creates, the Operator
	// annotations take precedence.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
}

type PodSpec struct {
	// Metadata is a subset of metav1.ObjectMeta applied to all pods.
	// +optional
	Metadata Metadata `json:"metadata,omitempty"`

	// Image is the docker reference in "repository:tag" format. E.g. ghcr.io/tempoxyz/tempo:v1.11.0.
	// This is for the main container running the chain process and the snapshot init container.
	// Overridden while a spec.scheduledUpgrades entry is active.
	// +kubebuilder:validation:MinLength:=1
	Image string `json:"image"`

	// Image pull policy.
	// One of Always, Never, IfNotPresent.
	// Defaults to Always if :latest tag is specified, or IfNotPresent otherwise.
	// Cannot be updated.
	// More info: https://kubernetes.io/docs/concepts/containers/images#updating-images
	// This is for the main container running the chain process.
	// +optional
	ImagePullPolicy corev1.PullPolicy `json:"imagePullPolicy,omitempty"`

	// ImagePullSecrets is a list of references to secrets in the same namespace to use for pulling any images
	// in pods that reference this ServiceAccount. ImagePullSecrets are distinct from Secrets because Secrets
	// can be mounted in the pod, but ImagePullSecrets are only accessed by the kubelet.
	// More info: https://kubernetes.io/docs/concepts/containers/images/#specifying-imagepullsecrets-on-a-pod
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// NodeSelector is a selector which must be true for the pod to fit on a node.
	// Selector which must match a node's labels for the pod to be scheduled on that node.
	// More info: https://kubernetes.io/docs/concepts/configuration/assign-pod-node/
	// Use this to pin pods onto node pools with local NVMe SSD for the execution datadir.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// If specified, the pod's scheduling constraints
	// This is an advanced configuration option.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// If specified, the pod's tolerations.
	// This is an advanced configuration option.
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// If specified, indicates the pod's priority. "system-node-critical" and
	// "system-cluster-critical" are two special keywords which indicate the
	// highest priorities with the former being the highest priority. Any other
	// name must be defined by creating a PriorityClass object with that name.
	// If not specified, the pod priority will be default or zero if there is no
	// default.
	// This is an advanced configuration option.
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`

	// The priority value. Various system components use this field to find the
	// priority of the pod. When Priority Admission Controller is enabled, it
	// prevents users from setting this field. The admission controller populates
	// this field from PriorityClassName.
	// The higher the value, the higher the priority.
	// This is an advanced configuration option.
	// +optional
	Priority *int32 `json:"priority,omitempty"`

	// Resources describes the compute resource requirements.
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`

	// Optional duration in seconds the pod needs to terminate gracefully. May be decreased in delete request.
	// Value must be non-negative integer. The value zero indicates stop immediately via
	// the kill signal (no opportunity to shut down).
	// If this value is nil, the default grace period will be used instead.
	// Set this value longer than the expected cleanup time for your process.
	// This is an advanced configuration option.
	// Defaults to 30 seconds.
	// +optional
	TerminationGracePeriodSeconds *int64 `json:"terminationGracePeriodSeconds,omitempty"`

	// Configure probes for the pods managed by the controller.
	// +optional
	Probes ProbesSpec `json:"probes,omitempty"`

	// List of volumes that can be mounted by containers belonging to the pod.
	// More info: https://kubernetes.io/docs/concepts/storage/volumes
	// A strategic merge patch is applied to the default volumes created by the controller.
	// Take extreme caution when using this feature. Use only for critical bugs.
	// This serves as an "escape hatch" for the user at the cost of maintainability.
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// List of initialization containers belonging to the pod.
	// More info: https://kubernetes.io/docs/concepts/workloads/pods/init-containers/
	// A strategic merge patch is applied to the default init containers created by the controller.
	// Take extreme caution when using this feature. Use only for critical bugs.
	// This serves as an "escape hatch" for the user at the cost of maintainability.
	// +optional
	InitContainers []corev1.Container `json:"initContainers,omitempty"`

	// List of containers belonging to the pod.
	// A strategic merge patch is applied to the default containers created by the controller.
	// Take extreme caution when using this feature. Use only for critical bugs.
	// This serves as an "escape hatch" for the user at the cost of maintainability.
	// +optional
	Containers []corev1.Container `json:"containers,omitempty"`
}

type ProbeStrategy string

const (
	ProbeStrategyNone      ProbeStrategy = "None"
	ProbeStrategyReachable ProbeStrategy = "Reachable"
	ProbeStrategyInSync    ProbeStrategy = "InSync"
)

// ProbesSpec configures probes for created pods
type ProbesSpec struct {
	// Strategy controls the default probes added by the controller.
	// None = Do not add any probes.
	// Reachable = Probe the node's JSON-RPC /health endpoint only.
	// InSync = Additionally gate readiness on the healthcheck sidecar reporting the node in sync
	// (eth_syncing false and a recent latest block).
	// If not set, defaults to InSync.
	// +kubebuilder:validation:Enum:=None;Reachable;InSync
	// +optional
	Strategy ProbeStrategy `json:"strategy,omitempty"`
}

type RetentionPolicy string

const (
	RetentionPolicyRetain RetentionPolicy = "Retain"
	RetentionPolicyDelete RetentionPolicy = "Delete"
)

// RolloutStrategy is an update strategy.
type RolloutStrategy struct {
	// The maximum number of pods that can be unavailable during an update.
	// Value can be an absolute number (ex: 5) or a percentage of desired pods (ex: 10%).
	// Absolute number is calculated from percentage by rounding down. The minimum max unavailable is 1.
	// Defaults to 25%.
	// Ignored (clamped to 1) for role validator.
	// Example: when this is set to 30%, pods are scaled down to 70% of desired pods
	// immediately when the rolling update starts. Once new pods are ready, pods
	// can be scaled down further, ensuring that the total number of pods available
	// at all times during the update is at least 70% of desired pods.
	// +kubebuilder:validation:XIntOrString
	// +optional
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`
}

type ServiceSpec struct {
	// Max number of external p2p services to create for peer exchange.
	// Controller creates p2p services for each pod so that every pod can peer with each other
	// internally in the cluster. This setting allows you to control the number of p2p services
	// exposed for peers outside of the cluster to use.
	// If not set, defaults to 1.
	// +kubebuilder:validation:Minimum:=0
	// +optional
	MaxP2PExternalAddresses *int32 `json:"maxP2PExternalAddresses,omitempty"`

	// Overrides for all P2P services that need external addresses.
	// +optional
	P2PTemplate ServiceOverridesSpec `json:"p2pTemplate,omitempty"`

	// Overrides for the single RPC service.
	// +optional
	RPCTemplate ServiceOverridesSpec `json:"rpcTemplate,omitempty"`
}

// ServiceOverridesSpec allows some overrides for the created services.
type ServiceOverridesSpec struct {
	// +optional
	Metadata Metadata `json:"metadata,omitempty"`

	// Describes ingress methods for a service.
	// If not set, defaults to "ClusterIP".
	// +kubebuilder:validation:Enum:=ClusterIP;NodePort;LoadBalancer;ExternalName
	// +optional
	Type *corev1.ServiceType `json:"type,omitempty"`

	// Sets the ClusterIP. Setting this to "None" makes a "headless service" (no virtual IP),
	// which is useful when direct endpoint connections are preferred and proxying is not required.
	// If not set, defaults to "".
	// +optional
	ClusterIP *string `json:"clusterIP,omitempty"`

	// List of additional ports to expose from the service.
	// +optional
	Ports []corev1.ServicePort `json:"ports,omitempty"`

	// Sets endpoint and routing behavior.
	// See: https://kubernetes.io/docs/tasks/access-application-cluster/create-external-load-balancer/#caveats-and-limitations-when-preserving-source-ips
	// If not set, defaults to "Cluster".
	// +kubebuilder:validation:Enum:=Cluster;Local
	// +optional
	ExternalTrafficPolicy *corev1.ServiceExternalTrafficPolicyType `json:"externalTrafficPolicy,omitempty"`
}

// InstanceOverridesSpec allows overriding an instance which is pod/pvc combo with an ordinal
type InstanceOverridesSpec struct {
	// Disables whole or part of the instance.
	// Used for scenarios like debugging.
	// Set to "Pod" to prevent controller from creating a pod for this instance, leaving the PVC.
	// Set to "All" to prevent the controller from managing a pod and pvc. Note, the PVC may not be
	// deleted if the RetentionPolicy is set to "Retain". If you need to remove the PVC, delete manually.
	// +kubebuilder:validation:Enum:=Pod;All
	// +optional
	DisableStrategy *DisableStrategy `json:"disable,omitempty"`

	// Overrides an individual instance's consensus PVC.
	// +optional
	ConsensusVolume *ConsensusVolumeSpec `json:"consensusVolume,omitempty"`

	// Overrides an individual instance's Image.
	// +optional
	Image string `json:"image,omitempty"`

	// NodeSelector is a selector which must be true for the pod to fit on a node.
	// Selector which must match a node's labels for the pod to be scheduled on that node.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
}

type DisableStrategy string

const (
	DisableAll DisableStrategy = "All"
	DisablePod DisableStrategy = "Pod"
)

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.role`
//+kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
//+kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// TempoFullNode is the Schema for the tempofullnodes API
type TempoFullNode struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   TempoFullNodeSpec   `json:"spec,omitempty"`
	Status TempoFullNodeStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// TempoFullNodeList contains a list of TempoFullNode
type TempoFullNodeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []TempoFullNode `json:"items"`
}

// IsValidator reports whether the CRD manages a consensus-participating validator.
func (crd *TempoFullNode) IsValidator() bool {
	return crd.Spec.Role == NodeRoleValidator
}

// FollowEnabled reports whether pods run `tempo node --follow`.
// Defaults by role (validator=false, rpc/archive=true), overridable via spec.chain.follow.
func (crd *TempoFullNode) FollowEnabled() bool {
	if v := crd.Spec.ChainSpec.Follow; v != nil {
		return *v
	}
	return !crd.IsValidator()
}

// RPCExposed reports whether the JSON-RPC server binds to all interfaces and is exposed via
// container ports and services. Defaults by role (validator=false, rpc/archive=true).
func (crd *TempoFullNode) RPCExposed() bool {
	if v := crd.Spec.RPC.Enabled; v != nil {
		return *v
	}
	return !crd.IsValidator()
}

// RPCPort returns the JSON-RPC HTTP port.
func (crd *TempoFullNode) RPCPort() int32 {
	if v := crd.Spec.RPC.Port; v != nil {
		return *v
	}
	return DefaultRPCPort
}

// WSPort returns the JSON-RPC websocket port.
func (crd *TempoFullNode) WSPort() int32 {
	if ws := crd.Spec.RPC.WS; ws != nil && ws.Port != nil {
		return *ws.Port
	}
	return DefaultWSPort
}

// WSEnabled reports whether the websocket server is enabled.
func (crd *TempoFullNode) WSEnabled() bool {
	return crd.Spec.RPC.WS != nil && crd.Spec.RPC.WS.Enabled
}

// P2PPort returns the p2p listen and discovery port.
func (crd *TempoFullNode) P2PPort() int32 {
	if v := crd.Spec.P2P.Port; v != nil {
		return *v
	}
	return DefaultP2PPort
}

// MetricsPort returns the execution metrics port.
func (crd *TempoFullNode) MetricsPort() int32 {
	if v := crd.Spec.Telemetry.MetricsPort; v != nil {
		return *v
	}
	return DefaultMetricsPort
}

// SnapshotProfileOrDefault returns the configured snapshot profile or the role-derived default.
func (crd *TempoFullNode) SnapshotProfileOrDefault() SnapshotProfile {
	if v := crd.Spec.SnapshotInit.Profile; v != nil {
		return *v
	}
	switch crd.Spec.Role {
	case NodeRoleValidator:
		return SnapshotProfileMinimal
	case NodeRoleArchive:
		return SnapshotProfileArchive
	default:
		return SnapshotProfileFull
	}
}

// SnapshotPolicyOrDefault returns the configured snapshot init policy, defaulting to Auto.
func (crd *TempoFullNode) SnapshotPolicyOrDefault() SnapshotInitPolicy {
	if p := crd.Spec.SnapshotInit.Policy; p != "" {
		return p
	}
	return SnapshotInitAuto
}
