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

### CometBFT ABCI++, Transactional Storage, And State Sync Lifecycle

- CometBFT is pinned at v0.39.3. Its module checksum in `go.sum` matches the signed `sum.golang.org` lookup. The initial ABCI dependency set matched upstream v0.39.3 sums; the full node dependency set is also sumdb-verified and deliberately raises `go-libp2p` to v0.48.0, `quic-go` to v0.59.1, and gRPC-Go to v1.79.3 to close reachable advisories while retaining v0.39.3 process compatibility evidence.
- `internal/abci` implements every v0.39.3 `Application` method. `Info`, strict latest-state `Query`, `CheckTx`, bounded `InsertTx`/`ReapTxs`, `InitChain`, proposal methods, finalize/commit, empty vote extensions, and verified snapshot methods no longer inherit permissive defaults.
- Canonical genesis app state binds `chainlab-v1`, chain ID, block gas, and the complete ChainLab state. `InitChain` requires an exact fixed validator set derived from compressed secp256k1 keys, equal voting power, a 16 MiB CometBFT block ceiling, matching block gas, bounded positive evidence retention, and disabled vote extensions.
- `PrepareProposal` performs bounded sequential simulation. `ProcessProposal` and `FinalizeBlock` independently replay every transaction from the committed state and reject malformed, invalid, over-count, over-byte, over-gas, wrong-height, and unknown-proposer blocks. Included deterministic failures retain their failed receipt and nonce/fee settlement.
- `FinalizeBlock` creates an unpublished candidate. Only an in-sequence `Commit` switches the committed state and app hash; duplicate or out-of-sequence finalize/commit calls fail. The application hash binds protocol, chain, height, Comet block hash, proposer, gas/base-fee state, transaction root, receipt root, normalized evidence root, and state root.
- Receipt events are converted with sorted attributes, so Go map iteration cannot change CometBFT transaction results. Fixed-set misbehavior is validated, normalized, committed into the evidence root, and emitted as events, but protocol v1 deliberately does not claim validator updates or evidence-to-slashing completion.
- The app-side mempool is capped at 4,096 transactions and 128 MiB, supports sequential nonces, deduplicates exact wire transactions, respects reap byte/gas limits, removes committed transactions, and deterministically rebuilds after commit.
- Differential tests feed the same successful and included-failure transaction corpus through the local PoA harness and ABCI++, comparing every receipt plus state, transaction, and receipt roots. A fixed `chainlab-v1` ABCI golden vector also matches across fresh processes.
- `cmd/chainlab-abci` loads a bounded regular file, requires canonical application-genesis bytes, and runs the official v0.39.3 socket server with signal-aware shutdown. An official socket client now exercises `Info`, `InitChain`, `CheckTx`, proposal processing, finalize/commit, and query across the wire.
- `cmd/chainlab-comet` and `internal/cometnode` generate and run four independent secp256k1 validator homes. Each home has a distinct private-validator key/last-sign state, P2P key, Comet database, ABCI/RPC/P2P port, full-mesh persistent peers, canonical app genesis, and a strict fail-closed node document. Startup cross-checks the private key, public identity, equal-power genesis set, app state, consensus parameters, and generated files before Comet starts.
- A real OS-process integration test runs four ABCI servers plus four Comet nodes, verifies a full peer mesh and raw transaction commitment, stops one validator while the remaining three continue, block-syncs the lagging node, performs a durable app restart, restarts a separate empty app to prove local block-store replay, and then removes that validator's Comet/application data while retaining its monotonic H/R/S file. The reset validator restores an ABCI snapshot through real Comet state sync using two light-client RPC sources, rejoins consensus, and broadcasts the next transaction. At each checkpoint every app commitment is internally height-consistent, all nodes agree on a common-height block/header app hash, and their current state root and sender nonce converge even if live RPC sampling catches adjacent heights.
- The ABCI application stores each height under a versioned Pebble prefix. One synchronous WAL batch atomically writes the commitment/app hash, per-key state, raw transactions, receipts, current-height pointer, and block-hash index. Startup binds the database to canonical genesis, recomputes state/transaction/receipt roots, verifies manifest checksums/counts, and rejects corruption or partial data before publication.
- Kill tests cover immediate process exit after a synchronized commit and forced termination around a multi-megabyte batch commit. Recovery observes only the complete previous or complete next version. Persistence failure injection proves candidate state is never published before durable success.
- Snapshot format 1 is canonical, bounded to 512 MiB, split into exact 1 MiB chunks, and binds genesis, height, app hash, state root, content hash, document checksum, proposer bindings, state, transactions, and receipts. Direct tests cover multi-chunk out-of-order import, untrusted app hashes, corrupt content, restore failure, restart, and stable advertised chunks across rotation.
- Socket mode is deliberately locked to Comet's bounded `flood` mempool. CometBFT v0.39.3 defines `InsertTx`/`ReapTxs` on its interfaces and client but does not dispatch them in the socket server, so app-mempool-over-socket is not claimed. The direct app-side mempool tests remain useful but cover a different transport boundary.
- The app hash binds height and block identity, so it changes for every empty block. `create_empty_blocks=false` would still trigger unbounded proof-block production; generated nodes instead configure explicit continuous blocks with a 750 ms commit interval. Normal restart keeps Comet's default `double_sign_check_height=0`; setting it positive rejects any retained validator key found in recent valid commits, while monotonic H/R/S protection remains in `priv_validator_state.json`.
- This is an initial crash-consistent application/network lifecycle, not a completed production network. The current storage writes a complete per-key state version each height and retains all versions; incremental MVCC/deltas, migrations, pruned/full/archive modes, backup/restore drills, evidence-to-slashing, validator epochs, partitions, Byzantine faults, rolling upgrades, remote signing, and Linux load/soak evidence remain open production gates.

