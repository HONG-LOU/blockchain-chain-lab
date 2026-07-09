# Current Blockchain Technology Roadmap

As of 2026-07-10, the practical way to build a serious chain is not to copy one monolithic L1. Treat a chain as layers that can be swapped: execution, consensus, data availability, networking, state storage, RPC/indexing, wallets, bridges, governance, and operations.

## Mainstream Directions

- Ethereum-aligned chains: EVM compatibility, L2 rollups, blob/data-availability scaling, account abstraction, MEV-aware block building, and increasingly stronger finality and statelessness work.
- Solana-aligned chains: high-throughput parallel execution, Rust/sBPF programs, account-based state passed into instructions, localized fees, low-latency consensus work, and multiple independent validator clients.
- Cosmos-style appchains: application-specific chains built from modules, usually with CometBFT consensus, IBC interoperability, staking, governance, and optional EVM compatibility.
- Polkadot/Substrate-style chains: Rust runtimes compiled to WASM, modular FRAME pallets, runtime upgrades through on-chain governance, and shared-security parachain deployment.
- Modular rollups: keep execution custom, but outsource settlement and/or data availability to systems such as Ethereum blobs or Celestia-style DA layers.

## Recommended Path For ChainLab

1. Keep ChainLab as a learning and prototype chain. The goal is to understand the state transition function, blocks, receipts, roots, mempool, signatures, RPC, contract VM boundary, and local validator operations.
2. Add advanced concepts in small verified slices: uploaded WASM, deterministic resource metering, ABI encoding, richer event indexing, BFT finality simulation, and EVM-style raw transactions.
   - Current ChainLab progress includes uploaded WASM code, deterministic WASM bytecode/host-resource/static function-body fuel, host-side WASM runtime timeout protection, limited Solidity-style `eth_call` calldata selectors plus ABI-shaped return data, EVM-style `eth_getCode` / `eth_getStorageAt` projections over ChainLab code IDs, delegated EOA code IDs, and native storage keys, EVM-style block hash / transaction-count / block-indexed transaction reads, block receipt reads through `eth_getBlockReceipts`, receipt-backed `debug_traceTransaction` summaries, JSON-RPC batch request bodies with ordered responses, WebSocket `eth_subscribe("newHeads")` header notifications, filtered `eth_subscribe("logs")` event notifications, and `eth_subscribe("newPendingTransactions")` pending hash notifications, basic `web3` / `net` / local account and mining probe RPCs, EVM-style block filter polling, pending and queued txpool transaction lookup through `eth_getTransactionByHash`, pending transaction filter polling through `eth_newPendingTransactionFilter`, node-local nonce-gap queued transactions with automatic promotion, Geth-style 10 percent same-sender/same-nonce pending or queued transaction replacement, an EIP-1559-style local fee market with `eth_feeHistory`, native signed JSON raw transactions plus limited Ethereum EIP-1559 type-2 raw value transfers, native paymaster-sponsored gas, native batched user operations, native single-owner and multisig smart contract accounts, native EIP-7702-style delegated EOAs through `set_code`, a canonical node event index for EVM-style logs/filter polling/explorer event pages, validator set changes, BFT-style finality certificates over PoA blocks, HTTP peer relay for finality votes, finality double-vote evidence capture, automatic local evidence-to-slashing transactions, and an explicit governance proposal lifecycle for parameter changes.
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
- Data and indexing: LevelDB/RocksDB/Pebble, pruning, archive nodes, event/log indexing, filter polling/subscriptions, block explorers, JSON-RPC compatibility, and tracing.
- Security and economics: gas markets, spam resistance, slashing, governance upgrades, bridge risk, MEV, audits, fuzzing, property tests, formal specs, and incident response.
- Product layer: wallets, account abstraction, paymasters/sponsored fees, stablecoins, token standards, NFT/RWA primitives, oracles, bridges, and compliance boundaries.

## Best Practices

- Make the state transition function deterministic and replayable before adding networking complexity.
- Commit contract code, validator sets, and governance state into state roots; never rely on node-local registries for consensus behavior.
- Separate finality certificate semantics from full consensus claims. ChainLab can now attach >2/3 validator commit signatures to a PoA block, relay those votes across configured HTTP peers, expose certified blocks as `safe`/`finalized`, record evidence when a validator double-votes for conflicting hashes at the same height, and turn that evidence into a normal local `validator.slash` transaction when the reporter can submit it; production BFT still needs rounds/timeouts, fork-choice integration, and robust slashing economics.
- Keep VM host functions minimal: no filesystem, network, clock, randomness, or global mutable process state.
- Charge deterministic fees for every consensus-relevant resource: bytecode size, storage growth, CPU steps, memory, logs, and state reads/writes.
- Keep fee, metering, and raw-transaction compatibility honest about scope: ChainLab now has a local EIP-1559-style base fee, base fee burn, priority fee rewards, uploaded WASM bytecode fees, host ABI storage/event/arg/return resource gas, deterministic static WASM function-body fuel, host-side context timeout protection for runaway WASM, and limited Ethereum EIP-1559 type-2 raw value transfers. Deterministic runtime step gas, general EVM calldata, contract creation, legacy RLP, and type-1 access-list transactions remain separate milestones.
- Treat txpool policy as consensus-adjacent operational logic, not only an RPC detail. ChainLab now supports a node-local pending/queued txpool, nonce-gap queued transaction promotion, and 10 percent same-sender/same-nonce transaction replacement in pending or queued pools for legacy gas-price and EIP-1559-style fee-cap transactions; eviction, journaling, capacity limits, local exemptions, and MEV-aware ordering remain separate milestones.
- Treat account abstraction as a user-operation workflow, not only a wallet UI feature. ChainLab now supports native paymaster sponsorship, atomic batched user operations, `account.v1` single-owner smart accounts, `multisig.v1` threshold accounts where the contract account owns nonce/balance while owners authorize through signatures, and a native EIP-7702-style delegated EOA experiment where an EOA keeps its address/balance/nonce while `account.v1` owner logic authorizes future operations. Full Ethereum type-4 raw transaction compatibility, ERC-4337 EntryPoint compatibility, policy-based paymasters, social recovery, session keys, and programmable account policies remain separate milestones.
- Prefer boring cryptography and audited libraries; do not invent signature schemes, hash functions, bridges, or consensus protocols for production.
- Treat bridges and upgrade keys as the highest-risk parts of the system.
- Build every feature with replay tests, persistence tests, CLI/RPC tests, and at least one real-node end-to-end path.

## Reference Points

- Ethereum roadmap: https://ethereum.org/roadmap/
- Ethereum account abstraction: https://ethereum.org/roadmap/account-abstraction/
- EIP-7702: https://eips.ethereum.org/EIPS/eip-7702
- ERC-4337/paymasters: https://docs.erc4337.io/paymasters/
- Solana programs and account model: https://solana.com/docs/core/programs
- OP Stack: https://docs.optimism.io/op-stack/protocol/getting-started
- Cosmos developer docs: https://docs.cosmos.network/
- Avalanche L1s: https://build.avax.network/docs/avalanche-l1s
- Polkadot parachain development: https://docs.polkadot.com/parachains/get-started/
- Celestia data availability: https://docs.celestia.org/learn/celestia-101/data-availability/
