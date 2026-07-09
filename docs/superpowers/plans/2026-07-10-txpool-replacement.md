# Txpool Replacement Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Geth-style same-sender/same-nonce pending transaction replacement with a fixed 10 percent fee bump.

**Architecture:** Keep ChainLab's single pending mempool. Detect a replacement inside `Node.submitTxLocked`, validate by replaying the mempool in order with the new transaction substituted at the matched index, then replace the old pending transaction in place. Existing RPC/filter/WebSocket paths continue to notify and expose only transactions accepted by the node.

**Tech Stack:** Go, ChainLab node/core/types/rpc packages, `go test`, native JSON-RPC tests.

---

### Task 1: Node RED Tests

**Files:**
- Modify: `internal/node/node_test.go`

- [ ] **Step 1: Add failing tests for replacement behavior**

Insert these tests after `TestNodePendingAccountIncludesMempoolTransactions`:

```go
func TestNodeReplacesPendingTransactionWithHigherFeeSameNonce(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	carol := "0xcccccccccccccccccccccccccccccccccccccccc"
	dave := "0xdddddddddddddddddddddddddddddddddddddddd"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}

	original := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       bob,
		Nonce:    0,
		Value:    10,
		GasLimit: 21_000,
		GasPrice: 10,
	})
	next := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       dave,
		Nonce:    1,
		Value:    5,
		GasLimit: 21_000,
		GasPrice: 10,
	})
	replacement := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       carol,
		Nonce:    0,
		Value:    20,
		GasLimit: 21_000,
		GasPrice: 11,
	})

	if err := n.SubmitTx(original); err != nil {
		t.Fatal(err)
	}
	if err := n.SubmitTx(next); err != nil {
		t.Fatal(err)
	}
	if err := n.SubmitTx(replacement); err != nil {
		t.Fatal(err)
	}

	pool := n.Mempool()
	if len(pool) != 2 {
		t.Fatalf("mempool = %#v", pool)
	}
	if pool[0].Hash() != replacement.Hash() || pool[1].Hash() != next.Hash() {
		t.Fatalf("mempool order = %#v", pool)
	}
	pending, err := n.PendingAccount(alice)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Nonce != 2 {
		t.Fatalf("pending nonce = %d", pending.Nonce)
	}

	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 2 || block.Transactions[0].Hash() != replacement.Hash() || block.Transactions[1].Hash() != next.Hash() {
		t.Fatalf("block transactions = %#v", block.Transactions)
	}
	if got := n.Account(bob).Balance; got != 0 {
		t.Fatalf("bob balance = %d", got)
	}
	if got := n.Account(carol).Balance; got != 20 {
		t.Fatalf("carol balance = %d", got)
	}
	if got := n.Account(dave).Balance; got != 5 {
		t.Fatalf("dave balance = %d", got)
	}
}

func TestNodeRejectsUnderpricedPendingReplacement(t *testing.T) {
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

	original := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       bob,
		Nonce:    0,
		Value:    10,
		GasLimit: 21_000,
		GasPrice: 10,
	})
	underpriced := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       carol,
		Nonce:    0,
		Value:    20,
		GasLimit: 21_000,
		GasPrice: 10,
	})

	if err := n.SubmitTx(original); err != nil {
		t.Fatal(err)
	}
	err = n.SubmitTx(underpriced)
	if err == nil || !strings.Contains(err.Error(), "replacement transaction underpriced") {
		t.Fatalf("underpriced replacement error = %v", err)
	}
	pool := n.Mempool()
	if len(pool) != 1 || pool[0].Hash() != original.Hash() {
		t.Fatalf("mempool = %#v", pool)
	}
}

func TestNodeEIP1559ReplacementRequiresBothFeeCapsBumped(t *testing.T) {
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

	original := signedNodeTx(t, key, types.Transaction{
		ChainID:              "chainlab-local",
		Type:                 types.TxTransfer,
		From:                 alice,
		To:                   bob,
		Nonce:                0,
		Value:                10,
		GasLimit:             21_000,
		MaxFeePerGas:         20,
		MaxPriorityFeePerGas: 10,
	})
	lowTip := signedNodeTx(t, key, types.Transaction{
		ChainID:              "chainlab-local",
		Type:                 types.TxTransfer,
		From:                 alice,
		To:                   carol,
		Nonce:                0,
		Value:                20,
		GasLimit:             21_000,
		MaxFeePerGas:         22,
		MaxPriorityFeePerGas: 10,
	})
	bumped := signedNodeTx(t, key, types.Transaction{
		ChainID:              "chainlab-local",
		Type:                 types.TxTransfer,
		From:                 alice,
		To:                   carol,
		Nonce:                0,
		Value:                20,
		GasLimit:             21_000,
		MaxFeePerGas:         22,
		MaxPriorityFeePerGas: 11,
	})

	if err := n.SubmitTx(original); err != nil {
		t.Fatal(err)
	}
	err = n.SubmitTx(lowTip)
	if err == nil || !strings.Contains(err.Error(), "replacement transaction underpriced") {
		t.Fatalf("low tip replacement error = %v", err)
	}
	if err := n.SubmitTx(bumped); err != nil {
		t.Fatal(err)
	}
	pool := n.Mempool()
	if len(pool) != 1 || pool[0].Hash() != bumped.Hash() {
		t.Fatalf("mempool = %#v", pool)
	}
}
```

