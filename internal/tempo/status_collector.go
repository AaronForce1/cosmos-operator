package tempo

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	tempov1alpha1 "github.com/aaronforce1/cosmos-operator/api/v1alpha1"
	"golang.org/x/sync/errgroup"
	corev1 "k8s.io/api/core/v1"
)

// Statuser fetches a Tempo node's sync state via its JSON-RPC endpoint.
type Statuser interface {
	Status(ctx context.Context, rpcHost string) (NodeStatus, error)
}

// StatusCollector collects the node status of all pods owned by a controller.
type StatusCollector struct {
	client  Statuser
	timeout time.Duration
}

// NewStatusCollector returns a valid StatusCollector.
// Timeout is exposed here because it is important for good performance in reconcile loops,
// and reminds callers to set it.
func NewStatusCollector(client Statuser, timeout time.Duration) *StatusCollector {
	return &StatusCollector{client: client, timeout: timeout}
}

// Collect returns a StatusCollection for the given pods.
// Any non-nil error can be treated as transient and retried.
func (coll StatusCollector) Collect(ctx context.Context, pods []corev1.Pod) StatusCollection {
	var eg errgroup.Group
	now := time.Now()
	statuses := make(StatusCollection, len(pods))

	for i := range pods {
		eg.Go(func() error {
			pod := pods[i]
			statuses[i].TS = now
			statuses[i].Pod = &pod
			ip := pod.Status.PodIP
			if ip == "" {
				// Check for IP, so we don't pay overhead of making a request.
				statuses[i].Err = errors.New("pod has no IP")
				return nil
			}
			rpcPort := tempov1alpha1.DefaultRPCPort
			for _, c := range pod.Spec.Containers {
				if c.Name == "node" {
					for _, p := range c.Ports {
						if p.Name == "http-rpc" {
							rpcPort = p.ContainerPort
							break
						}
					}
					break
				}
			}
			host := fmt.Sprintf("http://%s:%d", ip, rpcPort)
			cctx, cancel := context.WithTimeout(ctx, coll.timeout)
			defer cancel()
			resp, err := coll.client.Status(cctx, host)
			if err != nil {
				statuses[i].Err = err
				return nil
			}
			statuses[i].Status = resp
			return nil
		})
	}

	_ = eg.Wait()
	sort.Sort(statuses)
	return statuses
}
