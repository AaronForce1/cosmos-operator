/*
Copyright 2022 Strangelove Ventures LLC.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"fmt"
	"time"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/aaronforce1/cosmos-operator/internal/fullnode"
	"github.com/aaronforce1/cosmos-operator/internal/kube"
	"github.com/aaronforce1/cosmos-operator/internal/tempo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

const controllerOwnerField = ".metadata.controller"

// TempoFullNodeReconciler reconciles a TempoFullNode object
type TempoFullNodeReconciler struct {
	client.Client

	cacheController *tempo.CacheController
	podControl      fullnode.PodControl
	pvcControl      fullnode.PVCControl
	recorder        record.EventRecorder
	serviceControl  fullnode.ServiceControl
	statusClient    *fullnode.StatusClient
}

// NewFullNode returns a valid TempoFullNode controller.
func NewFullNode(
	client client.Client,
	recorder record.EventRecorder,
	statusClient *fullnode.StatusClient,
	cacheController *tempo.CacheController,
) *TempoFullNodeReconciler {
	return &TempoFullNodeReconciler{
		Client: client,

		cacheController: cacheController,
		podControl:      fullnode.NewPodControl(client, cacheController),
		pvcControl:      fullnode.NewPVCControl(client),
		recorder:        recorder,
		serviceControl:  fullnode.NewServiceControl(client),
		statusClient:    statusClient,
	}
}

var (
	stopResult    ctrl.Result
	requeueResult = ctrl.Result{RequeueAfter: 3 * time.Second}
)

//+kubebuilder:rbac:groups=tempo.aaronforce.io,resources=tempofullnodes,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=tempo.aaronforce.io,resources=tempofullnodes/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=tempo.aaronforce.io,resources=tempofullnodes/finalizers,verbs=update
// Generate RBAC roles to watch and update resources. IMPORTANT!!!! All resource names must be lowercase or cluster role will not work.
//+kubebuilder:rbac:groups="",resources=pods;persistentvolumeclaims;services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=events,verbs=create;update;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *TempoFullNodeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.V(1).Info("Entering reconcile loop", "request", req.NamespacedName)

	// Get the CRD
	crd := new(tempov1alpha1.TempoFullNode)
	if err := r.Get(ctx, req.NamespacedName, crd); err != nil {
		// Ignore not found errors because can't be fixed by an immediate requeue. We'll have to wait for next notification.
		// Also, will get "not found" error if crd is deleted.
		// No need to explicitly delete resources. Kube GC does so automatically because we set the controller reference
		// for each resource.
		return stopResult, client.IgnoreNotFound(err)
	}

	reporter := kube.NewEventReporter(logger, r.recorder, crd)

	fullnode.ResetStatus(crd)

	syncInfo := fullnode.SyncInfoStatus(ctx, crd, r.cacheController)

	pvcStatusChanges := fullnode.PVCStatusChanges{}

	defer r.updateStatus(ctx, crd, syncInfo, &pvcStatusChanges)

	errs := &kube.ReconcileErrors{}

	// Order of operations is important. E.g. PVCs won't delete unless pods are deleted first.
	// K8S can create pods first even if the PVC isn't ready. Pods won't be in a ready state until PVC is bound.

	// Create or update Services.
	err := r.serviceControl.Reconcile(ctx, reporter, crd)
	if err != nil {
		errs.Append(err)
	}

	// Reconcile pods.
	podResult, err := r.podControl.Reconcile(ctx, reporter, crd, syncInfo)
	if err != nil {
		errs.Append(err)
	}

	// Reconcile pvcs.
	pvcRequeue, err := r.pvcControl.Reconcile(ctx, reporter, crd, &pvcStatusChanges)
	if err != nil {
		errs.Append(err)
	}

	if errs.Any() {
		return r.resultWithErr(crd, errs)
	}

	if podResult.WaitingForFence {
		// A validator pod is stuck terminating; the replacement is deliberately gated
		// (double-sign guard). Poll while waiting for the node to be fenced or repaired.
		crd.Status.Phase = tempov1alpha1.TempoFullNodePhaseWaitingForFence
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	if podResult.Requeue || pvcRequeue {
		return requeueResult, nil
	}

	crd.Status.Phase = tempov1alpha1.TempoFullNodePhaseComplete
	// Requeue to constantly poll consensus state and to catch scheduled-upgrade roll windows.
	return ctrl.Result{RequeueAfter: 60 * time.Second}, nil
}

func (r *TempoFullNodeReconciler) resultWithErr(crd *tempov1alpha1.TempoFullNode, err kube.ReconcileError) (ctrl.Result, kube.ReconcileError) {
	if err.IsTransient() {
		r.recorder.Event(crd, kube.EventWarning, "ErrorTransient", fmt.Sprintf("%v; retrying.", err))
		crd.Status.StatusMessage = ptr(fmt.Sprintf("Transient error: system is retrying: %v", err))
		crd.Status.Phase = tempov1alpha1.TempoFullNodePhaseTransientError
		return requeueResult, err
	}

	crd.Status.Phase = tempov1alpha1.TempoFullNodePhaseError
	crd.Status.StatusMessage = ptr(fmt.Sprintf("Unrecoverable error: human intervention required: %v", err))
	r.recorder.Event(crd, kube.EventWarning, "Error", err.Error())
	return stopResult, err
}

func (r *TempoFullNodeReconciler) updateStatus(
	ctx context.Context,
	crd *tempov1alpha1.TempoFullNode,
	syncInfo map[string]*tempov1alpha1.SyncInfoPodStatus,
	pvcStatusChanges *fullnode.PVCStatusChanges,
) {
	upgradeStatus := fullnode.UpgradeStatus(crd, time.Now())
	if err := r.statusClient.SyncUpdate(ctx, client.ObjectKeyFromObject(crd), func(status *tempov1alpha1.TempoFullNodeStatus) {
		status.ObservedGeneration = crd.Status.ObservedGeneration
		status.Phase = crd.Status.Phase
		status.StatusMessage = crd.Status.StatusMessage
		status.SyncInfo = syncInfo
		status.Upgrade = upgradeStatus
		if status.SelfHealing.PVCAutoScale != nil {
			for _, k := range pvcStatusChanges.Deleted {
				delete(status.SelfHealing.PVCAutoScale, k)
			}
		}
	}); err != nil {
		log.FromContext(ctx).Error(err, "Failed to patch status")
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *TempoFullNodeReconciler) SetupWithManager(ctx context.Context, mgr ctrl.Manager) error {
	// Index pods.
	err := mgr.GetFieldIndexer().IndexField(
		ctx,
		&corev1.Pod{},
		controllerOwnerField,
		kube.IndexOwner[*corev1.Pod](tempov1alpha1.TempoFullNodeController),
	)
	if err != nil {
		return fmt.Errorf("pod index field %s: %w", controllerOwnerField, err)
	}

	// Index PVCs.
	err = mgr.GetFieldIndexer().IndexField(
		ctx,
		&corev1.PersistentVolumeClaim{},
		controllerOwnerField,
		kube.IndexOwner[*corev1.PersistentVolumeClaim](tempov1alpha1.TempoFullNodeController),
	)
	if err != nil {
		return fmt.Errorf("pvc index field %s: %w", controllerOwnerField, err)
	}

	// Index Services.
	err = mgr.GetFieldIndexer().IndexField(
		ctx,
		&corev1.Service{},
		controllerOwnerField,
		kube.IndexOwner[*corev1.Service](tempov1alpha1.TempoFullNodeController),
	)
	if err != nil {
		return fmt.Errorf("service index field %s: %w", controllerOwnerField, err)
	}

	cbuilder := ctrl.NewControllerManagedBy(mgr).For(&tempov1alpha1.TempoFullNode{})

	// Watch for delete events for certain resources.
	for _, kind := range []*source.Kind{
		{Type: &corev1.Pod{}},
		{Type: &corev1.PersistentVolumeClaim{}},
		{Type: &corev1.Service{}},
	} {
		cbuilder.Watches(
			kind,
			&handler.EnqueueRequestForOwner{OwnerType: &tempov1alpha1.TempoFullNode{}, IsController: true},
			builder.WithPredicates(&predicate.Funcs{
				DeleteFunc: func(_ event.DeleteEvent) bool { return true },
			}),
		)
	}

	return cbuilder.Complete(r)
}