- [ ] **Step 2: Run RED node tests**

Run:

```powershell
go test -count=1 -run "TestNode(ReplacesPendingTransactionWithHigherFeeSameNonce|RejectsUnderpricedPendingReplacement|EIP1559ReplacementRequiresBothFeeCapsBumped)" ./internal/node
```

Expected: fail. The first test should fail with a nonce-related error from submitting the replacement, and the underpriced tests should fail because the node does not yet return `replacement transaction underpriced`.

### Task 2: Node Replacement Policy

**Files:**
- Modify: `internal/node/node.go`

- [ ] **Step 1: Add replacement constants and submit path**

Add near the fee market constants:

```go
	txpoolReplacementPriceBumpPercent uint64 = 10
```

Add near `checkedAdd` or the submit helpers:

```go
var errReplacementTransactionUnderpriced = errors.New("replacement transaction underpriced")
```

Replace `submitTxLocked` with:

```go
func (n *Node) submitTxLocked(tx types.Transaction) error {
	replacementIndex := n.mempoolReplacementIndex(tx)
	if replacementIndex >= 0 {
		if err := canReplacePendingTransaction(n.mempool[replacementIndex], tx); err != nil {
			return err
		}
		if err := n.validateMempoolWithReplacementLocked(replacementIndex, tx); err != nil {
			return err
		}
		n.mempool[replacementIndex] = tx
		return nil
	}

	working, err := n.pendingStateLocked()
	if err != nil {
		return err
	}
	blockHeight := n.blocks[len(n.blocks)-1].Header.Height + 1
	baseFee := n.nextBaseFeeLocked()
	if _, err := n.executor.ExecuteWithContext(working, tx, core.ExecutionContext{BlockHeight: blockHeight, BaseFeePerGas: baseFee}); err != nil {
		return err
	}
	n.mempool = append(n.mempool, tx)
	return nil
}
```

- [ ] **Step 2: Add replacement validation helpers**

Add below `pendingStateLocked`:

```go
func (n *Node) mempoolReplacementIndex(tx types.Transaction) int {
	from := normalizedAddress(tx.From)
	if from == "" {
		return -1
	}
	for i, pending := range n.mempool {
		if normalizedAddress(pending.From) == from && pending.Nonce == tx.Nonce {
			return i
		}
	}
	return -1
}

func (n *Node) validateMempoolWithReplacementLocked(index int, replacement types.Transaction) error {
	working := n.state.Clone()
	blockHeight := n.blocks[len(n.blocks)-1].Header.Height + 1
	baseFee := n.nextBaseFeeLocked()
	for i, pending := range n.mempool {
		candidate := pending
		if i == index {
			candidate = replacement
		}
		if _, err := n.executor.ExecuteWithContext(working, candidate, core.ExecutionContext{BlockHeight: blockHeight, BaseFeePerGas: baseFee}); err != nil {
			return err
		}
	}
	return nil
}

func normalizedAddress(address string) string {
	return strings.ToLower(strings.TrimSpace(address))
}
```

