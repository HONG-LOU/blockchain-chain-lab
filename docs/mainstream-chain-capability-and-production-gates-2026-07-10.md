# Mainstream Chain Capability And Production Gates

Audit date: 2026-07-10

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
| State transition and roots | Executor, canonical hashing, replay tests | Initial production protocol implementation with incomplete evidence | Add property tests, cross-process/cross-architecture vectors, inclusion proofs, and an independent verifier. |
| Finality | PoA blocks plus validator-count commit certificates | PoA finality-attestation simulation | Either make certified checkpoints non-revertible with epoch/voting-power rules and safety tests, or keep `safe/finalized` explicitly heuristic. Production BFT remains an external-stack responsibility. |
| Fork choice | Longer-height canonical branch wins | Development fork choice | Must not reorg behind a certified checkpoint. Add adversarial reorg and validator-set transition tests. |
| WASM | Wasmtime-Go v46.0.1 with deterministic fuel, versioned guest/host/instantiation gas, fixed declared memory/table sizes, structural and ABI admission, bounded host I/O/events/writes, read isolation, rollback, state-bound singleflight caching, a process-local sticky runtime-fault halt, and a committed Windows development golden vector | Initial deterministic runtime implementation; not activated for production validators | P0: deterministic guest call-depth independent of native stack layout, Linux/amd64 release replay, hard native/JIT memory lifecycle bounds, charged failed transactions, versioned native-contract gas, persistent protocol-wide fatal runtime-fault halt plus deterministic recovery/upgrade behavior across ABCI++ validators, reproducible artifacts/checksums/SBOM, fuzz/load/dependency evidence, activation, and external audit. Windows is development-only; other validator targets are unsupported until proven. |
| Persistence | Whole-chain JSON candidate snapshot written before in-memory canonical-head switch via temporary rename | Development snapshot with explicit-error API atomicity, but no fsync/WAL/power-loss guarantee | P0: embedded KV with atomic batches, schema/version, WAL/reopen, directory/file fsync, corruption and kill fault injection, state snapshots, and explicit pruned/full/archive modes. |
| Networking | Configured HTTP peer relay/import | Local devnet transport | Replace on the production path with CometBFT discovery, gossip, peer management, evidence propagation, state sync, sentry topology, and partition/load validation. |
| Txpool | Pending/queued with 2 MiB transaction, 128 MiB/4,096 total, 2,048 queued, 64-per-sender and 64-nonce-gap caps; principal-safe replacement, block-gas admission, bounded batches, and canonical-import revalidation | Bounded functional local txpool | P0: deterministic fee-aware eviction/TTL, journal/restart, incremental accounting, gas-bounded proposal simulation/revalidation, orphaned-transaction reinsertion after reorg, local policy, charged execution failures, and sustained DoS/load evidence. |
| RPC/indexing | REST, EVM-shaped JSON-RPC, filters, WebSocket, local event index; static/base gas estimation plus byte-scaled WASM upload estimation | Useful development interface | P0: bound imported block/transaction and RPC/peer bodies before decode/copy, dynamic state-executing gas estimation for WASM deploy/call/batch-call, batch/log-range limits, rate limits, timeouts, WS backpressure, pagination, external indexer boundary, and canonical reorg semantics. |
| Account policies | Native smart accounts, multisig, paymaster, batch, delegated EOA, session keys, guardian recovery | ChainLab-native AA experiments | Keep native semantics explicit. ERC-4337/7702 conformance requires canonical encodings, EntryPoint/bundler behavior, official vectors, and ecosystem tests. ERC-7579 is still draft and is research-only. |
| Governance/upgrades | Parameter proposals | Minimal governance demo | P0: protocol version, scheduled activation, deterministic migration, rollback policy, upgrade test vectors and runbook. Token economics and upgrade-key governance require a product threat model. |
| Operations | `/health`, CLI keys, snapshots | Development operations | P0: readiness/liveness, metrics, structured logs, backup/restore drill, keystore/permissions/rotation, remote-signer boundary, incident runbooks. |
| Security evidence | Unit/integration tests | Insufficient for hostile deployment | P0: fuzz/property/race, malicious WASM/RPC/raw-tx corpora, dependency/vulnerability scan, benchmarks/load, threat model, external review. |

