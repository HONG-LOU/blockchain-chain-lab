package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/node"
	"chainlab/internal/types"
)

func TestBoundedJSONRPCRejectsBusyServerAndRecovers(t *testing.T) {
	n := newResourceLimitTestNode(t, 0)
	server := &Server{
		node:     n,
		rpcSlots: make(chan struct{}, MaxConcurrentJSONRPCRequests),
	}
	handler := server.routes()
	for index := 0; index < MaxConcurrentJSONRPCRequests; index++ {
		server.rpcSlots <- struct{}{}
	}
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "web3_clientVersion",
	})
	if err != nil {
		t.Fatal(err)
	}

	responseReady := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPost, "/rpc", bytes.NewReader(body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		responseReady <- response
	}()

	var busyResponse *httptest.ResponseRecorder
	select {
	case busyResponse = <-responseReady:
	case <-time.After(time.Second):
		<-server.rpcSlots
		<-responseReady
		t.Fatal("request blocked while the JSON-RPC semaphore was full")
	}
	if busyResponse.Code != http.StatusServiceUnavailable {
		t.Fatalf("busy status = %d, body = %s", busyResponse.Code, busyResponse.Body.String())
	}
	if retryAfter := busyResponse.Header().Get("Retry-After"); retryAfter != "1" {
		t.Fatalf("Retry-After = %q", retryAfter)
	}
	if !strings.Contains(busyResponse.Body.String(), "json-rpc server is busy") {
		t.Fatalf("busy body = %s", busyResponse.Body.String())
	}

	<-server.rpcSlots
	recovered := performResourceRPCRequest(t, handler, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "web3_clientVersion",
	})
	if recovered.Code != http.StatusOK {
		t.Fatalf("recovered status = %d, body = %s", recovered.Code, recovered.Body.String())
	}
}

func TestHTTPPOSTConcurrencyLimitRejectsOverflow(t *testing.T) {
	slots := make(chan struct{}, MaxConcurrentHTTPPOSTRequests)
	for range MaxConcurrentHTTPPOSTRequests {
		slots <- struct{}{}
	}
	handler := limitConcurrentPOSTRequests(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("saturated handler must not run")
	}), slots)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/tx", strings.NewReader("{}")))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
}

