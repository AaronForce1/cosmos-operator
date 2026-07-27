package fullnode

import (
	"errors"
	"fmt"
	"strings"
	"time"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/aaronforce1/cosmos-operator/internal/healthcheck"
	"github.com/aaronforce1/cosmos-operator/internal/kube"
	"github.com/aaronforce1/cosmos-operator/internal/version"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	healthCheckPort = healthcheck.Port
	mainContainer   = "node"

	// snapshotInitContainer downloads a chain snapshot into the exec datadir via `tempo download`.
	snapshotInitContainer = "snapshot-init"

	// operatorImageRepo hosts the operator's own image, used for the healthcheck sidecar.
	operatorImageRepo = "ghcr.io/aaronforce1/tempo-operator"
)

// PodBuilder builds corev1.Pods
type PodBuilder struct {
	crd *tempov1alpha1.TempoFullNode
	pod *corev1.Pod
}

// NewPodBuilder returns a valid PodBuilder.
//
// The "now" argument selects the image from spec.scheduledUpgrades (timestamp-activated
// hardforks); pass time.Now() outside of tests.
//
// Panics if crd is nil.
func NewPodBuilder(crd *tempov1alpha1.TempoFullNode, now time.Time) PodBuilder {
	if crd == nil {
		panic(errors.New("nil TempoFullNode"))
	}

	var (
		tpl    = crd.Spec.PodTemplate
		image  = DesiredImage(crd, now)
		probes = podReadinessProbes(crd)
	)

	pod := corev1.Pod{
		TypeMeta: metav1.TypeMeta{
			Kind:       "Pod",
			APIVersion: "v1",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   crd.Namespace,
			Labels:      defaultLabels(crd),
			Annotations: make(map[string]string),
		},
		Spec: corev1.PodSpec{
			ServiceAccountName: crd.Spec.ServiceAccountName,
			SecurityContext: &corev1.PodSecurityContext{
				RunAsUser:           ptr(int64(1025)),
				RunAsGroup:          ptr(int64(1025)),
				RunAsNonRoot:        ptr(true),
				FSGroup:             ptr(int64(1025)),
				FSGroupChangePolicy: ptr(corev1.FSGroupChangeOnRootMismatch),
				SeccompProfile:      &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Subdomain: crd.Name,
			Containers: []corev1.Container{
				// Main node container: single tempo process (reth execution + Commonware consensus).
				{
					Name:            mainContainer,
					Image:           image,
					Command:         []string{"tempo"},
					Args:            nodeArgs(crd),
					Env:             envVars(crd),
					Ports:           buildPorts(crd),
					Resources:       tpl.Resources,
					ReadinessProbe:  probes[0],
					ImagePullPolicy: tpl.ImagePullPolicy,
					WorkingDir:      workDir,
				},
				// healthcheck sidecar
				{
					Name:    "healthcheck",
					Image:   operatorImageRepo + ":" + version.DockerTag(),
					Command: []string{"/manager", "healthcheck", "--rpc-host", fmt.Sprintf("http://localhost:%d", crd.RPCPort())},
					Ports:   []corev1.ContainerPort{{ContainerPort: healthCheckPort, Protocol: corev1.ProtocolTCP}},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("5m"),
							corev1.ResourceMemory: resource.MustParse("16Mi"),
						},
					},
					ReadinessProbe:  probes[1],
					ImagePullPolicy: tpl.ImagePullPolicy,
				},
			},
		},
	}

	preserveMergeInto(pod.Labels, tpl.Metadata.Labels)
	preserveMergeInto(pod.Annotations, tpl.Metadata.Annotations)

	return PodBuilder{
		crd: crd,
		pod: &pod,
	}
}

func podReadinessProbes(crd *tempov1alpha1.TempoFullNode) []*corev1.Probe {
	if crd.Spec.PodTemplate.Probes.Strategy == tempov1alpha1.ProbeStrategyNone {
		return []*corev1.Probe{nil, nil}
	}

	// reth's HTTP server exposes a plain /health endpoint that returns 200 once the server is up.
	mainProbe := &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path:   "/health",
				Port:   intstr.FromInt(int(crd.RPCPort())),
				Scheme: corev1.URISchemeHTTP,
			},
		},
		InitialDelaySeconds: 1,
		TimeoutSeconds:      10,
		PeriodSeconds:       10,
		SuccessThreshold:    1,
		FailureThreshold:    5,
	}

	if crd.Spec.PodTemplate.Probes.Strategy == tempov1alpha1.ProbeStrategyReachable {
		return []*corev1.Probe{mainProbe, nil}
	}

	sidecarProbe := &corev1.Probe{
		ProbeHandler: corev1.ProbeHandler{
			HTTPGet: &corev1.HTTPGetAction{
				Path:   "/",
				Port:   intstr.FromInt(healthCheckPort),
				Scheme: corev1.URISchemeHTTP,
			},
		},
		InitialDelaySeconds: 1,
		TimeoutSeconds:      10,
		PeriodSeconds:       10,
		SuccessThreshold:    1,
		FailureThreshold:    3,
	}

	return []*corev1.Probe{mainProbe, sidecarProbe}
}

