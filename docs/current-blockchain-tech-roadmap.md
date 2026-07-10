# ChainLab Production Technology Roadmap

Status date: 2026-07-10

## Product Target

ChainLab is being built as a real production-grade sovereign blockchain, not as a learning chain or disposable toy. The network owns its application protocol, accounts, fees, governance, economics, execution policy, RPC contract, releases, and operational security.

Production does not require inventing every component. Mainstream chains combine original protocol logic with maintained consensus, database, cryptographic, VM, and networking implementations. ChainLab follows that model and validates the complete integrated system.

The detailed mainstream-chain comparison, official version references, and gate-by-gate audit are in [mainstream-chain-capability-and-production-gates-2026-07-10.md](mainstream-chain-capability-and-production-gates-2026-07-10.md).

## Selected Architecture

- **Application protocol:** ChainLab's deterministic Go state-transition function, versioned transaction/receipt/state formats, account model, gas, governance, and native account abstraction.
- **Production consensus and networking:** CometBFT ABCI++ for rounds, locks, finality, validator evidence, P2P, mempool proposal flow, block sync, and state sync.
- **Application storage:** a maintained transactional KV engine with atomic block commits, WAL/reopen guarantees, versioned schemas, checksums, snapshots, and pruned/full/archive profiles.
- **Execution:** native contracts plus version-pinned Wasmtime-Go v46.0.1. Metering version `chainlab-wasm-v1` uses deterministic fuel, a shared host-gas budget, paid fixed memory/table declarations, strict structural/ABI admission, a maximum-64 acyclic direct-call graph, bounded I/O/events/writes, read isolation, and rollback. Native contracts use the sealed `chainlab-native-v1` schedule, and included deterministic failures commit nonce/fees while rolling back business state. These rules still require protocol activation and production-network evidence; no wall-clock or native stack layout decides a consensus outcome.
- **Interoperability:** light-client-based protocols such as IBC or a separately audited bridge design; no ad hoc multisig bridge.
- **Operations:** protected validator signing, sentry topology, reproducible releases, metrics/alerts, backups, upgrade coordination, incident response, and disaster exercises.

The existing local PoA node remains a fast development/differential-test harness. It is not the production consensus path.

## Current Verified Harness Boundary

- The first `chainlab-v1` CometBFT v0.39.3 ABCI++ application foundation now strictly binds canonical genesis and consensus parameters, reuses the deterministic executor, performs bounded prepare and full process/finalize replay, publishes only on commit, commits execution metadata into app hash, exposes bounded app-side mempool methods, and has fresh-process plus local-harness receipt/root differential tests. It is in-memory and not yet connected to a real CometBFT process, durable database, snapshot/state sync path, or multi-process fault network.
- The local harness now persists a checksum- and generation-bound snapshot v2 plus a data-directory manifest, replays canonical and known branches from an explicitly timestamped genesis, validates every replayed state root, and refuses silent genesis recreation when initialized data is incomplete. Public genesis and validator secrets are separate artifacts. This is fail-closed development persistence, not the transactional production database.
- A valid highest certificate is a monotonic local finality lock. A branch that does not descend from it is rejected, compatible certificate subsets are merged, conflicting quorum certificates produce a persistent sticky halt, and uncertified `finalized` remains at genesis. Head-depth observations are exposed only as `depth_confirmation`.
- The validator set is explicit and fixed by genesis. `validator.join` and `validator.leave` are rejected until the CometBFT path defines certified epoch transitions. Double-sign slashing accepts only canonical, signature-verified evidence, consumes one offence per validator height regardless of evidence pair, and does not mutate the fixed set.
- Governance proposal/vote/execute transactions are also fail-closed. Current-balance voting was removed because immediate unstake/transfer/restake allowed one capital position to be counted from multiple addresses; activation now depends on finalized voting-power snapshots and unbonding/locking rules.
- Included deterministic failures preserve nonce and actual fee settlement while rolling back business writes; OOG consumes the full gas limit, sponsored failures charge the paymaster, and batch calls remain atomic.
- HTTP and JSON-RPC ingress, all-request/POST/RPC concurrency, batch recording, txpool queries, fee-history, log/filter definitions and TTL, pending-filter retained hashes, WebSocket lifecycle/subscriptions/log work, transaction, block, validator, chain-ID, known-fork, snapshot, WASM, and per-account storage have explicit limits. Published state views are immutable, and long `eth_call` reads no longer hold the node lock or clone the whole state.
- `/health` and `/health/ready` fail while the node is halted, `/health/live` remains a process-liveness signal, the CLI HTTP server has read-header, read, write, and idle timeouts, and persistent nodes exclusively lock their data directory until closed. These are foundations for operations, not a substitute for metrics, alerting, or staged-network evidence.

