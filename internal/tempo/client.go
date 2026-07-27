// Package tempo contains a JSON-RPC client for Tempo (reth + Commonware) nodes
// plus the status collection and caching machinery built on top of it.
package tempo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SyncedBlockAgeThreshold is the maximum age of the latest block for a node to be considered
// caught up with the chain tip. Tempo block times are sub-second, so a node whose latest block
// is older than this is either syncing, stalled, or partitioned even if eth_syncing reports
// false (reth reports false while idle).
const SyncedBlockAgeThreshold = 60 * time.Second

// NodeStatus is a Tempo node's sync state collected via JSON-RPC.
type NodeStatus struct {
	// Syncing is true if eth_syncing reports an active sync.
	Syncing bool
	// Height is the latest block number from eth_blockNumber.
	Height uint64
	// PeerCount is the number of connected peers from net_peerCount.
	PeerCount uint64
	// LatestBlockTime is the timestamp of the latest block.
	LatestBlockTime time.Time
}

// CaughtUp reports whether the node is in sync with the chain tip as of "now":
// not actively syncing AND its latest block is recent. The block-age guard catches nodes that
// report eth_syncing == false while stalled or partitioned.
func (s NodeStatus) CaughtUp(now time.Time) bool {
	if s.Syncing {
		return false
	}
	if s.LatestBlockTime.IsZero() {
		return false
	}
	return now.Sub(s.LatestBlockTime) <= SyncedBlockAgeThreshold
}

// Client makes JSON-RPC requests against a Tempo node's HTTP endpoint.
// This package uses a hand-rolled client because the queries are trivial and it prevents any
// dependency on go-ethereum packages.
type Client struct {
	httpDo func(req *http.Request) (*http.Response, error)
}

func NewClient(client *http.Client) *Client {
	return &Client{httpDo: client.Do}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
}

type rpcResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

const (
	idSyncing = iota + 1
	idBlockNumber
	idPeerCount
	idLatestBlock
)

// Status fetches the node's sync state with a single batched JSON-RPC request:
// eth_syncing, eth_blockNumber, net_peerCount, and eth_getBlockByNumber("latest").
func (client *Client) Status(ctx context.Context, rpcHost string) (NodeStatus, error) {
	var status NodeStatus
	u, err := url.ParseRequestURI(rpcHost)
	if err != nil {
		return status, fmt.Errorf("malformed host: %w", err)
	}

	batch := []rpcRequest{
		{JSONRPC: "2.0", ID: idSyncing, Method: "eth_syncing", Params: []any{}},
		{JSONRPC: "2.0", ID: idBlockNumber, Method: "eth_blockNumber", Params: []any{}},
		{JSONRPC: "2.0", ID: idPeerCount, Method: "net_peerCount", Params: []any{}},
		{JSONRPC: "2.0", ID: idLatestBlock, Method: "eth_getBlockByNumber", Params: []any{"latest", false}},
	}
	body, err := json.Marshal(batch)
	if err != nil {
		return status, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return status, fmt.Errorf("malformed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.httpDo(req)
	if err != nil {
		return status, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return status, errors.New(resp.Status)
	}

	var responses []rpcResponse
	if err = json.NewDecoder(resp.Body).Decode(&responses); err != nil {
		return status, fmt.Errorf("malformed json: %w", err)
	}

	byID := make(map[int]rpcResponse, len(responses))
	for _, r := range responses {
		byID[r.ID] = r
	}

	if err = parseSyncing(byID[idSyncing], &status); err != nil {
		return status, err
	}
	if status.Height, err = parseHexUint(byID[idBlockNumber], "eth_blockNumber"); err != nil {
		return status, err
	}
	if status.PeerCount, err = parseHexUint(byID[idPeerCount], "net_peerCount"); err != nil {
		// net_peerCount may be unavailable if the net API is not enabled; not fatal.
		status.PeerCount = 0
	}
	if err = parseLatestBlock(byID[idLatestBlock], &status); err != nil {
		return status, err
	}

	return status, nil
}

func parseSyncing(resp rpcResponse, status *NodeStatus) error {
	if err := rpcError(resp, "eth_syncing"); err != nil {
		return err
	}
	// eth_syncing returns false when not syncing, or an object while syncing.
	var syncing bool
	if err := json.Unmarshal(resp.Result, &syncing); err != nil {
		// Not a bool, so it's the syncing progress object.
		status.Syncing = true
		return nil
	}
	status.Syncing = syncing
	return nil
}

func parseLatestBlock(resp rpcResponse, status *NodeStatus) error {
	if err := rpcError(resp, "eth_getBlockByNumber"); err != nil {
		return err
	}
	var block struct {
		Timestamp string `json:"timestamp"`
	}
	if err := json.Unmarshal(resp.Result, &block); err != nil {
		return fmt.Errorf("eth_getBlockByNumber: malformed result: %w", err)
	}
	if block.Timestamp == "" {
		return errors.New("eth_getBlockByNumber: missing latest block")
	}
	ts, err := hexToUint(block.Timestamp)
	if err != nil {
		return fmt.Errorf("eth_getBlockByNumber: malformed timestamp: %w", err)
	}
	status.LatestBlockTime = time.Unix(int64(ts), 0)
	return nil
}

func parseHexUint(resp rpcResponse, method string) (uint64, error) {
	if err := rpcError(resp, method); err != nil {
		return 0, err
	}
	var hex string
	if err := json.Unmarshal(resp.Result, &hex); err != nil {
		return 0, fmt.Errorf("%s: malformed result: %w", method, err)
	}
	return hexToUint(hex)
}

func rpcError(resp rpcResponse, method string) error {
	if resp.Error != nil {
		return fmt.Errorf("%s: rpc error %d: %s", method, resp.Error.Code, resp.Error.Message)
	}
	if len(resp.Result) == 0 {
		return fmt.Errorf("%s: missing result", method)
	}
	return nil
}

func hexToUint(hex string) (uint64, error) {
	return strconv.ParseUint(strings.TrimPrefix(hex, "0x"), 16, 64)
}
