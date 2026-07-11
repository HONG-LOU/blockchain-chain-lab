# Mainstream Chain Capability And Production Gates

Audit date: 2026-07-11

## Decision

ChainLab's target is a production-grade sovereign blockchain that can operate as a real public network. The current repository is the initial implementation, not the product boundary. Production readiness is not expressed as a percentage: a property such as Byzantine finality, deterministic execution, crash durability, state sync, or key security is either proven against a defined gate or it is not present.

ChainLab keeps ownership of its protocol, application state transition, account model, economics, governance, execution policy, RPC contract, and network releases. It will integrate maintained production components where inventing a substitute would reduce safety. The selected base architecture is a ChainLab ABCI++ application on CometBFT, with transactionally durable application storage and a deterministically metered execution runtime. The existing local PoA node remains a development harness until the production path replaces it.

## Target Architecture

```text
wallets / SDKs / operators
          |
ChainLab RPC, gRPC, indexer, explorer
          |
ChainLab application protocol (accounts, fees, AA, governance, WASM)
          |
ABCI++ application boundary + versioned deterministic state migrations
          |
CometBFT consensus / P2P / mempool / evidence / state sync
          |
transactional KV, snapshots, pruning/full/archive, backups
```

Production deployment also includes validator remote-signing or protected keystores, sentry topology, metrics/alerts, upgrade coordination, incident response, load capacity, independent verification, and release reproducibility. These are chain deliverables, not optional documentation work.

## Compatibility Vocabulary

- **Shape-compatible:** request/response fields resemble another protocol.
- **Protocol-compatible:** canonical encoding, validation, error, and state semantics match the referenced protocol and its conformance vectors.
- **Execution-compatible:** the referenced bytecode/runtime and gas rules execute with equivalent results.
- **Ecosystem-tested:** mainstream wallets, SDKs, indexers, and tooling pass maintained integration tests.

ChainLab currently has EVM-shaped JSON-RPC projections and limited protocol subsets. It does not have general EVM execution compatibility. `set_code` is an EIP-7702-style native experiment, not EIP-7702 type-4 transaction compatibility. Receipt-backed trace summaries are not opcode traces. Native storage/code projections are not Ethereum historical state.

## Mainstream Stack Matrix

| Stack | Audited 2026 reference point | Production capabilities relevant to ChainLab | Correct adoption boundary |
|---|---|---|---|
| Ethereum + OP Stack | Ethereum Pectra/Fusaka in production; EIP-7702, ERC-4337, and PeerDAS finalized. OP `op-node` v1.19.2 and `op-geth` v1.101702.2. | EVM execution, L1 settlement and DA, sequencer/batcher/proposer/challenger roles, fault proofs, standard bridge contracts, archive/HA/operator guidance. | Use for a product requiring Solidity, wallets, liquidity, and Ethereum settlement. Build a separate OP deployment; do not evolve ChainLab RPC projections into a claimed rollup. |
| Cosmos SDK + CometBFT | Cosmos SDK v0.54.3, CometBFT v0.39.3, IBC-Go v11.1.0. | ABCI++, validator rounds, prevote/precommit locks, gossip, state sync, modular keepers, Store v2, upgrades, governance, IBC. Some SDK v0.54 BlockSTM/libp2p/AdaptiveSync paths remain experimental. | Selected production consensus/network/state-sync foundation. ChainLab remains its own protocol and ABCI++ application; Cosmos SDK modules are adopted only where their semantics match. |
| Avalanche L1 | AvalancheGo v1.14.2, Subnet-EVM v0.8.0, ACP-77/99 validator management and ICM. | Sovereign validator sets, custom gas token and fees, official L1 lifecycle, Subnet-EVM, BLS aggregate cross-chain messages. | Use official Avalanche L1/VM when configurable validators, permissioning, or isolated EVM performance are product requirements. Do not recreate Avalanche consensus or P-Chain management. |
| Solana / Agave | Agave v4.1.1. Official Firedancer material still describes Frankendancer as a hybrid that depends on Agave. | Explicit account access, writable locks and parallel execution, sBPF, compute-unit fees, AccountsDB/ledger, high-spec validator operations. | First build a Solana program when account-parallel throughput is the product need. An SVM clone and independent validator clients are not ChainLab milestones. |
| Polkadot SDK | `polkadot-stable2606`. | FRAME/Cumulus, WASM runtime, shared security, XCM, forkless governed runtime upgrades, Agile Coretime. | Choose only when shared security, governed WASM runtime upgrades, and XCM are core requirements; this is a Rust/FRAME rewrite, not an incremental ChainLab feature. |
| Celestia | `celestia-app` v9.0.4 and `celestia-node` v0.31.3 reference line. | Namespaced Merkle trees, data-availability sampling, blob ordering/availability. | A DA option only after a modular rollup defines execution, settlement, proofs, and bridge security. Celestia alone is not a production execution-chain base. |