## Gate 1: Protocol Correctness And Determinism

- Preserve the implemented guardian recovery rules for `account.v1` and delegated EOAs through ABCI++ differential replay, property/fuzz tests, protocol upgrades, and staged-network operations.
- Complete the remaining WASM production evidence around `chainlab-wasm-v1`: hard JIT-memory lifecycle bounds, protocol-wide fatal runtime-fault halt and deterministic recovery/upgrade behavior, a pinned Linux/amd64 golden block-replay environment, reproducible native builds/checksums/SBOM, malicious corpora/fuzzing, load tests, scheduled activation, and external review. Do not admit other validator targets without matching evidence.
- Freeze and activate the implemented maximum-64 call graph, included-failure settlement, and `chainlab-native-v1` schedule in an explicit protocol version; differential-test identical receipts, fees, nonces, and roots through the production ABCI++ lifecycle.
- Version every consensus encoding and reject non-canonical or ambiguous transactions.
- Add state/transaction/receipt inclusion proofs and an independent verifier.
- Add cross-process and supported cross-architecture replay vectors; identical blocks must produce identical receipts and roots.
- Define and property-test nonce, fee conservation, atomicity, root, validator-set, governance, and upgrade invariants.

## Gate 2: CometBFT ABCI++ Production Path

- Preserve and extend the implemented v0.39.3 `chainlab-v1` foundation for `Info`, `Query`, `CheckTx`, `InsertTx`, `ReapTxs`, `InitChain`, `PrepareProposal`, `ProcessProposal`, `FinalizeBlock`, `Commit`, strict empty vote extensions, fixed-set evidence commitment, and explicit unsupported snapshot restore behavior.
- Connect the application to real CometBFT processes and durable storage so CometBFT height/app-hash becomes the authoritative production block lifecycle across restart and replay.
- Implement validator updates, evidence-to-slashing rules, verified snapshot export/import, and state-sync application before claiming the complete ABCI++ lifecycle.
- Replace the harness's fixed genesis validator set with explicitly versioned voting power, epochs, certified set transitions, evidence-to-slashing rules, quorum rounding, genesis, chain ID, and upgrade compatibility in the authoritative CometBFT lifecycle.
- Run multi-process tests for normal rounds, delayed/missing proposers, equivocation evidence, partitions, reconnects, state sync, validator changes, and rolling restarts.
- Differential-test ABCI++ execution against the local harness using the same transaction corpus and roots.

## Gate 3: Durable State And History

- Preserve snapshot-v2 checksum, manifest, replay, finality-lock, evidence, and fail-closed schema invariants while replacing JSON persistence.
- Preserve commit-uncertain sticky halt semantics, then replace the ambiguity with a WAL-backed authoritative recovery decision.
- Commit application state, receipts, indexes, and app hash atomically per finalized block.
- Recover after kill/power-loss fault injection at every commit phase without partial state.
- Add schema/version metadata, deterministic migrations, corruption detection, backup/restore, and snapshot import verification.
- Define pruned, full, and archive profiles with exact historical query guarantees.
- Bound indexes and log queries; support an external production indexer without making it consensus-critical.

