package fullnode

import (
	"context"
	"fmt"
	"time"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/aaronforce1/cosmos-operator/internal/diff"
	"github.com/aaronforce1/cosmos-operator/internal/kube"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Client is a controller client. It is a subset of client.Client.
type Client interface {
	client.Reader
	client.Writer

	Scheme() *runtime.Scheme
}

type CacheInvalidator interface {
	Invalidate(controller client.ObjectKey, pods []string)
}

// PodControlResult reports the outcome of a pod reconcile pass.
type PodControlResult struct {
	// Requeue indicates the controller should requeue the request.
	Requeue bool
	// WaitingForFence indicates a validator pod is stuck terminating past its grace period.
	// The replacement is deliberately gated on the old pod fully disappearing (double-sign guard);
	// availability is sacrificed for key safety. Surfaced as its own phase so operators can follow
	// the fencing runbook instead of force-deleting.
	WaitingForFence bool
}

// PodControl reconciles pods for a TempoFullNode.
type PodControl struct {
	client           Client
	cacheInvalidator CacheInvalidator
	computeRollout   func(maxUnavail *intstr.IntOrString, desired, ready int) int
	now              func() time.Time
}

// NewPodControl returns a valid PodControl.
func NewPodControl(client Client, cacheInvalidator CacheInvalidator) PodControl {
	return PodControl{
		client:           client,
		cacheInvalidator: cacheInvalidator,
		computeRollout:   kube.ComputeRollout,
		now:              time.Now,
	}
}

// validatorMaxUnavailable clamps the rollout budget to a single pod for validators.
var validatorMaxUnavailable = intstr.FromInt(1)

// Reconcile is the control loop for pods.
//
// Double-sign guards (see docs/double_sign_safety.md):
//   - Creates derive purely from API state via the diff: a pod is only created once no pod object
//     with the same name exists in any phase, including Terminating. A controller restart re-enters
//     the same gate (no in-memory rollout bookkeeping influences creates).
//   - Validator pods are never force-deleted, and no new delete is issued while a previous
//     validator pod is still terminating. A pod stuck terminating past its grace period surfaces
//     as WaitingForFence rather than being replaced.
//   - The rollout budget is clamped to 1 for validators regardless of spec.strategy.
func (pc PodControl) Reconcile(
	ctx context.Context,
	reporter kube.Reporter,
	crd *tempov1alpha1.TempoFullNode,
	syncInfo map[string]*tempov1alpha1.SyncInfoPodStatus,
) (PodControlResult, kube.ReconcileError) {
	var result PodControlResult

	var pods corev1.PodList
	if err := pc.client.List(ctx, &pods,
		client.InNamespace(crd.Namespace),
		client.MatchingFields{kube.ControllerOwnerField: crd.Name},
	); err != nil {
		return result, kube.TransientError(fmt.Errorf("list existing pods: %w", err))
	}

	wantPods, err := BuildPods(crd, pc.now())
	if err != nil {
		return result, kube.UnrecoverableError(fmt.Errorf("build pods: %w", err))
	}
	diffed := diff.New(ptrSlice(pods.Items), wantPods)

	result.WaitingForFence = pc.detectStuckTermination(crd, pods.Items)
	if result.WaitingForFence {
		reporter.Info("Validator pod stuck terminating; replacement gated on fence (double-sign guard)")
		reporter.RecordInfo("WaitingForFence", "A validator pod is stuck terminating. Its replacement is gated until the pod fully terminates and releases its consensus volume. Do NOT force-delete; see the fencing runbook.")
	}

	for _, pod := range diffed.Creates() {
		reporter.Info("Creating pod", "name", pod.Name)
		if err := ctrl.SetControllerReference(crd, pod, pc.client.Scheme()); err != nil {
			return requeue(result), kube.TransientError(fmt.Errorf("set controller reference on pod %q: %w", pod.Name, err))
		}
		if err := pc.client.Create(ctx, pod); kube.IgnoreAlreadyExists(err) != nil {
			return requeue(result), kube.TransientError(fmt.Errorf("create pod %q: %w", pod.Name, err))
		}
	}

	var invalidateCache []string

	defer func() {
		if pc.cacheInvalidator == nil {
			return
		}
		if len(invalidateCache) > 0 {
			pc.cacheInvalidator.Invalidate(client.ObjectKeyFromObject(crd), invalidateCache)
		}
	}()

	for _, pod := range diffed.Deletes() {
		reporter.Info("Deleting pod", "name", pod.Name)
		if err := pc.client.Delete(ctx, pod, client.PropagationPolicy(metav1.DeletePropagationForeground)); kube.IgnoreNotFound(err) != nil {
			return requeue(result), kube.TransientError(fmt.Errorf("delete pod %q: %w", pod.Name, err))
		}
		delete(syncInfo, pod.Name)
		invalidateCache = append(invalidateCache, pod.Name)
	}

	if len(diffed.Creates())+len(diffed.Deletes()) > 0 {
		// Scaling happens first; then updates. So requeue to handle updates after scaling finished.
		return requeue(result), nil
	}

	diffedUpdates := diffed.Updates()
	if len(diffedUpdates) == 0 {
		// Finished, pod state matches CRD.
		return result, nil
	}

	// A validator pod that is still terminating means an update round is already in flight;
	// never issue another delete until the previous holder is fully gone.
	if crd.IsValidator() && anyTerminating(pods.Items) {
		return requeue(result), nil
	}

	var (
		inSyncPods       = 0
		rpcReachablePods = 0
	)
	for _, existing := range pods.Items {
		if existing.DeletionTimestamp != nil {
			continue
		}
		if ps, ok := syncInfo[existing.Name]; ok {
			if ps.InSync != nil && *ps.InSync {
				inSyncPods++
			}
			if ps.Error == nil {
				rpcReachablePods++
			}
		}
	}

	// If we don't have any pods in sync, we are down anyway, so we use the number of RPC reachable
	// pods for computing the rollout, with the goal of recovering as quickly as possible.
	ready := inSyncPods
	if ready == 0 {
		ready = rpcReachablePods
	}

	maxUnavail := crd.Spec.RolloutStrategy.MaxUnavailable
	if crd.IsValidator() {
		maxUnavail = &validatorMaxUnavailable
	}
	numUpdates := pc.computeRollout(maxUnavail, int(crd.Spec.Replicas), ready)

	var updated int
	for _, pod := range diffedUpdates {
		podName := pod.Name
		reporter.Info("Deleting pod for update", "name", podName)
		// Because we watch for deletes, we get a re-queued request, detect the pod is missing,
		// and re-create it.
		if err := pc.client.Delete(ctx, pod, client.PropagationPolicy(metav1.DeletePropagationForeground)); client.IgnoreNotFound(err) != nil {
			return requeue(result), kube.TransientError(fmt.Errorf("update pod %q: %w", podName, err))
		}
		if ps, ok := syncInfo[podName]; ok {
			ps.InSync = nil
			ps.Error = ptr("update in progress")
		}
		invalidateCache = append(invalidateCache, podName)
		updated++
		if updated >= numUpdates {
			// Done for this round.
			break
		}
	}

	if updated < len(diffedUpdates) {
		// Not all pods are updated yet.
		return requeue(result), nil
	}

	return result, nil
}

func requeue(result PodControlResult) PodControlResult {
	result.Requeue = true
	return result
}

func anyTerminating(pods []corev1.Pod) bool {
	for i := range pods {
		if pods[i].DeletionTimestamp != nil {
			return true
		}
	}
	return false
}

// detectStuckTermination reports whether a validator pod has been terminating past its grace
// period plus slack. That is the signature of an unreachable kubelet (node failure or partition):
// the pod object stays in the API and the replacement stays gated. This is correct behavior for
// a signer; the operator surfaces it loudly instead of forcing progress.
func (pc PodControl) detectStuckTermination(crd *tempov1alpha1.TempoFullNode, pods []corev1.Pod) bool {
	if !crd.IsValidator() {
		return false
	}
	const slack = 30 * time.Second
	now := pc.now()
	for i := range pods {
		pod := pods[i]
		if pod.DeletionTimestamp == nil {
			continue
		}
		grace := int64(30)
		if pod.Spec.TerminationGracePeriodSeconds != nil {
			grace = *pod.Spec.TerminationGracePeriodSeconds
		}
		deadline := pod.DeletionTimestamp.Time.Add(time.Duration(grace)*time.Second + slack)
		if now.After(deadline) {
			return true
		}
	}
	return false
}
