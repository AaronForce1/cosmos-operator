package fullnode

import (
	"fmt"
	"strings"
	"testing"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/aaronforce1/cosmos-operator/internal/diff"
	"github.com/aaronforce1/cosmos-operator/internal/kube"
	"github.com/aaronforce1/cosmos-operator/internal/test"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestBuildPVCs(t *testing.T) {
	t.Parallel()

	t.Run("happy path", func(t *testing.T) {
		crd := defaultCRD()
		crd.Name = "juno"
		crd.Spec.Replicas = 3
		crd.Spec.ConsensusVolume.StorageClassName = "test-storage-class"

		crd.Spec.InstanceOverrides = map[string]tempov1alpha1.InstanceOverridesSpec{
			"juno-0": {},
		}

		initial := BuildPVCs(&crd, nil)
		for i, r := range initial {
			require.Equal(t, int64(i), r.Ordinal())
			require.NotEmpty(t, r.Revision())
		}

		initialPVCs := lo.Map(initial, func(r diff.Resource[*corev1.PersistentVolumeClaim], _ int) *corev1.PersistentVolumeClaim {
			return r.Object()
		})

		pvcs := lo.Map(BuildPVCs(&crd, initialPVCs), func(r diff.Resource[*corev1.PersistentVolumeClaim], _ int) *corev1.PersistentVolumeClaim {
			return r.Object()
		})

		require.Len(t, pvcs, 3)

		gotNames := lo.Map(pvcs, func(pvc *corev1.PersistentVolumeClaim, _ int) string { return pvc.Name })
		require.Equal(t, []string{"pvc-juno-0", "pvc-juno-1", "pvc-juno-2"}, gotNames)

		for i, got := range pvcs {
			require.Equal(t, crd.Namespace, got.Namespace)
			require.Equal(t, "PersistentVolumeClaim", got.Kind)
			require.Equal(t, "v1", got.APIVersion)

			wantLabels := map[string]string{
				"app.kubernetes.io/created-by": "tempo-operator",
				"app.kubernetes.io/component":  "TempoFullNode",
				"app.kubernetes.io/name":       "juno",
				"app.kubernetes.io/instance":   fmt.Sprintf("juno-%d", i),
				"app.kubernetes.io/version":    "v1.2.3",
				"tempo.aaronforce.io/chain":    "mainnet",
				"tempo.aaronforce.io/role":     "rpc",
			}
			require.Equal(t, wantLabels, got.Labels)

			require.Len(t, got.Spec.AccessModes, 1)
			require.Equal(t, corev1.ReadWriteOnce, got.Spec.AccessModes[0])

			require.Equal(t, crd.Spec.ConsensusVolume.Resources, got.Spec.Resources)
			require.Equal(t, "test-storage-class", *got.Spec.StorageClassName)
			require.Equal(t, corev1.PersistentVolumeFilesystem, *got.Spec.VolumeMode)
		}
	})

	t.Run("happy path with non 0 starting ordinal", func(t *testing.T) {
		crd := defaultCRD()
		crd.Name = "juno"
		crd.Spec.Replicas = 3
		crd.Spec.ConsensusVolume.StorageClassName = "test-storage-class"
		crd.Spec.Ordinals.Start = 2

		crd.Spec.InstanceOverrides = map[string]tempov1alpha1.InstanceOverridesSpec{
			fmt.Sprintf("juno-%d", crd.Spec.Ordinals.Start): {},
		}

		initial := BuildPVCs(&crd, nil)
		require.Equal(t, crd.Spec.Replicas, int32(len(initial)))
		for _, r := range initial {
			require.NotEmpty(t, r.Revision())
		}

		initialPVCs := lo.Map(initial, func(r diff.Resource[*corev1.PersistentVolumeClaim], _ int) *corev1.PersistentVolumeClaim {
			return r.Object()
		})

		pvcs := lo.Map(BuildPVCs(&crd, initialPVCs), func(r diff.Resource[*corev1.PersistentVolumeClaim], _ int) *corev1.PersistentVolumeClaim {
			return r.Object()
		})

		require.Equal(t, crd.Spec.Replicas, int32(len(pvcs)))

		wantNames := make([]string, crd.Spec.Replicas)
		for i := range wantNames {
			wantNames[i] = fmt.Sprintf("pvc-juno-%d", crd.Spec.Ordinals.Start+int32(i))
		}

		gotNames := lo.Map(pvcs, func(pvc *corev1.PersistentVolumeClaim, _ int) string { return pvc.Name })
		require.Equal(t, wantNames, gotNames)
	})

	t.Run("validator is clamped to a single pvc", func(t *testing.T) {
		crd := defaultValidatorCRD()
		crd.Name = "val"
		crd.Spec.Replicas = 3 // Rejected by CEL; clamped by the controller as defense in depth.

		pvcs := BuildPVCs(&crd, nil)
		require.Len(t, pvcs, 1)
		require.Equal(t, "pvc-val-0", pvcs[0].Object().Name)
	})

	t.Run("advanced configuration", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.Replicas = 1
		crd.Spec.ConsensusVolume.Metadata = tempov1alpha1.Metadata{
			Labels:      map[string]string{"label": "value", "app.kubernetes.io/created-by": "should not see me"},
			Annotations: map[string]string{"annot": "value"},
		}
		crd.Spec.ConsensusVolume.VolumeMode = ptr(corev1.PersistentVolumeBlock)

		pvcs := BuildPVCs(&crd, nil)
		require.NotEmpty(t, pvcs)

		got := pvcs[0].Object()
		// Access modes are always forced to RWO; the single-attach fence is a double-sign guard.
		require.Equal(t, []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}, got.Spec.AccessModes)
		require.Equal(t, corev1.PersistentVolumeBlock, *got.Spec.VolumeMode)

		require.Equal(t, "value", got.Annotations["annot"])

		require.Equal(t, "tempo-operator", got.Labels[kube.ControllerLabel])
		require.Equal(t, "value", got.Labels["label"])

		// No dataSource seeding is offered (double-sign guard G6).
		require.Nil(t, got.Spec.DataSource)
		require.Nil(t, got.Spec.DataSourceRef)
	})

	t.Run("instance override", func(t *testing.T) {
		crd := defaultCRD()
		crd.Name = "cosmoshub"
		crd.Spec.Replicas = 3
		crd.Spec.InstanceOverrides = map[string]tempov1alpha1.InstanceOverridesSpec{
			"cosmoshub-0": {
				ConsensusVolume: &tempov1alpha1.ConsensusVolumeSpec{
					StorageClassName: "override",
				},
			},
			"cosmoshub-1": {
				DisableStrategy: ptr(tempov1alpha1.DisableAll),
			},
			"cosmoshub-2": {
				DisableStrategy: ptr(tempov1alpha1.DisablePod),
			},
			"does-not-exist": {
				ConsensusVolume: &tempov1alpha1.ConsensusVolumeSpec{
					StorageClassName: "should never see me",
				},
			},
		}

		pvcs := BuildPVCs(&crd, nil)
		require.Equal(t, 2, len(pvcs))

		got1, got2 := pvcs[0].Object(), pvcs[1].Object()

		require.NotEqual(t, got1.Spec, got2.Spec)
		require.Equal(t, []string{"pvc-cosmoshub-0", "pvc-cosmoshub-2"}, []string{got1.Name, got2.Name})
		require.Equal(t, "override", *got1.Spec.StorageClassName)
	})

	t.Run("long names", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.Replicas = 3
		crd.Name = strings.Repeat("Y", 300)

		pvcs := BuildPVCs(&crd, nil)
		require.NotEmpty(t, pvcs)

		for _, got := range pvcs {
			test.RequireValidMetadata(t, got.Object())
		}
	})

	t.Run("existing bound size is grow-only", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.Replicas = 1
		crd.Spec.ConsensusVolume.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("10Gi")

		existing := &corev1.PersistentVolumeClaim{}
		existing.Name = "pvc-osmosis-0"
		existing.Status.Phase = corev1.ClaimBound
		existing.Status.Capacity = corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("20Gi")}

		pvcs := BuildPVCs(&crd, []*corev1.PersistentVolumeClaim{existing})
		require.Len(t, pvcs, 1)

		got := pvcs[0].Object().Spec.Resources.Requests.Storage()
		want := resource.MustParse("20Gi")
		require.Equal(t, want.Value(), got.Value())
	})

	t.Run("pvc auto scale with padding", func(t *testing.T) {
		for _, tt := range []struct {
			SpecQuant, AutoScaleQuant, WantQuant string
		}{
			{"100G", "97G", "100G"},  // auto scale less than current size
			{"102G", "100G", "102G"}, // auto scale equal to current size (with padding)
			{"100G", "100G", "102G"}, // auto scale greater than current size
		} {
			crd := defaultCRD()
			crd.Spec.Replicas = 1
			crd.Spec.ConsensusVolume.Resources.Requests[corev1.ResourceStorage] = resource.MustParse(tt.SpecQuant)

			crd.Status.SelfHealing.PVCAutoScale = map[string]*tempov1alpha1.PVCAutoScaleStatus{
				"pvc-osmosis-0": {
					RequestedSize: resource.MustParse(tt.AutoScaleQuant),
				},
			}

			pvcs := BuildPVCs(&crd, nil)
			require.Len(t, pvcs, 1, tt)

			want := corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(tt.WantQuant)}
			require.Equal(t, want.Storage().Value(), pvcs[0].Object().Spec.Resources.Requests.Storage().Value(), tt)
		}
	})

	test.HasRoleLabel(t, func(crd tempov1alpha1.TempoFullNode) []map[string]string {
		crd.Spec.ConsensusVolume.Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
		}
		pvcs := BuildPVCs(&crd, nil)
		labels := make([]map[string]string, 0)
		for _, pvc := range pvcs {
			labels = append(labels, pvc.Object().Labels)
		}
		return labels
	})
}
