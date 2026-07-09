# Own Chain Design

## Goal

Build a local-first blockchain implementation that can evolve toward a production appchain. The first release must run on one machine, produce verifiable blocks, process signed account transactions, maintain deterministic state roots, expose an HTTP RPC, and support a smart-contract-like execution interface.

This project is not a mainnet-ready L1 in its first release. It is a serious development chain with clear seams for replacing the local proof-of-authority consensus, native contract runtime, and storage backend with production components later.

## Recommended Architecture

The first implementation is a Go monorepo named `chainlab`.

Go is selected because this workstation has Go 1.25 available, Go is common in production blockchain clients, and it is practical for networking, cryptography, and deterministic state-machine code. Rust/Solana-style and WASM work can be added later once the local chain core is proven.

## Alternatives Considered

1. EVM Rollup stack first, using OP Stack or Arbitrum Orbit.
   - Strongest ecosystem compatibility.
   - Heavy local setup and operational complexity.
   - Best later when the product target is known.

2. Cosmos SDK appchain.
   - Production-grade modular chain framework.
   - Good fit for appchain governance, staking, and IBC.
   - Too much framework surface before Eric understands the chain internals.

3. Custom Go development chain.
   - Best learning and control for this phase.
   - Lets us build and verify the core state machine directly.
   - Requires honesty that this is a dev chain, not production consensus.

The selected path is option 3 for phase 1, with phase 2 preparing EVM/WASM compatibility and phase 3 selecting a production appchain or rollup framework.

## Phase 1 Scope

Phase 1 creates a runnable local blockchain with these capabilities:

- Account model with balances, nonces, optional contract code id, and key-value storage.
- Secp256k1 transaction signatures and Ethereum-style 20-byte addresses.
- Transaction types for transfer, contract deployment, contract calls, staking, unstaking, and governance voting.
- Validator lifecycle starts with staked validator join transactions. The active validator set is committed into state roots.
- Gas accounting with gas limit, gas price, and deterministic fee charging.
- Block production with deterministic transaction, receipt, and state roots.
- Local proof-of-authority validation with a validator set.
- Mempool that validates signatures, nonces, balances, and gas before inclusion.
- Native deterministic contract runtime with built-in example contracts.
- Modules for staking and governance.
- HTTP RPC for chain head, account lookup, transaction submission, and block lookup.
- CLI commands for key generation, genesis initialization, local node, and demo execution.
- Automated tests for consensus-critical behavior.

## Out Of Scope For Phase 1

- Public mainnet launch.
- Byzantine fault tolerant multi-node consensus.
- P2P networking.
- Production bridge security.
- Permissionless arbitrary WASM or EVM bytecode execution.
- Slashing economics tied to real stake.
- Production-grade wallet UX.

These are intentionally deferred because getting them wrong is more dangerous than not shipping them.

## Core Components

### `internal/crypto`

Owns secp256k1 key generation, signing, verification, address derivation, and hex encoding helpers. It uses pure-Go `github.com/decred/dcrd/dcrec/secp256k1/v4` and `golang.org/x/crypto/sha3` so the project builds without a C toolchain.

### `internal/types`

Defines canonical data structures: accounts, transactions, receipts, events, blocks, headers, validators, genesis, and chain config.

All consensus-critical hashes use deterministic JSON encoding over normalized structures. This is acceptable for phase 1 and can be replaced with SSZ, RLP, Protobuf, or SCALE later.

### `internal/state`

Maintains accounts, native balances, contract storage, staking records, governance proposals, active validators, and deterministic state roots.

The phase 1 backend is in-memory with snapshot cloning for tests. A disk backend can be added behind the same store interface.

### `internal/contracts`

Implements a native contract runtime with a stable interface:

- `Deploy(codeID, creator, args)`
- `Call(address, caller, method, args)`
- event emission
- gas metering
- account storage access

Built-in contracts:

- `counter.v1`: increment and read a counter.
- `token.v1`: mint, transfer, and read token balances.

This gives the chain smart-contract behavior now, while leaving a clean slot for EVM or WASM later.

### `internal/core`

