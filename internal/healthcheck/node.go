// Package healthcheck typically enables readiness or liveness probes within kubernetes.
// IMPORTANT: If you update this behavior, be sure to update internal/fullnode/pod_builder.go with the new
// operator image in the "healthcheck" container.
package healthcheck

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/aaronforce1/cosmos-operator/internal/tempo"
	"github.com/go-logr/logr"
)

// Statuser can query a Tempo node's sync state.
type Statuser interface {
	Status(ctx context.Context, rpcHost string) (tempo.NodeStatus, error)
}

type healthResponse struct {
	Address   string `json:"address"`
	InSync    bool   `json:"in_sync"`
	Height    uint64 `json:"height,omitempty"`
	PeerCount uint64 `json:"peer_count,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Node checks a Tempo node's JSON-RPC endpoint to determine if the node is in-sync or not.
// The response contract is stable and consumed by both kubelet readiness probes and the operator:
// 200 = in sync, 422 = reachable but catching up (or stalled), 503 = unreachable.
type Node struct {
	client     Statuser
	lastStatus int32
	logger     logr.Logger
	rpcHost    string
	timeout    time.Duration
}

func NewNode(logger logr.Logger, client Statuser, rpcHost string, timeout time.Duration) *Node {
	return &Node{
		client:  client,
		logger:  logger,
		rpcHost: rpcHost,
		timeout: timeout,
	}
}

// ServeHTTP implements http.Handler.
func (h *Node) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var resp healthResponse
	resp.Address = h.rpcHost

	ctx, cancel := context.WithTimeout(r.Context(), h.timeout)
	defer cancel()

	status, err := h.client.Status(ctx, h.rpcHost)
	if err != nil {
		resp.Error = err.Error()
		h.writeResponse(http.StatusServiceUnavailable, w, resp)
		return
	}

	resp.Height = status.Height
	resp.PeerCount = status.PeerCount
	resp.InSync = status.CaughtUp(time.Now())
	if !resp.InSync {
		h.writeResponse(http.StatusUnprocessableEntity, w, resp)
		return
	}

	h.writeResponse(http.StatusOK, w, resp)
}

func (h *Node) writeResponse(code int, w http.ResponseWriter, resp healthResponse) {
	w.WriteHeader(code)
	w.Header().Set("Content-Type", "application/json")
	mustJSONEncode(resp, w)
	// Only log when status code changes, so we don't spam logs.
	if atomic.SwapInt32(&h.lastStatus, int32(code)) != int32(code) {
		h.logger.Info("Health state change", "statusCode", code, "response", resp)
	}
}
