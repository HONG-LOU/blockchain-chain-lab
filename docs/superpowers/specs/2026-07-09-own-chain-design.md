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
- ChainLab-native raw transaction encoding for offline signing and later broadcast.
- Transaction types for transfer, batched transfer/call user operations, contract deployment, contract calls, staking, unstaking, proposal submission, proposal execution, and governance voting. Transactions can optionally carry a `signer` for single-owner contract-account authorization or `authorizations` for multisig threshold accounts.
- Validator lifecycle starts with staked validator join transactions, explicit validator leave transactions, and validator slashing transactions. The active validator set is committed into state roots.
- Gas accounting with gas limit, legacy gas price, EIP-1559-style max fee / priority fee caps, deterministic fee charging, base fee burn, priority fee rewards, and native paymaster-sponsored fee payment.
- Block production with deterministic transaction, receipt, and state roots.
- Local proof-of-authority validation with a validator set.
- Local safe/finalized checkpoints derived from BFT-style validator commit certificates when quorum exists, with conservative block-depth fallback when no certificate exists. This models modern read semantics without claiming full Tendermint/HotStuff networking.
- Mempool that validates signatures, nonces, balances, and gas before inclusion.
- Pending nonce calculation and txpool inspection for uncommitted transactions.
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
- Production-grade arbitrary WASM execution, CosmWasm compatibility, or EVM bytecode execution.
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

Maintains accounts, native balances, contract storage, staking records, governance proposals, governance parameters, active validators, and deterministic state roots.

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
- `wasm.echo.v1`: a sandboxed WASM-backed example contract that reads args through a restricted host ABI, writes contract storage, emits events, and supports read-only return data.

Uploaded contract code:

- `wasm.upload`: validates a WASM module, stores bytecode in consensus state, commits it into snapshots and state roots, and returns a deterministic `wasm:<keccak>` code id.
- Deploy can use either a built-in code id or an uploaded WASM code id.

This gives the chain smart-contract behavior now, while leaving a clean slot for EVM, CosmWasm, or a richer WASM ABI later.
Native contracts expose read-only methods through `eth_call` and CLI `query call`; `eth_call` accepts ChainLab-native `payload` calls or limited Solidity-style `data` / `input` selectors for `get()`, `symbol()`, `owner()`, and `balanceOf(address)`, and returns minimal ABI-shaped data for `uint256`, `address`, and dynamic `string` results. A full ABI registry and general calldata decoder remain later work.
The first WASM path is intentionally constrained to the ChainLab host ABI, not CosmWasm compatibility or unrestricted system access.

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
- `GET /proposal/{id}`
- `GET /param/{key}`
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
- staking and governance proposal lifecycle transactions produce expected proposal and parameter state
- RPC exposes chain state and accepts signed transactions
- CLI demo completes without errors

## Future Phases

Phase 2:

- persistent storage for blocks, committed state, and transaction lookup
- multi-node devnet
- EVM-compatible transaction and JSON-RPC subset
- WASM runtime experiment with a constrained built-in module
- faucet and explorer page

Phase 2 progress:

- Node snapshots can be persisted under `--data-dir` and reloaded on restart.
- Committed transactions are indexed by hash with receipt, block hash, height, and index.
- Receipt events are indexed from the canonical chain with block hash, height, transaction index, event index, EVM-style log index, address, topic0, and attributes. The node rebuilds this event index from persisted blocks on restart and after longer-branch reorg replay.
- REST adds `GET /tx/{hash}`.
- JSON-RPC adds an EVM-compatible read subset: `eth_chainId`, `eth_blockNumber`, `eth_getBalance`, `eth_getTransactionCount`, `eth_getCode`, `eth_getStorageAt`, `eth_getTransactionByHash`, `eth_getTransactionReceipt`, `eth_getBlockByNumber`, `eth_getLogs`, `eth_newFilter`, `eth_getFilterLogs`, `eth_getFilterChanges`, `eth_uninstallFilter`, `eth_call`, and `eth_estimateGas`.
- Contract receipt events are projected from the node event index into EVM-style logs with block range, contract address, and topic filtering. `topic[0]` is the Keccak hash of the native event type string, not a full Solidity ABI signature.
- RPC keeps an in-memory EVM-style log filter registry for polling incremental event changes. Filters are node-local and restart-volatile, matching the development-chain scope; a separate external indexer remains a later milestone.
- `eth_call` supports native contract read methods such as `counter.get`, `token.balanceOf`, `token.symbol`, and `token.owner`; callers can use ChainLab `payload` objects or limited Solidity-style calldata selectors for `get()`, `symbol()`, `owner()`, and `balanceOf(address)`. Results return minimal ABI-shaped `uint256`, `address`, and dynamic `string` data while CLI `query call` decodes it back to a readable value; `eth_estimateGas` returns the deterministic gas schedule for supported transaction types.
- `eth_getCode` returns `0x` for accounts without code and a deterministic hex projection of ChainLab account `CodeID` for contract accounts. `eth_getStorageAt` reads current canonical ChainLab storage by plain key or hex-encoded key and returns one 32-byte word; full EVM compiler storage-layout decoding and historical archive-state reads remain later work.
- Local devnet peers can relay submitted transactions, produce a block through `POST /chain/produce`, broadcast produced blocks, relay finality votes, and import peer blocks after replaying transactions and checking receipt root, state root, PoA signature, height, and parent hash.
- Nodes now retain known imported branches and use a simple longest-branch fork-choice for the local devnet: equal-height side branches are stored without replacing the canonical head, and a longer validated branch triggers a canonical reorg by replaying from the persisted genesis state and rebuilding state plus transaction and event indexes. This is still not BFT finality.
- CLI nodes accept repeated `--peer` URLs for HTTP peer sync.
- CLI wallet-style commands can fetch nonce over RPC, sign and submit transfers, native contract deploys, native contract write calls, produce blocks, and query head/account/transaction records.
- RPC and CLI expose mempool state through `GET /txpool`, `txpool_status`, `txpool_content`, and `query mempool`; `eth_getTransactionCount(..., "pending")` replays pending transactions on a cloned state so wallet-style commands can submit multiple uncommitted transactions with sequential nonces.
- Raw transaction submission is available through `eth_sendRawTransaction`, `POST /tx/raw`, CLI `tx transfer --raw-only`, and CLI `tx raw-submit`. The current raw format is ChainLab's signed transaction JSON encoded as `0x` hex; full Ethereum RLP/EIP-1559 raw transaction compatibility remains future work.
- Nodes expose deterministic `safe` and `finalized` checkpoints through `GET /chain/finality`, JSON-RPC `chain_finality`, CLI `query finality`, and EVM-style `safe` / `finalized` block tags. A block with a valid `finality_certificate` from more than two thirds of the active validators becomes both safe and finalized with source `bft_certificate`; otherwise the node falls back to head-minus-one safe and head-minus-two finalized checkpoints, clamped to genesis. Submitted finality votes are relayed to configured HTTP peers through `/peer/finality-vote` so peers that already imported the block can assemble the same certificate. If the same validator signs conflicting block hashes at the same height, the second vote is rejected and persisted as `FinalityEquivocationEvidence`, exposed through `GET /chain/finality/evidence`, JSON-RPC `chain_finalityEvidence`, and CLI `query finality-evidence`; when the local node can act as an active reporter and the target still has stake, it also signs a normal `validator.slash` transaction into the mempool.
- Devnet faucet support is available through `POST /faucet`, JSON-RPC `chain_faucet`, and CLI `faucet request`. It signs a normal proposer-funded transfer into the mempool and relies on block production for settlement; it is a development utility, not a production issuance or airdrop mechanism.
- A local block explorer is available at `GET /explorer`; it server-renders head/finality checkpoints, recent blocks, head-block transactions, pending mempool transactions, and validators from node state without adding a separate frontend build.
- The explorer supports drill-down pages for `GET /explorer/block/{height}`, `GET /explorer/tx/{hash}`, `GET /explorer/account/{address}`, and `GET /explorer/events`, with overview links for block, transaction, sender, recipient, proposer, validator, and recent contract-event navigation.
- The default contract runtime includes a constrained wazero-backed `wasm.echo.v1` contract. The module can copy transaction args from the host, write contract storage, emit events, and set read return data through ChainLab-specific host functions. This proves the WASM VM boundary.
- Chain state supports uploaded WASM modules through `wasm.upload`; uploaded bytecode is validated, stored in snapshots, included in state roots, and deployable by the returned deterministic code id. CLI `tx wasm-upload` can submit a `.wasm` file, `0x` bytecode, or the built-in `--example echo` module for local E2E testing.
- WASM upload gas now scales with bytecode size. WASM deploy and write-call execution meters deterministic ChainLab host ABI usage for instantiation, argument copies, storage reads/writes, return data, emitted event bytes, and static exported function-body fuel. Runtime calls also use wazero context cancellation to interrupt runaway guest execution; this is a host sandbox safety valve, while deterministic runtime step gas remains a future VM-level improvement.
- Blocks now carry `base_fee_per_gas`, `gas_limit`, and `gas_used`; transactions can use legacy `gas_price` or EIP-1559-style `max_fee_per_gas` / `max_priority_fee_per_gas`; receipts record effective gas price, burned base fee, and proposer priority fee. RPC exposes `eth_gasPrice`, `eth_maxPriorityFeePerGas`, `chain_feeMarket`, and EVM block fee fields, while CLI exposes `query fees` and capped transfer flags. This models the fee market without claiming full Ethereum typed raw transaction compatibility.
- Transactions can include a native paymaster authorization. The user signs the operation and consumes their own nonce, the paymaster signs the exact user-signed transaction, and execution charges gas to the paymaster while preserving replay-deterministic receipts through `fee_payer` and a `paymaster.sponsored` event. RPC exposes `chain_sendUserOperation`, and CLI transfers support `--paymaster-private-key`. This models sponsored gas/account-abstraction UX without claiming full ERC-4337 EntryPoint or EIP-7702 compatibility.
- Transactions can use native `batch` user operations. A batch consumes one sender nonce, executes transfer/call operations atomically on one cloned state, charges one transaction-level fee, supports paymaster sponsorship, and exposes operation count in RPC/explorer views. CLI `tx batch-transfer` builds a repeated `--to address:amount` batch. This models one-click account-abstraction UX without claiming full ERC-4337 EntryPoint or EIP-7702 compatibility.
- The default native runtime includes `account.v1`, a single-owner smart contract account. A transaction with `from=<account contract>` and `signer=<owner>` is authorized by the stored owner; nonce, value transfers, and default fee payment belong to the contract account, while the owner EOA is only the authorization key. CLI transfer and batch-transfer commands expose this through `--from`, and RPC/explorer projections include `signer`.
- The default native runtime includes `multisig.v1`, a threshold smart contract account. It stores comma-separated owners and a threshold, verifies distinct owner signatures from transaction `authorizations`, consumes the multisig account nonce, spends the multisig account balance, supports paymaster sponsorship, and exposes authorization count through RPC/explorer projections. CLI transfer and batch-transfer commands add repeated `--auth-private-key` flags for owner signatures.
- Genesis files include a `validators` array, and `chainlab node --private-key` can start a different local validator from the same genesis file.
- PoA now enforces deterministic proposer rotation and treats repeated imports of already-known canonical blocks as idempotent peer sync events.
- Staked accounts can submit `validator.join`; accepted joins update the active validator set for subsequent block scheduling, are included in state roots, persist in snapshots, and can be queried over REST, JSON-RPC, and CLI.
- Active validators can submit `validator.leave`; accepted leaves remove them from the active validator set, refuse to remove the final validator, update subsequent block scheduling, persist in snapshots, and are available through CLI transaction submission.
- Active validators can submit `validator.slash` with target, amount, and evidence; accepted slashes burn target stake, remove a depleted target from the active validator set while preserving at least one validator, update scheduling, and are available through CLI transaction submission.
- Governance now has an explicit proposal lifecycle: staked accounts can submit `proposal.submit` parameter-change proposals, staked voters can cast `vote` transactions while the voting period is open, and `proposal.execute` applies passing `param.change` proposals after the period closes. Proposal metadata, votes, voters, status, and executed params are committed into state roots, persisted in snapshots, exposed over REST/JSON-RPC, and available through CLI commands.

Phase 3:

- choose OP Stack, Cosmos SDK, Avalanche L1, or another production base
- bridge from phase 1 concepts into that framework
- validator operations, monitoring, snapshots, and upgrade governance
