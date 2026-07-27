package fullnode

import (
	"context"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/aaronforce1/cosmos-operator/internal/tempo"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ResetStatus is used at the beginning of the reconcile loop.
// It resets the crd's status to a fresh state.
func ResetStatus(crd *tempov1alpha1.TempoFullNode) {
	crd.Status.ObservedGeneration = crd.Generation
	crd.Status.Phase = tempov1alpha1.TempoFullNodePhaseProgressing
	crd.Status.StatusMessage = nil
}

type StatusCollector interface {
	Collect(ctx context.Context, controller client.ObjectKey) tempo.StatusCollection
}

// SyncInfoStatus returns the status of the full node's sync info.
func SyncInfoStatus(
	ctx context.Context,
	crd *tempov1alpha1.TempoFullNode,
	collector StatusCollector,
) map[string]*tempov1alpha1.SyncInfoPodStatus {
	status := make(map[string]*tempov1alpha1.SyncInfoPodStatus, crd.Spec.Replicas)

	coll := collector.Collect(ctx, client.ObjectKeyFromObject(crd))

	for _, item := range coll {
		var stat tempov1alpha1.SyncInfoPodStatus
		podName := item.GetPod().Name
		stat.Timestamp = metav1.NewTime(item.Timestamp())
		nodeStatus, err := item.GetStatus()
		if err != nil {
			stat.Error = ptr(err.Error())
			status[podName] = &stat
			continue
		}
		stat.Height = ptr(nodeStatus.Height)
		stat.PeerCount = ptr(nodeStatus.PeerCount)
		stat.InSync = ptr(nodeStatus.CaughtUp(item.Timestamp()))
		status[podName] = &stat
	}

	return status
}
