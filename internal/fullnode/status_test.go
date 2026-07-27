package fullnode

import (
	"context"
	"errors"
	"testing"
	"time"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/aaronforce1/cosmos-operator/internal/tempo"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestResetStatus(t *testing.T) {
	t.Parallel()

	var crd tempov1alpha1.TempoFullNode
	crd.Generation = 123
	crd.Status.StatusMessage = ptr("should not see me")
	crd.Status.Phase = "should not see me"
	ResetStatus(&crd)

	require.EqualValues(t, 123, crd.Status.ObservedGeneration)
	require.Nil(t, crd.Status.StatusMessage)
	require.Equal(t, tempov1alpha1.TempoFullNodePhaseProgressing, crd.Status.Phase)
}

type mockStatusCollector struct {
	CollectFn func(ctx context.Context, controller client.ObjectKey) tempo.StatusCollection
}

func (m mockStatusCollector) Collect(ctx context.Context, controller client.ObjectKey) tempo.StatusCollection {
	return m.CollectFn(ctx, controller)
}

func TestSyncInfoStatus(t *testing.T) {
	t.Parallel()

	const (
		name      = "agoric"
		namespace = "default"
	)

	var crd tempov1alpha1.TempoFullNode
	crd.Name = name
	crd.Namespace = namespace

	ts := time.Now()

	var collector mockStatusCollector
	collector.CollectFn = func(ctx context.Context, controller client.ObjectKey) tempo.StatusCollection {
		require.NotNil(t, ctx)
		require.Equal(t, name, controller.Name)
		require.Equal(t, namespace, controller.Namespace)

		var notInSync tempo.NodeStatus
		notInSync.Syncing = true
		notInSync.Height = 9999
		notInSync.LatestBlockTime = ts

		var inSync tempo.NodeStatus
		inSync.Height = 10000
		inSync.LatestBlockTime = ts

		return tempo.StatusCollection{
			// Purposefully out of order to test sorting.
			{Pod: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-0"}}, Status: notInSync, TS: ts},
			{Pod: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-1"}}, Status: inSync, TS: ts},
			{Pod: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-2"}}, Err: errors.New("some error"), TS: ts},
		}
	}

	wantTS := metav1.NewTime(ts)
	want := map[string]*tempov1alpha1.SyncInfoPodStatus{
		"pod-0": {
			Timestamp: wantTS,
			Height:    ptr(uint64(9999)),
			PeerCount: ptr(uint64(0)),
			InSync:    ptr(false),
		},
		"pod-1": {
			Timestamp: wantTS,
			Height:    ptr(uint64(10000)),
			PeerCount: ptr(uint64(0)),
			InSync:    ptr(true),
		},
		"pod-2": {
			Timestamp: wantTS,
			Error:     ptr("some error"),
		},
	}

	status := SyncInfoStatus(context.Background(), &crd, collector)
	require.Equal(t, want, status)
}
