package fullnode

import (
	"fmt"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/aaronforce1/cosmos-operator/internal/diff"
	"github.com/aaronforce1/cosmos-operator/internal/kube"
	"github.com/samber/lo"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const maxP2PServiceDefault = int32(1)

// BuildServices returns a list of services given the crd.
//
// Creates a p2p service per pod, and a single RPC service when the JSON-RPC server is exposed
// (spec.rpc.enabled, defaulting by role).
//
// P2P services diverge from traditional web and kubernetes architecture which typically uses a single
// service backed by multiple pods. This is necessary because:
//  1. Pods may be in various states even with proper readiness probes
//  2. An outside peer connecting to a single address must always reach the same node identity;
//     a shared p2p service would route the same address to different nodes across connections.
//
// Services are created with either LoadBalancer (for external access) or ClusterIP type, controlled
// by MaxP2PExternalAddresses setting.
func BuildServices(crd *tempov1alpha1.TempoFullNode) []diff.Resource[*corev1.Service] {
	max := maxP2PServiceDefault
	if v := crd.Spec.Service.MaxP2PExternalAddresses; v != nil {
		max = *v
	}
	maxExternal := lo.Clamp(max, 0, crd.Spec.Replicas)

	svcs := make([]diff.Resource[*corev1.Service], 0, crd.Spec.Replicas+1)

	startOrdinal := crd.Spec.Ordinals.Start
	for i := int32(0); i < crd.Spec.Replicas; i++ {
		ordinal := startOrdinal + i
		var svc corev1.Service
		svc.Name = p2pServiceName(crd, ordinal)
		svc.Namespace = crd.Namespace
		svc.Kind = "Service"
		svc.APIVersion = "v1"
		svc.Labels = defaultLabels(crd,
			kube.InstanceLabel, instanceName(crd, ordinal),
			kube.ComponentLabel, "p2p",
		)
		svc.Annotations = map[string]string{}
		svc.Spec.Ports = []corev1.ServicePort{
			{
				Name:       "p2p",
				Protocol:   corev1.ProtocolTCP,
				Port:       crd.P2PPort(),
				TargetPort: intstr.FromString("p2p"),
			},
			{
				Name:       "p2p-udp",
				Protocol:   corev1.ProtocolUDP,
				Port:       crd.P2PPort(),
				TargetPort: intstr.FromString("p2p-udp"),
			},
		}
		svc.Spec.Selector = map[string]string{kube.InstanceLabel: instanceName(crd, ordinal)}
		kube.NormalizeMetadata(&svc.ObjectMeta)
		if i < maxExternal {
			preserveMergeInto(svc.Labels, crd.Spec.Service.P2PTemplate.Metadata.Labels)
			preserveMergeInto(svc.Annotations, crd.Spec.Service.P2PTemplate.Metadata.Annotations)
			svc.Spec.Type = *valOrDefault(crd.Spec.Service.P2PTemplate.Type, ptr(corev1.ServiceTypeLoadBalancer))
			svc.Spec.ExternalTrafficPolicy = *valOrDefault(crd.Spec.Service.P2PTemplate.ExternalTrafficPolicy, ptr(corev1.ServiceExternalTrafficPolicyTypeLocal))
		} else {
			svc.Spec.Type = corev1.ServiceTypeClusterIP
			svc.Spec.ClusterIP = *valOrDefault(crd.Spec.Service.P2PTemplate.ClusterIP, ptr(""))
		}
		svcs = append(svcs, diff.Adapt(&svc, len(svcs)))
	}

	// Add RPC service when the JSON-RPC server is exposed outside the pod.
	if crd.RPCExposed() {
		svcs = append(svcs, diff.Adapt(rpcService(crd), len(svcs)))
	}

	return svcs
}

func rpcService(crd *tempov1alpha1.TempoFullNode) *corev1.Service {
	var svc corev1.Service
	svc.Name = rpcServiceName(crd)
	svc.Namespace = crd.Namespace
	svc.Kind = "Service"
	svc.APIVersion = "v1"
	svc.Labels = defaultLabels(crd,
		kube.ComponentLabel, "rpc",
	)
	svc.Annotations = map[string]string{}
	svc.Spec.Ports = []corev1.ServicePort{
		{
			Name:       "http-rpc",
			Protocol:   corev1.ProtocolTCP,
			Port:       crd.RPCPort(),
			TargetPort: intstr.FromString("http-rpc"),
		},
	}
	if crd.WSEnabled() {
		svc.Spec.Ports = append(svc.Spec.Ports, corev1.ServicePort{
			Name:       "ws",
			Protocol:   corev1.ProtocolTCP,
			Port:       crd.WSPort(),
			TargetPort: intstr.FromString("ws"),
		})
	}
	svc.Spec.Selector = map[string]string{kube.NameLabel: appName(crd)}
	svc.Spec.Type = corev1.ServiceTypeClusterIP
	rpcSpec := crd.Spec.Service.RPCTemplate
	preserveMergeInto(svc.Labels, rpcSpec.Metadata.Labels)
	preserveMergeInto(svc.Annotations, rpcSpec.Metadata.Annotations)
	svc.Spec.Ports = append(svc.Spec.Ports, rpcSpec.Ports...)
	kube.NormalizeMetadata(&svc.ObjectMeta)
	if v := rpcSpec.ExternalTrafficPolicy; v != nil {
		svc.Spec.ExternalTrafficPolicy = *v
	}
	if v := rpcSpec.Type; v != nil {
		svc.Spec.Type = *v
	}
	if v := rpcSpec.ClusterIP; v != nil {
		svc.Spec.ClusterIP = *v
	}
	return &svc
}

func p2pServiceName(crd *tempov1alpha1.TempoFullNode, ordinal int32) string {
	return fmt.Sprintf("%s-p2p-%d", appName(crd), ordinal)
}

func rpcServiceName(crd *tempov1alpha1.TempoFullNode) string {
	return fmt.Sprintf("%s-rpc", appName(crd))
}