// Build assigns the TempoFullNode crd as the owner and returns a fully constructed pod.
func (b PodBuilder) Build() (*corev1.Pod, error) {
	pod := b.pod.DeepCopy()

	if err := kube.ApplyStrategicMergePatch(pod, podPatch(b.crd)); err != nil {
		return nil, err
	}

	if o, ok := b.crd.Spec.InstanceOverrides[pod.Name]; ok {
		if o.DisableStrategy != nil {
			return nil, nil
		}
		if o.Image != "" {
			setChainContainerImage(pod, o.Image)
		}
		if o.NodeSelector != nil {
			pod.Spec.NodeSelector = o.NodeSelector
		}
	}

	kube.NormalizeMetadata(&pod.ObjectMeta)
	return pod, nil
}

const (
	volExecData   = "vol-exec-data"   // Execution datadir; node-local, disposable, re-seeded by `tempo download`.
	volConsensus  = "vol-consensus"   // Consensus datadir PVC; holds the DKG signing share.
	volSigningKey = "vol-signing-key" // Validator signing key material, projected from Secrets.
)

const (
	workDir = "/home/operator"

	// ExecDataDir is the container path of the execution datadir (`--datadir`).
	ExecDataDir = workDir + "/data"

	// ConsensusDataDir is the container path of the consensus datadir (`--consensus.datadir`).
	ConsensusDataDir = workDir + "/consensus"

	signingKeyDir        = "/etc/tempo-keys"
	signingKeyFile       = signingKeyDir + "/signing-key"
	encryptionSecretFile = signingKeyDir + "/consensus-secret"
)

// WithOrdinal updates adds name and other metadata to the pod using "ordinal" which is the pod's
// ordered sequence. Pods have deterministic, consistent names similar to a StatefulSet instead of generated names.
func (b PodBuilder) WithOrdinal(ordinal int32) PodBuilder {
	pod := b.pod.DeepCopy()
	name := instanceName(b.crd, ordinal)

	pod.Labels[kube.InstanceLabel] = name

	pod.Name = name
	// The init container runs `tempo download` from the same (upgrade-resolved) image as the node.
	pod.Spec.InitContainers = initContainers(b.crd, pod.Spec.Containers[0].Image)

	pod.Spec.Hostname = pod.Name
	pod.Spec.Subdomain = b.crd.Name

	pod.Spec.Volumes = []corev1.Volume{
		{
			Name:         volExecData,
			VolumeSource: execVolumeSource(b.crd),
		},
		{
			Name: volConsensus,
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName(b.crd, ordinal)},
			},
		},
	}

	if b.crd.Spec.SigningKey != nil {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name: volSigningKey,
			VolumeSource: corev1.VolumeSource{
				Projected: &corev1.ProjectedVolumeSource{
					// 0400 requested; kubelet widens group permissions when fsGroup is set.
					DefaultMode: ptr(int32(0o400)),
					Sources:     signingKeyProjections(b.crd.Spec.SigningKey),
				},
			},
		})
	}

	mounts := []corev1.VolumeMount{
		{Name: volExecData, MountPath: ExecDataDir},
		{Name: volConsensus, MountPath: ConsensusDataDir},
	}
	if b.crd.Spec.SigningKey != nil {
		mounts = append(mounts, corev1.VolumeMount{Name: volSigningKey, MountPath: signingKeyDir, ReadOnly: true})
	}

	// The snapshot init container only needs the exec datadir.
	for i := range pod.Spec.InitContainers {
		pod.Spec.InitContainers[i].VolumeMounts = []corev1.VolumeMount{
			{Name: volExecData, MountPath: ExecDataDir},
		}
	}

	// At this point, guaranteed to have at least 2 containers.
	pod.Spec.Containers[0].VolumeMounts = mounts
	pod.Spec.Containers[1].VolumeMounts = []corev1.VolumeMount{
		// The healthcheck sidecar needs access to the data directories so it can read disk usage.
		{Name: volExecData, MountPath: ExecDataDir, ReadOnly: true},
		{Name: volConsensus, MountPath: ConsensusDataDir, ReadOnly: true},
	}

	b.pod = pod
	return b
}

func execVolumeSource(crd *tempov1alpha1.TempoFullNode) corev1.VolumeSource {
	if v := crd.Spec.ExecVolume.Ephemeral; v != nil {
		return corev1.VolumeSource{Ephemeral: v}
	}
	if v := crd.Spec.ExecVolume.EmptyDir; v != nil {
		return corev1.VolumeSource{EmptyDir: v}
	}
	return corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}
}

