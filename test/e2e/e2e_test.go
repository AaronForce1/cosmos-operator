//go:build e2e

// Package e2e brings up TempoFullNode resources against a real cluster (kind or GKE) and asserts
// the nodes sync against a live network. It uses the cluster from KUBECONFIG / in-cluster config.
//
// Prerequisites:
//   - The TempoFullNode CRD is installed (make install) and the operator is running
//     (make run, or make deploy with a pushed image).
//   - The cluster can pull ghcr.io/tempoxyz/tempo and reach the public network
//     (snapshots.tempo.xyz and the chain's p2p/bootnodes).
//   - Nodes have enough local disk for the chosen snapshot profile. On kind, prefer
//     E2E_CHAIN=moderato and small replica counts; kind is a smoke test, not a perf test.
//
// Run: make test-e2e   (or: go test -tags e2e -count=1 -timeout=60m ./test/e2e/...)
//
// Tunables (env): E2E_NAMESPACE, E2E_CHAIN, E2E_IMAGE, E2E_REPLICAS, E2E_STORAGE_CLASS,
// E2E_SYNC_TIMEOUT (Go duration).
package e2e

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
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
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestTempoFullNodeSyncsAgainstNetwork(t *testing.T) {
	var (
		namespace    = envOr("E2E_NAMESPACE", "tempo-e2e")
		chain        = envOr("E2E_CHAIN", "moderato")
		image        = envOr("E2E_IMAGE", "ghcr.io/tempoxyz/tempo:v1.11.0")
		storageClass = envOr("E2E_STORAGE_CLASS", "standard")
	)
	replicas, err := strconv.Atoi(envOr("E2E_REPLICAS", "2"))
	require.NoError(t, err)
	syncTimeout, err := time.ParseDuration(envOr("E2E_SYNC_TIMEOUT", "45m"))
	require.NoError(t, err)

	scheme := runtime.NewScheme()
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(tempov1alpha1.AddToScheme(scheme))

	cfg, err := ctrl.GetConfig()
	require.NoError(t, err, "no kubeconfig; point KUBECONFIG at a kind or GKE cluster")

	k8s, err := client.New(cfg, client.Options{Scheme: scheme})
	require.NoError(t, err)

	ctx := context.Background()

	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
	if err := k8s.Create(ctx, ns); err != nil {
		require.True(t, apierrors.IsAlreadyExists(err), "create namespace: %v", err)
	}

	crd := &tempov1alpha1.TempoFullNode{
		ObjectMeta: metav1.ObjectMeta{Name: "e2e", Namespace: namespace},
		Spec: tempov1alpha1.TempoFullNodeSpec{
			Role:     tempov1alpha1.NodeRoleRPC,
			Replicas: int32(replicas),
			ChainSpec: tempov1alpha1.TempoChainSpec{
				Chain: chain,
			},
			PodTemplate: tempov1alpha1.PodSpec{
				Image: image,
			},
			ConsensusVolume: tempov1alpha1.ConsensusVolumeSpec{
				StorageClassName: storageClass,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
				},
			},
			Service: tempov1alpha1.ServiceSpec{
				// No LoadBalancers on kind.
				MaxP2PExternalAddresses: ptr(int32(0)),
			},
		},
	}
	require.NoError(t, k8s.Create(ctx, crd))
	t.Cleanup(func() {
		_ = k8s.Delete(context.Background(), crd)
	})

	key := client.ObjectKeyFromObject(crd)

	// Phase 1: all pods scheduled and running (snapshot download can take a while).
	t.Logf("waiting for %d pods to run (snapshot download in progress)...", replicas)
	require.Eventually(t, func() bool {
		var pods corev1.PodList
		if err := k8s.List(ctx, &pods, client.InNamespace(namespace)); err != nil {
			return false
		}
		running := 0
		for _, pod := range pods.Items {
			if pod.Status.Phase == corev1.PodRunning {
				running++
			}
		}
		return running >= replicas
	}, syncTimeout/2, 10*time.Second)

	// Phase 2: every pod reports in-sync via the CRD status, sourced from eth_syncing +
	// block-age against the live network.
	t.Log("waiting for all pods to report in-sync with the network...")
	require.Eventually(t, func() bool {
		var got tempov1alpha1.TempoFullNode
		if err := k8s.Get(ctx, key, &got); err != nil {
			return false
		}
		if len(got.Status.SyncInfo) < replicas {
			return false
		}
		for name, info := range got.Status.SyncInfo {
			if info.InSync == nil || !*info.InSync {
				t.Logf("pod %s not in sync yet (err=%v)", name, info.Error)
				return false
			}
		}
		return true
	}, syncTimeout, 15*time.Second)

	// Phase 3: heights advance — the nodes follow consensus, not just a static snapshot.
	heightOf := func() uint64 {
		var got tempov1alpha1.TempoFullNode
		require.NoError(t, k8s.Get(ctx, key, &got))
		var max uint64
		for _, info := range got.Status.SyncInfo {
			if info.Height != nil && *info.Height > max {
				max = *info.Height
			}
		}
		return max
	}
	before := heightOf()
	require.Eventually(t, func() bool {
		return heightOf() > before
	}, 5*time.Minute, 15*time.Second, "heights are not advancing; nodes are not following the chain")

	t.Logf("e2e OK: %d nodes in sync on %s, height advancing (>= %d)", replicas, chain, before)
}

func ptr[T any](v T) *T { return &v }