func TestHTTPConcurrencyLimitCoversReadsButPreservesLiveness(t *testing.T) {
	server := newServer(newResourceLimitTestNode(t, 0), nil)
	server.requestSlots = make(chan struct{}, 1)
	server.requestSlots <- struct{}{}
	handler := server.routes()

	busy := httptest.NewRecorder()
	handler.ServeHTTP(busy, httptest.NewRequest(http.MethodGet, "/chain/block/0", nil))
	if busy.Code != http.StatusServiceUnavailable || !strings.Contains(busy.Body.String(), "http server is busy") {
		t.Fatalf("busy GET response = %d %s", busy.Code, busy.Body.String())
	}
	if retryAfter := busy.Header().Get("Retry-After"); retryAfter != "1" {
		t.Fatalf("Retry-After = %q", retryAfter)
	}

	live := httptest.NewRecorder()
	handler.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if live.Code != http.StatusOK {
		t.Fatalf("liveness response = %d %s", live.Code, live.Body.String())
	}

	server.livenessSlots = make(chan struct{}, 1)
	server.livenessSlots <- struct{}{}
	busyLive := httptest.NewRecorder()
	server.routes().ServeHTTP(busyLive, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if busyLive.Code != http.StatusServiceUnavailable || !strings.Contains(busyLive.Body.String(), "liveness server is busy") {
		t.Fatalf("busy liveness response = %d %s", busyLive.Code, busyLive.Body.String())
	}
	<-server.livenessSlots

	nonGetLive := httptest.NewRecorder()
	handler.ServeHTTP(nonGetLive, httptest.NewRequest(http.MethodOptions, "/health/live", nil))
	if nonGetLive.Code != http.StatusServiceUnavailable {
		t.Fatalf("non-GET liveness bypassed HTTP slots: %d %s", nonGetLive.Code, nonGetLive.Body.String())
	}

	invalidUpgradeRequest := httptest.NewRequest(http.MethodGet, "/rpc/ws", nil)
	invalidUpgradeRequest.Header.Set("Connection", "Upgrade")
	invalidUpgradeRequest.Header.Set("Upgrade", "websocket")
	invalidUpgradeRequest.Header.Set("Sec-WebSocket-Version", "13")
	invalidUpgradeRequest.Header.Set("Sec-WebSocket-Key", "not-a-16-byte-base64-key")
	invalidUpgrade := httptest.NewRecorder()
	handler.ServeHTTP(invalidUpgrade, invalidUpgradeRequest)
	if invalidUpgrade.Code != http.StatusServiceUnavailable {
		t.Fatalf("invalid websocket bypassed HTTP slots: %d %s", invalidUpgrade.Code, invalidUpgrade.Body.String())
	}

	nonGetUpgradeRequest := httptest.NewRequest(http.MethodPut, "/rpc/ws", nil)
	nonGetUpgradeRequest.Header.Set("Connection", "keep-alive, Upgrade")
	nonGetUpgradeRequest.Header.Set("Upgrade", "websocket")
	nonGetUpgradeRequest.Header.Set("Sec-WebSocket-Version", "13")
	nonGetUpgradeRequest.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	nonGetUpgrade := httptest.NewRecorder()
	handler.ServeHTTP(nonGetUpgrade, nonGetUpgradeRequest)
	if nonGetUpgrade.Code != http.StatusServiceUnavailable {
		t.Fatalf("non-GET websocket bypassed HTTP slots: %d %s", nonGetUpgrade.Code, nonGetUpgrade.Body.String())
	}

	oversizedHeaderRequest := httptest.NewRequest(http.MethodGet, "/rpc/ws", nil)
	oversizedHeaderRequest.Header.Set("Connection", strings.Repeat("x", maxWebSocketConnectionHeaderBytes+1))
	oversizedHeaderRequest.Header.Set("Upgrade", "websocket")
	oversizedHeaderRequest.Header.Set("Sec-WebSocket-Version", "13")
	oversizedHeaderRequest.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	oversizedHeader := httptest.NewRecorder()
	handler.ServeHTTP(oversizedHeader, oversizedHeaderRequest)
	if oversizedHeader.Code != http.StatusServiceUnavailable {
		t.Fatalf("oversized websocket header bypassed HTTP slots: %d %s", oversizedHeader.Code, oversizedHeader.Body.String())
	}
}

func TestInstalledFiltersAreBoundedAndExpire(t *testing.T) {
	server := &Server{filters: newLogFilterStore()}
	now := time.Unix(1_000, 0)
	server.filters.now = func() time.Time { return now }
	for index := 0; index < MaxInstalledFilters; index++ {
		if _, err := server.registerBlockFilter(0); err != nil {
			t.Fatalf("register filter %d: %v", index, err)
		}
	}
	if _, err := server.registerBlockFilter(0); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("overflow filter error = %v", err)
	}
	now = now.Add(FilterTTL + time.Second)
	if _, err := server.registerBlockFilter(0); err != nil {
		t.Fatalf("register after TTL: %v", err)
	}
}

func TestWebSocketSubscriptionsHaveGlobalLimit(t *testing.T) {
	server := &Server{
		wsNewHeads:            make(map[string]chan types.Block),
		wsLogs:                make(map[string]*wsLogSubscription),
		wsPendingTransactions: make(map[string]chan string),
	}
	for index := 0; index < MaxWebSocketSubscriptions; index++ {
		if _, _, err := server.registerNewHeadSubscription(nil); err != nil {
			t.Fatalf("register subscription %d: %v", index, err)
		}
	}
	if _, _, err := server.registerNewHeadSubscription(nil); err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("overflow subscription error = %v", err)
	}
}

func TestBatchResponseRecorderDoesNotBufferPastLimit(t *testing.T) {
	recorder := newRPCResponseRecorder(8)
	if written, err := recorder.Write([]byte("123456789")); err != nil || written != 9 {
		t.Fatalf("write = %d, %v", written, err)
	}
	if !recorder.overflow || recorder.body.Len() != 0 {
		t.Fatalf("recorder overflow=%v bytes=%d", recorder.overflow, recorder.body.Len())
	}
}

