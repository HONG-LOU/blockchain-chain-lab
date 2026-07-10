# EIP-7702-Style Delegated EOA Implementation Plan

> Historical scope note: session keys and social recovery were added in later milestones; the non-goals below describe only this plan's original implementation boundary.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a ChainLab-native EIP-7702-style delegated EOA flow where an EOA can set `account.v1` delegation, keep its own balance/nonce, and authorize transfers or batches through the delegated owner.

**Architecture:** Add delegation as first-class consensus account state, separate from contract `CodeID`. Add a `set_code` transaction in the executor, reuse existing `account.v1` owner authorization for delegated EOAs, and expose the new state through RPC, CLI, explorer, and docs.

**Tech Stack:** Go, ChainLab `internal/types`, `internal/state`, `internal/core`, `internal/node`, `internal/rpc`, `cmd/chainlab`, existing JSON serialization and Go test suite.

---

## File Structure

- Modify `internal/types/types.go`: add `TxSetCode` and `Account.DelegatedCodeID`.
- Modify `internal/state/store.go` and `internal/state/store_test.go`: persist/clone/root delegation state and add helpers.
- Modify `internal/core/executor.go` and `internal/core/executor_test.go`: execute `set_code` and authorize delegated EOA transactions.
- Modify `internal/node/node_test.go`: prove delegated state survives node snapshot reload.
- Modify `internal/rpc/server.go`, `internal/rpc/explorer.go`, and `internal/rpc/server_test.go`: expose delegated account state, `eth_getCode`, transaction projections, and explorer account page.
- Modify `cmd/chainlab/main.go` and `cmd/chainlab/main_test.go`: add `tx set-code` and real-node CLI E2E coverage.
- Modify `README.md`, `docs/current-blockchain-tech-roadmap.md`, and `docs/superpowers/specs/2026-07-09-own-chain-design.md`: document the supported delegated EOA slice and explicit non-goals.

## Task 1: Types And State Delegation

**Files:**
- Modify: `internal/types/types.go`
- Modify: `internal/state/store.go`
- Modify: `internal/state/store_test.go`

- [ ] **Step 1: Write the failing state test**

Add this test to `internal/state/store_test.go`:

```go
func TestDelegatedCodeIDPersistsThroughCloneSnapshotAndRoot(t *testing.T) {
	store := state.NewStore()
	alice := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store.SetBalance(alice, 100)
	store.SetDelegatedCodeID(alice, "account.v1")
	store.SetStorage(alice, "owner", "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	root := store.Root()

	clone := store.Clone()
	if got := clone.GetAccount(alice).DelegatedCodeID; got != "account.v1" {
		t.Fatalf("clone delegated code id = %q", got)
	}
	if got := clone.GetStorage(alice, "owner"); got != "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("clone owner = %q", got)
	}

	restored := state.NewStoreFromSnapshot(store.Snapshot())
	if got := restored.GetAccount(alice).DelegatedCodeID; got != "account.v1" {
		t.Fatalf("snapshot delegated code id = %q", got)
	}
	if restored.Root() != root {
		t.Fatalf("restored root = %s, want %s", restored.Root(), root)
	}

	restored.ClearDelegation(alice)
	if got := restored.GetAccount(alice).DelegatedCodeID; got != "" {
		t.Fatalf("cleared delegated code id = %q", got)
	}
	if got := restored.GetStorage(alice, "owner"); got != "" {
		t.Fatalf("cleared owner = %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify RED**

Run:

```powershell
go test -count=1 -run TestDelegatedCodeIDPersistsThroughCloneSnapshotAndRoot ./internal/state
```

Expected: fail because `Account.DelegatedCodeID`, `SetDelegatedCodeID`, and `ClearDelegation` do not exist.

- [ ] **Step 3: Implement state support**

Update `types.Account`:

```go
DelegatedCodeID string `json:"delegated_code_id,omitempty"`
```

Add state helpers:

```go
func (s *Store) SetDelegatedCodeID(address string, codeID string) {
	account := s.account(address)
	account.DelegatedCodeID = strings.TrimSpace(codeID)
	s.accounts[normalize(address)] = account
}

