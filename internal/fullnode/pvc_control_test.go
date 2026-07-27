package fullnode

import (
	"context"
	"testing"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/aaronforce1/cosmos-operator/internal/diff"
	"github.com/aaronforce1/cosmos-operator/internal/test"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var nopReporter test.NopReporter

func TestPVCControl_Reconcile(t *testing.T) {
	t.Parallel()

	type mockPVCClient = mockClient[*corev1.PersistentVolumeClaim]

	ctx := context.Background()
	const namespace = "test"

	t.Run("no changes", func(t *testing.T) {
		crd := defaultCRD()
		crd.Name = "hub"
		crd.Namespace = namespace
		crd.Spec.Replicas = 1
		existing := diff.New(nil, BuildPVCs(&crd, nil)).Creates()[0]
		existing.Status.Phase = corev1.ClaimBound

		var mClient mockPVCClient
		mClient.ObjectList = corev1.PersistentVolumeClaimList{
			Items: []corev1.PersistentVolumeClaim{
				*existing,
			},
		}

		control := NewPVCControl(&mClient)

		requeue, err := control.Reconcile(ctx, nopReporter, &crd, &PVCStatusChanges{})
		require.NoError(t, err)
		require.False(t, requeue)

		require.Len(t, mClient.GotListOpts, 2)
		var listOpt client.ListOptions
		for _, opt := range mClient.GotListOpts {
			opt.ApplyToList(&listOpt)
		}
		require.Equal(t, namespace, listOpt.Namespace)
		require.Zero(t, listOpt.Limit)
		require.Equal(t, ".metadata.controller=hub", listOpt.FieldSelector.String())

		require.Empty(t, mClient.LastPatchObject)
	})

	t.Run("scale phase", func(t *testing.T) {
		crd := defaultCRD()
		crd.Namespace = namespace
		crd.Name = "hub"
		crd.Spec.Replicas = 1
		existing := BuildPVCs(&crd, nil)[0].Object()

		var mClient mockPVCClient
		mClient.ObjectList = corev1.PersistentVolumeClaimList{
			Items: []corev1.PersistentVolumeClaim{
				{ObjectMeta: metav1.ObjectMeta{Name: "pvc-hub-98", Namespace: namespace}}, // delete
				{ObjectMeta: metav1.ObjectMeta{Name: "pvc-hub-99", Namespace: namespace}}, // delete
				*existing,
			},
		}

		crd.Spec.Replicas = 4
		control := NewPVCControl(&mClient)
		requeue, err := control.Reconcile(ctx, nopReporter, &crd, &PVCStatusChanges{})
		require.NoError(t, err)
		require.True(t, requeue)

		require.Equal(t, 3, mClient.CreateCount)
		require.Equal(t, 2, mClient.DeleteCount)
		require.Zero(t, mClient.UpdateCount)

		require.NotEmpty(t, mClient.LastCreateObject.OwnerReferences)
		require.Equal(t, crd.Name, mClient.LastCreateObject.OwnerReferences[0].Name)
		require.Equal(t, "TempoFullNode", mClient.LastCreateObject.OwnerReferences[0].Kind)
		require.True(t, *mClient.LastCreateObject.OwnerReferences[0].Controller)
	})

	t.Run("updates", func(t *testing.T) {
		crd := defaultCRD()
		crd.Namespace = namespace
		crd.Name = "hub"
		crd.Spec.Replicas = 1
		existing := BuildPVCs(&crd, nil)[0].Object()
		existing.Status.Phase = corev1.ClaimBound

		var mClient mockPVCClient
		mClient.ObjectList = corev1.PersistentVolumeClaimList{
			Items: []corev1.PersistentVolumeClaim{*existing},
		}

		// Increase requested size to trigger an update.
		crd.Spec.ConsensusVolume.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("50Gi")

		control := NewPVCControl(&mClient)
		requeue, err := control.Reconcile(ctx, nopReporter, &crd, &PVCStatusChanges{})
		require.NoError(t, err)
		require.False(t, requeue)

		require.Zero(t, mClient.CreateCount)
		require.Zero(t, mClient.DeleteCount)
		require.Equal(t, 1, mClient.PatchCount)

		// Only the storage size is patched; immutable fields are omitted.
		patched, ok := mClient.LastPatchObject.(*corev1.PersistentVolumeClaim)
		require.True(t, ok)
		require.Nil(t, patched.Spec.StorageClassName)
		gotSize := patched.Spec.Resources.Requests[corev1.ResourceStorage]
		require.Equal(t, "50Gi", gotSize.String())
	})

	t.Run("updates with unbound volumes requeues", func(t *testing.T) {
		crd := defaultCRD()
		crd.Namespace = namespace
		crd.Name = "hub"
		crd.Spec.Replicas = 1
		existing := BuildPVCs(&crd, nil)[0].Object()
		existing.Status.Phase = corev1.ClaimPending

		var mClient mockPVCClient
		mClient.ObjectList = corev1.PersistentVolumeClaimList{
			Items: []corev1.PersistentVolumeClaim{*existing},
		}

		crd.Spec.ConsensusVolume.Resources.Requests[corev1.ResourceStorage] = resource.MustParse("50Gi")

		control := NewPVCControl(&mClient)
		requeue, err := control.Reconcile(ctx, nopReporter, &crd, &PVCStatusChanges{})
		require.NoError(t, err)
		require.True(t, requeue)
		require.Zero(t, mClient.PatchCount)
	})

	t.Run("retention policy retain", func(t *testing.T) {
		crd := defaultCRD()
		crd.Namespace = namespace
		crd.Name = "hub"
		crd.Spec.Replicas = 0
		crd.Spec.RetentionPolicy = ptr(tempov1alpha1.RetentionPolicyRetain)

		var mClient mockPVCClient
		mClient.ObjectList = corev1.PersistentVolumeClaimList{
			Items: []corev1.PersistentVolumeClaim{
				{ObjectMeta: metav1.ObjectMeta{Name: "pvc-hub-0", Namespace: namespace}},
			},
		}

		control := NewPVCControl(&mClient)
		requeue, err := control.Reconcile(ctx, nopReporter, &crd, &PVCStatusChanges{})
		require.NoError(t, err)
		require.False(t, requeue)

		require.Zero(t, mClient.DeleteCount)
	})
}
