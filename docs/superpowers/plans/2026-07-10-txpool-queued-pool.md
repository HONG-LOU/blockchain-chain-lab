# Txpool Queued Pool Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a node-local queued txpool for nonce-gap transactions, with promotion to pending and RPC/CLI visibility.

**Architecture:** Keep pending transactions in `Node.mempool` and add `Node.queued`. Pending remains the only block-production pool. `SubmitTx` classifies future-nonce transactions into queued, replacement works in both pools, and promotion moves executable queued transactions into pending after pending state changes.

**Tech Stack:** Go, ChainLab `internal/node`, `internal/rpc`, `cmd/chainlab`, existing JSON-RPC and REST tests.

---

### Task 1: Node Queued Pool Behavior

**Files:**
- Modify: `internal/node/node.go`
- Modify: `internal/node/node_test.go`
- Modify: `internal/state/store.go`

- [ ] **Step 1: Write the failing node tests**

Add tests to `internal/node/node_test.go`:

```go
func TestNodeQueuesFutureNonceTransactionsAndPromotesWhenGapFills(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	carol := "0xcccccccccccccccccccccccccccccccccccccccc"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}

	future := signedNodeTx(t, key, types.Transaction{ChainID: "chainlab-local", Type: types.TxTransfer, From: alice, To: carol, Nonce: 1, Value: 20, GasLimit: 21_000, GasPrice: 1})
	if err := n.SubmitTx(future); err != nil {
		t.Fatal(err)
	}
	pool := n.TxPool()
	if pool.PendingCount != 0 || pool.QueuedCount != 1 || len(pool.Queued) != 1 || pool.Queued[0].Hash() != future.Hash() {
		t.Fatalf("queued pool = %+v", pool)
	}
	pending, err := n.PendingAccount(alice)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Nonce != 0 {
		t.Fatalf("pending nonce = %d", pending.Nonce)
	}

	first := signedNodeTx(t, key, types.Transaction{ChainID: "chainlab-local", Type: types.TxTransfer, From: alice, To: bob, Nonce: 0, Value: 10, GasLimit: 21_000, GasPrice: 1})
	if err := n.SubmitTx(first); err != nil {
		t.Fatal(err)
	}
	pool = n.TxPool()
	if pool.PendingCount != 2 || pool.QueuedCount != 0 {
		t.Fatalf("promoted pool = %+v", pool)
	}
	if pool.Pending[0].Hash() != first.Hash() || pool.Pending[1].Hash() != future.Hash() {
		t.Fatalf("pending order = %+v", pool.Pending)
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 2 || block.Transactions[0].Hash() != first.Hash() || block.Transactions[1].Hash() != future.Hash() {
		t.Fatalf("block txs = %+v", block.Transactions)
	}
}

func TestNodeReplacesQueuedTransactionWithHigherFeeSameNonce(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	carol := "0xcccccccccccccccccccccccccccccccccccccccc"
	n, err := node.New(node.Config{ChainID: "chainlab-local", ProposerKey: key, GenesisBalance: map[string]uint64{alice: 1_000_000}})
	if err != nil {
		t.Fatal(err)
	}

	original := signedNodeTx(t, key, types.Transaction{ChainID: "chainlab-local", Type: types.TxTransfer, From: alice, To: bob, Nonce: 2, Value: 10, GasLimit: 21_000, GasPrice: 10})
	underpriced := signedNodeTx(t, key, types.Transaction{ChainID: "chainlab-local", Type: types.TxTransfer, From: alice, To: carol, Nonce: 2, Value: 20, GasLimit: 21_000, GasPrice: 10})
	replacement := signedNodeTx(t, key, types.Transaction{ChainID: "chainlab-local", Type: types.TxTransfer, From: alice, To: carol, Nonce: 2, Value: 20, GasLimit: 21_000, GasPrice: 11})
	if err := n.SubmitTx(original); err != nil {
		t.Fatal(err)
	}
	if err := n.SubmitTx(underpriced); err == nil || !strings.Contains(err.Error(), "replacement transaction underpriced") {
		t.Fatalf("underpriced queued replacement error = %v", err)
	}
	if err := n.SubmitTx(replacement); err != nil {
		t.Fatal(err)
	}
	pool := n.TxPool()
	if pool.QueuedCount != 1 || pool.Queued[0].Hash() != replacement.Hash() {
		t.Fatalf("queued replacement pool = %+v", pool)
	}
}
```

