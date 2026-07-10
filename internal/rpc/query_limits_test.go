package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/node"
)

func TestFeeHistoryBlockCountLimit(t *testing.T) {
	n := newQueryLimitNode(t)
	if _, err := feeHistory(n, []any{quantity(MaxFeeHistoryBlockCount), "latest"}); err != nil {
		t.Fatalf("Max fee history block count: %v", err)
	}
	if _, err := feeHistory(n, []any{quantity(MaxFeeHistoryBlockCount + 1), "latest"}); err == nil || !strings.Contains(err.Error(), "block count exceeds") {
		t.Fatalf("Max+1 fee history error = %v", err)
	}
}

func TestLogFilterShapeLimits(t *testing.T) {
	tags := blockTags{latest: MaxLogBlockRange}

	addresses := make([]any, MaxLogAddresses)
	for index := range addresses {
		addresses[index] = fmt.Sprintf("0x%040x", index+1)
	}
	if _, err := parseLogFilter(map[string]any{"address": addresses}, tags); err != nil {
		t.Fatalf("Max address filter: %v", err)
	}
	addresses = append(addresses, "0xffffffffffffffffffffffffffffffffffffffff")
	if _, err := parseLogFilter(map[string]any{"address": addresses}, tags); err == nil || !strings.Contains(err.Error(), "address filter exceeds") {
		t.Fatalf("Max+1 address filter error = %v", err)
	}

	topics := make([]any, MaxLogTopics)
	for index := range topics {
		topics[index] = nil
	}
	if _, err := parseLogFilter(map[string]any{"topics": topics}, tags); err != nil {
		t.Fatalf("Max topic positions: %v", err)
	}
	topics = append(topics, nil)
	if _, err := parseLogFilter(map[string]any{"topics": topics}, tags); err == nil || !strings.Contains(err.Error(), "topics filter exceeds") {
		t.Fatalf("Max+1 topic positions error = %v", err)
	}

	alternatives := make([]any, MaxLogTopicAlternatives)
	for index := range alternatives {
		alternatives[index] = fmt.Sprintf("0x%064x", index+1)
	}
	if _, err := parseLogFilter(map[string]any{"topics": []any{alternatives}}, tags); err != nil {
		t.Fatalf("Max topic alternatives: %v", err)
	}
	alternatives = append(alternatives, "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	if _, err := parseLogFilter(map[string]any{"topics": []any{alternatives}}, tags); err == nil || !strings.Contains(err.Error(), "topic filter position exceeds") {
		t.Fatalf("Max+1 topic alternatives error = %v", err)
	}
}

func TestEVMLogQueryRangeResultAndResponseLimits(t *testing.T) {
	n := newQueryLimitNode(t)
	if _, err := evmLogs(n, logFilter{fromBlock: 0, toBlock: MaxLogBlockRange - 1}); err != nil {
		t.Fatalf("Max log block range: %v", err)
	}
	if _, err := evmLogs(n, logFilter{fromBlock: 0, toBlock: MaxLogBlockRange}); err == nil || !strings.Contains(err.Error(), "block range exceeds") {
		t.Fatalf("Max+1 log block range error = %v", err)
	}

	collector := newEVMLogCollector(MaxLogResponseBytes)
	log := map[string]any{"address": "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "data": "0x"}
	for index := 0; index < MaxLogResults; index++ {
		if err := collector.append(log); err != nil {
			t.Fatalf("append result %d/%d: %v", index+1, MaxLogResults, err)
		}
	}
	if err := collector.append(log); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("Max+1 log result error = %v", err)
	}

	oversized := newEVMLogCollector(MaxLogResponseBytes)
	if err := oversized.append(map[string]any{"data": strings.Repeat("x", MaxLogResponseBytes)}); err == nil || !strings.Contains(err.Error(), "response exceeds") {
		t.Fatalf("oversized log response error = %v", err)
	}

	id := strings.Repeat("i", 1024)
	budget, err := logResultByteBudget(id)
	if err != nil {
		t.Fatal(err)
	}
	emptyResponse, err := json.Marshal(rpcResponse{ID: id, Result: []map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if len(emptyResponse)+1 > MaxLogResponseBytes || budget != MaxLogResponseBytes-(len(emptyResponse)-2+1) {
		t.Fatalf("response byte budget = %d, encoded empty response = %d", budget, len(emptyResponse)+1)
	}
}

func TestLogFilterCursorAdvancesOnlyAfterSuccessfulQuery(t *testing.T) {
	server := &Server{node: newQueryLimitNode(t), filters: newLogFilterStore()}
	id, err := server.registerLogFilter(logFilter{fromBlock: 0, toBlock: MaxLogBlockRange}, 0)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok, err := server.logFilterChangesAt(id, MaxLogBlockRange, MaxLogResponseBytes); !ok || err == nil {
		t.Fatalf("oversized range query: ok=%v err=%v", ok, err)
	}
	assertQueryLimitFilterCursor(t, server, id, 0)

	server.filters.mu.Lock()
	stored := server.filters.filters[id]
	stored.filter.toBlock = 0
	server.filters.filters[id] = stored
	server.filters.mu.Unlock()
	if _, ok, err := server.logFilterChangesAt(id, 0, 1); !ok || err == nil {
		t.Fatalf("oversized response query: ok=%v err=%v", ok, err)
	}
	assertQueryLimitFilterCursor(t, server, id, 0)

	logs, ok, err := server.logFilterChangesAt(id, 0, MaxLogResponseBytes)
	if !ok || err != nil || len(logs) != 0 {
		t.Fatalf("successful filter query: logs=%v ok=%v err=%v", logs, ok, err)
	}
	assertQueryLimitFilterCursor(t, server, id, 1)
}

func TestRPCDispatchEnforcesFeeHistoryAndLogRangeLimits(t *testing.T) {
	handler := NewServer(newQueryLimitNode(t))
	tests := []struct {
		name   string
		method string
		params []any
		error  string
	}{
		{
			name:   "fee history Max+1",
			method: "eth_feeHistory",
			params: []any{quantity(MaxFeeHistoryBlockCount + 1), "latest"},
			error:  "block count exceeds",
		},
		{
			name:   "log range Max+1",
			method: "eth_getLogs",
			params: []any{map[string]any{"fromBlock": "0x0", "toBlock": quantity(MaxLogBlockRange)}},
			error:  "block range exceeds",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := queryLimitRPCRequest(t, handler, test.method, test.params)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), test.error) {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}

	response := queryLimitRPCRequest(t, handler, "eth_getLogs", []any{map[string]any{
		"fromBlock": "0x0",
		"toBlock":   quantity(MaxLogBlockRange - 1),
	}})
	if response.Code != http.StatusOK {
		t.Fatalf("Max log range status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Body.Len() > MaxLogResponseBytes {
		t.Fatalf("log response bytes = %d, max = %d", response.Body.Len(), MaxLogResponseBytes)
	}
}

func assertQueryLimitFilterCursor(t *testing.T, server *Server, id string, want uint64) {
	t.Helper()
	server.filters.mu.Lock()
	defer server.filters.mu.Unlock()
	if got := server.filters.filters[id].nextBlock; got != want {
		t.Fatalf("filter cursor = %d, want %d", got, want)
	}
}

func newQueryLimitNode(t *testing.T) *node.Node {
	t.Helper()
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	n, err := node.NewDevelopment(node.Config{ChainID: "chainlab-query-limits", ProposerKey: key, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func queryLimitRPCRequest(t *testing.T, handler http.Handler, method string, params []any) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/rpc", bytes.NewReader(body))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
