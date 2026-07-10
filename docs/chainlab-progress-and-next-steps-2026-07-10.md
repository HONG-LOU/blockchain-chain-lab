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

### Deterministic Execution And Resource Safety

- Wasmtime-Go v46.0.1 is pinned behind protocol identifier `chainlab-wasm-v1`. Guest fuel and host-call charging share one deterministic budget; wall-clock timeout does not decide consensus execution.
- Included deterministic failures produce `Success=false` receipts with stable failure codes, roll back business state, consume the nonce, and settle the actual fee. Out-of-gas consumes the full gas limit, sponsored failures charge the paymaster, and batch failure rolls back the complete business batch.
- WASM admission rejects unsupported or nondeterministic structure, including WASI/start functions, growable resources, recursion, indirect calls, tail calls, and function-reference calls. The admitted direct-call graph is acyclic and has a maximum depth of 64.
- Memory, tables, host I/O, UTF-8 fields, return data, events, storage growth, payloads, and writes have protocol bounds. Cold and warm compilation paths use the same consensus gas.
- Cold-cache WASM calls perform an O(1) encoded-size gas preflight before bytecode decode, hashing, envelope scan, or call-graph analysis. Snapshot restore fixes the accepted metering version and rejects encoded modules above 512 KiB before decoding.
- Native contracts use the sealed `chainlab-native-v1` schedule for invocation, input, reads, writes, deletes, scans, account writes, events, and output. Golden vectors cover exact gas, rollback, registry sealing, and fresh-process execution/state roots.
- Mempool admission enforces 2 MiB per transaction, 128 MiB and 4,096 total entries, 2,048 queued entries, 64 entries per sender, a nonce gap of 64, gas compatibility, and 128 batch operations.
- Canonical transaction size is capped at 2 MiB. An unauthenticated block body is bounded to approximately 12 MiB so the full canonical block remains within 16 MiB after reserving 4 MiB for the quorum certificate. Transaction, receipt, validator, and finality-signature counts are bounded.
- Chain IDs are bounded to 128 canonical bytes. The fixed validator set is capped at 4,096 members; a maximum certificate is approximately 1.88 MiB and fits inside the reserved budget.
- Signed max-fee exposure, fee arithmetic, sender value exposure, and paymaster capacity are checked before execution. Priority fees are consensus-bound to the proposer, and imported blocks must inherit protocol gas parameters.
- Zero-value transfers are state no-ops and cannot create recipient accounts. Each account is limited to 4,096 storage entries, 256 bytes per key, 64 KiB per value, and 8 MiB of total key/value bytes. A clone/restore-safe derived byte cache makes ordinary writes O(1) in existing entry count; positive-value account growth still requires the transactional-state/economics solution below.

### Restart, Fork Choice, And Local Finality Safety

- Snapshot version 2 persists a generation and checksum together with the explicitly timestamped genesis, state, known blocks, finality lock, partial finality votes, and evidence.
- Genesis JSON is strict public consensus data and contains no private key. `init` and `keygen` create separate non-overwriting validator key files with Unix mode `0600`; node startup requires exactly one key source and an address present in the shared validator set. Windows DACL enforcement and encrypted/remote signing remain production gates.
- Startup is fail closed. The node requires a valid data manifest and genesis identity, rejects unknown or non-canonical snapshot state, replays canonical and known chains from genesis, and compares every state root. Missing chain identity files in an initialized directory are not treated as a fresh chain.
- Persistent nodes hold an exclusive cross-platform lock for the complete node lifecycle, reject a second process on the same data directory, and release the lock only through `Node.Close` or process exit.
- Snapshot reads and writes share a 512 MiB ceiling, so the running node cannot commit a file that its next restart must reject. Known forks are bounded to 16 blocks per height and 4,096 side blocks, and restore capacity is checked before state replay.
- A rename followed by failed directory synchronization is classified as commit-uncertain and forces sticky HALT; the node cannot continue signing from rolled-back memory. Transactional storage/WAL is still required to establish power-loss durability.
- Recovery retains state only for the child-referenced frontier instead of caching a complete `Store` for every historical block. Because membership is fixed, all restored blocks share one validator slice instead of copying up to 4,096 addresses per height.
- The highest valid quorum certificate becomes a monotonic finality lock. A longer conflicting fork cannot roll it back. A later certificate for the same block can upgrade or merge signer subsets, while two valid conflicting certificates trigger a checksummed persistent sticky halt.
- Partial finality votes and equivocation evidence survive restart, so a restart cannot erase the node's double-sign detection memory.
- Canonical evidence identity includes the sorted conflicting block-hash pair. The persistent one-time offence tombstone binds chain, validator, and height, so A/B followed by A/C cannot slash newly staked funds again. Applicable automatic slash work is reconstructed after restart.
- Genesis time is explicit, positive, committed into the genesis hash, and bounded away from signed timestamp exhaustion. Imported block time may advance by at most one hour from its parent, while local production clamps to half that step. A proposer cannot exhaust the signed timestamp domain in one block.
- Without a quorum certificate, the `finalized` view remains at genesis. Head-minus-depth is exposed only as `depth_confirmation`; it is not represented as Byzantine finality.
- These guarantees harden the local PoA harness. They do not implement CometBFT rounds, prevote/precommit, peer gossip, evidence propagation, block sync, state sync, or epoch transitions.

