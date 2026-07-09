# Session Key Accounts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add ChainLab-native session keys for `account.v1` smart accounts and delegated EOAs.

**Architecture:** Session-key policy is stored in account storage under deterministic `session:<key>:...` keys. A new `account.session_key` transaction lets the owner add or revoke keys, and existing transfer authorization learns to accept a session key when policy constraints pass.

**Tech Stack:** Go, ChainLab `internal/types`, `internal/state`, `internal/core`, `cmd/chainlab`, existing JSON transaction signing, Go test suite.

---

## File Structure

- Modify `internal/types/types.go`: add `TxSessionKey`.
- Modify `internal/state/store.go`: add `DeleteStorage`.
- Modify `internal/state/store_test.go`: verify storage deletion affects snapshots and roots.
- Modify `internal/core/executor.go`: authorize session keys, add/revoke policy, track spent, estimate gas, emit events.
- Modify `internal/core/executor_test.go`: cover add/use/reject/revoke behavior.
- Modify `cmd/chainlab/main.go`: add `tx session-key` and signed builder support with `--from`.
- Modify `cmd/chainlab/main_test.go`: add real-server CLI session-key E2E.
- Modify `README.md`, `docs/current-blockchain-tech-roadmap.md`, and `docs/superpowers/specs/2026-07-09-own-chain-design.md`: document the supported slice and non-goals.

## Task 1: State Storage Deletion

**Files:**
- Modify: `internal/state/store.go`
- Modify: `internal/state/store_test.go`

- [ ] **Step 1: Write failing state test**

Add `TestDeleteStorageRemovesKeyFromSnapshotAndRoot` to `internal/state/store_test.go`:

```go
func TestDeleteStorageRemovesKeyFromSnapshotAndRoot(t *testing.T) {
	store := state.NewStore()
	account := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store.SetStorage(account, "session:0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb:limit", "100")
	rootWithKey := store.Root()

	store.DeleteStorage(account, "session:0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb:limit")
	if got := store.GetStorage(account, "session:0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb:limit"); got != "" {
		t.Fatalf("deleted storage = %q", got)
	}
	if store.Root() == rootWithKey {
		t.Fatal("root did not change after deleting storage")
	}
	restored := state.NewStoreFromSnapshot(store.Snapshot())
	if got := restored.GetStorage(account, "session:0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb:limit"); got != "" {
		t.Fatalf("restored deleted storage = %q", got)
	}
}
```

- [ ] **Step 2: Verify RED**

Run:

```powershell
go test -count=1 -run TestDeleteStorageRemovesKeyFromSnapshotAndRoot ./internal/state
```

Expected: fail because `DeleteStorage` does not exist.

- [ ] **Step 3: Implement `DeleteStorage`**

Add to `internal/state/store.go`:

```go
func (s *Store) DeleteStorage(address string, key string) {
	account := s.account(address)
	if account.Storage != nil {
		delete(account.Storage, key)
	}
	s.accounts[normalize(address)] = account
}
```

- [ ] **Step 4: Verify GREEN**

Run:

```powershell
go test -count=1 ./internal/state
```

Expected: pass.

## Task 2: Core Session-Key Semantics

**Files:**
- Modify: `internal/types/types.go`
- Modify: `internal/core/executor.go`
- Modify: `internal/core/executor_test.go`

- [ ] **Step 1: Write failing core tests**

Add tests for:

- owner adds session key and session key transfers within limit;
- non-owner cannot add session key;
- session key cannot exceed limit, use wrong recipient, use after expiry, or use after revoke.

Use real generated keys and existing `signedTx` helper. For expiry, call `executor.ExecuteWithContext(store, tx, core.ExecutionContext{BlockHeight: 6})` after adding `expires=5`.

- [ ] **Step 2: Verify RED**

Run:

```powershell
go test -count=1 -run "TestAccountSessionKey" ./internal/core
```

Expected: fail because `TxSessionKey` and session-key authorization do not exist.

- [ ] **Step 3: Add type and gas estimate**

In `internal/types/types.go`:

```go
TxSessionKey TxType = "account.session_key"
```