Arbitrum Chains remains documented as public preview at the audit date. Polygon CDK is currently positioned as a Polygon-operated bespoke/managed offering rather than a self-service kit. Both are product-selection inputs, not ChainLab implementation requirements.

## Current-State Audit

| Capability | Current evidence | Honest classification | Required disposition |
|---|---|---|---|
| State transition and roots | Executor, canonical hashing, replay tests, account-storage caps, V3 exact-total transaction/receipt/state roots, and V4 mutation-aware sparse state with membership/non-membership proofs, fixed vectors, and incremental/full cross-checks | Versioned root/proof protocols with incomplete cross-target and production-load evidence | Add broader root/state-transition properties, cross-architecture vectors, a second independent verifier, trusted-header integration, and load/external review. |
| ABCI++ application lifecycle | CometBFT v0.39.3 is pinned with signed checksum evidence. V2 adds validator identity/evidence slashing and epoch removal. A genesis-committed V2-to-V3-to-V4-to-V5 schedule publishes each Comet app-version update one height early. V5 timestamped offences compact only after both Comet retention dimensions expire; legacy tombstones remain. Real four-validator tests prove duplicate-vote removal, V3/V4/V5 version/root/proof convergence, pre-activation rolling replacement from V4-capped to V5 applications, 3-of-4 progress, deterministic 2-of-4 halt, symmetric 2+2 P2P partition isolation, and queued-transaction commit plus full convergence after recovery. | Initial authoritative crash-consistent multi-process application/consensus/block-sync/state-sync/evidence-removal/scheduled-upgrade lifecycle | P0: runtime-authorized upgrade governance and rollback policy; certified validator admission/re-entry and economics; dynamic message faults/asymmetric partitions, additional Byzantine faults, load, signed release compatibility, Comet binary/staged operator rolling drills, and validator signing. |
| Finality | PoA blocks plus fixed-genesis-set commit certificates; the highest valid certificate is persisted as a monotonic lock, compatible quorum subsets merge, conflicting quorum certificates persistently halt, and uncertified `finalized` stays at genesis | Finality-lock safety boundary in the local harness, not a complete BFT protocol | Preserve these invariants in differential tests, but make CometBFT rounds/prevote/precommit, voting power, evidence, and certified epoch transitions authoritative before using the production-BFT label. |
| Fork choice | Longest valid branch above a non-revertible highest-certificate lock; conflicting locked branches are rejected, known-fork cardinality is bounded, and restart replay revalidates ancestry and roots | Bounded development fork choice with a tested certified-checkpoint safety rule | Differential-test the rule through CometBFT block/state sync and define pruning, epoch transitions, and recovery behavior under the production database. |
| WASM | Wasmtime-Go v46.0.1 with deterministic fuel, versioned guest/host/instantiation gas, fixed declared memory/table sizes, structural and ABI admission, a maximum-64 acyclic defined-plus-import direct-call graph, rejection of indirect/tail/function-reference calls, bounded host I/O/events/writes, read isolation, rollback, state-bound singleflight caching, a process-local sticky runtime-fault halt, and a committed Windows development golden vector | Initial deterministic runtime implementation; not activated for production validators | P0: Linux/amd64 release block replay, hard native/JIT memory lifecycle bounds, persistent protocol-wide fatal runtime-fault halt plus deterministic recovery/upgrade behavior across ABCI++ validators, reproducible artifacts/checksums/SBOM, broader property/fuzz/load/dependency evidence, scheduled activation, and external audit. Windows is development-only; other validator targets are unsupported until proven. |
| Execution failure and native gas | Included deterministic failures produce stable failed receipts, roll back business state, consume sender nonce, settle actual gas, charge paymasters for sponsored execution, and consume the full gas limit on OOG. Native contracts use sealed `chainlab-native-v1` metering with fixed input/storage/event/output bounds and exact fresh-process vectors. ABCI++ differentially matches local-harness successful/failed receipts and roots; real multi-process successful-transfer restart/sync replay now converges across four nodes. | Initial deterministic settlement and native-metering implementation; exercised in a real private process network but not activated on a production network | P0: freeze these rules in an activated protocol version, expand the multi-process corpus to included failures and adversarial cases, broaden fee/nonce/rollback property and fuzz evidence, load-test resource ceilings, and complete external review. |
| Persistence | Application Store V2 uses Pebble with one synchronized batch per height. It atomically binds flat live state, mutation-journal deltas, checkpoints, results, indexes, and history metadata. V4 ordinary commits update copy-on-write sparse accumulators only for touched entries and publish only after persistence; checkpoints compare the journal to a complete diff and the incremental root to a complete sparse rebuild. Manifests version legacy and sparse roots so mixed history remains verifiable. Deterministic migration, archive/full/pruned profiles, historical reads, backup, state-sync rebasing, forced-kill recovery, and corruption rejection are tested. | Crash-consistent mutation-aware disk history and authenticated state; evidence remains Windows-focused and production capacity is unproven | P0: production-size compaction/load/soak evidence, broader filesystem power-loss coverage, operational restore drills, and external rollback protection. |
| Verifiability | V3 provides exact-total transaction/receipt/state inclusion proofs. V4 adds compressed sparse membership/non-membership for account and every flat-state kind; V5 retains those transaction, receipt, state-root, and proof formats while authenticated offence entries can transition to non-membership after safe compaction. Current and retained mixed-version proofs are verified by one protocol-detecting trusted-root CLI; fixed vectors, restart, snapshot/state sync, tamper rejection, and a real four-node V3/V4/V5 flow pass. | Broad authenticated state and minimal verifier; trust-anchor integration and proof-service isolation remain external | P0: bounded production proof serving, trusted light-client/header integration, second independent implementation, bridge/wallet/explorer consumption, and external review. |
| Networking | The production path has independent CometBFT homes, full-mesh persistent peers, flood-mempool gossip, real four-validator rounds, 3-of-4 progress, 2-of-4 quorum-loss recovery, symmetric 2+2 P2P partition isolation/healing, restart, block sync, destructive-data state sync via two light-client RPC sources, and duplicate-vote evidence broadcast/propagation followed by a live four-to-three validator transition. | Initial CometBFT private-network block/state-sync/evidence path; validator outage and symmetric topology partition behavior are tested, while discovery policy, dynamic message faults/asymmetric partitions, sentries, and load remain incomplete | Complete discovery/peer policy, light-client-attack and packet-level delay/drop/reorder coverage, asymmetric partitions, sentry topology, additional Byzantine tests, ingress bounds, and load validation. |
| Txpool | Pending/queued with 2 MiB transaction, 128 MiB/4,096 total, 2,048 queued, 64-per-sender and 64-nonce-gap caps; incremental canonical-byte accounting across insertion/replacement/promotion/removal/reorg/rollback; snapshot-v3 restart with v2 migration and full transaction revalidation; bounded deterministic orphan reinsertion after canonical reorg; principal-safe replacement, block-gas admission, bounded batches, canonical-import revalidation, and production/import sequencing of included failures before the next nonce | Bounded, restart-durable local txpool; persistent admission rewrites the development snapshot | P0: deterministic fee-aware eviction/TTL, a production WAL-backed journal, gas-bounded proposal simulation/revalidation, local policy, and sustained DoS/load evidence. |
| RPC/indexing | REST, EVM-shaped JSON-RPC, filters, WebSocket, and local event index. Ordinary HTTP work, POST, and JSON-RPC each have 32-slot bounds; POST is capped at 16 MiB and JSON-RPC at 5 MiB/100 batch items/16 MiB batch responses. Txpool sources stop at 8 MiB; fee history at 1,024 blocks. Logs stop at 10,000 blocks, 1,000 results, 8 MiB, 256 canonical addresses, four topic positions, 256 canonical alternatives, and 64 KiB installed definitions. Filters use random IDs, total 1,024 with active five-minute expiry; pending filters stop at 64 and 65,536 retained hashes. WebSockets stop at 64 connections, 64 subscriptions per connection, 1,024 total, 64 log subscriptions, and 100,000 log matches per block, with heartbeat/read/write deadlines and bounded queues. `eth_call` accepts only `latest` and reads an immutable state view without holding the node lock. | Resource-bounded development interface; load and hostile-client evidence remain incomplete | P0: principal-aware rate limits, WebSocket subscription TTL and measured disconnect/backpressure policy, dynamic state-executing gas estimation for WASM deploy/call/batch-call, pagination, external indexer boundary, canonical reorg semantics, and load/soak evidence. |
| Account policies | Native smart accounts, multisig, paymaster, batch, delegated EOA, session keys, guardian recovery | ChainLab-native AA experiments | Keep native semantics explicit. ERC-4337/7702 conformance requires canonical encodings, EntryPoint/bundler behavior, official vectors, and ecosystem tests. ERC-7579 is still draft and is research-only. |
| Governance/upgrades | `proposal.submit`, `vote`, and `proposal.execute` remain reserved schema/RPC/CLI surfaces but fail before admission; rejection does not enter the txpool or change state, nonce, or fees, including across restart | Deliberately disabled until voting-power safety and activation rules exist | P0: finalized voting-power snapshots, unbonding or vote locks, proposal deposits, quorum/timelock rules, protocol-versioned activation, deterministic migration, rollback policy, upgrade vectors, and a runbook. Token economics and upgrade-key governance require a product threat model. |
| Validator lifecycle | The local harness and `chainlab-v1` remain genesis-fixed with join/leave rejected. `chainlab-v2` persistently binds consensus address/account/pubkey/power, exact evidence retention and slash ratios, one validator-height tombstone, overflow-safe stake penalty, epoch-bound removal, and the Comet `H+2` update delay. Direct restart/snapshot tests and real four-process evidence propagation prove removal. | Initial evidence-driven validator-removal lifecycle; admission, re-entry, power/economics, and signing operations remain incomplete | P0: implement certified admission/re-entry, stake-derived power, unbonding/rewards, tombstone retention/compaction, governance authority, and remote-signer monotonic last-sign state; do not re-enable ad hoc state-driven membership. |
| Operations | Halt-aware `/health` and `/health/ready`, process-only `/health/live`, HTTP/WS timeouts, checksummed persistent halt marker, exclusive cross-platform data-directory lock, strict public genesis separated from non-overwriting Unix-0600 validator key files, and development snapshots. Windows DACL protection is not claimed. | Initial operational safety signals | P0: Linux permission/install verification, encrypted keystore and remote signer/HSM, metrics, structured logs, alerts, backup/restore drill, key rotation, monotonic sign state, incident runbooks, and staged-network exercises. |
| Security evidence | The full ABCI++/Comet/Pebble/proof tree passes unit/race suites, repeated restart/block-sync/state-sync, snapshot, v2 validator-lifecycle/evidence-removal, V5 offence-retention boundaries, real V3/V4/V5 upgrade/proof, rolling-application, quorum-loss/recovery, and symmetric partition/heal networks, abrupt-exit/forced-kill tests, vet, tidy-diff, four production builds, V1/V3/V4/V5 fresh-process vectors, demo, and diff checks. Fixed `govulncheck` v1.6.0 reports zero reachable vulnerabilities, with two imported-package and 20 required-module advisories not reaching vulnerable symbols. CometBFT's module sums match signed sumdb; Windows directory verification remains non-green because the upstream zip contains a trailing-space filename. | Strong Windows development regression, fault, upgrade/proof, and signed dependency evidence; insufficient for hostile deployment | P0: broader fuzz/property/fault/cross-target evidence, malicious WASM/RPC/raw-tx/snapshot/proof corpora, benchmarks/load/soak, clean Linux verification, threat model, and external review. |

