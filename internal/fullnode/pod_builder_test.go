package fullnode

import (
	"strings"
	"testing"
	"time"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/aaronforce1/cosmos-operator/internal/test"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var testNow = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

func argsAfterFlag(t *testing.T, args []string, flag string) string {
	t.Helper()
	for i, a := range args {
		if a == flag {
			require.Less(t, i+1, len(args), "flag %s has no value", flag)
			return args[i+1]
		}
	}
	t.Fatalf("flag %s not found in %v", flag, args)
	return ""
}

func TestPodBuilder(t *testing.T) {
	t.Parallel()

	t.Run("happy path - critical fields", func(t *testing.T) {
		crd := defaultCRD()
		builder := NewPodBuilder(&crd, testNow)
		pod, err := builder.WithOrdinal(5).Build()
		require.NoError(t, err)

		require.Equal(t, "Pod", pod.Kind)
		require.Equal(t, "v1", pod.APIVersion)
		require.Equal(t, "test", pod.Namespace)
		require.Equal(t, "osmosis-5", pod.Name)

		wantLabels := map[string]string{
			"app.kubernetes.io/instance":   "osmosis-5",
			"app.kubernetes.io/created-by": "tempo-operator",
			"app.kubernetes.io/component":  "TempoFullNode",
			"app.kubernetes.io/name":       "osmosis",
			"app.kubernetes.io/version":    "v1.2.3",
			"tempo.aaronforce.io/chain":    "mainnet",
			"tempo.aaronforce.io/role":     "rpc",
		}
		require.Equal(t, wantLabels, pod.Labels)

		require.EqualValues(t, 30, *pod.Spec.TerminationGracePeriodSeconds)
		require.Equal(t, "osmosis-5", pod.Spec.Hostname)
		require.Equal(t, "osmosis", pod.Spec.Subdomain)

		sc := pod.Spec.SecurityContext
		require.EqualValues(t, 1025, *sc.RunAsUser)
		require.EqualValues(t, 1025, *sc.RunAsGroup)
		require.EqualValues(t, 1025, *sc.FSGroup)
		require.True(t, *sc.RunAsNonRoot)

		// node + healthcheck sidecar
		require.Len(t, pod.Spec.Containers, 2)

		node := pod.Spec.Containers[0]
		require.Equal(t, "node", node.Name)
		require.Equal(t, "ghcr.io/tempoxyz/tempo:v1.2.3", node.Image)
		require.Equal(t, []string{"tempo"}, node.Command)
		require.Equal(t, "node", node.Args[0])
		require.Equal(t, "/home/operator", node.WorkingDir)

		require.Equal(t, ExecDataDir, argsAfterFlag(t, node.Args, "--datadir"))
		require.Equal(t, "mainnet", argsAfterFlag(t, node.Args, "--chain"))
		require.Equal(t, ConsensusDataDir, argsAfterFlag(t, node.Args, "--consensus.datadir"))
		// rpc role runs with --follow.
		require.Contains(t, node.Args, "--follow")
		// No signing key flags without spec.signingKey.
		require.NotContains(t, node.Args, "--consensus.signing-key")
		require.Equal(t, "30303", argsAfterFlag(t, node.Args, "--port"))
		require.Equal(t, "0.0.0.0", argsAfterFlag(t, node.Args, "--discovery.addr"))
		require.Equal(t, "30303", argsAfterFlag(t, node.Args, "--discovery.port"))
		require.Contains(t, node.Args, "--http")
		require.Equal(t, "0.0.0.0", argsAfterFlag(t, node.Args, "--http.addr"))
		require.Equal(t, "8545", argsAfterFlag(t, node.Args, "--http.port"))
		require.Equal(t, "eth,net,web3,txpool,trace", argsAfterFlag(t, node.Args, "--http.api"))
		require.Equal(t, "9000", argsAfterFlag(t, node.Args, "--metrics"))
		require.NotContains(t, node.Args, "--ws")

		wantEnv := []corev1.EnvVar{
			{Name: "HOME", Value: "/home/operator"},
			{Name: "CHAIN", Value: "mainnet"},
			{Name: "DATA_DIR", Value: ExecDataDir},
			{Name: "CONSENSUS_DATA_DIR", Value: ConsensusDataDir},
		}
		require.Equal(t, wantEnv, node.Env)

		healthcheckC := pod.Spec.Containers[1]
		require.Equal(t, "healthcheck", healthcheckC.Name)
		require.Equal(t, "ghcr.io/aaronforce1/tempo-operator:latest", healthcheckC.Image)
		require.Equal(t, []string{"/manager", "healthcheck", "--rpc-host", "http://localhost:8545"}, healthcheckC.Command)

		// Ports
		portNames := make(map[string]corev1.ContainerPort)
		for _, p := range node.Ports {
			portNames[p.Name+"/"+string(p.Protocol)] = p
		}
		require.EqualValues(t, 30303, portNames["p2p/TCP"].ContainerPort)
		require.EqualValues(t, 30303, portNames["p2p-udp/UDP"].ContainerPort)
		require.EqualValues(t, 8545, portNames["http-rpc/TCP"].ContainerPort)
		require.EqualValues(t, 9000, portNames["metrics/TCP"].ContainerPort)

		// Volumes: exec emptyDir + consensus PVC. No signing key for rpc role.
		require.Len(t, pod.Spec.Volumes, 2)
		require.Equal(t, "vol-exec-data", pod.Spec.Volumes[0].Name)
		require.NotNil(t, pod.Spec.Volumes[0].EmptyDir)
		require.Equal(t, "vol-consensus", pod.Spec.Volumes[1].Name)
		require.Equal(t, "pvc-osmosis-5", pod.Spec.Volumes[1].PersistentVolumeClaim.ClaimName)

		require.Equal(t, []corev1.VolumeMount{
			{Name: "vol-exec-data", MountPath: ExecDataDir},
			{Name: "vol-consensus", MountPath: ConsensusDataDir},
		}, node.VolumeMounts)

		// Sidecar mounts both datadirs read-only for disk usage collection.
		require.Equal(t, []corev1.VolumeMount{
			{Name: "vol-exec-data", MountPath: ExecDataDir, ReadOnly: true},
			{Name: "vol-consensus", MountPath: ConsensusDataDir, ReadOnly: true},
		}, healthcheckC.VolumeMounts)
	})

	t.Run("validator", func(t *testing.T) {
		crd := defaultValidatorCRD()
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)

		node := pod.Spec.Containers[0]
		// Validators do not follow.
		require.NotContains(t, node.Args, "--follow")
		require.Equal(t, "/etc/tempo-keys/signing-key", argsAfterFlag(t, node.Args, "--consensus.signing-key"))
		require.Equal(t, "/etc/tempo-keys/consensus-secret", argsAfterFlag(t, node.Args, "--consensus.secret"))

		// Signing key volume projected from a single secret with 0400 mode.
		require.Len(t, pod.Spec.Volumes, 3)
		keyVol := pod.Spec.Volumes[2]
		require.Equal(t, "vol-signing-key", keyVol.Name)
		require.NotNil(t, keyVol.Projected)
		require.EqualValues(t, 0o400, *keyVol.Projected.DefaultMode)
		require.Len(t, keyVol.Projected.Sources, 1) // same secret for both keys
		items := keyVol.Projected.Sources[0].Secret.Items
		require.Equal(t, "signing-key", items[0].Path)
		require.Equal(t, "consensus-secret", items[1].Path)

		// Mounted read-only into the node container only.
		require.Equal(t, corev1.VolumeMount{Name: "vol-signing-key", MountPath: "/etc/tempo-keys", ReadOnly: true}, node.VolumeMounts[2])
		require.Len(t, pod.Spec.Containers[1].VolumeMounts, 2)

		// Key material must never appear in env vars.
		for _, env := range node.Env {
			require.NotContains(t, strings.ToLower(env.Name), "key")
			require.NotContains(t, strings.ToLower(env.Value), "secret")
		}
	})

	t.Run("validator with separate secrets", func(t *testing.T) {
		crd := defaultValidatorCRD()
		crd.Spec.SigningKey.EncryptionSecret.Name = "other-secret"
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)

		keyVol := pod.Spec.Volumes[2]
		require.Len(t, keyVol.Projected.Sources, 2)
	})

	t.Run("follow override", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.ChainSpec.Follow = ptr(false)
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)
		require.NotContains(t, pod.Spec.Containers[0].Args, "--follow")
	})

	t.Run("websocket enabled", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.RPC.WS = &tempov1alpha1.WSSpec{Enabled: true}
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)

		node := pod.Spec.Containers[0]
		require.Contains(t, node.Args, "--ws")
		require.Equal(t, "8546", argsAfterFlag(t, node.Args, "--ws.port"))

		var wsPort *corev1.ContainerPort
		for i, p := range node.Ports {
			if p.Name == "ws" {
				wsPort = &node.Ports[i]
			}
		}
		require.NotNil(t, wsPort)
		require.EqualValues(t, 8546, wsPort.ContainerPort)
	})

	t.Run("telemetry", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.Telemetry.TelemetryURL = ptr("https://telemetry.example.com")
		crd.Spec.Telemetry.MetricsInterval = &metav1.Duration{Duration: 10 * time.Second}
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)

		node := pod.Spec.Containers[0]
		require.Equal(t, "https://telemetry.example.com", argsAfterFlag(t, node.Args, "--telemetry-url"))
		require.Equal(t, "10s", argsAfterFlag(t, node.Args, "--telemetry-metrics-interval"))
	})

	t.Run("telemetry url from secret", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.Telemetry.TelemetryURL = ptr("https://should-not-see-me.example.com")
		crd.Spec.Telemetry.TelemetryURLSecret = &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "telemetry"},
			Key:                  "url",
		}
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)

		node := pod.Spec.Containers[0]
		// The literal URL never lands in args; k8s expands $(VAR) from env.
		require.Equal(t, "$(TEMPO_TELEMETRY_URL)", argsAfterFlag(t, node.Args, "--telemetry-url"))

		var found bool
		for _, env := range node.Env {
			if env.Name == "TEMPO_TELEMETRY_URL" {
				found = true
				require.Equal(t, "telemetry", env.ValueFrom.SecretKeyRef.Name)
			}
		}
		require.True(t, found)
	})

	t.Run("additional args appended last", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.ChainSpec.AdditionalArgs = []string{"--custom-flag", "value"}
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)

		args := pod.Spec.Containers[0].Args
		require.Equal(t, []string{"--custom-flag", "value"}, args[len(args)-2:])
	})

	t.Run("custom ports", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.P2P.Port = ptr(int32(40404))
		crd.Spec.RPC.Port = ptr(int32(9545))
		crd.Spec.Telemetry.MetricsPort = ptr(int32(9100))
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)

		node := pod.Spec.Containers[0]
		require.Equal(t, "40404", argsAfterFlag(t, node.Args, "--port"))
		require.Equal(t, "9545", argsAfterFlag(t, node.Args, "--http.port"))
		require.Equal(t, "9100", argsAfterFlag(t, node.Args, "--metrics"))
		require.Equal(t, []string{"/manager", "healthcheck", "--rpc-host", "http://localhost:9545"}, pod.Spec.Containers[1].Command)
	})

	t.Run("probes", func(t *testing.T) {
		crd := defaultCRD()
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)

		// Default InSync: main probe on /health, sidecar probe on /.
		require.Equal(t, "/health", pod.Spec.Containers[0].ReadinessProbe.HTTPGet.Path)
		require.EqualValues(t, 8545, pod.Spec.Containers[0].ReadinessProbe.HTTPGet.Port.IntValue())
		require.Equal(t, "/", pod.Spec.Containers[1].ReadinessProbe.HTTPGet.Path)
		require.EqualValues(t, 1251, pod.Spec.Containers[1].ReadinessProbe.HTTPGet.Port.IntValue())

		crd.Spec.PodTemplate.Probes.Strategy = tempov1alpha1.ProbeStrategyReachable
		pod, err = NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)
		require.NotNil(t, pod.Spec.Containers[0].ReadinessProbe)
		require.Nil(t, pod.Spec.Containers[1].ReadinessProbe)

		crd.Spec.PodTemplate.Probes.Strategy = tempov1alpha1.ProbeStrategyNone
		pod, err = NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)
		require.Nil(t, pod.Spec.Containers[0].ReadinessProbe)
		require.Nil(t, pod.Spec.Containers[1].ReadinessProbe)
	})

	t.Run("exec volume ephemeral", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.ExecVolume.Ephemeral = &corev1.EphemeralVolumeSource{
			VolumeClaimTemplate: &corev1.PersistentVolumeClaimTemplate{},
		}
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)
		require.NotNil(t, pod.Spec.Volumes[0].Ephemeral)
		require.Nil(t, pod.Spec.Volumes[0].EmptyDir)
	})

	t.Run("exec volume emptyDir size limit", func(t *testing.T) {
		crd := defaultCRD()
		limit := resource.MustParse("1500Gi")
		crd.Spec.ExecVolume.EmptyDir = &corev1.EmptyDirVolumeSource{SizeLimit: &limit}
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)
		require.Equal(t, "1500Gi", pod.Spec.Volumes[0].EmptyDir.SizeLimit.String())
	})

	t.Run("scheduled upgrade image", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.ScheduledUpgrades = []tempov1alpha1.ScheduledUpgrade{
			{
				ActivatesAt: metav1.NewTime(testNow.Add(3 * time.Hour)),
				Image:       "ghcr.io/tempoxyz/tempo:v2.0.0",
			},
		}

		// Inside the default 6h lead window: the new image applies to node AND init container.
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)
		require.Equal(t, "ghcr.io/tempoxyz/tempo:v2.0.0", pod.Spec.Containers[0].Image)
		require.Equal(t, "ghcr.io/tempoxyz/tempo:v2.0.0", pod.Spec.InitContainers[0].Image)

		// Before the lead window: old image.
		pod, err = NewPodBuilder(&crd, testNow.Add(-4*time.Hour)).WithOrdinal(0).Build()
		require.NoError(t, err)
		require.Equal(t, "ghcr.io/tempoxyz/tempo:v1.2.3", pod.Spec.Containers[0].Image)
	})

	t.Run("instance overrides", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.InstanceOverrides = map[string]tempov1alpha1.InstanceOverridesSpec{
			"osmosis-0": {Image: "ghcr.io/tempoxyz/tempo:override"},
			"osmosis-1": {DisableStrategy: ptr(tempov1alpha1.DisablePod)},
			"osmosis-2": {NodeSelector: map[string]string{"pool": "special"}},
		}

		builder := NewPodBuilder(&crd, testNow)

		pod0, err := builder.WithOrdinal(0).Build()
		require.NoError(t, err)
		require.Equal(t, "ghcr.io/tempoxyz/tempo:override", pod0.Spec.Containers[0].Image)
		require.Equal(t, "ghcr.io/tempoxyz/tempo:override", pod0.Spec.InitContainers[0].Image)

		pod1, err := builder.WithOrdinal(1).Build()
		require.NoError(t, err)
		require.Nil(t, pod1)

		pod2, err := builder.WithOrdinal(2).Build()
		require.NoError(t, err)
		require.Equal(t, map[string]string{"pool": "special"}, pod2.Spec.NodeSelector)
	})

	t.Run("strategic merge pod patch", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.PodTemplate.Volumes = []corev1.Volume{
			{Name: "extra-vol", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		}
		crd.Spec.PodTemplate.NodeSelector = map[string]string{"disk": "local-nvme"}
		crd.Spec.PodTemplate.TerminationGracePeriodSeconds = ptr(int64(120))

		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)

		volNames := make([]string, 0)
		for _, v := range pod.Spec.Volumes {
			volNames = append(volNames, v.Name)
		}
		require.Contains(t, volNames, "extra-vol")
		require.Contains(t, volNames, "vol-exec-data")
		require.Equal(t, map[string]string{"disk": "local-nvme"}, pod.Spec.NodeSelector)
		require.EqualValues(t, 120, *pod.Spec.TerminationGracePeriodSeconds)
	})

	t.Run("long names", func(t *testing.T) {
		crd := defaultCRD()
		crd.Name = strings.Repeat("Y", 300)
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)
		test.RequireValidMetadata(t, pod)
	})

	t.Run("PVCName", func(t *testing.T) {
		crd := defaultCRD()
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(3).Build()
		require.NoError(t, err)
		require.Equal(t, "pvc-osmosis-3", PVCName(pod))
	})
}