In `core.EstimateGas`:

```go
case types.TxSessionKey:
	return 45_000, nil
```

- [ ] **Step 4: Add executor helpers**

Implement helpers in `internal/core/executor.go`:

- `executeSessionKey`
- `validateSessionKeyAuthorization`
- `applySessionKeySpend`
- `sessionKeyStoragePrefix`
- `parseSessionKeyPolicy`
- `isAccountV1Authority`

The helper must reject `account.session_key` unless the signer is the owner. Session-key authorization must only pass for `transfer`.

- [ ] **Step 5: Wire executor**

Change `validateTransactionAuthorization` to accept block height:

```go
if err := validateTransactionAuthorization(store, tx, context.BlockHeight); err != nil {
	return types.Receipt{}, err
}
```

Add `case types.TxSessionKey` in the switch and call `applySessionKeySpend` after successful `transfer`.

- [ ] **Step 6: Verify GREEN**

Run:

```powershell
go test -count=1 ./internal/core
```

Expected: pass.

## Task 3: CLI Session-Key Command

**Files:**
- Modify: `cmd/chainlab/main.go`
- Modify: `cmd/chainlab/main_test.go`

- [ ] **Step 1: Write failing CLI test**

Add `TestSessionKeyCommandInstallsKeyAndSessionTransferWorks` to `cmd/chainlab/main_test.go`. The test should:

- start a node with owner funds;
- deploy `account.v1`;
- fund the account;
- run `sessionKeyCommand --from <account> --private-key <owner> --key <session> --limit 100 --to <receiver>`;
- produce a block;
- run `transferCommand --from <account> --private-key <session> --to <receiver> --value 40`;
- produce a block;
- assert receiver balance is `40`.

- [ ] **Step 2: Verify RED**

Run:

```powershell
go test -count=1 -run TestSessionKeyCommandInstallsKeyAndSessionTransferWorks ./cmd/chainlab
```

Expected: fail because `sessionKeyCommand` does not exist.

- [ ] **Step 3: Implement CLI**

Add `session-key` to `txCommand` usage and switch. Implement:

```go
func sessionKeyCommand(args []string, out io.Writer) error
```

Flags:

- `--rpc`
- `--from`
- `--private-key`
- `--key`
- `--limit`
- `--expires`
- `--to`
- `--revoke`
- `--gas-limit`
- `--gas-price`

Use `buildSignedTransactionFromSpec` with `fromOverride` so the owner key becomes `signer`.

- [ ] **Step 4: Verify GREEN**

Run:

```powershell
go test -count=1 ./cmd/chainlab
```

Expected: pass.

## Task 4: Documentation And Full Verification

**Files:**
- Modify: `README.md`
- Modify: `docs/current-blockchain-tech-roadmap.md`
- Modify: `docs/superpowers/specs/2026-07-09-own-chain-design.md`

- [ ] **Step 1: Update docs**

Document:

- `account.session_key` in transaction/API capability lists.
- CLI install/use/revoke examples.
- Session keys are limited to transfers in this slice.
- Full ERC-4337, ERC-7579, social recovery, and general policy engines remain later work.

- [ ] **Step 2: Full verification**

Run:

```powershell
gofmt -w internal\state\store.go internal\state\store_test.go internal\types\types.go internal\core\executor.go internal\core\executor_test.go cmd\chainlab\main.go cmd\chainlab\main_test.go
go test -count=1 ./internal/state
go test -count=1 ./internal/core
go test -count=1 ./cmd/chainlab
go test -count=1 ./...
go run ./cmd/chainlab demo
go build -o $env:TEMP\chainlab-session-key-verify.exe ./cmd/chainlab
git diff --check
git diff --cached --check
```

Expected: all commands exit 0.

## Self-Review

- The plan maps every requirement in the session-key design to a code or docs task.
- Scope stays on ChainLab-native transfer session keys and avoids claiming full ERC-4337/ERC-7579 compatibility.
- TDD order is explicit for state, core, and CLI behavior.
- Verification includes package tests, all tests, demo, build, and diff checks.
