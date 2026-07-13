# ChainLab Progress And Next Steps

Status date: 2026-07-13
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

- Snapshot version 3 persists a generation and checksum together with the explicitly timestamped genesis, state, known blocks, finality lock, partial finality votes, evidence, and pending/queued txpool. V2 snapshots migrate deterministically; a manifest claiming v3 rejects rollback to v2.
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

- The local harness and protocol V1 consensus validator sets remain fixed by genesis.
- Protocol V2 genesis can pre-authorize up to 64 future validators through ordered, chain-bound certificates carrying `2/3+1` canonical genesis-validator signatures. Their power is derived as `floor(genesis_stake/1000)`, and certified identities persist across restart, emit the Comet `H+2` positive-power update, and can propose only during their epoch-aligned active interval. Optional `unbonding_epochs>=2` keeps their stake locked until that many complete epochs after evidence removal; zero preserves the legacy permanent lock and roots.
- `validator.join` and `validator.leave` are disabled at schema and admission layers until a finalized CometBFT epoch-transition protocol exists.
- `validator.slash` requires two canonical low-s signatures from the target validator for different block hashes at the same chain and height. Each validator-height offence is single-use regardless of the proving pair and clears all current stake, but does not mutate the fixed consensus validator set immediately.
- Runtime admission/re-entry and power changes, voluntary-leave unbonding/rewards, and key operations remain disabled; evidence-removal unbonding plus the offline genesis certificate path are not a completed dynamic staking lifecycle.

### Governance Safety Boundary

- `proposal.submit`, `vote`, and `proposal.execute` are disabled before admission across executor, node, RPC, and CLI paths.
- The removed current-stake demo allowed stake to be unstaked, transferred, restaked under another address, and counted repeatedly. Merely correcting repeat-vote arithmetic would not prevent this capital reuse.
- Re-enabling governance requires finalized voting-power snapshots, unbonding or per-proposal stake locks, proposal deposits, quorum/timelock/authority rules, versioned activation, migration vectors, and adversarial economics tests.

### Immutable Reads And Public RPC Bounds

- Published canonical state is immutable. Commits switch a state pointer, and `state.ReadView` exposes a read-only capability.
- `eth_call` captures an O(1) immutable view under the node lock, then executes outside it. Long concurrent reads no longer block block production/reorg and continue to observe their original snapshot.
- All ordinary HTTP paths share 32 execution slots, with liveness and valid WebSocket upgrades on independent bounded paths. POST bodies are capped at 16 MiB. JSON-RPC is additionally limited to 32 concurrent requests, 100 batch items, a 5 MiB request body, and a 16 MiB batch response enforced while each child response is recorded. Txpool content is rejected above an 8 MiB source budget before cloning.
- Txpool admission maintains an incremental canonical-byte total across pending/queued insertion, replacement, promotion, block removal, canonical reorg, and rollback paths; the 128 MiB admission check no longer rescans the complete pool.
- Persistent local nodes commit txpool mutations in the same snapshot generation as chain/state changes. Restart revalidates canonical size/count/sender bounds, hash and sender-nonce uniqueness, signatures, pending replay, queued nonce gaps, fees, and execution before publishing the pool; a failed snapshot write rolls the admission back in memory.
- Canonical reorg rebuild now considers transactions from orphaned old-canonical blocks in deterministic block/transaction order. It skips hashes included by the new branch, stale or newly invalid transactions, current-pool sender/nonce conflicts, and additions beyond count/byte/queued/per-sender limits; accepted entries are revalidated, promoted when gaps close, and committed atomically with the new head.
- `eth_feeHistory` is limited to 1,024 blocks. Log queries are limited to a 10,000-block range, 1,000 results, an 8 MiB HTTP response, 256 addresses, four topics, and 256 alternatives per topic. Filter cursors advance only after successful delivery.
- `eth_call` explicitly supports only `latest`. Pending nonce calculation no longer replays the entire mempool while holding the global node lock.
- Installed filters use unpredictable IDs, are capped at 1,024, actively expire after five idle minutes, limit log definitions to 64 KiB, and return at most 1,000 block hashes per poll. Pending filters are separately capped at 64 and collectively retain at most 65,536 hashes.
- WebSockets are capped at 64 connections, 64 subscriptions per connection, 1,024 subscriptions per node, and 64 log subscriptions. Read-idle, Ping, and write deadlines release dead/slow clients; queues and per-block log-match work are bounded. Subscription TTL, principal-aware quotas, and sustained-load evidence remain open.
- `/health` and `/health/ready` return 503 during a sticky halt; `/health/live` remains available to distinguish a live but unsafe process.

