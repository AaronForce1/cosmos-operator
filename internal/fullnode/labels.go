package fullnode

import (
	"errors"
	"fmt"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/aaronforce1/cosmos-operator/internal/kube"
)

const (
	chainLabel = "tempo.aaronforce.io/chain"
	roleLabel  = "tempo.aaronforce.io/role"
)

// kv is a list of extra kv pairs to add to the labels. Must be even.
func defaultLabels(crd *tempov1alpha1.TempoFullNode, kvPairs ...string) map[string]string {
	if len(kvPairs)%2 != 0 {
		panic(errors.New("key/value pairs must be even"))
	}
	labels := map[string]string{
		kube.ControllerLabel: "tempo-operator",
		kube.ComponentLabel:  tempov1alpha1.TempoFullNodeController,
		kube.NameLabel:       appName(crd),
		kube.VersionLabel:    kube.ParseImageVersion(crd.Spec.PodTemplate.Image),
		chainLabel:           crd.Spec.ChainSpec.Chain,
		roleLabel:            string(crd.Spec.Role),
	}
	for k, v := range crd.Labels {
		labels[k] = v
	}
	for i := 0; i < len(kvPairs); i += 2 {
		labels[kvPairs[i]] = kvPairs[i+1]
	}
	return labels
}

func appName(crd *tempov1alpha1.TempoFullNode) string {
	return kube.ToName(crd.Name)
}

func instanceName(crd *tempov1alpha1.TempoFullNode, ordinal int32) string {
	return kube.ToName(fmt.Sprintf("%s-%d", appName(crd), ordinal))
}

// Conditionally add custom labels or annotations, preserving key/values already set on 'into'.
// 'into' must not be nil.
func preserveMergeInto(into map[string]string, other map[string]string) {
	for k, v := range other {
		_, ok := into[k]
		if !ok {
			into[k] = v
		}
	}
}
