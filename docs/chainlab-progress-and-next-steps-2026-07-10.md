# ChainLab Progress And Next Steps

Status date: 2026-07-10
Branch: `feature/own-chain-mvp`
Project: `D:\blockchain-chain-lab`

## Product Target

ChainLab is intended to become a real production-grade sovereign blockchain. It is not scoped as a learning chain, toy, or permanently local prototype.

The current local PoA implementation is the deterministic development and differential-test harness. The selected production path preserves ChainLab's protocol/application state machine and integrates CometBFT ABCI++ for Byzantine consensus, P2P, evidence, proposal flow, block sync, and state sync. Production also requires transactional versioned storage, deterministic WASM limits, protected validator signing, protocol upgrades, security evidence, economics, and staged network operations.

Production readiness is tracked by explicit gates rather than a completion percentage. See:

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

## Verification Evidence

The following commands all exited 0 after the implementation:

```powershell
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go run ./cmd/chainlab demo
go build -o $env:TEMP\chainlab-account-recovery-production.exe ./cmd/chainlab
git diff --check
```

The race run covered every package. The demo completed at height 21 with transfer, sponsored transfer, batch, smart account, multisig, native contracts, WASM, staking, and governance results intact.

## Current Production Blockers

Ordered by consensus/security dependency, not feature visibility:

1. **WASM determinism**
   - Current static function-body and host-resource charging is incomplete.
   - Wall-clock timeout can still influence guest success/failure under host load, which is unacceptable for consensus.
   - Add deterministic instruction/epoch fuel and hard limits for memory, tables, stack, host I/O, logs, reads/writes, and storage growth; verify cross-process/root equality.

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
   - Txpool capacity/per-account quotas/eviction/journal.
   - RPC body/batch/log-range/rate/time limits and WebSocket backpressure.
   - Code, payload, event, storage, peer, and queue bounds with load/DoS evidence.

6. **Security and operations**
   - Fuzz/property/race/fault/load/soak/cross-architecture test programs and dependency scans.
   - Validator keystore/remote signer, sentries, metrics, readiness/liveness, alerts, backups, restore drills, reproducible releases, SBOM/provenance, and incident runbooks.

7. **Economics and ecosystem**
   - Specify and simulate supply, rewards, staking/unbonding/slashing, governance/deposits/timelocks, treasury, and upgrade authority.
   - Production wallet/SDK/indexer/explorer/token/oracle/interoperability support and staged public/incentivized testnets.

## Next Immediate Work

Start with deterministic WASM execution because every later consensus and multi-node test is invalid if nodes can disagree on a guest result.

Required sequence:

1. Audit wazero's current deterministic fuel/epoch interruption and resource-limit APIs against the pinned version.
2. Define a consensus resource schedule and module admission limits.
3. Add RED tests for instruction loops, memory/table growth, stack/recursion, host I/O, emitted bytes, storage growth, and cross-process replay.
4. Remove wall-clock outcome dependence; timeout may remain only as a local process safety backstop whose firing cannot commit a receipt/state.
5. Run full/race/fuzz/replay verification and update the production gate evidence.

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