## Execution Semantics Basis

The included-failure boundary is deliberate, not inferred from RPC conventions. Geth v1.17.4 returns pre-check failures as consensus errors but carries EVM revert, trap, and out-of-gas results in `ExecutionResult`; the Ethereum execution-specification Osaka implementation still increments nonce/prepays gas, refunds unused gas, records success or failure in the receipt, and adds cumulative gas after message execution. Cosmos SDK v0.54.3 provides a second reference shape: a successful AnteHandler branch is written before message execution, while message writes remain in a nested cache and are committed only when message and post handling succeed. Aptos node v1.47.1 makes the distinction explicit as `TransactionStatus::Discard` versus `TransactionStatus::Keep(ExecutionStatus)`.

ChainLab therefore uses three protocol outcomes: pre-check invalid transactions are not included and change nothing; deterministic execution failures are included with a failed receipt, nonce, and actual fee while business writes/events roll back; runtime or state-invariant faults are fatal and must halt deterministic progress without committing ante state. This is an analogous safety boundary, not a claim that ChainLab gas, receipts, or transaction formats are Ethereum, Cosmos, or Aptos compatible.

The native gas model follows the same structural lessons as Cosmos `GasKV` and versioned Sui protocol configuration: charge state access at the host boundary, bound independent resource dimensions, and version consensus-visible weights. The numeric `chainlab-native-v1` weights are ChainLab protocol constants; they are not copied from another chain and remain subject to pinned Linux/amd64 benchmark, load, economics, activation, and upgrade evidence.

