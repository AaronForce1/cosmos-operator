package fullnode

import (
	"context"
	"time"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/aaronforce1/cosmos-operator/internal/kube"
	"github.com/aaronforce1/cosmos-operator/internal/tempo"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DriftDetection detects pods that are lagging behind the latest block height.
type DriftDetection struct {
	available      func(pods []*corev1.Pod, minReady time.Duration, now time.Time) []*corev1.Pod
	collector      StatusCollector
	computeRollout func(maxUnavail *intstr.IntOrString, desired, ready int) int
}

func NewDriftDetection(collector StatusCollector) DriftDetection {
	return DriftDetection{
		available:      kube.AvailablePods,
		collector:      collector,
		computeRollout: kube.ComputeRollout,
	}
}

// LaggingPods returns pods that are lagging behind the latest block height.
//
// Validator CRDs never yield lagging pods: automated deletion of a signer is a double-sign
// hazard, so a lagging validator is surfaced by the caller as an event instead.
func (d DriftDetection) LaggingPods(ctx context.Context, crd *tempov1alpha1.TempoFullNode) []*corev1.Pod {
	if crd.IsValidator() {
		return nil
	}

	synced := d.collector.Collect(ctx, client.ObjectKeyFromObject(crd)).Synced()
	if len(synced) == 0 {
		return nil
	}

	maxHeight := lo.MaxBy(synced, func(a tempo.StatusItem, b tempo.StatusItem) bool {
		return a.Status.Height > b.Status.Height
	}).Status.Height

	thresh := uint64(crd.Spec.SelfHeal.HeightDriftMitigation.Threshold)
	lagging := lo.FilterMap(synced, func(item tempo.StatusItem, _ int) (*corev1.Pod, bool) {
		isLagging := maxHeight-item.Status.Height >= thresh
		return item.GetPod(), isLagging
	})

	avail := d.available(synced.Pods(), 5*time.Second, time.Now())
	rollout := d.computeRollout(crd.Spec.RolloutStrategy.MaxUnavailable, int(crd.Spec.Replicas), len(avail))
	return lo.Slice(lagging, 0, rollout)
}