### Validator Safety Boundary

- The current consensus validator set is fixed by genesis.
- `validator.join` and `validator.leave` are disabled at schema and admission layers until a finalized CometBFT epoch-transition protocol exists.
- `validator.slash` requires two canonical low-s signatures from the target validator for different block hashes at the same chain and height. Each validator-height offence is single-use regardless of the proving pair and clears all current stake, but does not mutate the fixed consensus validator set immediately.
- This is an explicit safety restriction, not a completed dynamic staking lifecycle.

### Governance Safety Boundary

- `proposal.submit`, `vote`, and `proposal.execute` are disabled before admission across executor, node, RPC, and CLI paths.
- The removed current-stake demo allowed stake to be unstaked, transferred, restaked under another address, and counted repeatedly. Merely correcting repeat-vote arithmetic would not prevent this capital reuse.
- Re-enabling governance requires finalized voting-power snapshots, unbonding or per-proposal stake locks, proposal deposits, quorum/timelock/authority rules, versioned activation, migration vectors, and adversarial economics tests.

### Immutable Reads And Public RPC Bounds

- Published canonical state is immutable. Commits switch a state pointer, and `state.ReadView` exposes a read-only capability.
- `eth_call` captures an O(1) immutable view under the node lock, then executes outside it. Long concurrent reads no longer block block production/reorg and continue to observe their original snapshot.
- All ordinary HTTP paths share 32 execution slots, with liveness and valid WebSocket upgrades on independent bounded paths. POST bodies are capped at 16 MiB. JSON-RPC is additionally limited to 32 concurrent requests, 100 batch items, a 5 MiB request body, and a 16 MiB batch response enforced while each child response is recorded. Txpool content is rejected above an 8 MiB source budget before cloning.
- `eth_feeHistory` is limited to 1,024 blocks. Log queries are limited to a 10,000-block range, 1,000 results, an 8 MiB HTTP response, 256 addresses, four topics, and 256 alternatives per topic. Filter cursors advance only after successful delivery.
- `eth_call` explicitly supports only `latest`. Pending nonce calculation no longer replays the entire mempool while holding the global node lock.
- Installed filters use unpredictable IDs, are capped at 1,024, actively expire after five idle minutes, limit log definitions to 64 KiB, and return at most 1,000 block hashes per poll. Pending filters are separately capped at 64 and collectively retain at most 65,536 hashes.
- WebSockets are capped at 64 connections, 64 subscriptions per connection, 1,024 subscriptions per node, and 64 log subscriptions. Read-idle, Ping, and write deadlines release dead/slow clients; queues and per-block log-match work are bounded. Subscription TTL, principal-aware quotas, and sustained-load evidence remain open.
- `/health` and `/health/ready` return 503 during a sticky halt; `/health/live` remains available to distinguish a live but unsafe process.

## Verification Evidence

The implementation has passed full unit, race, vet, module-integrity, build, demo, and fresh-process vector runs during this hardening milestone:

```powershell
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go mod verify
go build -o $env:TEMP\chainlab-production-verify.exe ./cmd/chainlab
go test -count=2 -run 'FreshProcess' ./internal/core
go run ./cmd/chainlab demo
git diff --check
```

The hardened demo no longer executes unsafe governance transactions; it explicitly reports governance disabled while retaining transfer, sponsored transfer, batch, smart account, multisig, native contract, WASM, and stake-accounting paths. The cached `golang.org/x/vuln` v1.6.0 scanner completed offline with zero reachable vulnerabilities; it reported 17 advisories in required modules for which no vulnerable symbols are called. The online scanner invocation was not used as evidence because `proxy.golang.org` timed out.

After fixing node lock lifecycle coverage and revision-binding the pending-filter cache, the complete command sequence above was rerun on the final integrated working tree. The previously intermittent Ethereum type-2 REST test and the pending-filter regression also passed 10 consecutive focused runs.

Coverage includes the 64/65 WASM call-depth boundary, recursion/indirect/tail-call rejection, charged transfer/native/WASM/paymaster/batch failures, exact native gas vectors, fresh-process determinism, failed receipt production/import, strict snapshot schemas, canonical replay, corrupt/missing identity files, finality-lock reorg/restart attacks, conflicting certificates, vote persistence, fork-capacity limits, immutable concurrent reads, RPC failed status/cumulative gas, and ingress/query bounds.

## Current Production Blockers

Ordered by consensus and security dependency rather than feature visibility:

