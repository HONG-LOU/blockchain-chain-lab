# ChainLab Progress And Next Steps

Status date: 2026-07-10
Branch: `feature/own-chain-mvp`
Project: `D:\blockchain-chain-lab`

## Product Target

ChainLab is intended to become a real production-grade sovereign blockchain. It is not scoped as a learning chain, toy, or permanently local prototype.

The current local PoA implementation is a development and differential-test harness for the deterministic application state transition; wall-clock block timestamps mean the whole local network is not cross-run deterministic. The selected production path preserves ChainLab's protocol/application state machine and integrates CometBFT ABCI++ for Byzantine consensus, P2P, evidence, proposal flow, block sync, and state sync. Production also requires transactional versioned storage, deterministic WASM limits, protected validator signing, protocol upgrades, security evidence, economics, and staged network operations.

Production readiness is tracked by explicit gates rather than a completion percentage. The capability-and-gates document is authoritative; the roadmap and the blocker summaries in this file are navigational views, not independent exhaustive checklists. See:

- `docs/current-blockchain-tech-roadmap.md`
- `docs/mainstream-chain-capability-and-production-gates-2026-07-10.md`

## Completed In This Milestone

### Account Social Recovery

Commits:

```text
b870ab2 docs: plan account social recovery
51f74d2 feat: add hardened account social recovery
c311d89 feat: add account recovery cli
```

Implemented behavior:

- `account.recovery` for `account.v1` contract accounts and delegated EOAs.
- Owner actions: `configure`, `cancel`, and `clear`.
- Guardian actions: `approve` and `execute`.
- Up to 16 normalized, non-zero EOA guardians; owner/account/contract addresses are rejected as guardians.
- Per-guardian voting before threshold so a minority cannot occupy the only pending target.
- Vote changes only when the change immediately reaches threshold, with a 256-block voting-round expiry for deadlock recovery.
- Delay starts when one target reaches threshold; execution has an inclusive 256-block window.
- Mandatory `execute.new_owner` binds the final target into the guardian signature.
- Owner actions are account-funded. Guardian approval/execution is guardian-funded, consumes the recovered account nonce, and leaves guardian nonce unchanged.
- Successful rotation advances `session:epoch`, invalidating old transfer and call session policies.
- Direct `setOwner` and delegated-code replacement/clear remove stale active recovery/session authority.
- Strict recovery envelopes reject multisig fields, signature kinds, paymasters, value/to/batch, and action-irrelevant payloads.
- Nonce and height additions reject overflow.
- Txpool replacement requires the same authorization principal, preventing one guardian from replacing another guardian's transaction.
- Recovery events are indexed at the recovered account and exposed through receipts and `eth_getLogs`.
- CLI `tx recovery` has strict action-specific flags and always writes `signer`.

Verification coverage includes smart-account and delegated-EOA end-to-end rotation, exact threshold/delay/expiry boundaries, split votes, round rollover, role isolation, fee ownership, insufficient guardian funds, session invalidation, malformed/corrupted stored state, nonce/height overflow, maximum guardian set, owner/delegation bypass cleanup, cross-guardian mempool replacement, account-addressed RPC logs, node persistence/restart, and new-owner transfer.

### Adjacent Security Fixes

- Shared strict 20-byte non-zero EOA normalization.
- Account/delegated owner validation prevents malformed, zero, and self ownership.
- Account nonce increment rejects `uint64` wraparound.
- Delegation clear removes `owner`, `session:*`, and `recovery:*` authorization state while preserving unrelated storage.
- Event source mapping is centralized so account-policy events cannot silently disappear from EVM-shaped logs.

### Deterministic WASM Runtime Slice

Commits:

```text
7e87821 feat: add deterministic wasmtime execution
0290e7d feat: harden node execution and txpool limits
```

