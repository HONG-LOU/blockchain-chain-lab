package rpc

import (
	"fmt"
	"strings"
	"testing"

	"chainlab/internal/types"
)

func TestWebSocketLogSubscriptionsHaveDedicatedLimit(t *testing.T) {
	server := &Server{
		wsNewHeads:            make(map[string]chan types.Block),
		wsLogs:                make(map[string]*wsLogSubscription),
		wsPendingTransactions: make(map[string]chan string),
	}
	for index := 0; index < MaxWebSocketLogSubscriptions; index++ {
		if _, _, err := server.registerLogSubscription(logFilter{}, nil); err != nil {
			t.Fatalf("register log subscription %d: %v", index, err)
		}
	}
	if _, _, err := server.registerLogSubscription(logFilter{}, nil); err == nil || !strings.Contains(err.Error(), "log subscription limit") {
		t.Fatalf("log subscription overflow error = %v", err)
	}
}

func TestWebSocketLogNotificationBuildsBlockProjectionOnceAndMatchesFilters(t *testing.T) {
	addressA := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	addressB := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	block := websocketLogBudgetBlock(addressA, addressB)
	server := &Server{
		wsNewHeads:            make(map[string]chan types.Block),
		wsLogs:                make(map[string]*wsLogSubscription),
		wsPendingTransactions: make(map[string]chan string),
	}
	_, eventsA, err := server.registerLogSubscription(logFilter{addresses: map[string]struct{}{addressA: {}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, eventsB, err := server.registerLogSubscription(logFilter{addresses: map[string]struct{}{addressB: {}}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	server.notifyLogs(block)
	assertWebSocketLogAddress(t, eventsA, addressA)
	assertWebSocketLogAddress(t, eventsB, addressB)
}

func TestWebSocketLogBudgetExhaustionClosesSubscriptionsInsteadOfDropping(t *testing.T) {
	address := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	server := &Server{
		wsNewHeads:            make(map[string]chan types.Block),
		wsLogs:                make(map[string]*wsLogSubscription),
		wsPendingTransactions: make(map[string]chan string),
	}
	_, events, err := server.registerLogSubscription(logFilter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	work, complete := server.notifyLogsWithLimit(websocketLogBudgetBlock(address, address), 1)
	if complete || work != 1 {
		t.Fatalf("budget result = work %d, complete %v", work, complete)
	}
	server.wsMu.Lock()
	remaining := len(server.wsLogs)
	server.wsMu.Unlock()
	if remaining != 0 {
		t.Fatalf("log subscriptions after budget exhaustion = %d", remaining)
	}
	if _, ok := <-events; ok {
		t.Fatal("budget-exhausted log subscription remained open")
	}
}

func websocketLogBudgetBlock(addressA string, addressB string) types.Block {
	transactions := []types.Transaction{
		{Type: types.TxCall, From: addressA, To: addressA},
		{Type: types.TxCall, From: addressB, To: addressB},
	}
	receipts := []types.Receipt{
		{Events: []types.Event{{Type: "first", Attributes: map[string]string{"value": "1"}}}},
		{Events: []types.Event{{Type: "second", Attributes: map[string]string{"value": "2"}}}},
	}
	return types.Block{
		Header: types.BlockHeader{
			ChainID:       "chainlab-local",
			Height:        1,
			TimeUnix:      1_700_000_001,
			Proposer:      addressA,
			GasLimit:      types.DefaultBlockGasLimit,
			BaseFeePerGas: types.InitialBaseFeePerGas,
			TxRoot:        types.TransactionRoot(transactions),
			ReceiptRoot:   types.ReceiptRoot(receipts),
			StateRoot:     fmt.Sprintf("0x%064x", 1),
		},
		Transactions: transactions,
		Receipts:     receipts,
	}
}

func assertWebSocketLogAddress(t *testing.T, events <-chan map[string]any, want string) {
	t.Helper()
	select {
	case log := <-events:
		if got := log["address"]; got != want {
			t.Fatalf("log address = %v, want %s", got, want)
		}
	default:
		t.Fatalf("no websocket log for %s", want)
	}
}
