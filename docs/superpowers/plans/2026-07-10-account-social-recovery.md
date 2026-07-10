# Account Social Recovery Implementation Plan

**Goal:** Add deterministic, delay-based guardian recovery for `account.v1` smart accounts and `account.v1` delegated EOAs without widening guardian authority to transfers, contract calls, or other account actions.

**Architecture:** Add a dedicated `account.recovery` transaction and authorization path. Before threshold, bounded guardian voting rounds collect one target vote per guardian without creating a pending proposal. Threshold freezes one target, starts its delay, and opens a bounded execution window. Owner actions (`configure`, `cancel`, `clear`) are account-funded; guardian actions (`approve`, `execute`) are guardian-funded. Every action consumes the recovered account nonce. Successful rotation clears active recovery state and advances a session epoch so pre-recovery session authority cannot survive.

**Compatibility boundary:** This is a ChainLab-native recovery policy inspired by mainstream smart-account recovery patterns. It is not ERC-4337 EntryPoint validation, an ERC-7579 recovery module, a Safe module, or EIP-7702 type-4 authorization-list compatibility.

## Security Invariants

- A guardian signature is valid only for `account.recovery`; it cannot authorize transfer, batch, call, session-key, or governance transactions.
- `account.recovery` requires an explicit `signer`, a valid signer signature, no multisig authorizations, no Ethereum signature kind, no paymaster, and no unrelated transaction/payload fields.
- Guardians and new owners are canonical, non-zero, 20-byte EOA addresses without contract code. Guardians cannot equal the recovered account or current owner and are capped at 16.
- `threshold` is positive and no larger than the guardian count. `delay` must be explicitly supplied and may be zero.
- A minority vote cannot occupy a pending target. Threshold must agree on one target; a vote change is allowed only when it immediately reaches threshold. Voting and execution windows are each bounded by 256 blocks.
- `execute_after = threshold_reached_height + delay` is overflow checked. Execute requires a mandatory matching `new_owner`, threshold, delay, and an unexpired execution window.
- Owner actions charge the recovered account; guardian actions charge the guardian without consuming guardian nonce.
- Every failed validation or execution leaves nonce, balance, owner, and recovery state unchanged.
- Successful execution rotates `owner`, clears active state, and advances `session:epoch` to invalidate old transfer and call policies in O(1).
- Clearing delegated code removes delegated authorization storage so old owner, guardian, or session policies cannot reactivate on a later delegation.

## Task 1: RED Core Coverage

**Files:**

- Modify `internal/core/executor_test.go`

- [x] Add a complete smart-account lifecycle test: configure two guardians, approve a target, prove threshold and delay gates, execute, verify owner rotation and pending-state deletion, reject old owner, and accept new owner.
- [x] Prove guardian signer nonce is unchanged while recovered-account nonce advances on successful recovery actions.
- [x] Add delegated-EOA lifecycle coverage using the same recovery semantics.
- [x] Add owner authorization negatives for non-owner configure/cancel/clear and missing signer.
- [x] Add guardian authorization negatives for unconfigured guardian, duplicate approval, mismatched execute target, and guardian use outside `account.recovery`.
- [x] Add validation negatives for empty/duplicate/malformed/zero guardians, invalid threshold, missing delay, malformed/new zero owner, corrupted stored config, and execute-height overflow.
- [x] Prove failed threshold/delay/signature/config checks do not consume nonce or mutate state.
- [x] Prove cancel preserves guardian configuration, while clear removes configuration and pending state.
- [x] Prove split target votes cannot block threshold convergence and expired voting rounds restart safely.
- [x] Prove successful recovery invalidates transfer and call session-key authorization by epoch.
- [x] Prove delegated-code clear removes all delegated authorization state.

Run the targeted tests before implementation and record the expected compile/test failure.

## Task 2: Core Types, Authorization, And State Transitions

**Files:**

