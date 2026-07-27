package healthcheck

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aaronforce1/cosmos-operator/internal/tempo"
	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
)

type mockStatuser func(ctx context.Context, rpcHost string) (tempo.NodeStatus, error)

func (fn mockStatuser) Status(ctx context.Context, rpcHost string) (tempo.NodeStatus, error) {
	return fn(ctx, rpcHost)
}

func TestNode_ServeHTTP(t *testing.T) {
	t.Parallel()

	const rpcHost = "http://localhost:8545"

	t.Run("in sync", func(t *testing.T) {
		client := mockStatuser(func(ctx context.Context, host string) (tempo.NodeStatus, error) {
			require.NotNil(t, ctx)
			require.Equal(t, rpcHost, host)
			return tempo.NodeStatus{Height: 100, PeerCount: 5, LatestBlockTime: time.Now()}, nil
		})

		handler := NewNode(logr.Discard(), client, rpcHost, time.Second)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

		require.Equal(t, http.StatusOK, rec.Code)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Equal(t, true, resp["in_sync"])
		require.EqualValues(t, 100, resp["height"])
		require.EqualValues(t, 5, resp["peer_count"])
	})

	t.Run("catching up", func(t *testing.T) {
		client := mockStatuser(func(ctx context.Context, host string) (tempo.NodeStatus, error) {
			return tempo.NodeStatus{Syncing: true, LatestBlockTime: time.Now()}, nil
		})

		handler := NewNode(logr.Discard(), client, rpcHost, time.Second)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	})

	t.Run("stalled node reports catching up", func(t *testing.T) {
		client := mockStatuser(func(ctx context.Context, host string) (tempo.NodeStatus, error) {
			return tempo.NodeStatus{LatestBlockTime: time.Now().Add(-10 * time.Minute)}, nil
		})

		handler := NewNode(logr.Discard(), client, rpcHost, time.Second)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

		require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	})

	t.Run("unreachable", func(t *testing.T) {
		client := mockStatuser(func(ctx context.Context, host string) (tempo.NodeStatus, error) {
			return tempo.NodeStatus{}, errors.New("connection refused")
		})

		handler := NewNode(logr.Discard(), client, rpcHost, time.Second)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

		require.Equal(t, http.StatusServiceUnavailable, rec.Code)

		var resp map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		require.Equal(t, "connection refused", resp["error"])
	})
}
