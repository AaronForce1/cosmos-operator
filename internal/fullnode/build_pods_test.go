package fullnode

import (
	"fmt"
	"testing"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/stretchr/testify/require"
)

func TestBuildPods(t *testing.T) {
	t.Parallel()

	t.Run("happy path", func(t *testing.T) {
		crd := defaultCRD()
		crd.Name = "agoric"
		crd.Spec.Replicas = 5

		pods, err := BuildPods(&crd, testNow)
		require.NoError(t, err)
		require.Len(t, pods, 5)

		for i, pod := range pods {
			require.EqualValues(t, i, pod.Ordinal())
			require.NotEmpty(t, pod.Revision())
			require.Equal(t, fmt.Sprintf("agoric-%d", i), pod.Object().Name)
		}
	})

	t.Run("non zero starting ordinal", func(t *testing.T) {
		crd := defaultCRD()
		crd.Name = "agoric"
		crd.Spec.Replicas = 3
		crd.Spec.Ordinals.Start = 10

		pods, err := BuildPods(&crd, testNow)
		require.NoError(t, err)
		require.Len(t, pods, 3)
		require.Equal(t, "agoric-10", pods[0].Object().Name)
		require.Equal(t, "agoric-12", pods[2].Object().Name)
	})

	t.Run("validator clamped to one pod", func(t *testing.T) {
		crd := defaultValidatorCRD()
		crd.Name = "val"
		crd.Spec.Replicas = 3 // Rejected by CEL; clamped here as defense in depth.

		pods, err := BuildPods(&crd, testNow)
		require.NoError(t, err)
		require.Len(t, pods, 1)
		require.Equal(t, "val-0", pods[0].Object().Name)
	})

	t.Run("instance override disable", func(t *testing.T) {
		crd := defaultCRD()
		crd.Name = "agoric"
		crd.Spec.Replicas = 3
		crd.Spec.InstanceOverrides = map[string]tempov1alpha1.InstanceOverridesSpec{
			"agoric-1": {DisableStrategy: ptr(tempov1alpha1.DisablePod)},
		}

		pods, err := BuildPods(&crd, testNow)
		require.NoError(t, err)
		require.Len(t, pods, 2)
		require.Equal(t, "agoric-0", pods[0].Object().Name)
		require.Equal(t, "agoric-2", pods[1].Object().Name)
	})

	t.Run("image change produces new revision", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.Replicas = 1

		pods1, err := BuildPods(&crd, testNow)
		require.NoError(t, err)

		crd.Spec.PodTemplate.Image = "ghcr.io/tempoxyz/tempo:v9.9.9"
		pods2, err := BuildPods(&crd, testNow)
		require.NoError(t, err)

		require.NotEqual(t, pods1[0].Revision(), pods2[0].Revision())
	})
}
