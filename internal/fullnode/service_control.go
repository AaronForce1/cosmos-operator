package fullnode

import (
	"context"
	"fmt"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/aaronforce1/cosmos-operator/internal/diff"
	"github.com/aaronforce1/cosmos-operator/internal/kube"
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ServiceControl creates or updates Services.
type ServiceControl struct {
	client Client
}

func NewServiceControl(client Client) ServiceControl {
	return ServiceControl{
		client: client,
	}
}

// Reconcile creates or updates services.
// Some services, like P2P, reserve public addresses of which should not change.
// Therefore, services are never deleted unless the CRD itself is deleted.
func (sc ServiceControl) Reconcile(ctx context.Context, log kube.Logger, crd *tempov1alpha1.TempoFullNode) kube.ReconcileError {
	var svcs corev1.ServiceList
	if err := sc.client.List(ctx, &svcs,
		client.InNamespace(crd.Namespace),
		client.MatchingFields{kube.ControllerOwnerField: crd.Name},
	); err != nil {
		return kube.TransientError(fmt.Errorf("list existing services: %w", err))
	}

	current := ptrSlice(svcs.Items)
	want := BuildServices(crd)
	diffed := diff.New(current, want)

	for _, svc := range diffed.Creates() {
		log.Info("Creating service", "svcName", svc.Name)
		if err := ctrl.SetControllerReference(crd, svc, sc.client.Scheme()); err != nil {
			return kube.TransientError(fmt.Errorf("set controller reference on service %q: %w", svc.Name, err))
		}
		// CreateOrUpdate (vs. only create) fixes a bug with current deployments where updating would remove the owner reference.
		// This ensures we update the service with the owner reference.
		if err := kube.CreateOrUpdate(ctx, sc.client, svc); err != nil {
			return kube.TransientError(fmt.Errorf("create service %q: %w", svc.Name, err))
		}
	}

	for _, svc := range diffed.Updates() {
		log.Info("Updating service", "svcName", svc.Name)
		if err := sc.client.Update(ctx, svc); err != nil {
			return kube.TransientError(fmt.Errorf("update service %q: %w", svc.Name, err))
		}
	}

	return nil
}