- Replaced wazero timeout/static-body charging with pinned Wasmtime-Go v46.0.1 and protocol identifier `chainlab-wasm-v1`.
- Guest execution uses deterministic fuel; host calls deduct from the same remaining budget before side effects. After a host trap, the invocation meter records and reports consumed fuel instead of under-reporting it; payer fee/nonce settlement for failed transactions remains open.
- Admission rejects start/WASI/unknown ABI, malformed section structure, growable resources, oversized modules, unsupported proposals, and invalid exports/signatures before state admission.
- Every invocation prices the admitted original code bytes, fixed declared memory pages, and fixed table elements before JIT/instantiation; low-resource-gas calls cannot trigger compilation.
- Memory, table, stack, host I/O, UTF-8 fields, return data, events, and storage writes/growth have explicit version-1 bounds.
- Dynamic compiled modules are bound to the current state code record, concurrent cache misses use singleflight, and cold/warm execution has identical consensus gas.
- Direct runtime deploy/call remains atomic. Executor-owned transaction stores use explicit in-transaction calls, eliminating the redundant whole-state copy per contract operation.
- Contract code stores its metering version in snapshots and state roots. A fresh-process end-to-end vector now pins upload/deploy/call/query gas, receipts, events, code/address derivation, and final root.
- Mempool admission enforces 2 MiB per transaction, 128 MiB/4,096 total entries, 2,048 queued, 64 entries per sender, a nonce gap of 64, transaction/block gas compatibility, and 128 batch operations. Block production selects by actual gas and revalidates retained/import-conflicting transactions without failing the whole candidate on stale entries. A gas-bounded proposal-simulation work budget is still required because failed/remainder WASM can otherwise be replayed multiple times without block inclusion.
- Signed max-fee exposure, fee overflow, sender value exposure, and paymaster capacity are checked before contract execution. Priority fees are consensus-bound to the block proposer, and imported blocks must inherit the protocol gas limit.
- Unexpected validation/JIT/linker/fuel failures while loading admitted code are classified as `ErrWasmRuntimeFault`; invalid upload bytes remain admission errors. The local node enters a process-local sticky halt across submit, produce, import, finality vote, revalidation, and queued promotion; persistent protocol-wide ABCI++ halt/recovery behavior remains open.

## Verification Evidence

The following commands all exited 0 under the security-patched Go 1.25.12 toolchain after the implementation:

```powershell
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go test -count=2 -run TestWASMExecutionMatchesAcrossFreshProcesses ./internal/core
go run ./cmd/chainlab demo
go build -o $env:TEMP\chainlab-wasm-production-verify.exe ./cmd/chainlab
go mod verify
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
git diff --check
```

The race run covered every package. The demo completed at height 21 with transfer, sponsored transfer, batch, smart account, multisig, native contracts, WASM, staking, and governance results intact. `govulncheck` initially found 15 reachable standard-library vulnerabilities under Go 1.25.5; raising the enforced minimum to Go 1.25.12 reduced the result to zero reachable vulnerabilities. It still reported 17 advisories in required modules whose vulnerable symbols are not called.

The earlier account-recovery milestone was recorded in the Obsidian diary and `ota/chainlab-production-chain.md` project record as notes commit `d4ddad6`. The current runtime/txpool milestone will be recorded after its implementation commits.

## Current Production Blockers

Ordered by consensus/security dependency, not feature visibility:

1. **WASM production evidence**
   - `chainlab-wasm-v1` now pins Wasmtime-Go v46.0.1 and uses deterministic dynamic fuel with a shared host budget; wall-clock timing no longer decides execution.
   - Fixed memory/table admission, instantiation-resource gas, structural prefiltering, stack/host-I/O/event/write/growth limits, read isolation, rollback, state-bound singleflight caching, and an exact Windows development golden vector are implemented with adversarial tests.
   - Still required before mainnet: deterministic guest call-depth independent of native stack layout, Linux/amd64 real-process vectors, hard native/JIT memory lifecycle bounds, persistent protocol-wide fatal runtime-fault halt plus deterministic recovery/upgrade behavior on every ABCI++ validator path, reproducible native artifacts/checksums/SBOM, fuzz/load/dependency evidence, versioned activation, and external review. Windows is development-only; arm64/macOS validators are unsupported for version 1.

