# Current Blockchain Technology Roadmap

As of 2026-07-09, the practical way to build a serious chain is not to copy one monolithic L1. Treat a chain as layers that can be swapped: execution, consensus, data availability, networking, state storage, RPC/indexing, wallets, bridges, governance, and operations.

## Mainstream Directions

- Ethereum-aligned chains: EVM compatibility, L2 rollups, blob/data-availability scaling, account abstraction, MEV-aware block building, and increasingly stronger finality and statelessness work.
- Solana-aligned chains: high-throughput parallel execution, Rust/sBPF programs, account-based state passed into instructions, localized fees, low-latency consensus work, and multiple independent validator clients.
- Cosmos-style appchains: application-specific chains built from modules, usually with CometBFT consensus, IBC interoperability, staking, governance, and optional EVM compatibility.
- Polkadot/Substrate-style chains: Rust runtimes compiled to WASM, modular FRAME pallets, runtime upgrades through on-chain governance, and shared-security parachain deployment.
- Modular rollups: keep execution custom, but outsource settlement and/or data availability to systems such as Ethereum blobs or Celestia-style DA layers.

## Recommended Path For ChainLab

1. Keep ChainLab as a learning and prototype chain. The goal is to understand the state transition function, blocks, receipts, roots, mempool, signatures, RPC, contract VM boundary, and local validator operations.
2. Add advanced concepts in small verified slices: uploaded WASM, deterministic resource metering, ABI encoding, richer event indexing, BFT finality simulation, and EVM-style raw transactions.
   - Current ChainLab progress includes uploaded WASM code, deterministic WASM host-resource gas, validator set changes, and an explicit governance proposal lifecycle for parameter changes.
3. Once the product target is clear, choose a production base instead of shipping the custom dev chain as a mainnet:
   - OP Stack or another Ethereum rollup stack if liquidity, Solidity, and wallet compatibility matter most.
   - Cosmos SDK if the chain needs sovereign governance, IBC, and custom modules.
   - Avalanche L1 if an EVM appchain with configurable validator economics is the fastest fit.
   - Polkadot SDK if runtime upgrades and WASM state-transition logic are central to the product.
   - Solana/SVM only if the product genuinely needs high-throughput account-parallel execution and the team is ready for Rust program development and Solana-style operations.

## Technology Stack To Learn

- Core implementation: Go or Rust, deterministic serialization, Merkle/state roots, append-only storage, replay, snapshots, and crash recovery.
- Cryptography: secp256k1/ed25519 signatures, Keccak/SHA-2/SHA-3, BLS basics, VRFs, multisig, threshold signatures, and post-quantum migration awareness.
- Consensus and networking: PoA for devnets, Tendermint/HotStuff-style BFT, fork choice, finality, p2p gossip, mempool policy, peer scoring, and validator operations.
- Execution: EVM/Solidity/Foundry, WASM/wazero or Wasmtime, CosmWasm, Solana Rust/sBPF, ABI design, gas/resource metering, deterministic host functions, and VM sandboxing.
- Data and indexing: LevelDB/RocksDB/Pebble, pruning, archive nodes, event/log indexing, block explorers, JSON-RPC compatibility, and tracing.
- Security and economics: gas markets, spam resistance, slashing, governance upgrades, bridge risk, MEV, audits, fuzzing, property tests, formal specs, and incident response.
- Product layer: wallets, account abstraction, paymasters/sponsored fees, stablecoins, token standards, NFT/RWA primitives, oracles, bridges, and compliance boundaries.

## Best Practices

- Make the state transition function deterministic and replayable before adding networking complexity.
- Commit contract code, validator sets, and governance state into state roots; never rely on node-local registries for consensus behavior.
- Keep VM host functions minimal: no filesystem, network, clock, randomness, or global mutable process state.
- Charge deterministic fees for every consensus-relevant resource: bytecode size, storage growth, CPU steps, memory, logs, and state reads/writes.
- Keep metering honest about scope: ChainLab currently charges uploaded WASM bytecode size and host ABI storage/event/arg/return resources; CPU instruction/fuel metering remains a separate VM-level milestone.
- Prefer boring cryptography and audited libraries; do not invent signature schemes, hash functions, bridges, or consensus protocols for production.
- Treat bridges and upgrade keys as the highest-risk parts of the system.
- Build every feature with replay tests, persistence tests, CLI/RPC tests, and at least one real-node end-to-end path.

## Reference Points

- Ethereum roadmap: https://ethereum.org/roadmap/
- Solana programs and account model: https://solana.com/docs/core/programs
- OP Stack: https://docs.optimism.io/op-stack/protocol/getting-started
- Cosmos developer docs: https://docs.cosmos.network/
- Avalanche L1s: https://build.avax.network/docs/avalanche-l1s
- Polkadot parachain development: https://docs.polkadot.com/parachains/get-started/
- Celestia data availability: https://docs.celestia.org/learn/celestia-101/data-availability/