### CometBFT ABCI++, Transactional Storage, And State Sync Lifecycle

- CometBFT is pinned at v0.39.3. Its module checksum in `go.sum` matches the signed `sum.golang.org` lookup. The initial ABCI dependency set matched upstream v0.39.3 sums; the full node dependency set is also sumdb-verified and deliberately raises `go-libp2p` to v0.48.0, `quic-go` to v0.59.1, and gRPC-Go to v1.79.3 to close reachable advisories while retaining v0.39.3 process compatibility evidence.
- `internal/abci` implements every v0.39.3 `Application` method. `Info`, latest and retained-height `Query`, `CheckTx`, bounded `InsertTx`/`ReapTxs`, `InitChain`, proposal methods, finalize/commit, empty vote extensions, and verified snapshot methods no longer inherit permissive defaults.
- Canonical genesis app state binds `chainlab-v1`, chain ID, block gas, and the complete ChainLab state. `InitChain` requires an exact fixed validator set derived from compressed secp256k1 keys, equal voting power, a 16 MiB CometBFT block ceiling, matching block gas, bounded positive evidence retention, and disabled vote extensions.
- `PrepareProposal` performs bounded sequential simulation. `ProcessProposal` and `FinalizeBlock` independently replay every transaction from the committed state and reject malformed, invalid, over-count, over-byte, over-gas, wrong-height, and unknown-proposer blocks. Included deterministic failures retain their failed receipt and nonce/fee settlement.
- `FinalizeBlock` creates an unpublished candidate. Only an in-sequence `Commit` switches the committed state and app hash; duplicate or out-of-sequence finalize/commit calls fail. The application hash binds protocol, chain, height, Comet block hash, proposer, gas/base-fee state, transaction root, receipt root, normalized evidence root, and state root.
- Receipt events are converted with sorted attributes, so Go map iteration cannot change CometBFT transaction results. Fixed-set misbehavior is validated, normalized, committed into the evidence root, and emitted as events, but protocol v1 deliberately does not claim validator updates or evidence-to-slashing completion.
- The app-side mempool is capped at 4,096 transactions and 128 MiB, supports sequential nonces, deduplicates exact wire transactions, respects reap byte/gas limits, removes committed transactions, and deterministically rebuilds after commit.
- Differential tests feed the same successful and included-failure transaction corpus through the local PoA harness and ABCI++, comparing every receipt plus state, transaction, and receipt roots. A fixed `chainlab-v1` ABCI golden vector also matches across fresh processes.
- `cmd/chainlab-abci` loads a bounded regular file, requires canonical application-genesis bytes, and runs the official v0.39.3 socket server with signal-aware shutdown. An official socket client now exercises `Info`, `InitChain`, `CheckTx`, proposal processing, finalize/commit, and query across the wire.
- `cmd/chainlab-comet` and `internal/cometnode` generate and run four independent secp256k1 validator homes. Each home has a distinct private-validator key/last-sign state, P2P key, Comet database, ABCI/RPC/P2P port, full-mesh persistent peers, canonical app genesis, and a strict fail-closed node document. Startup cross-checks the private key, public identity, equal-power genesis set, app state, consensus parameters, and generated files before Comet starts.
- A real OS-process integration test runs four ABCI servers plus four Comet nodes, verifies a full peer mesh and raw transaction commitment, stops one validator while the remaining three continue, block-syncs the lagging node, performs a durable app restart, restarts a separate empty app to prove local block-store replay, and then removes that validator's Comet/application data while retaining its monotonic H/R/S file. The reset validator restores an ABCI snapshot through real Comet state sync using two light-client RPC sources, rejoins consensus, and broadcasts the next transaction. At each checkpoint every app commitment is internally height-consistent, all nodes agree on a common-height block/header app hash, and their current state root and sender nonce converge even if live RPC sampling catches adjacent heights.
- Application Store V2 keeps canonical flat live state, per-height sets/deletes, and periodic full checkpoints. One synchronous Pebble batch atomically advances those artifacts with commitment/app hash, raw transactions, receipts, current-height pointer, minimum retained height, block-hash index, and pruning. Startup binds canonical genesis and the exact storage profile, validates the current live state and retained checkpoint boundary, and rejects checksum/count/root, delta, result, index, or profile corruption before publication.
- Store V2 delta generation is mutation-aware: `state.Store` clones carry detached dirty keys for account metadata, individual storage entries, code, stake, proposals, parameters, validator identities/offences, and the validator slice until a successful durable commit. Ordinary heights encode only those keys and collapse write-back-to-prior-value changes. Checkpoint heights compare the journal against a complete flatten/diff and reject the commit on any mismatch.
- `archive` retains every locally available height, `full` retains at least its configured recent window from a checkpoint boundary, and `pruned` retains only latest. ABCI queries can replay retained checkpoints/deltas; height zero remains Comet's latest-height sentinel. State sync rebases the minimum history to the restored trusted height instead of claiming unavailable prehistory.
- A deterministic V1-to-V2 migration builds a shadow V2 range appropriate for the selected profile and activates it atomically. An interrupted migration is discarded and rebuilt from authoritative V1 data. Consistent Pebble checkpoint backups open directly as application data, backup destinations cannot overwrite or nest inside live data, manual compaction is exposed, and profile changes fail closed.
- Kill tests cover immediate process exit after a synchronized commit and forced termination around a multi-megabyte batch commit. Recovery observes only the complete previous or complete next version. Persistence failure injection proves candidate state is never published before durable success.
- Snapshot format 1 is canonical, bounded to 512 MiB, split into exact 1 MiB chunks, and binds genesis, height, app hash, state root, content hash, document checksum, proposer bindings, state, transactions, and receipts. Direct tests cover multi-chunk out-of-order import, untrusted app hashes, corrupt content, restore failure, restart, and stable advertised chunks across rotation.
- Socket mode is deliberately locked to Comet's bounded `flood` mempool. CometBFT v0.39.3 defines `InsertTx`/`ReapTxs` on its interfaces and client but does not dispatch them in the socket server, so app-mempool-over-socket is not claimed. The direct app-side mempool tests remain useful but cover a different transport boundary.
- The app hash binds height and block identity, so it changes for every empty block. `create_empty_blocks=false` would still trigger unbounded proof-block production; generated nodes instead configure explicit continuous blocks with a 750 ms commit interval. Normal restart keeps Comet's default `double_sign_check_height=0`; setting it positive rejects any retained validator key found in recent valid commits, while monotonic H/R/S protection remains in `priv_validator_state.json`.
- This is a crash-consistent incremental application/network lifecycle, not a completed production network. Store V2 removes complete per-height disk copies and full-state delta comparison at ordinary heights, but flat integrity roots, V3 full-state proof roots, and checkpoints still traverse current state. Wider power-loss phases, operational restore drills, external rollback protection, dynamic message faults/asymmetric partitions, broader Byzantine faults, signed release/Comet binary rolling drills, remote signing, and Linux load/soak evidence remain open production gates. The exact storage contract is in `docs/chainlab-application-storage-v2.md`.