## Production Completion Gates

Every gate needs an authoritative test or artifact. A passing narrow unit test cannot prove a broader gate.

1. **Deterministic execution**
   - No wall clock, scheduler timing, filesystem, network, or process-global mutable state may determine consensus output.
   - Meter instructions, memory, tables, stack depth, host I/O, emitted bytes, reads, writes, and storage growth.
   - Replay identical blocks in fresh processes and supported GOARCH targets; compare receipt and state roots.

2. **Finality-safe local fork choice and production consensus handoff**
   - Preserve the implemented fixed-set quorum validation, monotonic highest-certificate lock, compatible-certificate merge, persistent conflicting-QC halt, restart vote/evidence memory, and locked-branch rejection.
   - Keep uncertified `finalized` at genesis; expose head-depth observations only as `depth_confirmation`.
   - Define epoch, voting power, quorum rounding, evidence retention, and certified validator transitions in CometBFT rounds/prevote/precommit. The local certificate harness must never be described as the production BFT protocol.

3. **Durable versioned storage**
   - Atomic block/state/index commit, restart after injected faults, versioned migrations, checksums/corruption handling.
   - Define pruned, full, and archive read guarantees; verify state sync/snapshot import independently.

4. **Protocol lifecycle**
   - Version every consensus-relevant encoding and state schema.
   - Preserve the implemented genesis-committed V2-to-V3-to-V4-to-V5 activation, Comet app-version transitions, deterministic root/lifecycle migrations, V5 offence retention, and incompatible-node rejection; add runtime authority, binary manifests, and rollback limits.

