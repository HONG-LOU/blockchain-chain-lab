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
- **Execution:** native contracts plus version-pinned Wasmtime-Go v46.0.1. Metering version `chainlab-wasm-v1` uses deterministic fuel, a shared host-gas budget, paid fixed memory/table declarations, strict structural/ABI admission, bounded stack/I/O/events/writes, read isolation, and rollback. No wall-clock outcome participates in WASM execution, but native contracts still require a versioned gas schedule before production.
- **Interoperability:** light-client-based protocols such as IBC or a separately audited bridge design; no ad hoc multisig bridge.
- **Operations:** protected validator signing, sentry topology, reproducible releases, metrics/alerts, backups, upgrade coordination, incident response, and disaster exercises.

The existing local PoA node remains a fast development/differential-test harness. It is not the production consensus path.

## Gate 1: Protocol Correctness And Determinism

- Complete guardian recovery for `account.v1` and delegated EOAs with role isolation, bounded voting rounds, threshold timelock, guardian-funded actions, session invalidation, replay/persistence, CLI, and indexed events.
- Complete the remaining WASM production evidence around `chainlab-wasm-v1`: deterministic guest call depth, charged failed transactions, hard JIT-memory lifecycle bounds, protocol-wide fatal runtime-fault halt and deterministic recovery/upgrade behavior, a pinned Linux/amd64 golden replay environment, reproducible native builds/checksums/SBOM, malicious corpora/fuzzing, load tests, activation, and external review. Do not admit other validator targets without matching evidence.
- Version every consensus encoding and reject non-canonical or ambiguous transactions.
- Add state/transaction/receipt inclusion proofs and an independent verifier.
- Add cross-process and supported cross-architecture replay vectors; identical blocks must produce identical receipts and roots.
- Define and property-test nonce, fee conservation, atomicity, root, validator-set, governance, and upgrade invariants.

## Gate 2: CometBFT ABCI++ Production Path

- Implement `Info`, `Query`, `CheckTx`, `PrepareProposal`, `ProcessProposal`, `FinalizeBlock`, `Commit`, validator updates, evidence handling, and snapshot/state-sync methods against the version selected in the production manifest.
- Make CometBFT height/app-hash the authoritative production block lifecycle.
- Define validator voting power, epochs, set transitions, evidence-to-slashing rules, quorum rounding, genesis, chain ID, and upgrade compatibility.
- Run multi-process tests for normal rounds, delayed/missing proposers, equivocation evidence, partitions, reconnects, state sync, validator changes, and rolling restarts.
- Differential-test ABCI++ execution against the local harness using the same transaction corpus and roots.

## Gate 3: Durable State And History

- Commit application state, receipts, indexes, and app hash atomically per finalized block.
- Recover after kill/power-loss fault injection at every commit phase without partial state.
- Add schema/version metadata, deterministic migrations, corruption detection, backup/restore, and snapshot import verification.
- Define pruned, full, and archive profiles with exact historical query guarantees.
- Bound indexes and log queries; support an external production indexer without making it consensus-critical.

## Gate 4: Resource Governance And Security

- Bound transaction/payload/code/storage/event sizes, txpool capacity, per-account quotas, RPC body/batch/log ranges, subscriptions, peers, and internal queues.
- Add deterministic eviction, txpool journal/restart, authorization-principal-safe replacement, incremental byte accounting, orphaned-transaction reinsertion after reorg, rate limits, timeouts, and WebSocket backpressure.
- Bound imported block/transaction and RPC/peer body bytes before decoding or deep-copying, and cap proposal simulation/revalidation work by a deterministic gas/work budget.
- Meter native contract work and state-dependent operations, charge included OOG/trap failures, hard-bound compiled native memory, and prevent pending replay from recompiling uploaded modules.
- Fuzz raw/native/Ethereum decoding, RPC parsing, WASM validation, proposal processing, state transition, snapshot import, and reorg/replay paths.
- Property-test and race-test consensus-adjacent state. Run fault injection, malicious corpora, benchmarks, sustained load, soak tests, and dependency/vulnerability scans in CI.
- Maintain a threat model for validator keys, owner/session/guardian/paymaster keys, governance, upgrades, RPC, database, supply, oracles, and interoperability.

## Gate 5: Protocol Upgrades And Operations

- Add protocol/application version negotiation, scheduled activation heights, deterministic migrations, incompatible-node rejection, and rollback limits.
- Remove validator secrets from command-line operational profiles; support encrypted keystore/remote signer, permissions, rotation, and recovery.
- Add liveness/readiness, OpenTelemetry/Prometheus metrics, structured logs, dashboards, alerts, tracing, audit logs, and capacity signals.
- Verify backup restore, state sync, validator replacement, rolling upgrade, emergency halt, governance recovery, and incident runbooks in staged networks.
- Produce signed reproducible releases, SBOM/provenance, configuration compatibility checks, and operator upgrade notes.

## Gate 6: Economics And Ecosystem

- Specify supply, issuance/burn, validator rewards, minimum stake, unbonding, slashing, governance quorum/deposits/timelocks, treasury, and upgrade authority.
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