## Verification Evidence

The implementation has passed full unit, race, vet, dependency-tidiness, build, demo, and fresh-process vector runs during this hardening milestone:

```powershell
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go mod tidy -diff
go build -o $env:TEMP\chainlab-production-verify.exe ./cmd/chainlab
go test -count=2 -run 'FreshProcess' ./internal/core
go test -count=1 ./internal/abci
go test -count=1 ./internal/cometnode
go test -count=3 -run 'FourValidatorProcessesRestartReplayBlockAndStateSync' ./internal/cometnode
go test -count=3 -run 'Snapshot' ./internal/abci
go test -count=2 -run 'ABCIExecutionMatchesAcrossFreshProcesses' ./internal/abci
go build -o $env:TEMP\chainlab-abci-production-verify.exe ./cmd/chainlab-abci
go build -o $env:TEMP\chainlab-comet-production-verify.exe ./cmd/chainlab-comet
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
go run ./cmd/chainlab demo
git diff --check
```

The hardened demo no longer executes unsafe governance transactions; it explicitly reports governance disabled while retaining transfer, sponsored transfer, batch, smart account, multisig, native contract, WASM, and stake-accounting paths. On the final integrated Pebble/state-sync tree, fixed `govulncheck` v1.6.0 completed through the signed alternate Go proxy/sumdb path with zero reachable vulnerabilities; two imported-package and 20 required-module advisories do not reach vulnerable symbols.

After fixing node lock lifecycle coverage and revision-binding the pending-filter cache, the complete command sequence above was rerun on the final integrated working tree. The previously intermittent Ethereum type-2 REST test and the pending-filter regression also passed 10 consecutive focused runs.

Coverage includes the 64/65 WASM call-depth boundary, recursion/indirect/tail-call rejection, charged transfer/native/WASM/paymaster/batch failures, exact native gas vectors, fresh-process determinism, failed receipt production/import, canonical replay, corrupt/missing identity files, finality-lock reorg/restart attacks, conflicting certificates, vote persistence, fork-capacity limits, immutable concurrent reads, RPC failed status/cumulative gas, and ingress/query bounds. The ABCI persistence/snapshot suite additionally covers atomic height/app-hash/state/transaction/receipt/index recovery, abrupt exit, forced kill around WAL sync, missing entries, receipt-root corruption, multi-chunk out-of-order state sync, trusted-app-hash rejection, content corruption, restore failure, snapshot rotation stability, and durable restart.

