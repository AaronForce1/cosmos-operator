package fullnode

import (
	"context"
	"testing"
	"time"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/aaronforce1/cosmos-operator/internal/diff"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type mockCacheInvalidator struct {
	GotKey  client.ObjectKey
	GotPods []string
}

func (m *mockCacheInvalidator) Invalidate(controller client.ObjectKey, pods []string) {
	m.GotKey = controller
	m.GotPods = pods
}

func TestPodControl_Reconcile(t *testing.T) {
	t.Parallel()

	type mockPodClient = mockClient[*corev1.Pod]

	ctx := context.Background()
	const namespace = "test"

	buildPodList := func(crd *tempov1alpha1.TempoFullNode) []corev1.Pod {
		want, err := BuildPods(crd, testNow)
		require.NoError(t, err)
		// diff.New stamps the revision label and ordinal annotation onto created objects,
		// mirroring what the pods look like once created in the cluster.
		creates := diff.New(nil, want).Creates()
		pods := make([]corev1.Pod, 0, len(creates))
		for _, p := range creates {
			pods = append(pods, *p)
		}
		return pods
	}

	testControl := func(client Client, invalidator CacheInvalidator) PodControl {
		control := NewPodControl(client, invalidator)
		control.now = func() time.Time { return testNow }
		return control
	}

	t.Run("no changes", func(t *testing.T) {
		crd := defaultCRD()
		crd.Namespace = namespace
		crd.Spec.Replicas = 1

		var mClient mockPodClient
		mClient.ObjectList = corev1.PodList{Items: buildPodList(&crd)}

		control := testControl(&mClient, nil)
		result, err := control.Reconcile(ctx, nopReporter, &crd, nil)
		require.NoError(t, err)
		require.False(t, result.Requeue)
		require.False(t, result.WaitingForFence)
		require.Zero(t, mClient.CreateCount)
		require.Zero(t, mClient.DeleteCount)
	})

	t.Run("scale up creates pods", func(t *testing.T) {
		crd := defaultCRD()
		crd.Namespace = namespace
		crd.Spec.Replicas = 1

		var mClient mockPodClient
		mClient.ObjectList = corev1.PodList{Items: buildPodList(&crd)}

		crd.Spec.Replicas = 3
		control := testControl(&mClient, nil)
		result, err := control.Reconcile(ctx, nopReporter, &crd, nil)
		require.NoError(t, err)
		require.True(t, result.Requeue)
		require.Equal(t, 2, mClient.CreateCount)
		require.Zero(t, mClient.DeleteCount)

		require.NotEmpty(t, mClient.LastCreateObject.OwnerReferences)
		require.Equal(t, "TempoFullNode", mClient.LastCreateObject.OwnerReferences[0].Kind)
	})

	t.Run("scale down deletes pods and invalidates cache", func(t *testing.T) {
		crd := defaultCRD()
		crd.Namespace = namespace
		crd.Spec.Replicas = 3

		var mClient mockPodClient
		mClient.ObjectList = corev1.PodList{Items: buildPodList(&crd)}

		crd.Spec.Replicas = 1
		invalidator := &mockCacheInvalidator{}
		control := testControl(&mClient, invalidator)
		syncInfo := map[string]*tempov1alpha1.SyncInfoPodStatus{
			"osmosis-1": {}, "osmosis-2": {},
		}
		result, err := control.Reconcile(ctx, nopReporter, &crd, syncInfo)
		require.NoError(t, err)
		require.True(t, result.Requeue)
		require.Equal(t, 2, mClient.DeleteCount)
		require.ElementsMatch(t, []string{"osmosis-1", "osmosis-2"}, invalidator.GotPods)
		require.Empty(t, syncInfo)
	})

	t.Run("rolling update honors budget from in-sync pods", func(t *testing.T) {
		crd := defaultCRD()
		crd.Namespace = namespace
		crd.Spec.Replicas = 5
		crd.Spec.RolloutStrategy.MaxUnavailable = ptr(intstr.FromInt(2))

		existing := buildPodList(&crd)
		// Change the image so every pod requires an update.
		crd.Spec.PodTemplate.Image = "ghcr.io/tempoxyz/tempo:v9.9.9"

		var mClient mockPodClient
		mClient.ObjectList = corev1.PodList{Items: existing}

		syncInfo := map[string]*tempov1alpha1.SyncInfoPodStatus{}
		for _, p := range existing {
			syncInfo[p.Name] = &tempov1alpha1.SyncInfoPodStatus{InSync: ptr(true)}
		}

		var gotMaxUnavail *intstr.IntOrString
		var gotReady int
		control := testControl(&mClient, &mockCacheInvalidator{})
		control.computeRollout = func(maxUnavail *intstr.IntOrString, desired, ready int) int {
			gotMaxUnavail = maxUnavail
			gotReady = ready
			return 2
		}

		result, err := control.Reconcile(ctx, nopReporter, &crd, syncInfo)
		require.NoError(t, err)
		require.True(t, result.Requeue) // more pods left to update
		require.Equal(t, 2, mClient.DeleteCount)
		require.Equal(t, ptr(intstr.FromInt(2)), gotMaxUnavail)
		require.Equal(t, 5, gotReady)
	})

	t.Run("rollout falls back to rpc-reachable when none in sync", func(t *testing.T) {
		crd := defaultCRD()
		crd.Namespace = namespace
		crd.Spec.Replicas = 2

		existing := buildPodList(&crd)
		crd.Spec.PodTemplate.Image = "ghcr.io/tempoxyz/tempo:v9.9.9"

		var mClient mockPodClient
		mClient.ObjectList = corev1.PodList{Items: existing}

		syncInfo := map[string]*tempov1alpha1.SyncInfoPodStatus{
			"osmosis-0": {InSync: ptr(false)},
			"osmosis-1": {InSync: ptr(false), Error: ptr("unreachable")},
		}

		var gotReady int
		control := testControl(&mClient, &mockCacheInvalidator{})
		control.computeRollout = func(maxUnavail *intstr.IntOrString, desired, ready int) int {
			gotReady = ready
			return 1
		}

		_, err := control.Reconcile(ctx, nopReporter, &crd, syncInfo)
		require.NoError(t, err)
		require.Equal(t, 1, gotReady) // only the reachable pod counts
	})

	t.Run("validator update clamps budget to 1", func(t *testing.T) {
		crd := defaultValidatorCRD()
		crd.Namespace = namespace
		// User sets an aggressive strategy; the controller must ignore it for validators.
		crd.Spec.RolloutStrategy.MaxUnavailable = ptr(intstr.FromString("100%"))

		existing := buildPodList(&crd)
		crd.Spec.PodTemplate.Image = "ghcr.io/tempoxyz/tempo:v9.9.9"

		var mClient mockPodClient
		mClient.ObjectList = corev1.PodList{Items: existing}

		var gotMaxUnavail *intstr.IntOrString
		control := testControl(&mClient, &mockCacheInvalidator{})
		control.computeRollout = func(maxUnavail *intstr.IntOrString, desired, ready int) int {
			gotMaxUnavail = maxUnavail
			return 1
		}

		syncInfo := map[string]*tempov1alpha1.SyncInfoPodStatus{"val-0": {InSync: ptr(true)}}
		_, err := control.Reconcile(ctx, nopReporter, &crd, syncInfo)
		require.NoError(t, err)
		require.Equal(t, intstr.FromInt(1), *gotMaxUnavail)
		require.Equal(t, 1, mClient.DeleteCount)
	})

	t.Run("validator never deletes while a pod is terminating", func(t *testing.T) {
		crd := defaultValidatorCRD()
		crd.Namespace = namespace

		existing := buildPodList(&crd)
		// Pod is mid-termination (within grace period).
		deleted := metav1.NewTime(testNow.Add(-10 * time.Second))
		existing[0].DeletionTimestamp = &deleted
		crd.Spec.PodTemplate.Image = "ghcr.io/tempoxyz/tempo:v9.9.9"

		var mClient mockPodClient
		mClient.ObjectList = corev1.PodList{Items: existing}

		control := testControl(&mClient, &mockCacheInvalidator{})
		result, err := control.Reconcile(ctx, nopReporter, &crd, map[string]*tempov1alpha1.SyncInfoPodStatus{})
		require.NoError(t, err)
		require.True(t, result.Requeue)
		require.False(t, result.WaitingForFence) // within grace; not stuck yet
		require.Zero(t, mClient.DeleteCount)
	})

	t.Run("validator stuck terminating past grace reports WaitingForFence", func(t *testing.T) {
		crd := defaultValidatorCRD()
		crd.Namespace = namespace

		existing := buildPodList(&crd)
		// Deletion started long past grace period (30s default) + slack.
		deleted := metav1.NewTime(testNow.Add(-5 * time.Minute))
		existing[0].DeletionTimestamp = &deleted
		existing[0].Spec.TerminationGracePeriodSeconds = ptr(int64(30))
		crd.Spec.PodTemplate.Image = "ghcr.io/tempoxyz/tempo:v9.9.9"

		var mClient mockPodClient
		mClient.ObjectList = corev1.PodList{Items: existing}

		control := testControl(&mClient, &mockCacheInvalidator{})
		result, err := control.Reconcile(ctx, nopReporter, &crd, map[string]*tempov1alpha1.SyncInfoPodStatus{})
		require.NoError(t, err)
		require.True(t, result.WaitingForFence)
		// No force-delete, no extra delete calls.
		require.Zero(t, mClient.DeleteCount)
	})

	t.Run("non-validator terminating pods do not gate updates", func(t *testing.T) {
		crd := defaultCRD()
		crd.Namespace = namespace
		crd.Spec.Replicas = 2

		existing := buildPodList(&crd)
		deleted := metav1.NewTime(testNow.Add(-5 * time.Minute))
		existing[0].DeletionTimestamp = &deleted
		crd.Spec.PodTemplate.Image = "ghcr.io/tempoxyz/tempo:v9.9.9"

		var mClient mockPodClient
		mClient.ObjectList = corev1.PodList{Items: existing}

		control := testControl(&mClient, &mockCacheInvalidator{})
		control.computeRollout = func(*intstr.IntOrString, int, int) int { return 2 }
		result, err := control.Reconcile(ctx, nopReporter, &crd, map[string]*tempov1alpha1.SyncInfoPodStatus{})
		require.NoError(t, err)
		require.False(t, result.WaitingForFence)
		require.NotZero(t, mClient.DeleteCount)
	})
}
