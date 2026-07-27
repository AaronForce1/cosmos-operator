package fullnode

import (
	"context"
	"fmt"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/aaronforce1/cosmos-operator/internal/diff"
	"github.com/aaronforce1/cosmos-operator/internal/kube"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PVCControl reconciles the consensus-datadir volumes for a TempoFullNode.
// Unlike StatefulSet, PVCControl will update volumes by deleting and recreating volumes.
type PVCControl struct {
	client Client
}

// NewPVCControl returns a valid PVCControl
func NewPVCControl(client Client) PVCControl {
	return PVCControl{
		client: client,
	}
}

type PVCStatusChanges struct {
	Deleted []string
}

// Reconcile is the control loop for PVCs. The bool return value, if true, indicates the controller should requeue
// the request.
func (control PVCControl) Reconcile(ctx context.Context, reporter kube.Reporter, crd *tempov1alpha1.TempoFullNode, pvcStatusChanges *PVCStatusChanges) (bool, kube.ReconcileError) {
	// Find any existing pvcs for this CRD.
	var vols corev1.PersistentVolumeClaimList
	if err := control.client.List(ctx, &vols,
		client.InNamespace(crd.Namespace),
		client.MatchingFields{kube.ControllerOwnerField: crd.Name},
	); err != nil {
		return false, kube.TransientError(fmt.Errorf("list existing pvcs: %w", err))
	}

	var (
		currentPVCs = ptrSlice(vols.Items)
		wantPVCs    = BuildPVCs(crd, currentPVCs)
		diffed      = diff.New(currentPVCs, wantPVCs)
	)

	for _, pvc := range diffed.Creates() {
		size := pvc.Spec.Resources.Requests[corev1.ResourceStorage]

		reporter.Info(
			"Creating pvc",
			"name", pvc.Name,
			"size", size.String(),
		)
		if err := ctrl.SetControllerReference(crd, pvc, control.client.Scheme()); err != nil {
			return true, kube.TransientError(fmt.Errorf("set controller reference on pvc %q: %w", pvc.Name, err))
		}
		if err := control.client.Create(ctx, pvc); kube.IgnoreAlreadyExists(err) != nil {
			return true, kube.TransientError(fmt.Errorf("create pvc %q: %w", pvc.Name, err))
		}
		pvcStatusChanges.Deleted = append(pvcStatusChanges.Deleted, pvc.Name)
	}

	var deletes int
	if !control.shouldRetain(crd) {
		for _, pvc := range diffed.Deletes() {
			reporter.Info("Deleting pvc", "name", pvc.Name)
			if err := control.client.Delete(ctx, pvc, client.PropagationPolicy(metav1.DeletePropagationForeground)); client.IgnoreNotFound(err) != nil {
				return true, kube.TransientError(fmt.Errorf("delete pvc %q: %w", pvc.Name, err))
			}
			pvcStatusChanges.Deleted = append(pvcStatusChanges.Deleted, pvc.Name)
		}
		deletes = len(diffed.Deletes())
	}

	if deletes+len(diffed.Creates()) > 0 {
		// Scaling happens first; then updates. So requeue to handle updates after scaling finished.
		return true, nil
	}

	if len(diffed.Updates()) == 0 {
		return false, nil
	}

	if _, unbound := lo.Find(currentPVCs, func(pvc *corev1.PersistentVolumeClaim) bool {
		return pvc.Status.Phase != corev1.ClaimBound
	}); unbound {
		return true, nil
	}

	// PVCs have many immutable fields, so only update the storage size.
	for _, pvc := range diffed.Updates() {
		size := pvc.Spec.Resources.Requests[corev1.ResourceStorage]
		reporter.Info(
			"Patching pvc",
			"name", pvc.Name,
			"size", size.String(),
		)
		patch := corev1.PersistentVolumeClaim{
			ObjectMeta: pvc.ObjectMeta,
			TypeMeta:   pvc.TypeMeta,
			Spec: corev1.PersistentVolumeClaimSpec{
				Resources: pvc.Spec.Resources,
			},
		}
		if err := control.client.Patch(ctx, &patch, client.Merge); err != nil {
			reporter.Error(err, "PVC patch failed", "name", pvc.Name)
			reporter.RecordError("PVCPatchFailed", err)
			continue
		}
	}

	return false, nil
}

func (control PVCControl) shouldRetain(crd *tempov1alpha1.TempoFullNode) bool {
	if policy := crd.Spec.RetentionPolicy; policy != nil {
		return *policy == tempov1alpha1.RetentionPolicyRetain
	}
	return false
}