2. **CometBFT ABCI++ production lifecycle**
   - Implement and test proposal, finalize/commit, validator update, evidence, query, snapshot, and state-sync methods.
   - Move authoritative production consensus/P2P away from the custom PoA/HTTP relay while preserving the local harness for differential tests.

3. **Transactional storage**
   - Replace whole-chain JSON snapshots with atomic versioned KV state/index commits.
   - Add WAL/reopen, kill/fault injection, corruption handling, migrations, verified snapshots, and pruned/full/archive semantics.

4. **Protocol lifecycle and proofs**
   - Version consensus encodings and state schema.
   - Add scheduled upgrades, deterministic migrations, incompatible-node rejection, rollback limits, transaction/receipt/state inclusion proofs, and an independent verifier.

5. **Resource governance**
   - Charged failure receipts and nonce semantics so valid OOG/trap calls cannot be replayed at no cost.
   - Versioned native-contract gas and bounds for owners, recovery/session records, payloads, prefix deletion, and storage growth.
   - Txpool deterministic fee-aware eviction/TTL/journal/restart and local policy; static count/byte/account/gap limits are implemented.
   - Gas-bounded/lazy proposal simulation, incremental txpool byte accounting, and orphaned-transaction reinsertion after reorg; current whole-pool replay can exceed the block gas work budget.
   - Imported block/transaction and RPC/peer body byte limits, RPC batch/log-range/rate/time limits, and WebSocket backpressure.
   - Hard compiled-code resident-memory limits, dynamic WASM gas estimation, and code/payload/event/storage/peer/queue bounds with load/DoS evidence.

6. **Security and operations**
   - Fuzz/property/race/fault/load/soak/cross-architecture test programs and dependency scans.
   - Validator keystore/remote signer, sentries, metrics, readiness/liveness, alerts, backups, restore drills, reproducible releases, SBOM/provenance, and incident runbooks.

7. **Economics and ecosystem**
   - Specify and simulate supply, rewards, staking/unbonding/slashing, governance/deposits/timelocks, treasury, and upgrade authority.
   - Production wallet/SDK/indexer/explorer/token/oracle/interoperability support and staged public/incentivized testnets.

## Next Immediate Work

The deterministic WASM implementation slice is locally testable, but its production gate is not closed. Preserve it as a protocol-versioned subsystem while closing the remaining consensus and release evidence.

Required sequence:

1. Define deterministic guest call-depth behavior, charged failure receipts, and versioned native-contract gas.
2. Run the committed golden and adversarial vectors in a pinned Linux/amd64 real-process release environment; do not enable other validator targets without equivalent evidence.
3. Replace GC-backed JIT eviction with a hard lifecycle bound and stop pending upload replay from recompiling admitted code.
4. Rebuild and sign the pinned Wasmtime native artifact with checksums, license notices, and an SBOM.
5. Add malicious-module fuzzing, compile/execution load tests, dependency review, activation records, and external runtime/ABI review.

After deterministic execution is proven, implement the CometBFT ABCI++ adapter before expanding lower-priority account or EVM-shaped features.

## Completion Audit

Do not call the long-running goal complete until every production gate has authoritative evidence:

- deterministic execution and cross-node replay;
- Byzantine consensus/P2P/state sync through the selected production stack;
- crash-consistent versioned storage and historical modes;
- protocol upgrades and inclusion proofs;
- bounded resources and abuse resistance;
- fuzz/property/race/fault/load/soak/security review;
- validator/operator security and disaster recovery;
- specified economics, governance, ecosystem, and staged network evidence;
- documentation and release artifacts matching deployed behavior.

A green local unit suite is necessary but not sufficient for production completion.