func (s *Store) ClearDelegation(address string) {
	account := s.account(address)
	account.DelegatedCodeID = ""
	if account.Storage != nil {
		delete(account.Storage, "owner")
	}
	s.accounts[normalize(address)] = account
}
```

Keep existing clone, snapshot, and root code using `types.Account`; the new field is included automatically by canonical JSON.

- [ ] **Step 4: Run state tests**

Run:

```powershell
go test -count=1 ./internal/state
```

Expected: pass.

## Task 2: Executor `set_code` And Delegated Authorization

**Files:**
- Modify: `internal/types/types.go`
- Modify: `internal/core/executor.go`
- Modify: `internal/core/executor_test.go`

- [ ] **Step 1: Write failing executor tests**

Add these tests to `internal/core/executor_test.go`:

```go
func TestSetCodeDelegatesEOAToAccountOwnerAndOwnerCanTransfer(t *testing.T) {
	eoaKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	eoa := chaincrypto.AddressFromPrivateKey(eoaKey)
	owner := chaincrypto.AddressFromPrivateKey(ownerKey)
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store := state.NewStore()
	store.SetBalance(eoa, 100_000)
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", contracts.NewRuntimeWithDefaults())

	setCode := signedTx(t, eoaKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxSetCode,
		From:     eoa,
		Nonce:    0,
		GasLimit: 45_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": contracts.AccountCodeID,
			"owner":   owner,
		},
	})
	receipt, err := executor.Execute(store, setCode)
	if err != nil {
		t.Fatal(err)
	}
	if store.GetAccount(eoa).DelegatedCodeID != contracts.AccountCodeID {
		t.Fatalf("delegation = %+v", store.GetAccount(eoa))
	}
	if store.GetStorage(eoa, "owner") != owner {
		t.Fatalf("owner = %q", store.GetStorage(eoa, "owner"))
	}
	if len(receipt.Events) == 0 || receipt.Events[0].Type != "account.delegation_set" {
		t.Fatalf("set-code events = %#v", receipt.Events)
	}

	transfer := signedTx(t, ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     eoa,
		Signer:   owner,
		To:       receiver,
		Nonce:    1,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	transferReceipt, err := executor.Execute(store, transfer)
	if err != nil {
		t.Fatal(err)
	}
	if !transferReceipt.Success {
		t.Fatalf("transfer receipt = %+v", transferReceipt)
	}
	if got := store.GetAccount(eoa).Nonce; got != 2 {
		t.Fatalf("eoa nonce = %d", got)
	}
	if got := store.GetAccount(receiver).Balance; got != 100 {
		t.Fatalf("receiver balance = %d", got)
	}
	if got := store.GetAccount(owner).Nonce; got != 0 {
		t.Fatalf("owner nonce = %d", got)
	}
}