### ChainLab V2 Evidence Slashing And Epoch Removal

- `chainlab-v2` is an explicit protocol version; `chainlab-v1` retains its fixed-set app-hash and lifecycle semantics.
- V2 genesis commits epoch length, duplicate-vote and light-client-attack slash ratios, and both Comet evidence-retention dimensions. `InitChain` requires exact retention and app-version agreement.
- Genesis secp256k1 public keys are persistently bound to Comet consensus addresses, ChainLab accounts, equal voting power, and active/inactive height intervals.
- Comet-authenticated misbehavior is independently checked against the historical validator set. The application mirrors Comet v0.39.3's expiry rule: evidence expires only after both block and duration limits are exceeded.
- Evidence runs before transaction simulation, preventing same-block stake movement from escaping punishment. Slash arithmetic is overflow-safe and rounds fractional base units upward.
- One tombstone per ChainLab validator and offence height prevents proof reordering, evidence-type changes, restart, replay, or state sync from punishing the same offence twice.
- Removal is scheduled at the first eligible epoch boundary. The power-zero `ValidatorUpdate` is returned exactly two heights earlier because CometBFT applies an update from height `H` at `H+2`; proposer and vote-extension eligibility change at the same effective height.
- Validator lifecycle, offences, scheduled removals, epoch, and validator root are bound into state/app hashes, Pebble manifests, startup checks, application snapshots, and state sync.
- Direct tests cover deterministic penalty, repeat evidence, policy/age rejection, epoch timing, removed-proposer rejection, restart, and snapshot restore. Real four-validator networks prove a single 4-to-3 removal and two offences producing one deterministic two-update batch, an atomic 4-to-2 transition, continued transaction progress, and four-full-node convergence.
- V2 currently supports evidence-driven removal only. Join, voluntary leave, re-entry, stake-derived power, unbonding, rewards, and governance transitions remain disabled. The normative behavior and exclusions are in `docs/chainlab-v2-validator-lifecycle.md`.

### ChainLab V3 Scheduled Proof Roots

