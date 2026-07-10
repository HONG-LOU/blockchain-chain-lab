# Session Key Contract Calls Implementation Plan

> Historical scope note: social recovery was implemented in the later `account.recovery` milestone; references below describe this plan's original scope.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add ChainLab-native contract-call session keys for `account.v1` accounts and delegated EOAs.

**Architecture:** Extend the existing session-key policy stored under `session:<key>:` account-storage keys with optional `call_to` and `call_method` fields. Authorization remains in `internal/core/executor.go`, where transfer policies continue to enforce value limits and call policies enforce a single target contract and method. CLI support extends `tx session-key` to install call policies and `tx call` to build account/delegated-EOA calls with a separate signer.

**Tech Stack:** Go 1.25, standard `testing`, ChainLab core/state/types/contracts packages, existing CLI command helpers.

---

### Task 1: Core Contract-Call Policy

**Files:**
- Modify: `internal/core/executor_test.go`
- Modify: `internal/core/executor.go`

- [ ] **Step 1: Write the failing core test**

Add this test near the existing session-key tests in `internal/core/executor_test.go`:

```go
func TestAccountSessionKeyCanCallAllowedContractMethod(t *testing.T) {
	ownerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sessionKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	owner := chaincrypto.AddressFromPrivateKey(ownerKey)
	session := chaincrypto.AddressFromPrivateKey(sessionKey)
	smartAccount := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	store.SetCodeID(smartAccount, contracts.AccountCodeID)
	store.SetStorage(smartAccount, "owner", owner)
	store.SetBalance(smartAccount, 300_000)
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", contracts.NewRuntimeWithDefaults())

	deploy := signedTx(t, ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     owner,
		Nonce:    0,
		GasLimit: 80_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": "counter.v1",
			"initial": "0",
		},
	})
	store.SetBalance(owner, 500_000)
	deployReceipt, err := executor.ExecuteWithContext(store, deploy, core.ExecutionContext{BlockHeight: 1})
	if err != nil {
		t.Fatal(err)
	}
	counter := deployReceipt.ContractAddress

	addSession := signedTx(t, ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxSessionKey,
		From:     smartAccount,
		Signer:   owner,
		Nonce:    0,
		GasLimit: 45_000,
		GasPrice: 1,
		Payload: map[string]string{
			"action":      "add",
			"key":         session,
			"expires":     "10",
			"call_to":     counter,
			"call_method": "increment",
		},
	})
	addReceipt, err := executor.ExecuteWithContext(store, addSession, core.ExecutionContext{BlockHeight: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(addReceipt.Events) == 0 || addReceipt.Events[0].Attributes["call_to"] != counter || addReceipt.Events[0].Attributes["call_method"] != "increment" {
		t.Fatalf("session add events = %#v", addReceipt.Events)
	}
	if got := store.GetStorage(smartAccount, "session:"+session+":call_to"); got != counter {
		t.Fatalf("session call_to = %q", got)
	}

	call := signedTx(t, sessionKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     smartAccount,
		Signer:   session,
		To:       counter,
		Nonce:    1,
		GasLimit: 60_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method": "increment",
			"amount": "4",
		},
	})
	callReceipt, err := executor.ExecuteWithContext(store, call, core.ExecutionContext{BlockHeight: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.GetStorage(counter, "count"); got != "4" {
		t.Fatalf("counter value = %q", got)
	}
	if got := store.GetAccount(smartAccount).Nonce; got != 2 {
		t.Fatalf("smart account nonce = %d", got)
	}
	if got := store.GetAccount(session).Nonce; got != 0 {
		t.Fatalf("session nonce = %d", got)
	}
	if len(callReceipt.Events) < 2 || callReceipt.Events[len(callReceipt.Events)-1].Type != "account.session_key_used" || callReceipt.Events[len(callReceipt.Events)-1].Attributes["type"] != "call" {
		t.Fatalf("session call events = %#v", callReceipt.Events)
	}
}
```

- [ ] **Step 2: Verify RED**

Run:

```powershell
go test -count=1 -run TestAccountSessionKeyCanCallAllowedContractMethod ./internal/core
```

Expected: FAIL because current session-key install requires `limit` and session-key authorization rejects non-transfer transactions.

- [ ] **Step 3: Implement minimal core support**

Modify `internal/core/executor.go`:

```go
type sessionKeyPolicy struct {
	Key        string
	Limit      uint64
	Spent      uint64
	Expires    uint64
	To         string
	CallTo     string
	CallMethod string
}
```

Update `executeSessionKey` add/revoke to store and delete `call_to` / `call_method`. A valid add must have either positive `limit` or both `call_to` and `call_method`; a partial call policy must fail.

Update authorization:

```go
func validateSessionKeyAuthorization(store *state.Store, tx types.Transaction, signer string, blockHeight uint64) error {
	policy, err := parseSessionKeyPolicy(store, tx.From, signer)
	if err != nil {
		return err
	}
	if policy.Expires != 0 && blockHeight > policy.Expires {
		return errors.New("session key expired")
	}
	switch tx.Type {
	case types.TxTransfer:
		return validateSessionKeyTransfer(tx, policy)
	case types.TxCall:
		return validateSessionKeyCall(tx, policy)
	default:
		return errors.New("session key can only authorize transfer or call")
	}
}
```