func signingKeyProjections(sk *tempov1alpha1.SigningKeySpec) []corev1.VolumeProjection {
	projections := []corev1.VolumeProjection{
		{
			Secret: &corev1.SecretProjection{
				LocalObjectReference: sk.SigningKeySecret.LocalObjectReference,
				Items: []corev1.KeyToPath{
					{Key: sk.SigningKeySecret.Key, Path: "signing-key"},
				},
			},
		},
	}
	if sk.EncryptionSecret.Name == sk.SigningKeySecret.Name {
		projections[0].Secret.Items = append(projections[0].Secret.Items, corev1.KeyToPath{
			Key: sk.EncryptionSecret.Key, Path: "consensus-secret",
		})
		return projections
	}
	return append(projections, corev1.VolumeProjection{
		Secret: &corev1.SecretProjection{
			LocalObjectReference: sk.EncryptionSecret.LocalObjectReference,
			Items: []corev1.KeyToPath{
				{Key: sk.EncryptionSecret.Key, Path: "consensus-secret"},
			},
		},
	})
}

const telemetryURLEnvVar = "TEMPO_TELEMETRY_URL"

func envVars(crd *tempov1alpha1.TempoFullNode) []corev1.EnvVar {
	env := []corev1.EnvVar{
		{Name: "HOME", Value: workDir},
		{Name: "CHAIN", Value: crd.Spec.ChainSpec.Chain},
		{Name: "DATA_DIR", Value: ExecDataDir},
		{Name: "CONSENSUS_DATA_DIR", Value: ConsensusDataDir},
	}
	if sel := crd.Spec.Telemetry.TelemetryURLSecret; sel != nil {
		env = append(env, corev1.EnvVar{
			Name:      telemetryURLEnvVar,
			ValueFrom: &corev1.EnvVarSource{SecretKeyRef: sel},
		})
	}
	return append(env, crd.Spec.ChainSpec.Env...)
}

// nodeArgs builds the `tempo node` command line from the spec. Configuration is CLI flags only;
// there is no config.toml/app.toml equivalent on Tempo.
func nodeArgs(crd *tempov1alpha1.TempoFullNode) []string {
	args := []string{
		"node",
		"--datadir", ExecDataDir,
		"--chain", crd.Spec.ChainSpec.Chain,
		"--consensus.datadir", ConsensusDataDir,
	}

	if crd.FollowEnabled() {
		args = append(args, "--follow")
	}

	if crd.Spec.SigningKey != nil {
		args = append(args,
			"--consensus.signing-key", signingKeyFile,
			"--consensus.secret", encryptionSecretFile,
		)
	}

	p2p := fmt.Sprint(crd.P2PPort())
	args = append(args,
		"--port", p2p,
		"--discovery.addr", "0.0.0.0",
		"--discovery.port", p2p,
	)

	// The JSON-RPC server is always enabled: the healthcheck sidecar and the operator's status
	// collector depend on it for eth_syncing/eth_blockNumber. spec.rpc.enabled only controls
	// service exposure outside the pod.
	apis := crd.Spec.RPC.APIs
	if len(apis) == 0 {
		apis = []string{"eth", "net", "web3", "txpool", "trace"}
	}
	args = append(args,
		"--http",
		"--http.addr", "0.0.0.0",
		"--http.port", fmt.Sprint(crd.RPCPort()),
		"--http.api", strings.Join(apis, ","),
	)

	if crd.WSEnabled() {
		args = append(args,
			"--ws",
			"--ws.addr", "0.0.0.0",
			"--ws.port", fmt.Sprint(crd.WSPort()),
		)
	}

	args = append(args, "--metrics", fmt.Sprint(crd.MetricsPort()))

	tel := crd.Spec.Telemetry
	switch {
	case tel.TelemetryURLSecret != nil:
		// Kubernetes expands $(VAR) in args from the container's env, keeping the token out of
		// the pod spec's literal args.
		args = append(args, "--telemetry-url", fmt.Sprintf("$(%s)", telemetryURLEnvVar))
	case tel.TelemetryURL != nil:
		args = append(args, "--telemetry-url", *tel.TelemetryURL)
	}
	if tel.MetricsInterval != nil {
		args = append(args, "--telemetry-metrics-interval", tel.MetricsInterval.Duration.String())
	}

	return append(args, crd.Spec.ChainSpec.AdditionalArgs...)
}