- A canonical V2 genesis may commit one V3 activation at height `A >= 2`; the schedule is bound into genesis/database identity and cannot be edited after initialization.
- `FinalizeBlock(A-1)` still commits V2 roots while returning a Comet consensus-parameter update for app version 3. Height `A` commits protocol V3 and switches transaction, receipt, and full flat-state roots together.
- V3 uses exact-total/index/domain-separated Keccak Merkle trees with deterministic power-of-two padding. Transaction and receipt leaves are canonical protocol objects; state leaves cover the Store V2 flat-key domains.
- `/proof/transaction`, `/proof/receipt`, and `/proof/account` serve current or retained V3 heights. V2 heights explicitly reject proofs because their legacy roots cannot authenticate a Merkle path.
- `cmd/chainlab-proof` parses a bounded canonical envelope and requires a separately supplied trusted root. It rejects unknown/non-canonical/tampered protocol, domain, index, total, leaf, sibling, and state-key data.
- Direct tests cover activation timing, app-version negotiation, simulated old-binary rejection, schedule immutability, restart, historical proof replay, and V3 snapshot/state sync. Fixed proof and fresh-process vectors prevent silent root drift.
- A real four-validator Comet network crosses the scheduled height, reports app version 3 in consensus params, converges on V3 commitments, and returns independently verified account proofs from all nodes.
- V3 remains the exact-total inclusion-proof layer. The later V4 upgrade adds sparse state and broader proof routes; runtime governance scheduling, rollback policy, trusted light-client integration, a second implementation, and production proof load remain open. Exact V3 behavior is in `docs/chainlab-v3-proofs-and-upgrades.md`.

### ChainLab V4 Mutation-Aware Sparse State

- A canonical V2 genesis may append a later V4 activation after V3. Heights are strictly increasing, the full schedule is bound to genesis/database identity, and version-3 binaries fail closed before publishing the version-4 transition.
- V4 retains V3 transaction/receipt roots and replaces only state with a fixed-depth, domain-separated sparse Merkle map. Compressed proofs support both membership and non-membership.
- FinalizeBlock and Store V2 update copy-on-write accumulators from the mutation journal. Overlays publish only after durable persistence succeeds; ordinary commits do not rebuild or sort complete state.
- Store manifests explicitly distinguish legacy flat roots from V4 sparse roots. Checkpoints validate the mutation journal, rebuild the complete sparse tree, and cross-check the incremental root. Old manifests without a root-protocol field remain valid.
- `/proof/account` supports existing and absent addresses at V4 heights. `/proof/state` covers account storage, code, stake, proposals, parameters, validators, identities, and offences. `chainlab-proof` automatically verifies V3 inclusion or V4 sparse envelopes.
- Fixed vectors and direct tests cover incremental/full equivalence, tampering, non-canonical proofs, schedule negotiation, persistence failure isolation, mixed historical roots, restart, snapshot/state sync, and four-validator V3/V4 activation. Exact behavior is in `docs/chainlab-v4-sparse-state.md`.

### ChainLab V5 Validator Offence Retention