The final ABCI++ application tree additionally passed full unit tests, the full race suite, a focused ABCI race run, vet, tidy-diff, production CLI build, demo, two-run ABCI/core fresh-process vectors, and an offline `govulncheck` v1.6.0 scan with zero reachable vulnerabilities. That earlier application-only graph contained advisories in three imported packages and 20 required modules, but no vulnerable symbol was called.

Adding the complete Comet node initially made three advisories symbol-reachable: QPACK trailer expansion in `quic-go` v0.59.0, gRPC missing-leading-slash authorization bypass in v1.79.2, and an unpatched `pion/dtls/v2` AES-GCM nonce issue pulled through Comet's compiled libp2p/WebRTC path even though ChainLab disables libp2p at runtime. The dependency floor now uses `quic-go` v0.59.1 and gRPC-Go v1.79.3, while `go-libp2p` v0.48.0 migrates the STUN/WebRTC graph to `pion/dtls/v3` and removes `dtls/v2` from the main module. The four-validator process suite, full tests, race, vet, and all builds pass with these overrides. A final fixed `govulncheck` v1.6.0 scan reports zero reachable vulnerabilities; two imported-package and 20 required-module advisories remain without reachable vulnerable symbols.

Windows `go mod verify` is not recorded as green for the CometBFT tree. The signed v0.39.3 module zip contains `.github/workflows/e2e-nightly-38x.yml ` with a trailing space; Windows normalizes the extracted cache path to the no-space name, so Go reports the directory as modified even though the file bytes match. Both `goproxy.cn/sumdb/sum.golang.org` and `sum.golang.google.cn` returned the committed module sums `h1:UegHXskZNomsijmm29nL5NkeXtnzkme6fg+q1hPQnEI=` and `h1:PmNfvtw256BC41ad0FABts236CSZnvZ0kjPOciBwTdM=`. The initial 60 non-Comet ABCI checksum lines match CometBFT v0.39.3's upstream `go.sum`; the larger node graph and security overrides were resolved through signed sumdb. A clean Linux module-cache verification remains part of the Linux/amd64 validator release gate.

## Current Production Blockers

Ordered by consensus and security dependency rather than feature visibility:

1. **Authoritative CometBFT ABCI++ lifecycle**
   - The `chainlab-v1` lifecycle now runs across real socket and Comet node processes. Four-validator tests cover proposal/finalize/commit, full-mesh P2P, transaction gossip, 3-of-4 progress, Comet restart, block sync, durable app restart, fresh-app replay, destructive-data state sync, common-height block/app-hash equality, and current state-root convergence. Keep the local harness for deterministic differential testing only.
   - Complete validator updates, evidence-to-slashing rules, rolling restart/upgrade, partition, delayed/missing proposer, equivocation, and Byzantine fault tests as one versioned production lifecycle.
   - Design finalized epoch transitions before enabling validator join/leave.

2. **Crash-consistent transactional storage**
   - Preserve the implemented Pebble synchronous-WAL batch that atomically binds versioned per-key state, height, app hash, transactions, receipts, and block index; replace full-state-per-height rewriting with an incremental MVCC/delta design before load testing large state.
   - Add deterministic migrations, explicit pruned/full/archive policies, historical reads, compaction/retention evidence, backup/restore, additional filesystem power-loss coverage, and rollback protection. Verified snapshot import/export, state sync, corruption rejection, and initial kill recovery are implemented.
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

1. Design certified validator epoch transitions and evidence-to-slashing behavior before enabling validator updates, then exercise them through real Comet processes.
2. Replace full-state-per-height persistence with bounded incremental MVCC/deltas and add migrations, pruned/full/archive retention, historical reads, compaction, backup/restore, and broader power-loss evidence.
3. Extend the multi-process network with partitions, missing proposers, equivocation, rolling restart/upgrade, and Byzantine fault cases.
4. Establish the Linux/amd64 validator release target, remote-signer boundary, JIT/RSS limits, reproducible build artifacts, and baseline operational telemetry.

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