Add below `checkedAdd`:

```go
func canReplacePendingTransaction(oldTx types.Transaction, newTx types.Transaction) error {
	oldMaxFee, oldPriorityFee := replacementFeeCaps(oldTx)
	newMaxFee, newPriorityFee := replacementFeeCaps(newTx)
	if !feeBumpedByPercent(oldMaxFee, newMaxFee, txpoolReplacementPriceBumpPercent) ||
		!feeBumpedByPercent(oldPriorityFee, newPriorityFee, txpoolReplacementPriceBumpPercent) {
		return errReplacementTransactionUnderpriced
	}
	return nil
}

func replacementFeeCaps(tx types.Transaction) (uint64, uint64) {
	if tx.MaxFeePerGas > 0 || tx.MaxPriorityFeePerGas > 0 {
		return tx.MaxFeePerGas, tx.MaxPriorityFeePerGas
	}
	return tx.GasPrice, tx.GasPrice
}

func feeBumpedByPercent(oldFee uint64, newFee uint64, percent uint64) bool {
	required, ok := bumpedFeeThreshold(oldFee, percent)
	return ok && newFee >= required
}

func bumpedFeeThreshold(oldFee uint64, percent uint64) (uint64, bool) {
	if oldFee == 0 {
		return 0, true
	}
	whole := oldFee / 100
	remainder := oldFee % 100
	if percent != 0 && whole > math.MaxUint64/percent {
		return 0, false
	}
	bump := whole * percent
	remainderProduct := remainder * percent
	if math.MaxUint64-bump < remainderProduct/100 {
		return 0, false
	}
	bump += remainderProduct / 100
	if remainderProduct%100 != 0 {
		bump++
	}
	if bump == 0 {
		bump = 1
	}
	if math.MaxUint64-oldFee < bump {
		return 0, false
	}
	return oldFee + bump, true
}
```

- [ ] **Step 3: Run GREEN node tests**

Run:

```powershell
go test -count=1 -run "TestNode(ReplacesPendingTransactionWithHigherFeeSameNonce|RejectsUnderpricedPendingReplacement|EIP1559ReplacementRequiresBothFeeCapsBumped|PendingAccountIncludesMempoolTransactions)" ./internal/node
```

Expected: pass.

### Task 3: RPC RED and GREEN Coverage

**Files:**
- Modify: `internal/rpc/server_test.go`

- [ ] **Step 1: Add failing RPC test**

Insert after `TestRPCTransactionLookupIncludesPendingTransactions`:

```go
func TestRPCSendReplacementTransactionUpdatesTxPool(t *testing.T) {
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
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	original := signedTransfer(t, key, alice, bob, 0, 100)
	original.GasPrice = 10
	original = signedRPCTransaction(t, key, original)
	replacement := signedTransfer(t, key, alice, carol, 0, 200)
	replacement.GasPrice = 11
	replacement = signedRPCTransaction(t, key, replacement)

	body, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(server.URL+"/tx", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("original status = %d", resp.StatusCode)
	}
	filterID := callRPC(t, server.URL, "eth_newPendingTransactionFilter", []any{})

	body, err = json.Marshal(replacement)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.Post(server.URL+"/tx", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("replacement status = %d", resp.StatusCode)
	}
	var accepted struct {
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.Hash != replacement.Hash() {
		t.Fatalf("replacement hash = %s, want %s", accepted.Hash, replacement.Hash())
	}

	content := callRPC(t, server.URL, "txpool_content", []any{})
	contentMap, ok := content.(map[string]any)
	if !ok {
		t.Fatalf("txpool_content = %#v", content)
	}
	pending := contentMap["pending"].(map[string]any)
	byNonce := pending[strings.ToLower(alice)].(map[string]any)
	txMap := byNonce["0x0"].(map[string]any)
	if txMap["hash"] != replacement.Hash() || txMap["to"] != carol || txMap["value"] != "0xc8" {
		t.Fatalf("replacement txpool entry = %#v", txMap)
	}
	if oldPending := callRPC(t, server.URL, "eth_getTransactionByHash", []any{original.Hash()}); oldPending != nil {
		t.Fatalf("old pending lookup = %#v", oldPending)
	}
	newPending := callRPC(t, server.URL, "eth_getTransactionByHash", []any{replacement.Hash()})
	newPendingMap, ok := newPending.(map[string]any)
	if !ok || newPendingMap["hash"] != replacement.Hash() || newPendingMap["blockHash"] != nil {
		t.Fatalf("new pending lookup = %#v", newPending)
	}
	changes := callRPC(t, server.URL, "eth_getFilterChanges", []any{filterID})
	hashes, ok := changes.([]any)
	if !ok || len(hashes) != 1 || hashes[0] != replacement.Hash() {
		t.Fatalf("pending filter changes = %#v", changes)
	}
}
```

Add this helper near `signedTransfer` if no equivalent exists:

```go
func signedRPCTransaction(t *testing.T, key chaincrypto.PrivateKey, tx types.Transaction) types.Transaction {
	t.Helper()
	tx.Signature = ""
	signature, err := chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = signature
	return tx
}
```

- [ ] **Step 2: Run RED RPC test before node implementation if Task 2 has not run**

Run:

```powershell
go test -count=1 -run TestRPCSendReplacementTransactionUpdatesTxPool ./internal/rpc
```

Expected before Task 2: fail because the replacement is rejected. Expected after Task 2: pass.

- [ ] **Step 3: Run adjacent RPC tests**

Run:

```powershell
go test -count=1 -run "TestRPC(SendReplacementTransactionUpdatesTxPool|ExposesTxPoolAndPendingNonce|TransactionLookupIncludesPendingTransactions|SendRawTransactionSubmitsSignedTx)|TestEthPendingTransactionFilterTracksNewHashes|TestWebSocketEthSubscribeNewPendingTransactionsPublishesAcceptedTransactionHashes" ./internal/rpc
```

Expected: pass.

### Task 4: Documentation and Final Verification

**Files:**
- Modify: `README.md`
- Modify: `docs/current-blockchain-tech-roadmap.md`
- Modify: `docs/superpowers/specs/2026-07-09-own-chain-design.md`

- [ ] **Step 1: Update docs**

Document that ChainLab now supports 10 percent same-sender/same-nonce pending tx replacement, and that queued txpool, eviction, journaling, and local transaction exemptions remain unsupported.

- [ ] **Step 2: Run full verification**

Run:

```powershell
go test -count=1 ./internal/node
go test -count=1 ./internal/rpc
go test -count=1 ./cmd/chainlab
go test -count=1 ./...
go run ./cmd/chainlab demo
go build -o $env:TEMP\chainlab-txpool-replacement-verify.exe ./cmd/chainlab
git diff --check
```

Expected: all commands exit 0. `git diff --check` may print LF/CRLF warnings but must exit 0.

- [ ] **Step 3: Commit implementation**

Run:

```powershell
git add internal/node/node.go internal/node/node_test.go internal/rpc/server_test.go README.md docs/current-blockchain-tech-roadmap.md docs/superpowers/specs/2026-07-09-own-chain-design.md
git diff --cached --check
git commit -m "feat: add txpool replacement policy"
```

Expected: commit succeeds.

## Self-Review

- Spec coverage: node replacement, fee bump, replacement validation, RPC visibility, pending filters, documentation, and final verification are all covered.
- Blank-field scan: this plan contains no incomplete work items.
- Type consistency: the plan uses existing `types.Transaction`, `node.Node`, `chaincrypto.Sign`, `Mempool`, `TxPool`, and RPC helper names already present in the repository.
