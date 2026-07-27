package fullnode

import (
	"fmt"
	"strings"
	"testing"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/aaronforce1/cosmos-operator/internal/test"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func TestBuildServices(t *testing.T) {
	t.Parallel()

	t.Run("p2p services", func(t *testing.T) {
		crd := defaultCRD()
		crd.Name = "terra"
		crd.Namespace = "test"
		crd.Spec.Replicas = 3

		svcs := BuildServices(&crd)

		// 3 p2p services + 1 rpc service (rpc role exposes JSON-RPC).
		require.Equal(t, 4, len(svcs))

		for i := 0; i < 3; i++ {
			p2p := svcs[i].Object()
			require.Equal(t, fmt.Sprintf("terra-p2p-%d", i), p2p.Name)
			require.Equal(t, "test", p2p.Namespace)

			wantLabels := map[string]string{
				"app.kubernetes.io/created-by": "tempo-operator",
				"app.kubernetes.io/component":  "p2p",
				"app.kubernetes.io/name":       "terra",
				"app.kubernetes.io/instance":   fmt.Sprintf("terra-%d", i),
				"app.kubernetes.io/version":    "v1.2.3",
				"tempo.aaronforce.io/chain":    "mainnet",
				"tempo.aaronforce.io/role":     "rpc",
			}
			require.Equal(t, wantLabels, p2p.Labels)

			require.Equal(t, map[string]string{"app.kubernetes.io/instance": fmt.Sprintf("terra-%d", i)}, p2p.Spec.Selector)

			require.Len(t, p2p.Spec.Ports, 2)
			require.Equal(t, corev1.ServicePort{
				Name:       "p2p",
				Protocol:   corev1.ProtocolTCP,
				Port:       30303,
				TargetPort: intstr.FromString("p2p"),
			}, p2p.Spec.Ports[0])
			require.Equal(t, corev1.ServicePort{
				Name:       "p2p-udp",
				Protocol:   corev1.ProtocolUDP,
				Port:       30303,
				TargetPort: intstr.FromString("p2p-udp"),
			}, p2p.Spec.Ports[1])
		}

		// By default, only the first p2p service is external.
		require.Equal(t, corev1.ServiceTypeLoadBalancer, svcs[0].Object().Spec.Type)
		require.Equal(t, corev1.ServiceExternalTrafficPolicyTypeLocal, svcs[0].Object().Spec.ExternalTrafficPolicy)
		require.Equal(t, corev1.ServiceTypeClusterIP, svcs[1].Object().Spec.Type)
		require.Equal(t, corev1.ServiceTypeClusterIP, svcs[2].Object().Spec.Type)
	})

	t.Run("max external p2p services", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.Replicas = 3
		crd.Spec.Service.MaxP2PExternalAddresses = ptr(int32(2))

		svcs := BuildServices(&crd)

		require.Equal(t, corev1.ServiceTypeLoadBalancer, svcs[0].Object().Spec.Type)
		require.Equal(t, corev1.ServiceTypeLoadBalancer, svcs[1].Object().Spec.Type)
		require.Equal(t, corev1.ServiceTypeClusterIP, svcs[2].Object().Spec.Type)
	})

	t.Run("p2p services with non 0 starting ordinal", func(t *testing.T) {
		crd := defaultCRD()
		crd.Name = "terra"
		crd.Spec.Replicas = 2
		crd.Spec.Ordinals.Start = 2

		svcs := BuildServices(&crd)
		require.Equal(t, "terra-p2p-2", svcs[0].Object().Name)
		require.Equal(t, "terra-p2p-3", svcs[1].Object().Name)
	})

	t.Run("rpc service", func(t *testing.T) {
		crd := defaultCRD()
		crd.Name = "terra"
		crd.Namespace = "test"
		crd.Spec.Replicas = 2

		svcs := BuildServices(&crd)
		rpc := svcs[len(svcs)-1].Object()
		require.Equal(t, "terra-rpc", rpc.Name)
		require.Equal(t, corev1.ServiceTypeClusterIP, rpc.Spec.Type)
		require.Equal(t, map[string]string{"app.kubernetes.io/name": "terra"}, rpc.Spec.Selector)

		require.Len(t, rpc.Spec.Ports, 1)
		require.Equal(t, corev1.ServicePort{
			Name:       "http-rpc",
			Protocol:   corev1.ProtocolTCP,
			Port:       8545,
			TargetPort: intstr.FromString("http-rpc"),
		}, rpc.Spec.Ports[0])
	})

	t.Run("rpc service with websocket", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.RPC.WS = &tempov1alpha1.WSSpec{Enabled: true}

		svcs := BuildServices(&crd)
		rpc := svcs[len(svcs)-1].Object()
		require.Len(t, rpc.Spec.Ports, 2)
		require.Equal(t, "ws", rpc.Spec.Ports[1].Name)
		require.EqualValues(t, 8546, rpc.Spec.Ports[1].Port)
	})

	t.Run("validator has no rpc service", func(t *testing.T) {
		crd := defaultValidatorCRD()
		crd.Name = "val"
		crd.Spec.Replicas = 1

		svcs := BuildServices(&crd)
		require.Len(t, svcs, 1)
		require.Equal(t, "val-p2p-0", svcs[0].Object().Name)
	})

	t.Run("rpc service overrides", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.Service.RPCTemplate = tempov1alpha1.ServiceOverridesSpec{
			Metadata: tempov1alpha1.Metadata{
				Labels:      map[string]string{"custom": "label"},
				Annotations: map[string]string{"custom": "annotation"},
			},
			Type:      ptr(corev1.ServiceTypeNodePort),
			ClusterIP: ptr("None"),
			Ports: []corev1.ServicePort{
				{Name: "extra", Port: 1234, Protocol: corev1.ProtocolTCP},
			},
			ExternalTrafficPolicy: ptr(corev1.ServiceExternalTrafficPolicyTypeLocal),
		}

		svcs := BuildServices(&crd)
		rpc := svcs[len(svcs)-1].Object()

		require.Equal(t, "label", rpc.Labels["custom"])
		require.Equal(t, "annotation", rpc.Annotations["custom"])
		require.Equal(t, corev1.ServiceTypeNodePort, rpc.Spec.Type)
		require.Equal(t, "None", rpc.Spec.ClusterIP)
		require.Equal(t, corev1.ServiceExternalTrafficPolicyTypeLocal, rpc.Spec.ExternalTrafficPolicy)
		require.Len(t, rpc.Spec.Ports, 2)
		require.Equal(t, "extra", rpc.Spec.Ports[1].Name)
	})

	t.Run("p2p service overrides", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.Replicas = 1
		crd.Spec.Service.P2PTemplate = tempov1alpha1.ServiceOverridesSpec{
			Metadata: tempov1alpha1.Metadata{
				Labels: map[string]string{"custom": "label"},
			},
			Type:                  ptr(corev1.ServiceTypeNodePort),
			ExternalTrafficPolicy: ptr(corev1.ServiceExternalTrafficPolicyTypeCluster),
		}

		svcs := BuildServices(&crd)
		p2p := svcs[0].Object()
		require.Equal(t, "label", p2p.Labels["custom"])
		require.Equal(t, corev1.ServiceTypeNodePort, p2p.Spec.Type)
		require.Equal(t, corev1.ServiceExternalTrafficPolicyTypeCluster, p2p.Spec.ExternalTrafficPolicy)
	})

	t.Run("custom p2p port", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.Replicas = 1
		crd.Spec.P2P.Port = ptr(int32(40404))

		svcs := BuildServices(&crd)
		require.EqualValues(t, 40404, svcs[0].Object().Spec.Ports[0].Port)
	})

	t.Run("long names", func(t *testing.T) {
		crd := defaultCRD()
		crd.Spec.Replicas = 2
		crd.Name = strings.Repeat("Y", 300)

		for _, svc := range BuildServices(&crd) {
			test.RequireValidMetadata(t, svc.Object())
		}
	})

	test.HasRoleLabel(t, func(crd tempov1alpha1.TempoFullNode) []map[string]string {
		svcs := BuildServices(&crd)
		labels := make([]map[string]string, 0)
		for _, svc := range svcs {
			labels = append(labels, svc.Object().Labels)
		}
		return labels
	})
}