func TestDelegatedEOARejectsUnauthorizedSignerAndClearDisablesDelegation(t *testing.T) {
	eoaKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	intruderKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	eoa := chaincrypto.AddressFromPrivateKey(eoaKey)
	owner := chaincrypto.AddressFromPrivateKey(ownerKey)
	intruder := chaincrypto.AddressFromPrivateKey(intruderKey)
	store := state.NewStore()
	store.SetBalance(eoa, 200_000)
	store.SetDelegatedCodeID(eoa, contracts.AccountCodeID)
	store.SetStorage(eoa, "owner", owner)
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", contracts.NewRuntimeWithDefaults())

	unauthorized := signedTx(t, intruderKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     eoa,
		Signer:   intruder,
		To:       "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Nonce:    0,
		Value:    10,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if _, err := executor.Execute(store, unauthorized); err == nil {
		t.Fatal("unauthorized delegated signer should fail")
	}
	if got := store.GetAccount(eoa).Nonce; got != 0 {
		t.Fatalf("nonce after unauthorized tx = %d", got)
	}

	clear := signedTx(t, eoaKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxSetCode,
		From:     eoa,
		Nonce:    0,
		GasLimit: 45_000,
		GasPrice: 1,
		Payload:  map[string]string{"code_id": ""},
	})
	if _, err := executor.Execute(store, clear); err != nil {
		t.Fatal(err)
	}
	if got := store.GetAccount(eoa).DelegatedCodeID; got != "" {
		t.Fatalf("delegation after clear = %q", got)
	}

	afterClear := signedTx(t, ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     eoa,
		Signer:   owner,
		To:       "0xcccccccccccccccccccccccccccccccccccccccc",
		Nonce:    1,
		Value:    10,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if _, err := executor.Execute(store, afterClear); err == nil {
		t.Fatal("owner signer should fail after delegation is cleared")
	}
}
```

- [ ] **Step 2: Run tests to verify RED**

Run:

```powershell
go test -count=1 -run "Test(SetCodeDelegatesEOAToAccountOwnerAndOwnerCanTransfer|DelegatedEOARejectsUnauthorizedSignerAndClearDisablesDelegation)" ./internal/core
```

Expected: fail because `TxSetCode` and delegated authorization are not implemented.

- [ ] **Step 3: Implement executor support**

Add transaction type:

```go
TxSetCode TxType = "set_code"
```

Add gas estimate:

```go
case types.TxSetCode:
	return 45_000, nil
```

Add executor case:

```go
case types.TxSetCode:
	if err := executeSetCode(working, tx); err != nil {
		return types.Receipt{}, err
	}
	eventType := "account.delegation_set"
	attributes := map[string]string{"account": strings.ToLower(tx.From)}
	if strings.TrimSpace(tx.Payload["code_id"]) == "" {
		eventType = "account.delegation_cleared"
	} else {
		attributes["code_id"] = strings.TrimSpace(tx.Payload["code_id"])
		attributes["owner"] = strings.ToLower(strings.TrimSpace(tx.Payload["owner"]))
	}
	receipt.Events = append(receipt.Events, types.Event{Type: eventType, Attributes: attributes})
```

Add helper:

```go
func executeSetCode(store *state.Store, tx types.Transaction) error {
	if strings.TrimSpace(tx.Signer) != "" || len(tx.Authorizations) > 0 || tx.SignatureKind != "" {
		return errors.New("set_code must be signed directly by from")
	}
	if !chaincrypto.Verify(tx.From, tx.SigningBytes(), tx.Signature) {
		return errors.New("invalid transaction signature")
	}
	account := store.GetAccount(tx.From)
	if strings.TrimSpace(account.CodeID) != "" {
		return errors.New("set_code cannot target a contract account")
	}
	codeID := strings.TrimSpace(tx.Payload["code_id"])
	if codeID == "" {
		store.ClearDelegation(tx.From)
		return nil
	}
	if codeID != contracts.AccountCodeID {
		return fmt.Errorf("unsupported delegated code id %q", codeID)
	}
	owner := strings.ToLower(strings.TrimSpace(tx.Payload["owner"]))
	if owner == "" {
		return errors.New("set_code account.v1 requires owner")
	}
	store.SetDelegatedCodeID(tx.From, codeID)
	store.SetStorage(tx.From, "owner", owner)
	return nil
}
```

Update `validateTransactionAuthorization` before smart-account signer rejection:

```go
if account.DelegatedCodeID == contracts.AccountCodeID && account.CodeID == "" {
	owner := strings.ToLower(strings.TrimSpace(store.GetStorage(tx.From, "owner")))
	if owner == "" {
		return errors.New("delegated account owner is not set")
	}
	if signer != owner {
		return errors.New("transaction signer is not delegated account owner")
	}
	return nil
}
```

Ensure `TxSetCode` direct signature is not verified twice by returning early in `validateTransactionAuthorization`:

```go
if tx.Type == types.TxSetCode {
	if strings.TrimSpace(tx.Signer) != "" || len(tx.Authorizations) > 0 || tx.SignatureKind != "" {
		return errors.New("set_code must be signed directly by from")
	}
	if !chaincrypto.Verify(tx.From, tx.SigningBytes(), tx.Signature) {
		return errors.New("invalid transaction signature")
	}
	return nil
}
```

- [ ] **Step 4: Run core tests**

Run:

```powershell
go test -count=1 ./internal/core
```

Expected: pass.

## Task 3: Node, RPC, Explorer Visibility

**Files:**
- Modify: `internal/node/node_test.go`
- Modify: `internal/rpc/server.go`
- Modify: `internal/rpc/explorer.go`
- Modify: `internal/rpc/server_test.go`

- [ ] **Step 1: Write failing RPC/node tests**

Add an RPC test that submits `set_code`, produces a block, checks account JSON, `eth_getCode`, and explorer account HTML:

```go
func TestRPCAndExplorerExposeDelegatedEOA(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	owner := chaincrypto.AddressFromPrivateKey(ownerKey)
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(rpc.NewServer(n))
	defer server.Close()

	tx := signedTransfer(t, key, alice, "", 0, 0)
	tx.Type = types.TxSetCode
	tx.GasLimit = 45_000
	tx.Payload = map[string]string{"code_id": contracts.AccountCodeID, "owner": owner}
	raw, err := json.Marshal(tx)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(server.URL+"/tx", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("set-code status = %d", resp.StatusCode)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	accountResp, err := http.Get(server.URL + "/account/" + alice)
	if err != nil {
		t.Fatal(err)
	}
	defer accountResp.Body.Close()
	var account types.Account
	if err := json.NewDecoder(accountResp.Body).Decode(&account); err != nil {
		t.Fatal(err)
	}
	if account.DelegatedCodeID != contracts.AccountCodeID {
		t.Fatalf("account delegation = %+v", account)
	}

	code := callRPC(t, server.URL, "eth_getCode", []any{alice, "latest"})
	wantCode := "0x" + hex.EncodeToString([]byte(contracts.AccountCodeID))
	if code != wantCode {
		t.Fatalf("delegated eth_getCode = %#v", code)
	}

	explorerResp, err := http.Get(server.URL + "/explorer/account/" + alice)
	if err != nil {
		t.Fatal(err)
	}
	defer explorerResp.Body.Close()
	body, _ := io.ReadAll(explorerResp.Body)
	if !strings.Contains(string(body), "Delegated Code") || !strings.Contains(string(body), contracts.AccountCodeID) {
		t.Fatalf("explorer account page missing delegation: %s", string(body))
	}
}
```

- [ ] **Step 2: Run tests to verify RED**

Run:

```powershell
go test -count=1 -run TestRPCAndExplorerExposeDelegatedEOA ./internal/rpc
```

Expected: fail because delegation is not exposed and `eth_getCode` still only uses `CodeID`.

- [ ] **Step 3: Implement visibility**

Update `eth_getCode` account lookup:

```go
codeID := account.CodeID
if codeID == "" {
	codeID = account.DelegatedCodeID
}
return codeIDHex(codeID)
```

Keep transaction projections unchanged in this slice. Delegation is account state, not transaction state, and account reads plus explorer account pages are the authoritative visibility path.

Update explorer account data/template to render:

```html
<dt>Delegated Code</dt><dd>{{if .Account.DelegatedCodeID}}{{.Account.DelegatedCodeID}}{{else}}-{{end}}</dd>
```

- [ ] **Step 4: Run RPC tests**

Run:

```powershell
go test -count=1 ./internal/rpc
```

Expected: pass.

## Task 4: CLI And Documentation

**Files:**
- Modify: `cmd/chainlab/main.go`
- Modify: `cmd/chainlab/main_test.go`
- Modify: `README.md`
- Modify: `docs/current-blockchain-tech-roadmap.md`
- Modify: `docs/superpowers/specs/2026-07-09-own-chain-design.md`

- [ ] **Step 1: Write failing CLI test**

Add a CLI E2E test that starts an in-memory test server, runs `tx set-code`, produces a block, then uses owner-key `tx transfer --from <delegated-eoa>` and verifies balances.

Use this assertion shape:

```go
if account.DelegatedCodeID != contracts.AccountCodeID {
	t.Fatalf("delegated account = %+v", account)
}
if receiverBalance != 100 {
	t.Fatalf("receiver balance = %d", receiverBalance)
}
```

- [ ] **Step 2: Run CLI test to verify RED**

Run:

```powershell
go test -count=1 -run TestSetCodeCommandDelegatesEOAAndOwnerTransfer ./cmd/chainlab
```

Expected: fail because `tx set-code` is not registered.

- [ ] **Step 3: Implement CLI command**

Update usage:

```go
usage: chainlab tx <transfer|batch-transfer|set-code|deploy|call|wasm-upload|stake|proposal-submit|vote|proposal-execute|validator-join|validator-leave|validator-slash|raw-submit>
```

Add switch case:

```go
case "set-code":
	if err := setCodeCommand(args[1:], out); err != nil {
		log.Fatal(err)
	}
```

Implement:

```go
func setCodeCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("tx set-code", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	privateKeyHex := flags.String("private-key", "", "EOA private key")
	codeID := flags.String("code-id", contracts.AccountCodeID, "delegated code id")
	owner := flags.String("owner", "", "delegated owner address")
	clear := flags.Bool("clear", false, "clear delegated code")
	gasLimit := flags.Uint64("gas-limit", 45_000, "gas limit")
	gasPrice := flags.Uint64("gas-price", 1, "gas price")
	if err := flags.Parse(args); err != nil {
		return err
	}
	payload := map[string]string{}
	if *clear {
		payload["code_id"] = ""
	} else {
		if strings.TrimSpace(*owner) == "" {
			return fmt.Errorf("owner is required")
		}
		payload["code_id"] = *codeID
		payload["owner"] = *owner
	}
	tx, err := buildSignedTransaction(*rpcURL, *privateKeyHex, types.TxSetCode, "", 0, *gasLimit, *gasPrice, payload)
	if err != nil {
		return err
	}
	response, err := submitTransaction(*rpcURL, tx)
	if err != nil {
		return err
	}
	return writeTo(out, response)
}
```

- [ ] **Step 4: Update docs**

Document:

- `set_code` in implemented feature list.
- CLI `tx set-code` examples.
- `Account.DelegatedCodeID` and `eth_getCode` delegated behavior.
- Explicit non-goals: no full Ethereum type-4 raw transaction, no ERC-4337 EntryPoint, no session keys/social recovery yet.

- [ ] **Step 5: Run full verification**

Run:

```powershell
go test -count=1 ./internal/state
go test -count=1 ./internal/core
go test -count=1 ./internal/node
go test -count=1 ./internal/rpc
go test -count=1 ./cmd/chainlab
go test -count=1 ./...
go run ./cmd/chainlab demo
go build -o $env:TEMP\chainlab-eip7702-delegated-eoa-verify.exe ./cmd/chainlab
git diff --check
git diff --cached --check
```

Expected: all commands exit 0.

## Self-Review

- The plan covers all requirements from `2026-07-10-eip7702-style-delegated-eoa-design.md`.
- The scope stays on ChainLab-native delegated EOA semantics and does not require full Ethereum type-4 raw transactions.
- The TDD order is explicit for state, executor, RPC/explorer, and CLI.
- The documentation task keeps the non-goals visible so the new feature is not overclaimed.