func buildPorts(crd *tempov1alpha1.TempoFullNode) []corev1.ContainerPort {
	ports := []corev1.ContainerPort{
		{
			Name:          "p2p",
			Protocol:      corev1.ProtocolTCP,
			ContainerPort: crd.P2PPort(),
		},
		{
			Name:          "p2p-udp",
			Protocol:      corev1.ProtocolUDP,
			ContainerPort: crd.P2PPort(),
		},
		{
			Name:          "http-rpc",
			Protocol:      corev1.ProtocolTCP,
			ContainerPort: crd.RPCPort(),
		},
		{
			Name:          "metrics",
			Protocol:      corev1.ProtocolTCP,
			ContainerPort: crd.MetricsPort(),
		},
	}
	if crd.WSEnabled() {
		ports = append(ports, corev1.ContainerPort{
			Name:          "ws",
			Protocol:      corev1.ProtocolTCP,
			ContainerPort: crd.WSPort(),
		})
	}
	return ports
}

// snapshotDownloadScript wraps `tempo download` for idempotence. The exec datadir may already be
// populated after a container restart on the same node; identity files (discovery-secret,
// known-peers.json) do not count as populated because `tempo download` preserves them.
const snapshotDownloadScript = `set -eu
if find "$DATA_DIR" -mindepth 1 -maxdepth 1 ! -name discovery-secret ! -name known-peers.json ! -name lost+found | grep -q .; then
	echo "Execution datadir $DATA_DIR already populated; skipping snapshot download."
	exit 0
fi
exec %s
`

const snapshotForceScript = `set -eu
exec %s
`

func initContainers(crd *tempov1alpha1.TempoFullNode, image string) []corev1.Container {
	policy := crd.SnapshotPolicyOrDefault()
	if policy == tempov1alpha1.SnapshotInitNever {
		return nil
	}

	download := []string{
		"tempo", "download",
		"--chain", crd.Spec.ChainSpec.Chain,
		"--datadir", ExecDataDir,
		"--" + string(crd.SnapshotProfileOrDefault()),
		"--non-interactive",
	}
	if v := crd.Spec.SnapshotInit.URL; v != nil {
		download = append(download, "--url", *v)
	}
	if v := crd.Spec.SnapshotInit.ManifestURL; v != nil {
		download = append(download, "--manifest-url", *v)
	}
	download = append(download, crd.Spec.ChainSpec.AdditionalDownloadArgs...)

	var script string
	if policy == tempov1alpha1.SnapshotInitAlways {
		// --force overwrites existing snapshot data while preserving discovery-secret and
		// known-peers.json.
		download = append(download, "--force")
		script = fmt.Sprintf(snapshotForceScript, strings.Join(download, " "))
	} else {
		script = fmt.Sprintf(snapshotDownloadScript, strings.Join(download, " "))
	}

	return []corev1.Container{
		{
			Name:            snapshotInitContainer,
			Image:           image,
			Command:         []string{"sh"},
			Args:            []string{"-c", script},
			Env:             envVars(crd),
			ImagePullPolicy: crd.Spec.PodTemplate.ImagePullPolicy,
			WorkingDir:      workDir,
		},
	}
}

func podPatch(crd *tempov1alpha1.TempoFullNode) *corev1.Pod {
	tpl := crd.Spec.PodTemplate
	// For fields with sliceOrDefault if you pass nil, the field is deleted.
	spec := corev1.PodSpec{
		Affinity:                      tpl.Affinity,
		Containers:                    sliceOrDefault(tpl.Containers, []corev1.Container{}),
		ImagePullSecrets:              sliceOrDefault(tpl.ImagePullSecrets, []corev1.LocalObjectReference{}),
		InitContainers:                sliceOrDefault(tpl.InitContainers, []corev1.Container{}),
		NodeSelector:                  tpl.NodeSelector,
		Priority:                      tpl.Priority,
		PriorityClassName:             tpl.PriorityClassName,
		TerminationGracePeriodSeconds: valOrDefault(tpl.TerminationGracePeriodSeconds, ptr(int64(30))),
		Tolerations:                   sliceOrDefault(tpl.Tolerations, []corev1.Toleration{}),
		Volumes:                       sliceOrDefault(tpl.Volumes, []corev1.Volume{}),
	}
	return &corev1.Pod{Spec: spec}
}

func setChainContainerImage(pod *corev1.Pod, image string) {
	for i := range pod.Spec.Containers {
		if pod.Spec.Containers[i].Name == mainContainer {
			pod.Spec.Containers[i].Image = image
			break
		}
	}

	for i := range pod.Spec.InitContainers {
		if pod.Spec.InitContainers[i].Name == snapshotInitContainer {
			pod.Spec.InitContainers[i].Image = image
			break
		}
	}
}

// PVCName returns the consensus PVC associated with the pod.
func PVCName(pod *corev1.Pod) string {
	for _, v := range pod.Spec.Volumes {
		if v.Name == volConsensus {
			if v.PersistentVolumeClaim == nil {
				return ""
			}
			return v.PersistentVolumeClaim.ClaimName
		}
	}
	return ""
}