Update `applySessionKeySpend` so transfer usage preserves existing `spent` behavior and call usage emits `account.session_key_used` with `type=call`, `to`, and `method`.

- [ ] **Step 4: Verify GREEN for target core test**

Run:

```powershell
go test -count=1 -run TestAccountSessionKeyCanCallAllowedContractMethod ./internal/core
```

Expected: PASS.

- [ ] **Step 5: Add failing negative core tests**

Add one test covering wrong target, wrong method, expiry, revoke, and call-only transfer rejection:

```go
func TestAccountSessionKeyRejectsInvalidContractCallPolicyUse(t *testing.T) {
	ownerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sessionKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	owner := chaincrypto.AddressFromPrivateKey(ownerKey)
	session := chaincrypto.AddressFromPrivateKey(sessionKey)
	smartAccount := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	counter := "0xcccccccccccccccccccccccccccccccccccccccc"
	otherCounter := "0xdddddddddddddddddddddddddddddddddddddddd"
	store := state.NewStore()
	store.SetCodeID(smartAccount, contracts.AccountCodeID)
	store.SetStorage(smartAccount, "owner", owner)
	store.SetStorage(smartAccount, "session:"+session+":call_to", counter)
	store.SetStorage(smartAccount, "session:"+session+":call_method", "increment")
	store.SetStorage(smartAccount, "session:"+session+":expires", "5")
	store.SetBalance(smartAccount, 300_000)
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", contracts.NewRuntimeWithDefaults())

	wrongTarget := signedTx(t, sessionKey, types.Transaction{ChainID: "chainlab-local", Type: types.TxCall, From: smartAccount, Signer: session, To: otherCounter, Nonce: 0, GasLimit: 60_000, GasPrice: 1, Payload: map[string]string{"method": "increment"}})
	if _, err := executor.ExecuteWithContext(store, wrongTarget, core.ExecutionContext{BlockHeight: 1}); err == nil {
		t.Fatal("session key should not call a disallowed target")
	}

	wrongMethod := signedTx(t, sessionKey, types.Transaction{ChainID: "chainlab-local", Type: types.TxCall, From: smartAccount, Signer: session, To: counter, Nonce: 0, GasLimit: 60_000, GasPrice: 1, Payload: map[string]string{"method": "reset"}})
	if _, err := executor.ExecuteWithContext(store, wrongMethod, core.ExecutionContext{BlockHeight: 1}); err == nil {
		t.Fatal("session key should not call a disallowed method")
	}

	expired := signedTx(t, sessionKey, types.Transaction{ChainID: "chainlab-local", Type: types.TxCall, From: smartAccount, Signer: session, To: counter, Nonce: 0, GasLimit: 60_000, GasPrice: 1, Payload: map[string]string{"method": "increment"}})
	if _, err := executor.ExecuteWithContext(store, expired, core.ExecutionContext{BlockHeight: 6}); err == nil {
		t.Fatal("session key should expire for calls")
	}

	transfer := signedTx(t, sessionKey, types.Transaction{ChainID: "chainlab-local", Type: types.TxTransfer, From: smartAccount, Signer: session, To: otherCounter, Nonce: 0, Value: 1, GasLimit: 21_000, GasPrice: 1})
	if _, err := executor.ExecuteWithContext(store, transfer, core.ExecutionContext{BlockHeight: 1}); err == nil {
		t.Fatal("call-only session key should not authorize transfer")
	}

	revoke := signedTx(t, ownerKey, types.Transaction{ChainID: "chainlab-local", Type: types.TxSessionKey, From: smartAccount, Signer: owner, Nonce: 0, GasLimit: 45_000, GasPrice: 1, Payload: map[string]string{"action": "revoke", "key": session}})
	if _, err := executor.ExecuteWithContext(store, revoke, core.ExecutionContext{BlockHeight: 1}); err != nil {
		t.Fatal(err)
	}
	if got := store.GetStorage(smartAccount, "session:"+session+":call_to"); got != "" {
		t.Fatalf("revoked call_to = %q", got)
	}
}
```

- [ ] **Step 6: Verify negative core tests and full core package**

Run:

```powershell
go test -count=1 -run "TestAccountSessionKey(CanCallAllowedContractMethod|RejectsInvalidContractCallPolicyUse|CanTransferWithinPolicy|RejectsInvalidPolicyUseAndRevocation)" ./internal/core
go test -count=1 ./internal/core
```

Expected: PASS.

- [ ] **Step 7: Commit core changes**

```powershell
git add internal/core/executor.go internal/core/executor_test.go
git commit -m "feat: add session key contract call policy"
```

### Task 2: CLI Contract-Call Session Keys

**Files:**
- Modify: `cmd/chainlab/main_test.go`
- Modify: `cmd/chainlab/main.go`

- [ ] **Step 1: Write failing CLI test**

Add this test near the existing session-key CLI test in `cmd/chainlab/main_test.go`:

```go
func TestSessionKeyCommandInstallsCallPolicyAndSessionCallWorks(t *testing.T) {
	ownerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sessionKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	owner := crypto.AddressFromPrivateKey(ownerKey)
	session := crypto.AddressFromPrivateKey(sessionKey)
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    ownerKey,
		GenesisBalance: map[string]uint64{owner: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var accountOut bytes.Buffer
	if err := deployCommand([]string{"--rpc", server.URL, "--private-key", crypto.PrivateKeyToHex(ownerKey), "--code-id", contracts.AccountCodeID, "--arg", "owner=" + owner}, &accountOut); err != nil {
		t.Fatal(err)
	}
	accountBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	account := accountBlock.Receipts[0].ContractAddress

	var counterOut bytes.Buffer
	if err := deployCommand([]string{"--rpc", server.URL, "--private-key", crypto.PrivateKeyToHex(ownerKey), "--code-id", "counter.v1", "--arg", "initial=0"}, &counterOut); err != nil {
		t.Fatal(err)
	}
	counterBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	counter := counterBlock.Receipts[0].ContractAddress

	var fundOut bytes.Buffer
	if err := transferCommand([]string{"--rpc", server.URL, "--private-key", crypto.PrivateKeyToHex(ownerKey), "--to", account, "--value", "300000"}, &fundOut); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	var sessionOut bytes.Buffer
	if err := sessionKeyCommand([]string{"--rpc", server.URL, "--from", account, "--private-key", crypto.PrivateKeyToHex(ownerKey), "--key", session, "--call-to", counter, "--call-method", "increment"}, &sessionOut); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	if got := n.Account(account).Storage["session:"+session+":call_method"]; got != "increment" {
		t.Fatalf("session call method = %q", got)
	}

	var callOut bytes.Buffer
	if err := contractCallCommand([]string{"--rpc", server.URL, "--from", account, "--private-key", crypto.PrivateKeyToHex(sessionKey), "--to", counter, "--method", "increment", "--arg", "amount=3"}, &callOut); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	if got := n.Account(counter).Storage["count"]; got != "3" {
		t.Fatalf("counter value = %q", got)
	}
	if got := n.Account(session).Nonce; got != 0 {
		t.Fatalf("session nonce = %d", got)
	}
}
```

- [ ] **Step 2: Verify RED**

Run:

```powershell
go test -count=1 -run TestSessionKeyCommandInstallsCallPolicyAndSessionCallWorks ./cmd/chainlab
```

Expected: FAIL because `tx session-key` does not accept `--call-to` / `--call-method`, and `tx call` does not accept `--from`.

- [ ] **Step 3: Implement minimal CLI support**

Modify `sessionKeyCommand`:

```go
callTo := flags.String("call-to", "", "optional allowed contract call target")
callMethod := flags.String("call-method", "", "optional allowed contract call method")
```

Set payload fields when present, and allow install when either `limit > 0` or both call fields are present.

Modify `contractCallCommand`:

```go
from := flags.String("from", "", "optional account address to call from; private key signs as owner or session key")
```

Build the call with `buildSignedTransactionFromSpec`, passing `fromOverride: *from`, `txType: types.TxCall`, `to: *to`, `payload: payload`.

- [ ] **Step 4: Verify CLI target test and package**

Run:

```powershell
go test -count=1 -run TestSessionKeyCommandInstallsCallPolicyAndSessionCallWorks ./cmd/chainlab
go test -count=1 ./cmd/chainlab
```

Expected: PASS.

- [ ] **Step 5: Commit CLI changes**

```powershell
git add cmd/chainlab/main.go cmd/chainlab/main_test.go
git commit -m "feat: add session key call cli"
```

### Task 3: Documentation And Final Verification

**Files:**
- Modify: `README.md`
- Modify: `docs/current-blockchain-tech-roadmap.md`
- Modify: `docs/superpowers/specs/2026-07-09-own-chain-design.md`

- [ ] **Step 1: Update docs**

Update docs to say session keys now support:

- value transfers with value cap, optional recipient allowlist, and optional expiry;
- single contract target plus method call policies;
- not ERC-4337, ERC-7579, social recovery, batch policy, parameter policy, or a general policy engine.

- [ ] **Step 2: Verify docs and full suite**

Run:

```powershell
go test -count=1 ./internal/core
go test -count=1 ./cmd/chainlab
go test -count=1 ./...
go run ./cmd/chainlab demo
go build -o $env:TEMP\chainlab-session-key-call-verify.exe ./cmd/chainlab
git diff --check
git diff --cached --check
```

Expected: every command exits 0. `git diff --check` may print Windows LF/CRLF warnings only if exit code remains 0.

- [ ] **Step 3: Commit docs**

```powershell
git add README.md docs/current-blockchain-tech-roadmap.md docs/superpowers/specs/2026-07-09-own-chain-design.md docs/superpowers/plans/2026-07-10-session-key-contract-calls.md
git commit -m "docs: plan session key contract calls"
```

- [ ] **Step 4: Final clean-state check**

Run:

```powershell
git status --short --branch
git log --oneline -5
```

Expected: worktree clean on `feature/own-chain-mvp`, with the session-key call commits at the top.