5. **Resource and abuse controls**
   - Preserve and load-test the implemented tx/block/snapshot/fork/validator/chain-ID, txpool, RPC body/concurrency/batch/response/fee-history/log, event, contract bytecode, and account-storage limits.
   - Load-test active filter expiry, pending-hash budgets, WebSocket heartbeat/write timeout, bounded queues and matching budget; add subscription TTL plus principal-aware rate limits, peer/ABCI/P2P ingress bounds, indexer-queue bounds, and limits for all future internal work queues.
   - Protocol-version and activate the implemented `chainlab-native-v1` schedule and included-failure nonce/fee settlement; verify them through ABCI++ differential replay, property/fuzz tests, load limits, and upgrade vectors.
   - Hard-bound compiled native/JIT memory and prevent pending-state replay from recompiling admitted uploads.
   - Verify backpressure, eviction, restart journals, malformed input, and sustained-load behavior.

6. **Security test program**
   - Fuzz raw/native/Ethereum transaction decoding, JSON-RPC parsing, WASM validation, state transition, and reorg replay.
   - Property-test root determinism, nonce, fee conservation, atomic batch rollback, txpool ordering, finality checkpoints, and snapshot round trips.
   - Run `go test -race`, `go vet`, `govulncheck`, benchmarks, fault injection, and dependency review in CI.

7. **Operational evidence**
   - Preserve halt-aware readiness and independent process liveness; add metrics, structured logs, dashboards/alerts, backup restore, key rotation, validator recovery, and incident exercises.
   - Validator secrets must not rely on command-line arguments in an operational profile.

8. **Verifiability boundary**
   - Preserve V3 exact-total proofs, V4 sparse membership/non-membership across flat-state kinds, and the standalone trusted-root verifier; add light-client trust anchoring, a second independent implementation, and production proof-serving evidence.

## Parallel Implementation Tracks

- Preserve the implemented ChainLab ABCI++/CometBFT authoritative block, block-sync, state-sync, snapshot, duplicate-vote slashing, epoch-removal, quorum-loss, and symmetric partition/heal lifecycle while adding certified admission/re-entry, dynamic message faults/asymmetric partitions, additional Byzantine evidence, and upgrade compatibility in multi-process networks.
- Preserve the mutation-aware incremental store, V4 authenticated root maintenance, checkpoint cross-checks, migrations, retention profiles, historical reads, compaction, and backup/open behavior; add complete Linux capacity, restore, fault, and rollback evidence before public testnet.
- Preserve the local PoA implementation only as a fast application state-transition replay harness; its wall-clock block timestamps are not cross-run deterministic. Differential-test the application result against ABCI++.
- Implement wallet connection, external indexing, token standards, oracle and interoperability interfaces as production ecosystem tracks with explicit security models.
- If Ethereum compatibility is selected, implement canonical ERC-4337 and EIP-7702 protocol paths against official vectors rather than extending analogous native labels.