func TestJSONRPCBatchLimitRejectsBeforeExecutionAndAcceptsMaximum(t *testing.T) {
	n := newResourceLimitTestNode(t, 0)
	handler := NewServer(n)
	recipient := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	faucet := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "chain_faucet",
		"params": map[string]any{
			"address": recipient,
			"amount":  1,
		},
	}

	oversized := make([]map[string]any, 0, MaxJSONRPCBatchRequests+1)
	oversized = append(oversized, faucet)
	for id := 2; id <= MaxJSONRPCBatchRequests+1; id++ {
		oversized = append(oversized, map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"method":  "web3_clientVersion",
		})
	}
	response := performResourceRPCRequest(t, handler, oversized)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("Max+1 status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), fmt.Sprintf("batch exceeds %d requests", MaxJSONRPCBatchRequests)) {
		t.Fatalf("Max+1 body = %s", response.Body.String())
	}
	if pool := n.TxPool(); pool.PendingCount != 0 || pool.QueuedCount != 0 {
		t.Fatalf("oversized batch executed before rejection: %+v", pool)
	}

	standalone := performResourceRPCRequest(t, handler, faucet)
	if standalone.Code != http.StatusOK {
		t.Fatalf("standalone sentinel status = %d, body = %s", standalone.Code, standalone.Body.String())
	}
	if pool := n.TxPool(); pool.PendingCount != 1 || pool.QueuedCount != 0 {
		t.Fatalf("standalone sentinel did not execute: %+v", pool)
	}

	maximum := make([]map[string]any, MaxJSONRPCBatchRequests)
	for index := range maximum {
		maximum[index] = map[string]any{
			"jsonrpc": "2.0",
			"id":      index + 1,
			"method":  "web3_clientVersion",
		}
	}
	response = performResourceRPCRequest(t, handler, maximum)
	if response.Code != http.StatusOK {
		t.Fatalf("Max status = %d, body = %s", response.Code, response.Body.String())
	}
	var responses []rpcResponse
	if err := json.Unmarshal(response.Body.Bytes(), &responses); err != nil {
		t.Fatal(err)
	}
	if len(responses) != MaxJSONRPCBatchRequests {
		t.Fatalf("Max response count = %d", len(responses))
	}
}

func TestJSONRPCBodyLimitRejectsDeclaredAndChunkedOversize(t *testing.T) {
	handler := NewServer(newResourceLimitTestNode(t, 0))

	t.Run("declared", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/rpc", strings.NewReader("{}"))
		request.ContentLength = int64(MaxJSONRPCRequestBodyBytes) + 1
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		assertJSONRPCRequestTooLarge(t, response)
	})

	t.Run("chunked", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, "/rpc", nil)
		request.ContentLength = -1
		request.Body = io.NopCloser(&resourceRepeatedByteReader{
			remaining: int64(MaxJSONRPCRequestBodyBytes) + 1,
			value:     ' ',
		})
		response := httptest.NewRecorder()

		handler.ServeHTTP(response, request)

		assertJSONRPCRequestTooLarge(t, response)
	})
}

func TestJSONRPCBatchResponseLimit(t *testing.T) {
	n := newResourceLimitTestNode(t, types.MaxValidators)
	handler := NewServer(n)
	batch := make([]map[string]any, MaxJSONRPCBatchRequests)
	for index := range batch {
		batch[index] = map[string]any{
			"jsonrpc": "2.0",
			"id":      index + 1,
			"method":  "chain_validators",
		}
	}

	response := performResourceRPCRequest(t, handler, batch)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body bytes = %d", response.Code, response.Body.Len())
	}
	if !strings.Contains(response.Body.String(), fmt.Sprintf("batch response exceeds %d bytes", MaxJSONRPCBatchResponseBytes)) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestEthCallRejectsNonLatestBlockTags(t *testing.T) {
	handler := NewServer(newResourceLimitTestNode(t, 0))
	for _, tag := range []any{"earliest", "safe", "finalized", "0x0", float64(0)} {
		t.Run(fmt.Sprintf("%v", tag), func(t *testing.T) {
			response := performResourceRPCRequest(t, handler, map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"method":  "eth_call",
				"params": []any{
					map[string]any{
						"to":     "0xcccccccccccccccccccccccccccccccccccccccc",
						"method": "get",
					},
					tag,
				},
			})
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), "eth_call currently supports only the latest block") {
				t.Fatalf("body = %s", response.Body.String())
			}
		})
	}
}

func performResourceRPCRequest(t *testing.T, handler http.Handler, payload any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/rpc", bytes.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func assertJSONRPCRequestTooLarge(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), fmt.Sprintf("json-rpc request body exceeds %d bytes", MaxJSONRPCRequestBodyBytes)) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func newResourceLimitTestNode(t *testing.T, validatorCount int) *node.Node {
	t.Helper()
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := chaincrypto.AddressFromPrivateKey(key)
	var validators []string
	if validatorCount > 0 {
		validators = make([]string, 0, validatorCount)
		validators = append(validators, proposer)
		for candidate := uint64(1); len(validators) < validatorCount; candidate++ {
			address := fmt.Sprintf("0x%040x", candidate)
			if address != proposer {
				validators = append(validators, address)
			}
		}
	}
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		Validators:     validators,
		GenesisBalance: map[string]uint64{proposer: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

type resourceRepeatedByteReader struct {
	remaining int64
	value     byte
}

func (r *resourceRepeatedByteReader) Read(output []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	count := len(output)
	if int64(count) > r.remaining {
		count = int(r.remaining)
	}
	for index := 0; index < count; index++ {
		output[index] = r.value
	}
	r.remaining -= int64(count)
	return count, nil
}