func TestInitContainers(t *testing.T) {
	t.Parallel()

	t.Run("auto policy is idempotent", func(t *testing.T) {
		crd := defaultCRD()
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)

		require.Len(t, pod.Spec.InitContainers, 1)
		init := pod.Spec.InitContainers[0]
		require.Equal(t, "snapshot-init", init.Name)
		require.Equal(t, crd.Spec.PodTemplate.Image, init.Image)
		require.Equal(t, []string{"sh"}, init.Command)

		script := init.Args[1]
		// Populated-datadir check preserves node identity files.
		require.Contains(t, script, "discovery-secret")
		require.Contains(t, script, "known-peers.json")
		require.Contains(t, script, "tempo download --chain mainnet --datadir "+ExecDataDir)
		// rpc role defaults to the full profile.
		require.Contains(t, script, "--full")
		require.Contains(t, script, "--non-interactive")
		require.NotContains(t, script, "--force")

		// Only mounts the exec datadir; never touches consensus or key material.
		require.Equal(t, []corev1.VolumeMount{{Name: "vol-exec-data", MountPath: ExecDataDir}}, init.VolumeMounts)
	})

	t.Run("always policy forces refresh", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.SnapshotInit.Policy = tempov1alpha1.SnapshotInitAlways
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)

		script := pod.Spec.InitContainers[0].Args[1]
		require.Contains(t, script, "--force")
		// No populated-dir short circuit when forcing.
		require.NotContains(t, script, "already populated")
	})

	t.Run("never policy omits init container", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.SnapshotInit.Policy = tempov1alpha1.SnapshotInitNever
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)
		require.Empty(t, pod.Spec.InitContainers)
	})

	t.Run("profile defaults by role", func(t *testing.T) {
		for role, want := range map[tempov1alpha1.NodeRole]string{
			tempov1alpha1.NodeRoleRPC:     "--full",
			tempov1alpha1.NodeRoleArchive: "--archive",
		} {
			crd := defaultCRD()
			crd.Spec.Role = role
			pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
			require.NoError(t, err)
			require.Contains(t, pod.Spec.InitContainers[0].Args[1], want, role)
		}

		crd := defaultValidatorCRD()
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)
		require.Contains(t, pod.Spec.InitContainers[0].Args[1], "--minimal")
	})

	t.Run("profile override", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.SnapshotInit.Profile = ptr(tempov1alpha1.SnapshotProfileMinimal)
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)
		require.Contains(t, pod.Spec.InitContainers[0].Args[1], "--minimal")
	})

	t.Run("url and manifest overrides with additional args", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.SnapshotInit.URL = ptr("https://snapshots.example.com/latest")
		crd.Spec.SnapshotInit.ManifestURL = ptr("https://snapshots.example.com/manifest.json")
		crd.Spec.ChainSpec.AdditionalDownloadArgs = []string{"--extra"}
		pod, err := NewPodBuilder(&crd, testNow).WithOrdinal(0).Build()
		require.NoError(t, err)

		script := pod.Spec.InitContainers[0].Args[1]
		require.Contains(t, script, "--url https://snapshots.example.com/latest")
		require.Contains(t, script, "--manifest-url https://snapshots.example.com/manifest.json")
		require.Contains(t, script, "--extra")
	})
}