Executes transactions against state and produces receipts. This is the state transition function and must be heavily tested.

### `internal/consensus`

Implements local proof-of-authority block validation:

- proposer must be in the validator set
- proposer must match deterministic height-based rotation: height 1 uses validator 0, height 2 uses validator 1, then cycles
- block height must increase by one
- parent hash must match
- transaction root, receipt root, and state root must match recomputation
- block signature must verify

### `internal/node`

Owns mempool, block production, chain storage, and RPC-facing operations.

### `internal/rpc`

Exposes HTTP endpoints:

- `GET /health`
- `GET /chain/head`
- `GET /chain/block/{height}`
- `GET /account/{address}`
- `GET /validators`
- `POST /tx`
- `POST /rpc` for JSON-RPC-style calls
- EVM-style read calls include account, block, transaction, receipt, and log queries.

### `cmd/chainlab`

Single CLI binary:

- `chainlab keygen`
- `chainlab init --out config/genesis.json`
- `chainlab node --genesis config/genesis.json --listen :8547`
- `chainlab demo`

## Data Flow

1. User signs a transaction.
2. Transaction enters RPC or direct node API.
3. Mempool verifies signature, nonce, balance, and gas budget.
4. Node produces a block from valid mempool transactions.
5. Core executor applies each transaction to a cloned state.
6. Receipts and events are produced.
7. Roots are computed.
8. Consensus validates the block.
9. Node commits the new state and block.

## Error Handling

Invalid transactions are rejected before block inclusion when possible. If a contract call fails during execution, the transaction still pays base gas and receives a failed receipt. State changes from failed calls are discarded except fees.

Consensus validation errors are fatal for the candidate block and never mutate committed state.

## Testing Requirements

The phase 1 suite must prove:

- generated signatures verify and tampered transactions fail
- valid transfers update balances and nonces
- insufficient funds and bad nonce transactions fail
- state roots are deterministic
- block validation catches wrong parent, wrong roots, and unauthorized proposers
- counter contract deploy/call changes isolated contract storage
- token contract mint/transfer updates token balances
- staking and governance module transactions produce expected state
- RPC exposes chain state and accepts signed transactions
- CLI demo completes without errors

## Future Phases

Phase 2:

- persistent storage for blocks, committed state, and transaction lookup
- multi-node devnet
- EVM-compatible transaction and JSON-RPC subset
- WASM runtime experiment
- faucet and explorer page

Phase 2 progress:

- Node snapshots can be persisted under `--data-dir` and reloaded on restart.
- Committed transactions are indexed by hash with receipt, block hash, height, and index.
- REST adds `GET /tx/{hash}`.
- JSON-RPC adds an EVM-compatible read subset: `eth_chainId`, `eth_blockNumber`, `eth_getBalance`, `eth_getTransactionCount`, `eth_getTransactionByHash`, `eth_getTransactionReceipt`, `eth_getBlockByNumber`, and `eth_getLogs`.
- Contract receipt events are projected into EVM-style logs with block range, contract address, and topic filtering. `topic[0]` is the Keccak hash of the native event type string, not a full Solidity ABI signature.
- Local devnet peers can relay submitted transactions, produce a block through `POST /chain/produce`, broadcast produced blocks, and import peer blocks after replaying transactions and checking receipt root, state root, PoA signature, height, and parent hash.
- CLI nodes accept repeated `--peer` URLs for HTTP peer sync.
- CLI wallet-style commands can fetch nonce over RPC, sign and submit transfers, produce blocks, and query head/account/transaction records.
- Genesis files include a `validators` array, and `chainlab node --private-key` can start a different local validator from the same genesis file.
- PoA now enforces deterministic proposer rotation and treats repeated imports of already-known canonical blocks as idempotent peer sync events.
- Staked accounts can submit `validator.join`; accepted joins update the active validator set for subsequent block scheduling, are included in state roots, persist in snapshots, and can be queried over REST, JSON-RPC, and CLI.

Phase 3:

- choose OP Stack, Cosmos SDK, Avalanche L1, or another production base
- bridge from phase 1 concepts into that framework
- validator operations, monitoring, snapshots, and upgrade governance