## Gate 4: Resource Governance And Security

- Preserve and adversarially test the implemented transaction, block, contract, event, txpool, validator, chain-ID, fork, snapshot, RPC body/concurrency/batch/response, fee-history, and log-query bounds. Add equivalent limits to each new ABCI/P2P/indexer ingress path.
- Add deterministic eviction, txpool journal/restart, incremental byte accounting, orphaned-transaction reinsertion after reorg, principal-aware rate limits, WebSocket subscription TTL, peer quotas, pagination, and bounded future internal queues. Preserve the implemented authorization-principal-safe replacement, all-request/POST/filter/subscription limits, active filter expiry, WebSocket heartbeat/write timeout/work budget, and HTTP timeouts.
- Adversarially verify the implemented transaction/block/HTTP ingress bounds, add equivalent limits for any future non-HTTP peer transport, and cap proposal simulation/revalidation work by a deterministic gas/work budget.
- Extend the implemented `chainlab-native-v1` and included OOG/trap settlement rules with property/fuzz/load/upgrade evidence; hard-bound compiled native/JIT memory and prevent pending replay from recompiling uploaded modules.
- Fuzz raw/native/Ethereum decoding, RPC parsing, WASM validation, proposal processing, state transition, snapshot import, and reorg/replay paths.
- Property-test and race-test consensus-adjacent state. Run fault injection, malicious corpora, benchmarks, sustained load, soak tests, and dependency/vulnerability scans in CI.
- Maintain a threat model for validator keys, owner/session/guardian/paymaster keys, governance, upgrades, RPC, database, supply, oracles, and interoperability.

## Gate 5: Protocol Upgrades And Operations

- Add protocol/application version negotiation, scheduled activation heights, deterministic migrations, incompatible-node rejection, and rollback limits.
- Remove validator secrets from command-line operational profiles; support encrypted keystore/remote signer, permissions, rotation, and recovery.
- Carry the implemented halt-aware liveness/readiness contract into the production process and add OpenTelemetry/Prometheus metrics, structured logs, dashboards, alerts, tracing, audit logs, and capacity signals.
- Verify backup restore, state sync, validator replacement, rolling upgrade, emergency halt, governance recovery, and incident runbooks in staged networks.
- Produce signed reproducible releases, SBOM/provenance, configuration compatibility checks, and operator upgrade notes.

## Gate 6: Economics And Ecosystem

- Specify supply, issuance/burn, validator rewards, minimum stake, unbonding, slashing, governance quorum/deposits/timelocks, treasury, and upgrade authority.
- Re-enable governance only with finalized voting-power snapshots, capital non-reuse through unbonding/locking, deterministic voter accounting, deposits, bounded lifecycle, and versioned migration/activation evidence.
- Simulate adversarial validator economics and governance capture before public mainnet.
- Ship wallet/SDK signing support, address/transaction standards, faucet/testnet tooling, explorer/indexer, token standards, oracle boundary, and developer tooling.
- Select and audit interoperability only after its trust, finality, rate-limit, pause, upgrade, and incident model is explicit.
- Implement canonical ERC-4337/EIP-7702/EVM paths only if the network commits to Ethereum compatibility; native analogous features must not be labeled protocol-compatible.

## Release Environments

1. **Local application replay network:** fast deterministic state-transition and differential tests; wall-clock block hashes are not cross-run deterministic.
2. **Multi-process private network:** CometBFT, durable DB, state sync, faults, upgrades, monitoring.
3. **Public persistent testnet:** external validators, wallets, indexers, load, security program, repeated upgrades and disaster drills.
4. **Incentivized adversarial testnet:** economic attacks, validator churn, governance and incident exercises.
5. **Mainnet candidate:** all gates have authoritative evidence, independent security review findings are closed, genesis and binaries are reproducible, and operators have completed launch/recovery rehearsals.

No environment is promoted by feature count or elapsed time. Promotion requires passing its defined safety, liveness, durability, performance, security, and operational gates.
