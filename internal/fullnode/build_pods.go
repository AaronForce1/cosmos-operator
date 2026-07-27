package fullnode

import (
	"time"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/aaronforce1/cosmos-operator/internal/diff"
	corev1 "k8s.io/api/core/v1"
)

// BuildPods creates the final state of pods given the crd as of time "now".
//
// Double-sign guard: a validator only ever gets a single pod (the first ordinal), regardless of
// replicas. The CRD's CEL validation already rejects replicas > 1 for validators; this clamp is
// defense in depth.
func BuildPods(crd *tempov1alpha1.TempoFullNode, now time.Time) ([]diff.Resource[*corev1.Pod], error) {
	var (
		builder = NewPodBuilder(crd, now)
		pods    []diff.Resource[*corev1.Pod]
	)
	replicas := crd.Spec.Replicas
	if crd.IsValidator() && replicas > 1 {
		replicas = 1
	}
	for i := crd.Spec.Ordinals.Start; i < crd.Spec.Ordinals.Start+replicas; i++ {
		pod, err := builder.WithOrdinal(i).Build()
		if err != nil {
			return nil, err
		}

		if pod == nil {
			continue
		}

		pods = append(pods, diff.Adapt(pod, i))
	}
	return pods, nil
}
