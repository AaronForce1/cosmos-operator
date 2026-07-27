package fullnode

import (
	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// defaultCRD returns a happy-path TempoFullNode for builder tests.
func defaultCRD() tempov1alpha1.TempoFullNode {
	return tempov1alpha1.TempoFullNode{
		ObjectMeta: metav1.ObjectMeta{
			Name:            "osmosis",
			Namespace:       "test",
			ResourceVersion: "_resource_version_",
		},
		Spec: tempov1alpha1.TempoFullNodeSpec{
			Role:     tempov1alpha1.NodeRoleRPC,
			Replicas: 3,
			ChainSpec: tempov1alpha1.TempoChainSpec{
				Chain: "mainnet",
			},
			PodTemplate: tempov1alpha1.PodSpec{
				Image: "ghcr.io/tempoxyz/tempo:v1.2.3",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
				},
			},
			ConsensusVolume: tempov1alpha1.ConsensusVolumeSpec{
				StorageClassName: "standard-rwo",
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
				},
			},
		},
	}
}

// defaultValidatorCRD returns a happy-path validator TempoFullNode.
func defaultValidatorCRD() tempov1alpha1.TempoFullNode {
	crd := defaultCRD()
	crd.Spec.Role = tempov1alpha1.NodeRoleValidator
	crd.Spec.Replicas = 1
	crd.Spec.SigningKey = &tempov1alpha1.SigningKeySpec{
		SigningKeySecret: corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "validator-keys"},
			Key:                  "signing-key",
		},
		EncryptionSecret: corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "validator-keys"},
			Key:                  "consensus-secret",
		},
	}
	return crd
}
