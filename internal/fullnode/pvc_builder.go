package fullnode

import (
	"fmt"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/aaronforce1/cosmos-operator/internal/diff"
	"github.com/aaronforce1/cosmos-operator/internal/kube"
	"gopkg.in/inf.v0"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	autoScaleGrowthFactor = 102
)

// Consensus PVCs are always ReadWriteOnce: the single-attach semantics double as a fence against
// two pods holding the same DKG signing share (double-sign guard).
var consensusAccessModes = []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce}

// BuildPVCs outputs desired consensus-datadir PVCs given the crd.
// There is deliberately no dataSource/autoDataSource seeding: cloning consensus state risks
// equivocation, and the DKG share is network-recoverable anyway.
func BuildPVCs(
	crd *tempov1alpha1.TempoFullNode,
	currentPVCs []*corev1.PersistentVolumeClaim,
) []diff.Resource[*corev1.PersistentVolumeClaim] {
	base := corev1.PersistentVolumeClaim{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "v1",
			Kind:       "PersistentVolumeClaim",
		},
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   crd.Namespace,
			Labels:      defaultLabels(crd),
			Annotations: make(map[string]string),
		},
	}

	var pvcs []diff.Resource[*corev1.PersistentVolumeClaim]
	replicas := crd.Spec.Replicas
	if crd.IsValidator() && replicas > 1 {
		replicas = 1
	}
	for i := crd.Spec.Ordinals.Start; i < crd.Spec.Ordinals.Start+replicas; i++ {
		if pvcDisabled(crd, i) {
			continue
		}

		pvc := base.DeepCopy()
		name := pvcName(crd, i)
		pvc.Name = name
		podName := instanceName(crd, i)
		pvc.Labels[kube.InstanceLabel] = podName

		var existingSize resource.Quantity
		for _, existing := range currentPVCs {
			if existing.Name == name {
				if existing.DeletionTimestamp == nil && existing.Status.Phase == corev1.ClaimBound {
					existingSize = existing.Status.Capacity[corev1.ResourceStorage]
				}
				break
			}
		}

		tpl := crd.Spec.ConsensusVolume
		if override, ok := crd.Spec.InstanceOverrides[podName]; ok {
			if overrideTpl := override.ConsensusVolume; overrideTpl != nil {
				tpl = *overrideTpl
			}
		}

		pvc.Spec = corev1.PersistentVolumeClaimSpec{
			AccessModes:      consensusAccessModes,
			Resources:        pvcResources(crd, name, existingSize, tpl.Resources),
			StorageClassName: ptr(tpl.StorageClassName),
			VolumeMode:       valOrDefault(tpl.VolumeMode, ptr(corev1.PersistentVolumeFilesystem)),
		}

		preserveMergeInto(pvc.Labels, tpl.Metadata.Labels)
		preserveMergeInto(pvc.Annotations, tpl.Metadata.Annotations)
		kube.NormalizeMetadata(&pvc.ObjectMeta)

		pvcs = append(pvcs, diff.Adapt(pvc, i))
	}
	return pvcs
}

// pvcResources sizes the PVC grow-only: the largest of the template request, a pending
// self-healing auto-scale request (with padding), and the currently bound capacity.
func pvcResources(
	crd *tempov1alpha1.TempoFullNode,
	name string,
	existingSize resource.Quantity,
	tplResources corev1.ResourceRequirements,
) corev1.ResourceRequirements {
	reqs := tplResources.DeepCopy()

	if autoScale := crd.Status.SelfHealing.PVCAutoScale; autoScale != nil {
		if status, ok := autoScale[name]; ok {
			requestedSize := status.RequestedSize.DeepCopy()
			newSize := requestedSize.AsDec()
			sizeWithPadding := resource.NewDecimalQuantity(*newSize.Mul(newSize, inf.NewDec(autoScaleGrowthFactor, 2)), resource.DecimalSI)
			if sizeWithPadding.Cmp(reqs.Requests[corev1.ResourceStorage]) > 0 {
				reqs.Requests[corev1.ResourceStorage] = *sizeWithPadding
			}
		}
	}

	if existingSize.Cmp(reqs.Requests[corev1.ResourceStorage]) > 0 {
		reqs.Requests[corev1.ResourceStorage] = existingSize
	}

	return *reqs
}

func pvcDisabled(crd *tempov1alpha1.TempoFullNode, ordinal int32) bool {
	name := instanceName(crd, ordinal)
	disable := crd.Spec.InstanceOverrides[name].DisableStrategy
	return disable != nil && *disable == tempov1alpha1.DisableAll
}

func pvcName(crd *tempov1alpha1.TempoFullNode, ordinal int32) string {
	name := fmt.Sprintf("pvc-%s-%d", appName(crd), ordinal)
	return kube.ToName(name)
}
