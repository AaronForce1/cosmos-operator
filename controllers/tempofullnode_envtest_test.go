package controllers

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"github.com/aaronforce1/cosmos-operator/internal/fullnode"
	"github.com/aaronforce1/cosmos-operator/internal/tempo"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

// TestTempoFullNodeEnvtest runs the TempoFullNode controller against a real kube-apiserver via
// envtest, covering create, scale, update (recreate-on-delete), CRD validation (CEL double-sign
// guards), and delete.
//
// Requires envtest binaries: run via `make test-envtest`, or set KUBEBUILDER_ASSETS.
// Skipped under -short (the default `make test`).
func TestTempoFullNodeEnvtest(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping envtest suite in -short mode; run make test-envtest")
	}
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		// Fall back to the repo-local install performed by `make test-envtest`.
		matches, _ := filepath.Glob("../bin/envtest/k8s/*")
		if len(matches) == 0 {
			t.Skip("KUBEBUILDER_ASSETS not set and no repo-local envtest binaries; run make test-envtest")
		}
		os.Setenv("KUBEBUILDER_ASSETS", matches[0])
	}

	ctrl.SetLogger(zap.New(zap.UseDevMode(false)))

	testEnv := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := testEnv.Start()
	require.NoError(t, err)
	defer func() { require.NoError(t, testEnv.Stop()) }()

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(tempov1alpha1.AddToScheme(scheme))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     "0",
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)

	httpClient := &http.Client{Timeout: 5 * time.Second}
	statusClient := fullnode.NewStatusClient(mgr.GetClient())
	cacheController := tempo.NewCacheController(
		tempo.NewStatusCollector(tempo.NewClient(httpClient), time.Second),
		mgr.GetClient(),
		mgr.GetEventRecorderFor(tempo.CacheControllerName),
	)
	defer func() { _ = cacheController.Close() }()
	require.NoError(t, cacheController.SetupWithManager(ctx, mgr))

	require.NoError(t, NewFullNode(
		mgr.GetClient(),
		mgr.GetEventRecorderFor(tempov1alpha1.TempoFullNodeController),
		statusClient,
		cacheController,
	).SetupWithManager(ctx, mgr))

	go func() {
		if err := mgr.Start(ctx); err != nil {
			panic(err)
		}
	}()

	k8sClient := mgr.GetClient()
	const namespace = "default"

	newCRD := func(name string, role tempov1alpha1.NodeRole, replicas int32) *tempov1alpha1.TempoFullNode {
		crd := &tempov1alpha1.TempoFullNode{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: tempov1alpha1.TempoFullNodeSpec{
				Role:     role,
				Replicas: replicas,
				ChainSpec: tempov1alpha1.TempoChainSpec{
					Chain: "moderato",
				},
				PodTemplate: tempov1alpha1.PodSpec{
					Image: "ghcr.io/tempoxyz/tempo:v1.11.0",
				},
				ConsensusVolume: tempov1alpha1.ConsensusVolumeSpec{
					StorageClassName: "standard",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("1Gi")},
					},
				},
			},
		}
		return crd
	}

	listOwned := func(t *testing.T, list client.ObjectList, name string) {
		t.Helper()
		require.NoError(t, k8sClient.List(ctx, list, client.InNamespace(namespace)))
	}

	// countPods counts live (non-terminating) pods owned by the named CRD. Terminating pods are
	// excluded because envtest runs no garbage collector: the foregroundDeletion finalizer set by
	// the controller's delete propagation policy never clears, so deleted pods linger forever.
	countPods := func(name string) int {
		var pods corev1.PodList
		if err := k8sClient.List(ctx, &pods, client.InNamespace(namespace)); err != nil {
			return -1
		}
		var n int
		for _, p := range pods.Items {
			if p.DeletionTimestamp != nil {
				continue
			}
			for _, ref := range p.OwnerReferences {
				if ref.Kind == "TempoFullNode" && ref.Name == name {
					n++
				}
			}
		}
		return n
	}

	const waitFor = 30 * time.Second
	const tick = 200 * time.Millisecond

	t.Run("create", func(t *testing.T) {
		crd := newCRD("rpc-node", tempov1alpha1.NodeRoleRPC, 2)
		require.NoError(t, k8sClient.Create(ctx, crd))

		require.Eventually(t, func() bool { return countPods("rpc-node") == 2 }, waitFor, tick)

		var pods corev1.PodList
		listOwned(t, &pods, "rpc-node")
		for _, pod := range pods.Items {
			if pod.OwnerReferences[0].Name != "rpc-node" {
				continue
			}
			require.Equal(t, "node", pod.Spec.Containers[0].Name)
			require.Contains(t, pod.Spec.Containers[0].Args, "--chain")
			require.Contains(t, pod.Spec.Containers[0].Args, "moderato")
			require.Contains(t, pod.Spec.Containers[0].Args, "--follow")
		}

		// Consensus PVCs, one per ordinal.
		require.Eventually(t, func() bool {
			var pvcs corev1.PersistentVolumeClaimList
			if err := k8sClient.List(ctx, &pvcs, client.InNamespace(namespace)); err != nil {
				return false
			}
			var n int
			for _, pvc := range pvcs.Items {
				for _, ref := range pvc.OwnerReferences {
					if ref.Kind == "TempoFullNode" && ref.Name == "rpc-node" {
						n++
					}
				}
			}
			return n == 2
		}, waitFor, tick)

		// Services: 2 p2p + 1 rpc.
		require.Eventually(t, func() bool {
			var svcs corev1.ServiceList
			if err := k8sClient.List(ctx, &svcs, client.InNamespace(namespace)); err != nil {
				return false
			}
			var n int
			for _, svc := range svcs.Items {
				for _, ref := range svc.OwnerReferences {
					if ref.Kind == "TempoFullNode" && ref.Name == "rpc-node" {
						n++
					}
				}
			}
			return n == 3
		}, waitFor, tick)
	})

	t.Run("scale up and down", func(t *testing.T) {
		var crd tempov1alpha1.TempoFullNode
		key := client.ObjectKey{Name: "rpc-node", Namespace: namespace}
		require.NoError(t, k8sClient.Get(ctx, key, &crd))
		crd.Spec.Replicas = 3
		require.NoError(t, k8sClient.Update(ctx, &crd))

		require.Eventually(t, func() bool { return countPods("rpc-node") == 3 }, waitFor, tick)

		require.NoError(t, k8sClient.Get(ctx, key, &crd))
		crd.Spec.Replicas = 1
		require.NoError(t, k8sClient.Update(ctx, &crd))

		// Note: envtest has no kubelet, so deleted pods linger in Terminating with finalizers
		// handled by the API server directly; they do go away because nothing pins them.
		require.Eventually(t, func() bool { return countPods("rpc-node") == 1 }, waitFor, tick)
	})

	t.Run("update image recreates deleted pods with new spec", func(t *testing.T) {
		var crd tempov1alpha1.TempoFullNode
		key := client.ObjectKey{Name: "rpc-node", Namespace: namespace}
		require.NoError(t, k8sClient.Get(ctx, key, &crd))
		crd.Spec.PodTemplate.Image = "ghcr.io/tempoxyz/tempo:v1.12.0"
		require.NoError(t, k8sClient.Update(ctx, &crd))

		// With no ready pods, the rollout budget blocks proactive deletes (correct behavior).
		// Simulate the pod going away (node drain / drift mitigation) and assert the
		// replacement runs the new image.
		var pods corev1.PodList
		require.NoError(t, k8sClient.List(ctx, &pods, client.InNamespace(namespace)))
		for i := range pods.Items {
			for _, ref := range pods.Items[i].OwnerReferences {
				if ref.Kind == "TempoFullNode" && ref.Name == "rpc-node" {
					require.NoError(t, k8sClient.Delete(ctx, &pods.Items[i]))
				}
			}
		}

		require.Eventually(t, func() bool {
			var pods corev1.PodList
			if err := k8sClient.List(ctx, &pods, client.InNamespace(namespace)); err != nil {
				return false
			}
			for _, pod := range pods.Items {
				for _, ref := range pod.OwnerReferences {
					if ref.Kind == "TempoFullNode" && ref.Name == "rpc-node" && pod.DeletionTimestamp == nil {
						if pod.Spec.Containers[0].Image == "ghcr.io/tempoxyz/tempo:v1.12.0" {
							return true
						}
					}
				}
			}
			return false
		}, waitFor, tick)
	})

	t.Run("CEL double-sign guards", func(t *testing.T) {
		// Validator with replicas > 1 must be rejected by the API server.
		bad := newCRD("bad-validator", tempov1alpha1.NodeRoleValidator, 2)
		bad.Spec.SigningKey = &tempov1alpha1.SigningKeySpec{
			SigningKeySecret: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "keys"}, Key: "signing-key",
			},
			EncryptionSecret: corev1.SecretKeySelector{
				LocalObjectReference: corev1.LocalObjectReference{Name: "keys"}, Key: "consensus-secret",
			},
		}
		err := k8sClient.Create(ctx, bad)
		require.Error(t, err)
		require.True(t, apierrors.IsInvalid(err))
		require.Contains(t, err.Error(), "double-sign guard")

		// Validator without a signing key must be rejected.
		noKey := newCRD("keyless-validator", tempov1alpha1.NodeRoleValidator, 1)
		err = k8sClient.Create(ctx, noKey)
		require.Error(t, err)
		require.Contains(t, err.Error(), "signingKey")

		// Role is immutable.
		ok := newCRD("mutating-role", tempov1alpha1.NodeRoleRPC, 1)
		require.NoError(t, k8sClient.Create(ctx, ok))
		ok.Spec.Role = tempov1alpha1.NodeRoleArchive
		err = k8sClient.Update(ctx, ok)
		require.Error(t, err)
		require.Contains(t, err.Error(), "immutable")
		require.NoError(t, k8sClient.Delete(ctx, ok))
	})

	t.Run("delete", func(t *testing.T) {
		var crd tempov1alpha1.TempoFullNode
		key := client.ObjectKey{Name: "rpc-node", Namespace: namespace}
		require.NoError(t, k8sClient.Get(ctx, key, &crd))
		require.NoError(t, k8sClient.Delete(ctx, &crd))

		require.Eventually(t, func() bool {
			err := k8sClient.Get(ctx, key, &tempov1alpha1.TempoFullNode{})
			return apierrors.IsNotFound(err)
		}, waitFor, tick)

		// envtest runs no garbage collector, so owned resources are not swept here.
		// Ownership is asserted in the create test; a real cluster GCs them.
		var pods corev1.PodList
		require.NoError(t, k8sClient.List(ctx, &pods, client.InNamespace(namespace)))
		for _, pod := range pods.Items {
			for _, ref := range pod.OwnerReferences {
				if ref.Kind == "TempoFullNode" && ref.Name == "rpc-node" {
					require.True(t, *ref.Controller)
					require.Equal(t, fmt.Sprintf("%s/%s", tempov1alpha1.GroupVersion.Group, tempov1alpha1.GroupVersion.Version), ref.APIVersion)
				}
			}
		}
	})
}