## Build-Versus-Integrate Boundaries

- ChainLab must deliver production Byzantine consensus, P2P, evidence, and state sync by integrating and validating CometBFT, not by promoting the current PoA simulation.
- ChainLab must deliver deterministic execution by configuring and testing a maintained WASM runtime with consensus-safe limits, not by relying on wall-clock cancellation.
- ChainLab must deliver durable storage through a maintained transactional KV engine plus ChainLab schemas/migrations, not by writing a new database.
- Interoperability must use audited light-client/message protocols such as IBC or a separately specified audited bridge. A bespoke multisig bridge is not acceptable.
- EVM/SVM compatibility, DA, rollup proofs, MEV/PBS, and additional client implementations are architecture tracks when required by the network specification; they are integrated or independently implemented as full programs with conformance evidence, never claimed from partial analogies.
- Tokenomics, validator economics, governance powers, and upgrade authority require an explicit economic/security specification and adversarial simulation before mainnet.

No production requirement is waived by choosing integration. ChainLab owns version selection, configuration, threat modeling, compatibility testing, incident handling, and the end-to-end security claim.

## Official References

- Ethereum roadmap: https://ethereum.org/en/roadmap/
- Ethereum account abstraction: https://ethereum.org/en/roadmap/account-abstraction/
- EIP-7702: https://eips.ethereum.org/EIPS/eip-7702
- ERC-4337: https://eips.ethereum.org/EIPS/eip-4337
- ERC-7579: https://eips.ethereum.org/EIPS/eip-7579
- PeerDAS / EIP-7594: https://eips.ethereum.org/EIPS/eip-7594
- Geth v1.17.4 state transition: https://github.com/ethereum/go-ethereum/blob/v1.17.4/core/state_transition.go
- Ethereum execution specifications, Osaka transaction processing: https://github.com/ethereum/execution-specs/blob/forks/amsterdam/src/ethereum/forks/osaka/fork.py
- OP Stack operator architecture: https://github.com/ethereum-optimism/docs/blob/main/pages/operators/chain-operators/architecture.mdx
- OP fault-proof specification: https://specs.optimism.io/fault-proof/index.html
- Cosmos SDK v0.54.3: https://github.com/cosmos/cosmos-sdk/releases/tag/v0.54.3
- Cosmos SDK v0.54.3 BaseApp transaction cache/Ante flow: https://github.com/cosmos/cosmos-sdk/blob/v0.54.3/baseapp/baseapp.go
- Cosmos SDK v0.54.3 GasKV host-boundary metering: https://github.com/cosmos/cosmos-sdk/blob/v0.54.3/store/gaskv/store.go
- Cosmos SDK v0.54 migration notes: https://github.com/cosmos/cosmos-sdk/blob/release/v0.54.x/UPGRADING.md
- CometBFT v0.39.3 consensus: https://github.com/cometbft/cometbft/blob/v0.39.3/spec/consensus/consensus.md
- Aptos node v1.47.1 transaction `Discard`/`Keep` status: https://github.com/aptos-labs/aptos-core/blob/aptos-node-v1.47.1/types/src/transaction/mod.rs
- Sui mainnet v1.74.1 protocol resource configuration: https://github.com/MystenLabs/sui/blob/mainnet-v1.74.1/crates/sui-protocol-config/src/lib.rs
- IBC-Go v11.1.0: https://github.com/cosmos/ibc-go/releases/tag/v11.1.0
- Avalanche L1s: https://build.avax.network/docs/avalanche-l1s
- Avalanche validator manager: https://build.avax.network/docs/avalanche-l1s/validator-manager/contract
- Solana accounts: https://solana.com/docs/core/accounts
- Agave validator requirements: https://docs.anza.xyz/operations/requirements
- Firedancer/Frankendancer status: https://docs.firedancer.io/guide/getting-started.html
- Polkadot parachains: https://docs.polkadot.com/reference/parachains/
- Polkadot runtime upgrades: https://docs.polkadot.com/parachains/runtime-maintenance/runtime-upgrades/
- Celestia data availability: https://docs.celestia.org/learn/celestia-101/data-availability/