- [ ] **Step 2: Run tests to verify RED**

Run:

```powershell
go test -count=1 -run "TestNode(QueuesFutureNonceTransactionsAndPromotesWhenGapFills|ReplacesQueuedTransactionWithHigherFeeSameNonce)" ./internal/node
```

Expected: fail because `MempoolSnapshot.Queued` does not exist and future nonce transactions are rejected.

- [ ] **Step 3: Implement node queued pool**

Add `queued []types.Transaction` to `Node`, add `Queued []types.Transaction` to `MempoolSnapshot`, add `SetNonce` to `state.Store`, update `submitTxLocked`, promotion, replacement, block production, import cleanup, and `TxPool`.

- [ ] **Step 4: Run node tests to verify GREEN**

Run:

```powershell
go test -count=1 -run "TestNode(QueuesFutureNonceTransactionsAndPromotesWhenGapFills|ReplacesQueuedTransactionWithHigherFeeSameNonce|PendingAccountIncludesMempoolTransactions|ReplacesPendingTransactionWithHigherFeeSameNonce|RejectsUnderpricedPendingReplacement|EIP1559ReplacementRequiresBothFeeCapsBumped)" ./internal/node
```

Expected: pass.

### Task 2: RPC And Filter Visibility

**Files:**
- Modify: `internal/rpc/server.go`
- Modify: `internal/rpc/filters.go`
- Modify: `internal/rpc/server_test.go`

- [ ] **Step 1: Write failing RPC test**

Add `TestRPCExposesQueuedTxPoolAndPromotesFutureNonceTransactions` to `internal/rpc/server_test.go`. It should submit nonce 1 first, verify `txpool_status.queued == "0x1"`, verify `txpool_content.queued[from]["0x1"]`, verify `eth_getTransactionByHash` returns the queued hash with null block fields, submit nonce 0, then verify both hashes are pending and queued is empty.

- [ ] **Step 2: Run RPC test to verify RED**

Run:

```powershell
go test -count=1 -run TestRPCExposesQueuedTxPoolAndPromotesFutureNonceTransactions ./internal/rpc
```

Expected: fail because queued content and lookup are not exposed.

- [ ] **Step 3: Implement RPC queued projection**

Update `pendingTransactionByHash` to search `pool.Queued`. Update `evmTxPool` to build both pending and queued maps. Update pending filter registration and changes to keep filtering only `pool.Pending`.

- [ ] **Step 4: Run focused RPC tests**

Run:

```powershell
go test -count=1 -run "TestRPC(ExposesQueuedTxPoolAndPromotesFutureNonceTransactions|ExposesTxPoolAndPendingNonce|SendReplacementTransactionUpdatesTxPool|TransactionLookupIncludesPendingTransactions)|TestEthPendingTransactionFilterTracksNewHashes" ./internal/rpc
```

Expected: pass.

### Task 3: CLI And Documentation

**Files:**
- Modify: `cmd/chainlab/main_test.go`
- Modify: `README.md`
- Modify: `docs/current-blockchain-tech-roadmap.md`
- Modify: `docs/superpowers/specs/2026-07-09-own-chain-design.md`

- [ ] **Step 1: Extend CLI mempool test**

Update the existing mempool CLI test to decode `queued_count` and `queued`, then assert queued transactions appear when the REST `/txpool` response includes them.

- [ ] **Step 2: Run CLI test**

Run:

```powershell
go test -count=1 -run TestTransferCommandUsesPendingNonceAndQueryMempool ./cmd/chainlab
```

Expected before implementation: fail if the snapshot type lacks queued fields. Expected after implementation: pass.

- [ ] **Step 3: Update documentation**

Change docs to state that ChainLab has a node-local pending/queued txpool with automatic nonce-gap promotion and same-nonce replacement in both pools. Keep explicit future-work wording for capacity limits, eviction, journaling, local exemptions, and MEV-aware ordering.

- [ ] **Step 4: Run full verification**

Run:

```powershell
go test -count=1 ./internal/node
go test -count=1 ./internal/rpc
go test -count=1 ./cmd/chainlab
go test -count=1 ./...
go run ./cmd/chainlab demo
go build -o $env:TEMP\chainlab-queued-txpool-verify.exe ./cmd/chainlab
git diff --check
```

Expected: all commands exit 0.
