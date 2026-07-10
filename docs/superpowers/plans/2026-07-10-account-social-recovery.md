# Account Social Recovery Implementation Plan

**Goal:** Add deterministic, delay-based guardian recovery for `account.v1` smart accounts and `account.v1` delegated EOAs without widening guardian authority to transfers, contract calls, or other account actions.

**Architecture:** Add a dedicated `account.recovery` transaction and authorization path. Recovery configuration and one pending recovery proposal live in consensus account storage. Owner actions (`configure`, `cancel`, `clear`) and guardian actions (`approve`, `execute`) are authorized separately before state mutation. Execution remains atomic on the executor's cloned store, consumes the recovered account nonce, and charges that account. A successful owner rotation clears pending recovery state and revokes existing session-key policy so authorization installed by a lost or compromised owner does not survive recovery.

**Compatibility boundary:** This is a ChainLab-native recovery policy inspired by mainstream smart-account recovery patterns. It is not ERC-4337 EntryPoint validation, an ERC-7579 recovery module, a Safe module, or EIP-7702 type-4 authorization-list compatibility.

## Security Invariants

- A guardian signature is valid only for `account.recovery`; it cannot authorize transfer, batch, call, session-key, or governance transactions.
- `account.recovery` requires an explicit `signer`, a valid signer signature, no multisig authorizations, no Ethereum signature kind, and no paymaster. The recovered account pays the fee.
- Guardians and new owners are canonical, non-zero, 20-byte hex EOA addresses. Guardian entries are unique after normalization.
- `threshold` is positive and no larger than the guardian count. `delay` must be explicitly supplied and may be zero.
- A different `new_owner` replaces the pending target, resets approvals, and restarts the delay. Duplicate approval for the same target fails.
- `execute_after = block_height + delay` is overflow checked. Execute requires both threshold and delay.
- Every failed validation or execution leaves nonce, balance, owner, and recovery state unchanged.
- Successful execution rotates `owner`, clears pending state, and revokes every `session:` storage entry.
- Clearing delegated code removes delegated authorization storage so old owner, guardian, or session policies cannot reactivate on a later delegation.

## Task 1: RED Core Coverage

**Files:**

- Modify `internal/core/executor_test.go`

- [ ] Add a complete smart-account lifecycle test: configure two guardians, approve a target, prove threshold and delay gates, execute, verify owner rotation and pending-state deletion, reject old owner, and accept new owner.
- [ ] Prove guardian signer nonce is unchanged while recovered-account nonce advances on successful recovery actions.
- [ ] Add delegated-EOA lifecycle coverage using the same recovery semantics.
- [ ] Add owner authorization negatives for non-owner configure/cancel/clear and missing signer.
- [ ] Add guardian authorization negatives for unconfigured guardian, duplicate approval, mismatched execute target, and guardian use outside `account.recovery`.
- [ ] Add validation negatives for empty/duplicate/malformed/zero guardians, invalid threshold, missing delay, malformed/new zero owner, corrupted stored config, and execute-height overflow.
- [ ] Prove failed threshold/delay/signature/config checks do not consume nonce or mutate state.
- [ ] Prove cancel preserves guardian configuration, while clear removes configuration and pending state.
- [ ] Prove changing the pending target resets approvals and delay.
- [ ] Prove successful recovery revokes transfer and call session-key storage and authorization.
- [ ] Prove delegated-code clear removes all delegated authorization state.

Run the targeted tests before implementation and record the expected compile/test failure.

## Task 2: Core Types, Authorization, And State Transitions

**Files:**

- Modify `internal/types/types.go`
- Modify `internal/state/store.go`
- Modify `internal/state/store_test.go`
- Modify `internal/core/executor.go`

- [ ] Add `TxAccountRecovery TxType = "account.recovery"`.
- [ ] Add a state helper that deletes storage by exact key set or prefix and verify clone/snapshot/root behavior.
- [ ] Harden delegated-code clearing and replacement so authorization-policy storage is removed deterministically.
- [ ] Route `TxAccountRecovery` through dedicated signature and action-role validation before generic owner/session-key authorization.
- [ ] Reject signerless, multisig, Ethereum-kind, and paymaster recovery envelopes.
- [ ] Add a 55,000 base gas estimate for recovery transactions.
- [ ] Implement normalized address parsing, guardian/config parsing, approval parsing, overflow-safe height calculation, pending-state clearing, and session-policy revocation.
- [ ] Implement `configure`, `approve`, `execute`, `cancel`, and `clear` with stable errors and event attributes.
- [ ] Emit `account.recovery_configured`, `account.recovery_approved`, `account.recovery_executed`, `account.recovery_cancelled`, and `account.recovery_cleared`.
- [ ] Run targeted core/state tests, then `go test -count=1 ./internal/state ./internal/core`.

## Task 3: CLI End-To-End Workflow

**Files:**

- Modify `cmd/chainlab/main.go`
- Modify `cmd/chainlab/main_test.go`

- [ ] Register `tx recovery` and update command usage.
- [ ] Add repeatable `--guardian`, required `--from`, `--private-key`, and `--action`; action-specific flags are `--threshold`, `--delay`, and `--new-owner`.
- [ ] Validate action/flag combinations locally and join guardian addresses deterministically.
- [ ] Build with pending nonce for `--from`, set `signer` to the owner or guardian key address, use the recovery gas default, sign, and submit through the existing transaction path.
- [ ] Add a real-node CLI test that deploys/funds `account.v1`, configures guardians, approves and executes recovery across produced block heights, and then sends from the account with the new owner key.
- [ ] Add CLI input-validation tests for missing fields and incompatible flags.
- [ ] Run `go test -count=1 ./cmd/chainlab`.

## Task 4: Documentation And Capability Matrix

**Files:**

- Modify `README.md`
- Modify `docs/current-blockchain-tech-roadmap.md`
- Modify `docs/superpowers/specs/2026-07-10-account-social-recovery-design.md`
- Modify `docs/superpowers/specs/2026-07-09-own-chain-design.md`

- [ ] Document commands, storage, events, nonce/fee behavior, delay semantics, owner rotation, session-key revocation, and delegated-code cleanup.
- [ ] State explicit non-goals and avoid claiming generic ERC-4337, ERC-7579, Safe, passkey, or email recovery compatibility.
- [ ] Link the feature to the maintained mainstream-chain capability matrix and official primary references.

## Task 5: Verification And Commits

- [ ] Run `gofmt` on every modified Go file.
- [ ] Run `go test -count=1 ./internal/state`.
- [ ] Run `go test -count=1 ./internal/core`.
- [ ] Run `go test -count=1 ./cmd/chainlab`.
- [ ] Run `go test -count=1 ./...`.
- [ ] Run `go vet ./...`.
- [ ] Run `go run ./cmd/chainlab demo`.
- [ ] Run `go build -o $env:TEMP\chainlab-account-recovery-verify.exe ./cmd/chainlab`.
- [ ] Run `git diff --check` and `git diff --cached --check`.
- [ ] Commit the plan, core, CLI, and documentation as separate coherent commits.
- [ ] Write the required Obsidian diary/project record, commit it, and push `D:\code\obsidian-note`.

## Completion Evidence

Completion requires passing tests that directly exercise every action and negative path above, a real-node CLI owner-rotation workflow, clean formatting/vet/build/demo results, documentation that matches code, and a clean ChainLab worktree. Passing only the happy path is insufficient.
