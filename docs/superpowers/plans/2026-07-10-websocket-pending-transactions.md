# WebSocket Pending Transactions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `eth_subscribe("newPendingTransactions")` so WebSocket clients receive hashes for transactions accepted into the local txpool after subscription.

**Architecture:** Extend the existing `internal/rpc/server.go` WebSocket subscription registry with a pending transaction hash channel map. Broadcast after each RPC/REST path successfully accepts a transaction into `node.SubmitTx` or `node.RequestFaucet`; do not modify node consensus or mempool semantics.

**Tech Stack:** Go, `net/http`, ChainLab's existing handwritten WebSocket JSON-RPC support, existing `types.Transaction.Hash()`, existing RPC test helpers in `internal/rpc/server_test.go`.

---

### Task 1: Add Pending Transaction WebSocket Subscription

**Files:**
- Modify: `internal/rpc/server_test.go`
- Modify: `internal/rpc/server.go`
- Modify: `README.md`
- Modify: `docs/current-blockchain-tech-roadmap.md`
- Modify: `docs/superpowers/specs/2026-07-09-own-chain-design.md`

- [ ] **Step 1: Write the failing WebSocket test**

Add this test near the existing WebSocket tests in `internal/rpc/server_test.go`:

```go
func TestWebSocketEthSubscribeNewPendingTransactionsPublishesAcceptedTransactionHashes(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bobKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	bob := chaincrypto.AddressFromPrivateKey(bobKey)
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	conn, reader := openWebSocket(t, server.URL, "/rpc/ws")
	defer conn.Close()
	writeWebSocketText(t, conn, `{"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["newPendingTransactions"]}`)
	subscribeRaw := readWebSocketText(t, conn, reader)
	var subscribeResp struct {
		ID     int    `json:"id"`
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	if err := json.Unmarshal([]byte(subscribeRaw), &subscribeResp); err != nil {
		t.Fatal(err)
	}
	if subscribeResp.ID != 1 || subscribeResp.Error != "" || subscribeResp.Result == "" {
		t.Fatalf("subscribe pending response = %#v raw=%s", subscribeResp, subscribeRaw)
	}

	tx := signedRPCTransaction(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       bob,
		Nonce:    0,
		Value:    12,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	body, err := json.Marshal(tx)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(server.URL+"/tx", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("submit status = %d", resp.StatusCode)
	}

	notificationRaw := readWebSocketText(t, conn, reader)
	var notification struct {
		Method string `json:"method"`
		Params struct {
			Subscription string `json:"subscription"`
			Result       string `json:"result"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(notificationRaw), &notification); err != nil {
		t.Fatal(err)
	}
	if notification.Method != "eth_subscription" || notification.Params.Subscription != subscribeResp.Result {
		t.Fatalf("pending notification envelope = %#v raw=%s", notification, notificationRaw)
	}
	if notification.Params.Result != tx.Hash() {
		t.Fatalf("pending notification hash = %s, want %s", notification.Params.Result, tx.Hash())
	}
}
```

- [ ] **Step 2: Run RED**

Run:

```powershell
go test -count=1 -run TestWebSocketEthSubscribeNewPendingTransactionsPublishesAcceptedTransactionHashes ./internal/rpc
```

Expected: fail with `unsupported subscription`.

- [ ] **Step 3: Add pending subscription state and helpers**

In `internal/rpc/server.go`, extend `Server` and constructor:

```go
wsPendingTransactions map[string]chan string
```

Initialize it in `NewServerWithPeers`:

```go
wsPendingTransactions: make(map[string]chan string),
```

Add helpers beside the existing WebSocket subscription helpers:

```go
func (s *Server) registerPendingTransactionSubscription() (string, <-chan string) {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	s.nextWSID++
	id := quantity(s.nextWSID)
	events := make(chan string, 16)
	s.wsPendingTransactions[id] = events
	return id, events
}

func (s *Server) unregisterPendingTransactionSubscription(id string) bool {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	events, ok := s.wsPendingTransactions[id]
	if !ok {
		return false
	}
	delete(s.wsPendingTransactions, id)
	close(events)
	return true
}

func (s *Server) notifyPendingTransaction(tx types.Transaction) {
	hash := tx.Hash()
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	for _, events := range s.wsPendingTransactions {
		select {
		case events <- hash:
		default:
		}
	}
}

func (s *Server) writePendingTransactionNotifications(conn net.Conn, writeMu *sync.Mutex, id string, events <-chan string) {
	for hash := range events {
		writeWebSocketJSON(conn, writeMu, map[string]any{
			"jsonrpc": "2.0",
			"method":  "eth_subscription",
			"params": map[string]any{
				"subscription": id,
				"result":       hash,
			},
		})
	}
}
```

Update `unregisterWebSocketSubscription`:

```go
if s.unregisterPendingTransactionSubscription(id) {
	return true
}
```

- [ ] **Step 4: Wire `eth_subscribe("newPendingTransactions")`**

Add a case in `handleWebSocketJSONRPC`:

```go
case "newPendingTransactions":
	id, events := s.registerPendingTransactionSubscription()
	localSubscriptions[id] = struct{}{}
	go s.writePendingTransactionNotifications(conn, &writeMu, id, events)
	writeWebSocketRPCResponse(conn, &writeMu, rpcResponse{ID: request.ID, Result: id})
```

- [ ] **Step 5: Broadcast after successful txpool acceptance**

Call `s.notifyPendingTransaction(tx)` immediately after successful `SubmitTx` or `RequestFaucet` in these paths:

```text
POST /tx
POST /tx/raw
POST /faucet
POST /peer/tx
eth_sendRawTransaction
chain_faucet
chain_sendUserOperation
chain_sendTx
```

Keep the call after success and before response writing. Do not notify on validation errors.

- [ ] **Step 6: Run GREEN**

Run:

```powershell
go test -count=1 -run TestWebSocketEthSubscribeNewPendingTransactionsPublishesAcceptedTransactionHashes ./internal/rpc
```

Expected: pass.

- [ ] **Step 7: Run adjacent coverage**

Run:

```powershell
go test -count=1 -run "TestWebSocketEthSubscribe(NewPendingTransactionsPublishesAcceptedTransactionHashes|LogsPublishesMatchingContractLogs|NewHeadsPublishesProducedBlocks)|TestEthPendingTransactionFilterTracksNewHashes|TestRPCExposesTxPoolAndPendingNonce|TestRPCTransactionLookupIncludesPendingTransactions" ./internal/rpc
```

Expected: pass.

- [ ] **Step 8: Update docs**

Update:

```text
README.md
docs/current-blockchain-tech-roadmap.md
docs/superpowers/specs/2026-07-09-own-chain-design.md
```

Replace the pending-WebSocket future-work wording with current support for `newHeads`, filtered `logs`, and `newPendingTransactions`.

- [ ] **Step 9: Run full verification**

Run:

```powershell
go test -count=1 ./internal/rpc
go test -count=1 ./...
go run ./cmd/chainlab demo
go build -o $env:TEMP\chainlab-ws-pending-verify.exe ./cmd/chainlab
git diff --check
```

Expected: test, demo, and build commands exit 0. `git diff --check` may print existing LF/CRLF warnings, but must exit 0.

- [ ] **Step 10: Commit implementation**

Run:

```powershell
git add README.md docs/current-blockchain-tech-roadmap.md docs/superpowers/specs/2026-07-09-own-chain-design.md internal/rpc/server.go internal/rpc/server_test.go
git diff --cached --check
git commit -m "feat: add websocket pending transaction subscriptions"
```

Expected: one implementation commit.