- Modify `internal/types/types.go`
- Modify `internal/state/store.go`
- Modify `internal/state/store_test.go`
- Modify `internal/core/executor.go`
- Add `internal/core/recovery.go`

- [x] Add `TxAccountRecovery TxType = "account.recovery"`.
- [x] Add a state helper that deletes storage by exact key set or prefix and verify clone/snapshot/root behavior.
- [x] Harden delegated-code clearing and replacement so authorization-policy storage is removed deterministically.
- [x] Route `TxAccountRecovery` through dedicated signature and action-role validation before generic owner/session-key authorization.
- [x] Reject signerless, multisig, Ethereum-kind, and paymaster recovery envelopes.
- [x] Add a 55,000 base gas estimate for recovery transactions.
- [x] Implement normalized address parsing, guardian vote/config parsing, overflow-safe voting/delay/expiry heights, pending-state clearing, and session-epoch invalidation.
- [x] Implement `configure`, `approve`, `execute`, `cancel`, and `clear` with stable errors and event attributes.
- [x] Emit `account.recovery_configured`, `account.recovery_approved`, `account.recovery_executed`, `account.recovery_cancelled`, and `account.recovery_cleared`.
- [x] Run targeted core/state tests, then `go test -count=1 ./internal/state ./internal/core`.

## Task 3: CLI End-To-End Workflow

**Files:**

- Modify `cmd/chainlab/main.go`
- Modify `cmd/chainlab/main_test.go`

- [x] Register `tx recovery` and update command usage.
- [x] Add repeatable `--guardian`, required `--from`, `--private-key`, and `--action`; action-specific flags are `--threshold`, `--delay`, and `--new-owner`.
- [x] Validate action/flag combinations locally and join guardian addresses deterministically.
- [x] Build with pending nonce for `--from`, set `signer` to the owner or guardian key address, use the recovery gas default, sign, and submit through the existing transaction path.
- [x] Add a real-node CLI test that deploys/funds `account.v1`, configures guardians, approves and executes recovery across produced block heights, and then sends from the account with the new owner key.
- [x] Add CLI input-validation tests for missing fields and incompatible flags.
- [x] Run `go test -count=1 ./cmd/chainlab`.

## Task 4: Documentation And Capability Matrix

**Files:**

- Modify `README.md`
- Modify `docs/current-blockchain-tech-roadmap.md`
- Modify `docs/superpowers/specs/2026-07-10-account-social-recovery-design.md`
- Modify `docs/superpowers/specs/2026-07-09-own-chain-design.md`

- [x] Document commands, storage, events, nonce/fee behavior, delay semantics, owner rotation, session-key revocation, and delegated-code cleanup.
- [x] State explicit non-goals and avoid claiming generic ERC-4337, ERC-7579, Safe, passkey, or email recovery compatibility.
- [x] Link the feature to the maintained mainstream-chain capability matrix and official primary references.

## Task 5: Verification And Commits

- [x] Run `gofmt` on every modified Go file.
- [x] Run `go test -count=1 ./internal/state`.
- [x] Run `go test -count=1 ./internal/core`.
- [x] Run `go test -count=1 ./cmd/chainlab`.
- [x] Run `go test -count=1 ./...`.
- [x] Run `go vet ./...`.
- [x] Run `go run ./cmd/chainlab demo`.
- [x] Run `go build -o $env:TEMP\chainlab-account-recovery-verify.exe ./cmd/chainlab`.
- [x] Run `git diff --check` and `git diff --cached --check`.
- [x] Commit the plan, core, CLI, and documentation as separate coherent commits.
- [ ] Write the required Obsidian diary/project record, commit it, and push `D:\code\obsidian-note`.

## Completion Evidence

Completion requires passing tests that directly exercise every action and negative path above, a real-node CLI owner-rotation workflow, clean formatting/vet/build/demo results, documentation that matches code, and a clean ChainLab worktree. Passing only the happy path is insufficient.
