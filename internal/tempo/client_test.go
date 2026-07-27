package tempo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// rpcHandler serves canned JSON-RPC batch responses keyed by method.
func rpcHandler(t *testing.T, results map[string]string) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))

		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		var batch []struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		require.NoError(t, json.Unmarshal(body, &batch))

		var responses []string
		for _, req := range batch {
			result, ok := results[req.Method]
			if !ok {
				responses = append(responses, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"error":{"code":-32601,"message":"method not found"}}`, req.ID))
				continue
			}
			responses = append(responses, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"result":%s}`, req.ID, result))
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "[%s]", joinComma(responses))
	}
}

func joinComma(elems []string) string {
	var out string
	for i, e := range elems {
		if i > 0 {
			out += ","
		}
		out += e
	}
	return out
}

func TestClient_Status(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("happy path - synced node", func(t *testing.T) {
		blockTime := time.Now().Add(-2 * time.Second)
		srv := httptest.NewServer(rpcHandler(t, map[string]string{
			"eth_syncing":          `false`,
			"eth_blockNumber":      `"0x1b4"`,
			"net_peerCount":        `"0xa"`,
			"eth_getBlockByNumber": fmt.Sprintf(`{"number":"0x1b4","timestamp":"0x%x"}`, blockTime.Unix()),
		}))
		defer srv.Close()

		client := NewClient(srv.Client())
		status, err := client.Status(ctx, srv.URL)
		require.NoError(t, err)

		require.False(t, status.Syncing)
		require.EqualValues(t, 436, status.Height)
		require.EqualValues(t, 10, status.PeerCount)
		require.Equal(t, blockTime.Unix(), status.LatestBlockTime.Unix())
		require.True(t, status.CaughtUp(time.Now()))
	})

	t.Run("syncing node returns progress object", func(t *testing.T) {
		srv := httptest.NewServer(rpcHandler(t, map[string]string{
			"eth_syncing":          `{"startingBlock":"0x0","currentBlock":"0x10","highestBlock":"0x1b4"}`,
			"eth_blockNumber":      `"0x10"`,
			"net_peerCount":        `"0x1"`,
			"eth_getBlockByNumber": `{"number":"0x10","timestamp":"0x1"}`,
		}))
		defer srv.Close()

		client := NewClient(srv.Client())
		status, err := client.Status(ctx, srv.URL)
		require.NoError(t, err)

		require.True(t, status.Syncing)
		require.False(t, status.CaughtUp(time.Now()))
	})

	t.Run("stalled node fails the block-age guard", func(t *testing.T) {
		stale := time.Now().Add(-10 * time.Minute)
		srv := httptest.NewServer(rpcHandler(t, map[string]string{
			"eth_syncing":          `false`,
			"eth_blockNumber":      `"0x1b4"`,
			"net_peerCount":        `"0x0"`,
			"eth_getBlockByNumber": fmt.Sprintf(`{"number":"0x1b4","timestamp":"0x%x"}`, stale.Unix()),
		}))
		defer srv.Close()

		client := NewClient(srv.Client())
		status, err := client.Status(ctx, srv.URL)
		require.NoError(t, err)

		require.False(t, status.Syncing)
		require.False(t, status.CaughtUp(time.Now()), "eth_syncing=false but stale block must not count as caught up")
	})

	t.Run("missing net api is not fatal", func(t *testing.T) {
		srv := httptest.NewServer(rpcHandler(t, map[string]string{
			"eth_syncing":          `false`,
			"eth_blockNumber":      `"0x1b4"`,
			"eth_getBlockByNumber": fmt.Sprintf(`{"number":"0x1b4","timestamp":"0x%x"}`, time.Now().Unix()),
		}))
		defer srv.Close()

		client := NewClient(srv.Client())
		status, err := client.Status(ctx, srv.URL)
		require.NoError(t, err)
		require.Zero(t, status.PeerCount)
	})

	t.Run("rpc error", func(t *testing.T) {
		srv := httptest.NewServer(rpcHandler(t, map[string]string{}))
		defer srv.Close()

		client := NewClient(srv.Client())
		_, err := client.Status(ctx, srv.URL)
		require.Error(t, err)
	})

	t.Run("http error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		}))
		defer srv.Close()

		client := NewClient(srv.Client())
		_, err := client.Status(ctx, srv.URL)
		require.Error(t, err)
	})

	t.Run("malformed host", func(t *testing.T) {
		client := NewClient(http.DefaultClient)
		_, err := client.Status(ctx, "not-a-url")
		require.Error(t, err)
		require.Contains(t, err.Error(), "malformed host")
	})

	t.Run("unreachable host", func(t *testing.T) {
		client := NewClient(&http.Client{Timeout: time.Second})
		_, err := client.Status(ctx, "http://127.0.0.1:1")
		require.Error(t, err)
	})
}

func TestNodeStatus_CaughtUp(t *testing.T) {
	t.Parallel()

	now := time.Now()

	require.True(t, NodeStatus{LatestBlockTime: now}.CaughtUp(now))
	require.True(t, NodeStatus{LatestBlockTime: now.Add(-SyncedBlockAgeThreshold)}.CaughtUp(now))
	require.False(t, NodeStatus{LatestBlockTime: now.Add(-SyncedBlockAgeThreshold - time.Second)}.CaughtUp(now))
	require.False(t, NodeStatus{Syncing: true, LatestBlockTime: now}.CaughtUp(now))
	require.False(t, NodeStatus{}.CaughtUp(now), "zero block time must not count as caught up")
}