- A canonical V2 genesis may append V5 after V3 and V4. Comet publishes app version 5 one height before activation; version-4 binaries reject before candidate publication.
- V5 offences commit the original evidence Unix seconds/nanoseconds. V2-V4 tombstones omit that data and remain permanent rather than guessing the duration-age condition.
- Before V5 proposal execution, timestamped offences compact only when block age and duration age are both strictly greater than the genesis evidence policy. One-dimensional expiry retains the tombstone; already expired evidence is rejected before deletion can expose its key.
- Compaction runs on the detached proposal state, enters mutation deltas, validator/sparse roots, restart/history, snapshot/state sync, and emits deterministic prune events. Sparse offence proofs transition from membership to non-membership.
- Direct boundary, old-binary, schedule, restart, state-sync, fixed fresh-process, real four-validator V3/V4/V5 activation, and pre-activation rolling application replacement tests pass. Exact behavior is in `docs/chainlab-v5-offence-retention.md`.

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
go test -count=3 -run 'FourValidatorV2EvidenceSlashingAndEpochRemoval' ./internal/cometnode
go test -count=3 -run 'FourValidatorV3ScheduledUpgradeAndProofs' ./internal/cometnode
go test -count=3 -run 'Snapshot' ./internal/abci
go test -count=3 -run 'ValidatorV2' ./internal/abci
go test -count=5 -run 'Test(FlatStateDelta|ArchiveStorage|FullStorage|PrunedStorage|LegacyV1Storage|IncrementalVersion)' ./internal/abci
go test -count=3 -run 'TestPersistentApplication(SurvivesAbruptProcessExitAfterCommit|RecoversWholeVersionWhenKilledDuringSync)' ./internal/abci
go test -count=2 -run 'ABCIExecutionMatchesAcrossFreshProcesses' ./internal/abci
go test -count=5 -run 'Test(Protocol|ABCI.*FreshProcesses)' ./internal/abci
go test -count=10 ./pkg/proof ./cmd/chainlab-proof
go build -o $env:TEMP\chainlab-abci-production-verify.exe ./cmd/chainlab-abci
go build -o $env:TEMP\chainlab-comet-production-verify.exe ./cmd/chainlab-comet
go build -o $env:TEMP\chainlab-proof-production-verify.exe ./cmd/chainlab-proof
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
go run ./cmd/chainlab demo
git diff --check
```

The hardened demo no longer executes unsafe governance transactions; it explicitly reports governance disabled while retaining transfer, sponsored transfer, batch, smart account, multisig, native contract, WASM, and stake-accounting paths. On the final integrated Pebble/state-sync tree, fixed `govulncheck` v1.6.0 completed through the signed alternate Go proxy/sumdb path with zero reachable vulnerabilities; two imported-package and 20 required-module advisories do not reach vulnerable symbols.

After fixing node lock lifecycle coverage and revision-binding the pending-filter cache, the complete command sequence above was rerun on the final integrated working tree. The previously intermittent Ethereum type-2 REST test and the pending-filter regression also passed 10 consecutive focused runs.

Coverage includes the 64/65 WASM call-depth boundary, recursion/indirect/tail-call rejection, charged transfer/native/WASM/paymaster/batch failures, exact native gas vectors, fresh-process determinism, failed receipt production/import, canonical replay, corrupt/missing identity files, finality-lock reorg/restart attacks, conflicting certificates, vote persistence, fork-capacity limits, immutable concurrent reads, RPC failed status/cumulative gas, and ingress/query bounds. The ABCI persistence/snapshot suite additionally covers atomic height/app-hash/state/transaction/receipt/index recovery, abrupt exit, forced kill around WAL sync, missing entries, receipt-root corruption, multi-chunk out-of-order state sync, trusted-app-hash rejection, content corruption, restore failure, snapshot rotation stability, and durable restart.

The final ABCI++ application tree additionally passed full unit tests, the full race suite, a focused ABCI race run, vet, tidy-diff, production CLI build, demo, two-run ABCI/core fresh-process vectors, and an offline `govulncheck` v1.6.0 scan with zero reachable vulnerabilities. That earlier application-only graph contained advisories in three imported packages and 20 required modules, but no vulnerable symbol was called.

The integrated v2 validator-lifecycle tree passed the full unit and race suites, vet, tidy-diff, all three production builds, demo, three repeated direct v2 lifecycle suites, and three repeated real four-validator evidence/removal process runs. The final fixed `govulncheck` v1.6.0 scan again reports zero reachable vulnerabilities, two imported-package advisories, and 20 required-module advisories whose vulnerable symbols are not called. The first scan attempt failed only because `proxy.golang.org` was unreachable over IPv6; the successful rerun used the signed `sum.golang.org` database through the documented `goproxy.cn` sumdb endpoint.

The Store V2 tree passed the full unit and race suites, vet, tidy-diff, all three production builds, demo, five repeated delta/profile/migration/corruption suites, three repeated abrupt-exit/forced-kill suites, three repeated snapshot/state-sync round trips, two fresh-process vector runs, and two real four-validator restart/block-sync/state-sync process runs. Fixed `govulncheck` v1.6.0 again reports zero reachable vulnerabilities, with the same two imported-package and 20 required-module advisories not reaching vulnerable symbols.

The scheduled V3/proof tree passed the full unit and race suites, vet, tidy-diff, all four production builds, demo, ten proof/verifier runs, five protocol/fresh-process runs, and three real four-validator scheduled-upgrade/proof networks. Direct coverage includes pre-activation V2 roots, the `A-1` Comet app-version update, V3 activation, old-binary rejection, modified-schedule rejection, exact-total tampering, historical proof replay, restart, and snapshot/state sync. Fixed `govulncheck` v1.6.0 again reports zero reachable vulnerabilities, with the same two imported-package and 20 required-module advisories not reaching vulnerable symbols.

The mutation-aware Store V2 tree passed full unit and race suites, a focused state/ABCI race run, vet, tidy-diff, ten repeated mutation-journal/delta runs, three repeated history/profile/migration/kill/snapshot suites, checkpoint fail-closed recovery, four production builds, demo, three fresh-process vector runs, and two real four-validator restart/block-sync/state-sync networks. A Windows/amd64 microbenchmark isolates single-key delta construction at roughly 1.3–1.7 microseconds for both 1,000 and 4,096 existing storage entries, versus roughly 1.46 and 6.2 milliseconds for the former full flatten/diff path. This benchmark does not cover flat/proof-root calculation or end-to-end commit latency. Fixed `govulncheck` v1.6.0 reports zero reachable vulnerabilities; two imported-package and 20 required-module advisories do not reach vulnerable symbols.

Adding the complete Comet node initially made three advisories symbol-reachable: QPACK trailer expansion in `quic-go` v0.59.0, gRPC missing-leading-slash authorization bypass in v1.79.2, and an unpatched `pion/dtls/v2` AES-GCM nonce issue pulled through Comet's compiled libp2p/WebRTC path even though ChainLab disables libp2p at runtime. The dependency floor now uses `quic-go` v0.59.1 and gRPC-Go v1.79.3, while `go-libp2p` v0.48.0 migrates the STUN/WebRTC graph to `pion/dtls/v3` and removes `dtls/v2` from the main module. The four-validator process suite, full tests, race, vet, and all builds pass with these overrides. A final fixed `govulncheck` v1.6.0 scan reports zero reachable vulnerabilities; two imported-package and 20 required-module advisories remain without reachable vulnerable symbols.

The V4 sparse-state tree passed the full unit and race suites, vet, tidy-diff, ten repeated proof/verifier runs, three repeated direct V4/fresh-process suites, three repeated real four-validator V3/V4 upgrade networks, four production builds, demo, and diff checks. Coverage includes old-binary and modified-schedule rejection, membership/non-membership tampering, incremental root versus checkpoint rebuild, persistence-failure overlay isolation, mixed V3/V4 archive history, restart, snapshot/state sync, and fixed sparse/V4 fresh-process vectors. A Windows/amd64 copy-on-write single-leaf benchmark is approximately 225-250 microseconds and 96.6 KiB for both 1,000 and 4,096 leaves, demonstrating state-size-independent update cost at these sizes while leaving allocation optimization and production load/soak open. Fixed `govulncheck` v1.6.0 reports zero reachable vulnerabilities; two imported-package and 20 required-module advisories do not reach vulnerable symbols.

Snapshot V3 txpool restart passed normal pending/queued restart, queued replacement, nonce-gap promotion, atomic post-block removal, checksum-valid signature tampering, queued-capacity rejection, V2 migration, interrupted migration resume, manifest rollback rejection, and snapshot-write rollback tests. The focused suite passed repeated runs, then the full unit/race suites, vet, tidy-diff, four production builds, height-18 demo, and diff checks passed. Fixed `govulncheck` v1.6.0 again reports zero reachable vulnerabilities; two imported-package and 20 required-module advisories remain unreachable. This proves fail-closed development restart behavior, not production WAL throughput or bounded write amplification.

Canonical-reorg orphan reinsertion passed an end-to-end longer-branch flow through snapshot-v3 restart and subsequent block inclusion, plus stale-nonce suppression, current-pool sender/nonce precedence, and per-sender capacity tests. The focused suite passed ten repetitions; the full unit/race suites, vet, tidy-diff, four builds, demo, and fixed vulnerability scan passed again. Deterministic eviction/TTL and sustained reorg/DoS load remain open.

V5 offence retention passed strict schedule/app-version negotiation, legacy permanence, timestamp schema validation, one-dimension retention, dual-expiry deletion, expired-evidence rejection, mutation-aware sparse membership/non-membership, Pebble restart, application snapshot/state sync, fixed fresh-process app/state/validator roots, a real four-validator V3/V4/V5 network, and one-at-a-time replacement of V4-capped applications before V5 while each 3-of-4 remainder commits a transaction. The full unit/race suites, vet, tidy-diff, ten repeated V5 runs, four production builds, height-18 demo, and fixed `govulncheck` v1.6.0 scan pass; production load/soak, runtime scheduling, signed releases/Comet binary drills, and validator economics remain open.

The V5 rolling-application network passed three consecutive runs. Every run started four V4-capped applications, crossed V3 and V4, replaced one application and Comet process at a time, committed four transactions while each corresponding validator was offline, replayed each replacement to common block/application state, proved all replacements were complete while the protocol was still V4, and then crossed V5 with app version 5 and valid sparse account proofs. The final tree again passed the full unit/race suites, vet, tidy-diff, four production builds, height-18 demo, and fixed vulnerability scan.

The four-validator quorum-loss network passed five consecutive runs. Each run stopped two Comet validators, waited until the remaining two RPC heights were equal and stable, accepted a transaction into the live mempool, and proved height plus application nonce stayed unchanged across multiple commit intervals. Restoring the third validator committed the queued transaction and converged three nodes; restoring the fourth rebuilt the full peer mesh and common block/application state. The final tree passed the full unit/race suites, vet, tidy-diff, four production builds, height-18 demo, and fixed `govulncheck` v1.6.0 scan. This is validator-outage evidence, not an actual bidirectional network partition or message-delay/drop test.

The symmetric 2+2 P2P partition network passed five consecutive runs. Every run restarted Comet with PEX disabled and exactly `{node0,node1}` / `{node2,node3}` persistent-peer pairs, verified each exact peer ID over RPC, kept transaction gossip and mempool counts on only the originating side, and proved both halves remained height-stable. Restarting one full-mesh bridge propagated and committed the isolated transaction, after which all processes returned to a full mesh and common state. The final tree passed the full unit/race suites, vet, tidy-diff, four production builds, height-18 demo, and fixed vulnerability scan. Exact guarantees and remaining packet-level faults are in `docs/chainlab-comet-fault-network.md`.

The targeted missing-proposer network passed five consecutive runs. Each run reconstructed the current Comet validator set from RPC priorities, verified the current and next-height proposers, stopped the validator predicted as the `H+2` round-0 proposer, and required `H+2` to commit from a different proposer at round greater than zero. The remaining 3-of-4 committed the transaction and advanced through `H+3`; restoring the stopped validator rebuilt the full mesh and converged all four nodes. This is missing-validator process evidence, not dynamic packet-delay or asymmetric-reachability evidence.

The simultaneous validator-removal network passed five consecutive runs. Each run broadcast valid duplicate-vote evidence for two validators, required their slash and inactive heights to converge, read exactly two power-zero updates from the same `BlockResults` height, and observed every RPC validator set change from four to two. The remaining two validators then committed a transaction while the removed validators continued as full nodes, with all four block/application hashes, state roots, and account nonces converging. This covers evidence-driven simultaneous removals only, not admission or arbitrary power changes.

The light-client-equivocation evidence network passed five consecutive runs. Each run built a conflicting header at a canonical historical height, signed a conflicting precommit with three of four validator keys, and broadcast the complete light block through Comet RPC. Comet verified the evidence and derived the three Byzantine validators before ABCI slashed and removed them; the remaining validator then committed a transaction while all four full nodes converged.

The forward-lunatic evidence network also passed five consecutive runs. Each run selected canonical common height `H`, changed the application hash of a conflicting header at `H+1`, signed it with three historical validator keys, waited until the full nodes had committed `H+2`, and broadcast the complete proof through Comet RPC. Comet verified the cross-height trust overlap, signatures, invalid deterministic header field, common-height time and power, then derived the three Byzantine validators. ABCI slashed and removed them at one epoch boundary, after which the remaining validator committed a transaction and all four full nodes converged. Amnesia evidence, production trusted-header sourcing, an externally operated light-client detector, and a conflicting height ahead of the full node remain open.

The connected delayed-proposer network passed five consecutive runs. Every run kept all four Comet/application processes and the full peer mesh online, predicted the `H+2` round-0 proposer, and delayed only that node's `PrepareProposal(H+2)` response for two seconds against a 500 ms timeout. A different proposer committed `H+2` at round greater than zero, the transaction committed, and all four nodes converged. Packet-level delay and asymmetric reachability remain open.

The invalid-proposal network passed five consecutive runs. Every run predicted the `H+2` round-0 proposer and used a one-shot test wrapper to append a malformed transaction after the real application prepared that proposal. The consumed trigger proved injection occurred; all applications rejected the proposal, another validator committed `H+2` at round greater than zero, the valid mempool transaction committed, and all four nodes converged. The final tree passed the full unit/race suites, vet, tidy-diff, four production builds, height-18 demo, diff checks, and fixed `govulncheck` v1.6.0 with zero reachable vulnerabilities; two imported-package and 20 required-module advisories remain without reachable vulnerable symbols. This covers application-level proposal rejection, not packet corruption or arbitrary Byzantine proposal behavior.

Windows `go mod verify` is not recorded as green for the CometBFT tree. The signed v0.39.3 module zip contains `.github/workflows/e2e-nightly-38x.yml ` with a trailing space; Windows normalizes the extracted cache path to the no-space name, so Go reports the directory as modified even though the file bytes match. Both `goproxy.cn/sumdb/sum.golang.org` and `sum.golang.google.cn` returned the committed module sums `h1:UegHXskZNomsijmm29nL5NkeXtnzkme6fg+q1hPQnEI=` and `h1:PmNfvtw256BC41ad0FABts236CSZnvZ0kjPOciBwTdM=`. The initial 60 non-Comet ABCI checksum lines match CometBFT v0.39.3's upstream `go.sum`; the larger node graph and security overrides were resolved through signed sumdb. A clean Linux module-cache verification remains part of the Linux/amd64 validator release gate.

## Current Production Blockers

Ordered by consensus and security dependency rather than feature visibility:

1. **Authoritative CometBFT ABCI++ lifecycle**
   - The `chainlab-v1` lifecycle now runs across real socket and Comet node processes. Four-validator tests cover proposal/finalize/commit, full-mesh P2P, transaction gossip, 3-of-4 progress, targeted missing/delayed/invalid-proposer round advancement, 2-of-4 halt/recovery, symmetric 2+2 partition/heal, Comet restart, block sync, durable app restart, fresh-app replay, destructive-data state sync, common-height block/app-hash equality, and current state-root convergence. Keep the local harness for deterministic differential testing only.
   - Preserve the implemented v2 duplicate-vote/light-client-equivocation/forward-lunatic slashing, epoch-removal, missing/connected-delayed/invalid proposer, quorum-loss/partition recovery, and rolling application replacement paths while adding certified admission/re-entry, stake-derived power, unbonding/rewards, Comet binary/staged rolling drills, dynamic packet faults/asymmetric partitions, amnesia light-client evidence, external detector/sourcing, and broader Byzantine fault tests.
   - Keep validator join/leave disabled until their certified transition and economics rules are explicitly activated.

2. **Crash-consistent transactional storage**
   - Preserve the implemented Pebble V2 atomic batch, flat live state, per-height deltas/checkpoints, deterministic migration, explicit profiles, historical reads, compaction, backup/open, state-sync rebase, and corruption/kill recovery evidence.
   - Preserve mutation-aware delta generation, V4/V5 incremental authenticated state, and checkpoint full-diff/root cross-checks; add production-size compaction/retention measurements, an operational restore drill, additional filesystem power-loss phases, and external rollback protection.
   - Replace sticky-halt handling of post-rename directory-sync uncertainty with an authoritative WAL/transaction recovery decision. Avoid rewriting the complete JSON snapshot for every partial vote.

3. **Protocol lifecycle and proofs**
   - Preserve genesis-scheduled V2-to-V3-to-V4-to-V5 activation, Comet app-version updates, deterministic root/lifecycle migrations, V5 offence retention, incompatible-node rejection, exact-total and sparse proofs, and the standalone trusted-root verifier.
   - Add runtime-authorized scheduling, signed binary compatibility manifests, rollback limits, light-client integration, a second independent implementation, isolated proof serving, Comet binary replacement, and staged operator rolling-upgrade evidence.

4. **Validator signing and node rollback protection**
   - Add remote signer/HSM support with monotonic last-sign state, single-instance locking, and slashing-safe recovery.
   - Protect the entire data directory against rollback to an older but internally valid snapshot; the current checksums detect corruption but cannot prove external monotonicity.

5. **Resource and RPC governance**
   - Add gas-bounded/lazy proposal simulation, deterministic eviction/TTL, and a production WAL-backed journal without full-snapshot write amplification; preserve the implemented snapshot-backed txpool restart, bounded orphaned-transaction reinsertion, and incremental byte accounting across every mutation and rollback path.
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

1. Extend the V2/V5 evidence-removal foundation into certified admission/re-entry, stake-to-power, unbonding/rewards, governance authority, key rotation, and rolling-upgrade rules without re-enabling ad hoc join/leave transactions; preserve permanent legacy tombstones and V5 forward compaction.
2. Add incremental Store V2 flat/proof roots, then production-size retention/compaction/load evidence, broader power-loss coverage, an operational restore drill, and external rollback protection.
3. Extend the multi-process network beyond implemented validator-outage, targeted missing/connected-delayed/invalid proposer, simultaneous evidence-driven removals, same-height light-client equivocation, cross-height forward-lunatic evidence, and symmetric partition recovery with dynamic packet faults/asymmetric partitions, amnesia light-client evidence, external detector/sourcing, broader Byzantine proposal behavior, non-removal power/admission changes, Comet binary replacement, staged operator drills, and other Byzantine cases while preserving the implemented application rolling upgrade.
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