1. **Authoritative CometBFT ABCI++ lifecycle**
   - Implement proposal preparation/processing, finalize/commit, validator updates, evidence, queries, snapshots, block sync, and state sync as one versioned production lifecycle.
   - Replace the custom PoA/HTTP relay as the authoritative network with multi-process Byzantine consensus and P2P. Keep the local harness for deterministic differential testing only.
   - Design finalized epoch transitions before enabling validator join/leave.

2. **Crash-consistent transactional storage**
   - Replace whole-chain JSON snapshots with transactional versioned KV state and atomic indexes.
   - Add a WAL, kill/power-loss fault injection, verified snapshot import/export, deterministic recovery, corruption handling, migrations, and pruned/full/archive modes.
   - Replace sticky-halt handling of post-rename directory-sync uncertainty with an authoritative WAL/transaction recovery decision. Avoid rewriting the complete JSON snapshot for every partial vote.

3. **Protocol lifecycle and proofs**
   - Version consensus encodings, metering schedules, state schemas, and activation heights.
   - Add scheduled upgrades, deterministic migrations, incompatible-node rejection, rollback rules, transaction/receipt/state proofs, and an independent verifier.

4. **Validator signing and node rollback protection**
   - Add remote signer/HSM support with monotonic last-sign state, single-instance locking, and slashing-safe recovery.
   - Protect the entire data directory against rollback to an older but internally valid snapshot; the current checksums detect corruption but cannot prove external monotonicity.

5. **Resource and RPC governance**
   - Add gas-bounded/lazy proposal simulation, incremental txpool accounting, deterministic eviction/TTL/journal/restart, and orphaned-transaction reinsertion.
   - Add hard JIT/compiled-code RSS and lifecycle limits plus Linux multi-process load evidence.
   - Preserve the implemented all-request/POST/filter/WebSocket/txpool bounds, active expiry, pending-hash accounting, heartbeat/write deadlines, bounded queues, and log-work budget; add subscription TTL, principal-aware rate limiting, measured disconnect/backpressure policy, pagination, and sustained abuse tests.

6. **Release and security evidence**
   - Run pinned Linux/amd64 replay vectors, cross-process/cross-node differential tests, fuzz/property/fault/load/soak programs, and dependency/license scans.
   - Produce reproducible artifacts, checksums, SBOM/provenance, and external security/consensus review. Windows remains a development environment rather than a supported validator target for protocol version 1.

7. **Operations, economics, and ecosystem**
   - Add structured logs, metrics, alerts, sentry topology, backups, restore drills, incident runbooks, capacity evidence, and staged public/incentivized testnets.
   - Specify and simulate native supply, rewards, staking/unbonding/slashing, snapshot-based governance, treasury, deposits/timelocks, and upgrade authority before re-enabling governance transactions.
   - Build production wallet/SDK/indexer/explorer/token/oracle/interoperability support. USDT/USDC availability requires issuer-native deployment or an audited interoperability path; ChainLab cannot unilaterally mint the official assets.

## Next Immediate Work

1. Commit this local hardening milestone and record its exact test/security evidence. The ChainLab repository has no configured remote, so the code commit is local until a remote is deliberately added.
2. Define the first production protocol version and carry deterministic execution, included-failure settlement, native metering, fixed-validator rules, and finality safety assertions into differential ABCI++ tests.
3. Build the smallest end-to-end CometBFT multi-process network with authoritative proposal/finalize/commit behavior, then add evidence, snapshot, block-sync, and state-sync paths.
4. Introduce transactional versioned storage and power-loss testing before treating long-lived validator data as durable.
5. Establish the Linux/amd64 validator release target, remote-signer boundary, JIT/RSS limits, reproducible build artifacts, and baseline operational telemetry.

Asset and deployment work stays downstream of the production core: specify the native gas asset and economics, define a production fungible-token standard and issuer controls, add wallet/indexer/explorer/oracle/DEX interfaces, select an audited IBC/bridge or issuer-native stablecoin path, and size validator/sentry/RPC/archive hardware from Linux multi-process load results rather than estimates.

## Completion Audit

Do not call the long-running production-chain goal complete until every gate has authoritative evidence:

- deterministic execution and cross-node replay;
- Byzantine consensus/P2P/evidence/block sync/state sync through the selected production stack;
- crash-consistent versioned storage and historical modes;
- protocol upgrades, migrations, and independently verified inclusion proofs;
- bounded resources and abuse resistance under load;
- fuzz/property/race/fault/load/soak/security-review evidence;
- protected validator signing, operations, and disaster recovery;
- specified economics, governance, ecosystem, and staged network evidence;
- documentation and release artifacts matching deployed behavior.

The local PoA harness now has materially stronger execution, recovery, finality-lock, and RPC safety properties. It is still a harness, not a completed production sovereign chain. A green local suite is necessary but not sufficient for mainnet readiness.