## Production Completion Gates

Every gate needs an authoritative test or artifact. A passing narrow unit test cannot prove a broader gate.

1. **Deterministic execution**
   - No wall clock, scheduler timing, filesystem, network, or process-global mutable state may determine consensus output.
   - Meter instructions, memory, tables, stack depth, host I/O, emitted bytes, reads, writes, and storage growth.
   - Replay identical blocks in fresh processes and supported GOARCH targets; compare receipt and state roots.

2. **Finality-safe local fork choice**
   - Define certificate validator set, epoch, voting power, quorum rounding, equivocation, and transition rules.
   - Reject any imported branch conflicting with the highest certified checkpoint.
   - If not implemented, rename the feature consistently to finality attestation and do not project certificate-backed finality as production BFT.

3. **Durable versioned storage**
   - Atomic block/state/index commit, restart after injected faults, versioned migrations, checksums/corruption handling.
   - Define pruned, full, and archive read guarantees; verify state sync/snapshot import independently.

4. **Protocol lifecycle**
   - Version every consensus-relevant encoding and state schema.
   - Schedule upgrades by height, execute deterministic migrations, reject incompatible nodes, and document rollback limits.

5. **Resource and abuse controls**
   - Bound txpool, RPC bodies/batches/ranges, events, contract bytecode, account storage, subscriptions, peers, and queues.
   - Meter native contracts and every state-dependent operation with a versioned schedule; charge nonce and fees for included execution failures.
   - Hard-bound compiled native/JIT memory and prevent pending-state replay from recompiling admitted uploads.
   - Verify backpressure, eviction, restart journals, malformed input, and sustained-load behavior.

6. **Security test program**
   - Fuzz raw/native/Ethereum transaction decoding, JSON-RPC parsing, WASM validation, state transition, and reorg replay.
   - Property-test root determinism, nonce, fee conservation, atomic batch rollback, txpool ordering, finality checkpoints, and snapshot round trips.
   - Run `go test -race`, `go vet`, `govulncheck`, benchmarks, fault injection, and dependency review in CI.

7. **Operational evidence**
   - Metrics, readiness/liveness, structured logs, dashboards/alerts, backup restore, key rotation, validator recovery, and incident exercises.
   - Validator secrets must not rely on command-line arguments in an operational profile.

8. **Verifiability boundary**
   - Add transaction/receipt/state inclusion proofs and an independent verifier, or use the narrower term “locally replay-verifiable.”

## Parallel Implementation Tracks

- Build the ChainLab ABCI++ adapter and make CometBFT the authoritative production block lifecycle. Verify validator updates, proposal processing, vote extensions where used, evidence, state sync, snapshots, and upgrade compatibility in multi-process networks.
- Replace whole-chain JSON persistence with transactional, versioned application storage and independently restorable snapshots before public testnet.
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
- OP Stack operator architecture: https://github.com/ethereum-optimism/docs/blob/main/pages/operators/chain-operators/architecture.mdx
- OP fault-proof specification: https://specs.optimism.io/fault-proof/index.html
- Cosmos SDK v0.54.3: https://github.com/cosmos/cosmos-sdk/releases/tag/v0.54.3
- Cosmos SDK v0.54 migration notes: https://github.com/cosmos/cosmos-sdk/blob/release/v0.54.x/UPGRADING.md
- CometBFT v0.39.3 consensus: https://github.com/cometbft/cometbft/blob/v0.39.3/spec/consensus/consensus.md
- IBC-Go v11.1.0: https://github.com/cosmos/ibc-go/releases/tag/v11.1.0
- Avalanche L1s: https://build.avax.network/docs/avalanche-l1s
- Avalanche validator manager: https://build.avax.network/docs/avalanche-l1s/validator-manager/contract
- Solana accounts: https://solana.com/docs/core/accounts
- Agave validator requirements: https://docs.anza.xyz/operations/requirements
- Firedancer/Frankendancer status: https://docs.firedancer.io/guide/getting-started.html
- Polkadot parachains: https://docs.polkadot.com/reference/parachains/
- Polkadot runtime upgrades: https://docs.polkadot.com/parachains/runtime-maintenance/runtime-upgrades/
- Celestia data availability: https://docs.celestia.org/learn/celestia-101/data-availability/
