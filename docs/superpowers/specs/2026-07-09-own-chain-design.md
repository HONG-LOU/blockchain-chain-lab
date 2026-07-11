# Own Chain Design

## Goal

Build ChainLab as a production-grade sovereign blockchain. The first local release establishes and verifies the application state transition, but it is a bootstrap stage of the production protocol rather than the final architecture.

The production path retains ChainLab's protocol and Go application logic while replacing the local PoA/network/snapshot harness with CometBFT ABCI++, transactional versioned storage, deterministic WASM resource metering, protected validator signing, state sync, protocol upgrades, and production operations. The authoritative gates are in `docs/mainstream-chain-capability-and-production-gates-2026-07-10.md`; `docs/current-blockchain-tech-roadmap.md` orders their implementation.

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
   - Too much framework surface before ChainLab's application protocol and invariants were defined.

3. Custom Go development chain.
   - Best protocol ownership and control for the bootstrap phase.
   - Lets us build and verify the core state machine directly.
   - Requires honesty that this is a dev chain, not production consensus.

Option 3 remains the phase-1 implementation path. The production consensus/network foundation is now selected as CometBFT ABCI++; ChainLab stays the sovereign application protocol instead of becoming a generic Cosmos SDK application. Alternative OP Stack, Avalanche, Polkadot, and Solana paths remain architecture references for product-specific execution or ecosystem requirements.

## Phase 1 Scope

Phase 1 creates a runnable local blockchain with these capabilities:

- Account model with balances, nonces, optional contract code id, optional delegated code id, and key-value storage.
- Secp256k1 transaction signatures and Ethereum-style 20-byte addresses.
- ChainLab-native raw transaction encoding for offline signing and later broadcast, plus a limited Ethereum EIP-1559 type-2 raw transfer decoder for wallet/SDK compatibility experiments.
- Transaction types for transfer, batched transfer/call user operations, EIP-7702-style `set_code` delegation, account session-key management, guardian recovery, contract deployment, contract calls, staking, unstaking, and evidence-backed validator slashing. Governance proposal/vote/execute names remain reserved, but fail before admission until finalized voting-power snapshots, unbonding locks, deposits, and versioned activation exist. Transactions can optionally carry a `signer` for single-owner contract-account, delegated-EOA, session-key, or guardian authorization, or `authorizations` for multisig threshold accounts.
- Validator membership is fixed by genesis in the local harness. Join/leave fail before admission; cryptographically verified one-time double-sign evidence clears stake without changing that fixed set. Certified epoch transitions remain production-consensus work.
- Gas accounting with gas limit, legacy gas price, EIP-1559-style max fee / priority fee caps, deterministic fee charging, base fee burn, priority fee rewards, and native paymaster-sponsored fee payment.
- Block production with deterministic transaction, receipt, and state roots.
- Local proof-of-authority validation with a validator set.
- Local safe/finalized checkpoints derived from fixed-set validator commit certificates when quorum exists. Without a certificate, `finalized` remains at genesis and a separate depth-confirmation result is only a local safe heuristic. This does not claim full Tendermint/HotStuff networking.
- Mempool that validates signatures, nonces, balances, and gas before inclusion, with a node-local queued pool for valid future-nonce transactions.
- Pending nonce calculation and pending/queued txpool inspection for uncommitted transactions.
- Native deterministic contract runtime with built-in example contracts.
- Stake accounting plus reserved, fail-closed governance schemas and query surfaces.
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

Maintains accounts, native balances, bounded contract storage, staking records, legacy governance proposal/parameter state, the fixed genesis validator set, and deterministic state roots. New governance state transitions are disabled before admission.

The canonical store is in-memory with snapshot cloning. The development node persists a checksummed snapshot v3 plus manifest, replays from genesis, and revalidates snapshot-bound pending/queued transactions on restart; v2 migrates deterministically. This is not the transactional versioned KV backend or WAL-backed txpool required for production.

### `internal/contracts`

Implements a native contract runtime with a stable interface:

- `Deploy(codeID, creator, args)`
- `Call(address, caller, method, args)`
- event emission
- transaction base gas plus versioned WASM invocation metering; versioned
  native-contract resource gas remains a production P0
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
- `POST /rpc` for JSON-RPC-style single calls and batch calls
- EVM-style read calls include account, block, transaction, receipt, and log queries.

### `cmd/chainlab`

Single CLI binary:

- `chainlab keygen --out config/validator-2-key.json`
- `chainlab init --out config/genesis.json --key-out config/validator-1-key.json`
- `chainlab node --genesis config/genesis.json --key-file config/validator-1-key.json --listen :8547`
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

Admission-invalid transactions are rejected before block inclusion and change nothing. Deterministic OOG, trap, and native-contract failures that begin execution are included with stable failed receipts, consume nonce and gas, settle actual fees, and revert business state; OOG consumes the full gas limit. Runtime or state-invariant faults halt progress without committing ante state. These implemented local semantics still require protocol-versioned activation and ABCI++ differential evidence before production use.

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
- staking transitions succeed, while governance proposal/vote/execute transactions fail before admission without changing state, nonce, or fees
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

- `--data-dir` uses a checksummed snapshot v3 and manifest with bounded fail-closed decoding, v2 migration, replay from persisted genesis, and snapshot-bound pending/queued txpool restart. Writes fsync a temporary file, rename it, and fsync the directory; a post-rename directory-fsync failure is commit-uncertain and enters persistent sticky HALT. This contains ambiguity and rewrites the full development snapshot on persistent txpool changes; it is not a production database/WAL or completed crash-durability gate.
- Committed transactions are indexed by hash with receipt, block hash, height, and index.
- Receipt events are indexed from the canonical chain with block hash, height, transaction index, event index, EVM-style log index, address, topic0, and attributes. The node rebuilds this event index from persisted blocks on restart and after longer-branch reorg replay.
- REST adds `GET /tx/{hash}`.
- JSON-RPC accepts either one request object or a batch array with ordered responses, includes basic client/network/local-account probe methods (`web3_clientVersion`, `net_version`, `net_listening`, `eth_accounts`, `eth_coinbase`, `eth_mining`, `eth_hashrate`, `eth_syncing`), and adds an EVM-compatible read/debug subset: `eth_chainId`, `eth_blockNumber`, `eth_getBalance`, `eth_getTransactionCount`, `eth_getCode`, `eth_getStorageAt`, `eth_getTransactionByHash`, `eth_getTransactionReceipt`, `debug_traceTransaction`, `eth_getBlockReceipts`, `eth_getBlockByNumber`, `eth_getBlockByHash`, `eth_getBlockTransactionCountByHash`, `eth_getBlockTransactionCountByNumber`, `eth_getTransactionByBlockHashAndIndex`, `eth_getTransactionByBlockNumberAndIndex`, `eth_feeHistory`, `eth_getLogs`, `eth_newBlockFilter`, `eth_newPendingTransactionFilter`, `eth_newFilter`, `eth_getFilterLogs`, `eth_getFilterChanges`, `eth_uninstallFilter`, `eth_call`, and `eth_estimateGas`. Every POST is capped at 16 MiB and shares 32 execution slots; JSON-RPC additionally has a 5 MiB body cap, 32 call slots, 100 batch items, and a 16 MiB batch-response cap. Txpool query sources are capped at 8 MiB and fee history at 1,024 blocks.
- Contract receipt events are projected from the node event index into EVM-style logs with block range, contract address, and topic filtering. Queries are capped at a 10,000-block range, 1,000 results, 8 MiB, 256 addresses, four topic positions, and 256 alternatives per position. `topic[0]` is the Keccak hash of the native event type string, not a full Solidity ABI signature.
- `debug_traceTransaction` returns a receipt-backed summary for committed transactions, including gas, failure status, fee payer, native events, and transaction location. Opcode `structLogs` is intentionally empty because ChainLab is not executing EVM bytecode.
- RPC keeps an in-memory EVM-style filter registry for polling new block hashes, newly seen pending transaction hashes, and incremental event changes. Random-ID filters are capped at 1,024 and actively expire after five idle minutes; installed log definitions stop at 64 KiB, pending filters stop at 64 and 65,536 retained hashes, and block-filter polls return at most 1,000 hashes. A failed bounded poll does not advance its cursor. Filters and WebSocket subscriptions remain node-local and restart-volatile, and a separate external indexer remains a later milestone.
- `GET /rpc/ws` supports WebSocket JSON-RPC `eth_subscribe("newHeads")`, filtered `eth_subscribe("logs")`, and `eth_subscribe("newPendingTransactions")` plus `eth_unsubscribe`, broadcasting EVM-style block header payloads, matching log notifications after local production or canonical peer import, and pending transaction hashes after local txpool acceptance. The development server caps 64 connections, 64 subscriptions per connection, 1,024 subscriptions per node, 64 log subscriptions, and 100,000 log-match checks per block; bounded queues, 60-second read-idle, 25-second Ping, and 10-second write deadlines release dead or slow clients. Subscription TTL, principal ownership, and sustained backpressure evidence remain production gates.
- `eth_call` supports native contract read methods such as `counter.get`, `token.balanceOf`, `token.symbol`, and `token.owner`; callers can use ChainLab `payload` objects or limited Solidity-style calldata selectors for `get()`, `symbol()`, `owner()`, and `balanceOf(address)`. Results return minimal ABI-shaped `uint256`, `address`, and dynamic `string` data while CLI `query call` decodes it back to a readable value. `eth_estimateGas` currently returns the static/base schedule for supported transaction types and a byte-scaled WASM upload estimate; it does not simulate dynamic WASM deploy/call execution, so the 50,000-gas call estimate can be lower than actual `chainlab-wasm-v1` consumption.
- `eth_getCode` returns `0x` for accounts without code and a deterministic hex projection of ChainLab account `CodeID` for contract accounts or `DelegatedCodeID` for delegated EOAs. `eth_getStorageAt` reads current canonical ChainLab storage by plain key or hex-encoded key and returns one 32-byte word; full EVM compiler storage-layout decoding and historical archive-state reads remain later work.
- `eth_getBlockByHash`, `eth_getBlockReceipts`, block transaction-count methods, and block+index transaction lookup methods project known ChainLab blocks into the same EVM-style block, receipt, and transaction response shapes used by existing number/hash reads. Missing blocks or out-of-range transaction indexes return `null`.
- Local devnet peers can relay submitted transactions, produce a block through `POST /chain/produce`, broadcast produced blocks, relay finality votes, and import peer blocks after replaying transactions and checking receipt root, state root, PoA signature, height, and parent hash.
- Nodes now retain known imported branches and use a simple longest-branch fork-choice for the local devnet: equal-height side branches are stored without replacing the canonical head, and a longer validated branch triggers a canonical reorg by replaying from the persisted genesis state and rebuilding state plus transaction and event indexes. Transactions from orphaned old-canonical blocks are considered in deterministic block/transaction order, revalidated against the new head, bounded by the existing txpool limits, and atomically persisted with the new branch; new-chain inclusion, stale nonces, valid current-pool sender/nonce conflicts, and invalid execution suppress reinsertion. This is still not BFT finality.
- CLI nodes accept repeated `--peer` URLs for HTTP peer sync.
- CLI wallet-style commands can fetch nonce over RPC, sign and submit transfers, native contract deploys, native contract write calls, produce blocks, and query head/account/transaction records.
- RPC and CLI expose mempool state through `GET /txpool`, `txpool_status`, `txpool_content`, and `query mempool`; `eth_getTransactionCount(..., "pending")` replays pending transactions on a cloned state so wallet-style commands can submit multiple uncommitted transactions with sequential nonces. ChainLab accepts future-nonce transactions within a gap of 64 into a node-local queued pool, keeps them out of block production and pending nonce calculation, and automatically promotes them to pending when earlier nonces arrive locally or through canonical block import. Admission caps each transaction at 2 MiB, the combined pool at 128 MiB/4,096 transactions, queued entries at 2,048, and one sender at 64 entries. ChainLab accepts Geth-style same-sender/same-nonce transaction replacement in either pending or queued when the new transaction bumps the legacy gas price or both EIP-1559 fee caps by at least 10 percent and stays within byte limits; pending replacement is validated by replaying the pending pool in order with the new transaction substituted at the old transaction's index. `eth_getTransactionByHash` falls back to txpool pending or queued transactions with null block fields, replaced hashes disappear from txpool reads, `eth_newPendingTransactionFilter` lets clients poll for newly seen pending transaction hashes including accepted pending replacements and queued transactions after promotion, and `eth_getTransactionReceipt` remains `null` until block inclusion. Snapshot v3 restores and fully revalidates pending/queued state. ChainLab still lacks deterministic fee-aware eviction/TTL, a production WAL-backed journal, local exemptions, and MEV-aware ordering.
- Raw transaction submission is available through `eth_sendRawTransaction`, `POST /tx/raw`, CLI `tx transfer --raw-only`, and CLI `tx raw-submit`. ChainLab accepts its native signed transaction JSON raw envelope and a limited Ethereum EIP-1559 type-2 raw transfer subset. Type-2 support covers value transfers with empty calldata and empty access lists; general EVM calldata, contract creation, legacy RLP, and type-1 access-list transactions remain later work.
- Nodes expose deterministic `safe` and `finalized` checkpoints through `GET /chain/finality`, JSON-RPC `chain_finality`, CLI `query finality`, and EVM-style `safe` / `finalized` block tags. A block with a valid `finality_certificate` from more than two thirds of the fixed genesis validator set becomes both safe and finalized with source `bft_certificate`; without a certificate, `finalized` conservatively remains at genesis and head-depth observations use the separate `depth_confirmation` source. The highest certified block is a persisted monotonic finality lock: branches conflicting with it are rejected, compatible certificate subsets merge, and conflicting valid quorum certificates enter a persistent sticky halt. Partial votes and equivocation evidence survive restart. These are local-harness safety rules, not CometBFT rounds/prevote/precommit finality.
- Devnet faucet support is available through `POST /faucet`, JSON-RPC `chain_faucet`, and CLI `faucet request`. It signs a normal proposer-funded transfer into the mempool and relies on block production for settlement; it is a development utility, not a production issuance or airdrop mechanism.
- A local block explorer is available at `GET /explorer`; it server-renders head/finality checkpoints, recent blocks, head-block transactions, pending mempool transactions, and validators from node state without adding a separate frontend build.
- The explorer supports drill-down pages for `GET /explorer/block/{height}`, `GET /explorer/tx/{hash}`, `GET /explorer/account/{address}`, and `GET /explorer/events`, with overview links for block, transaction, sender, recipient, proposer, validator, and recent contract-event navigation.
- The default contract runtime includes a constrained Wasmtime-backed `wasm.echo.v1` contract. The module can copy transaction args from the host, write contract storage, emit events, and set read return data through ChainLab-specific host functions.
- Chain state supports uploaded WASM modules through `wasm.upload`; uploaded bytecode is validated, stored in snapshots, included in state roots, and deployable by the returned deterministic code id. CLI `tx wasm-upload` can submit a `.wasm` file, `0x` bytecode, or the built-in `--example echo` module for local E2E testing.
- WASM upload gas scales with bytecode size and rejects modules above 512 KiB before decoding/compilation. Metering version `chainlab-wasm-v1` pins Wasmtime-Go v46.0.1, prices code bytes plus fixed declared memory/table resources before instantiation, uses engine fuel for guest execution, deducts host work from the same remaining budget, rejects start/WASI/unknown ABI and growable resources, admits only a maximum-64 acyclic direct-call graph, bounds host I/O/events/storage writes and growth, isolates reads, and rolls back failed business state. Included deterministic failures retain nonce and actual fee settlement; OOG consumes the full gas limit. Remaining mainnet gates include hard JIT memory lifecycle bounds, Linux/amd64 golden block replay, reproducible native artifacts/SBOM, fuzz/load evidence, explicit protocol activation/upgrades, ABCI++ differential replay, and external review.
- Blocks now carry `base_fee_per_gas`, `gas_limit`, and `gas_used`; transactions can use legacy `gas_price` or EIP-1559-style `max_fee_per_gas` / `max_priority_fee_per_gas`; receipts record effective gas price, burned base fee, and proposer priority fee. RPC exposes `eth_gasPrice`, `eth_maxPriorityFeePerGas`, `eth_feeHistory`, `chain_feeMarket`, and EVM block fee fields, while CLI exposes `query fees` and capped transfer flags. `eth_feeHistory` derives base fee history, gas-used ratios, next base fee, and optional gas-weighted priority-fee reward percentiles from canonical blocks and receipts. This models the fee market and accepts a limited Ethereum type-2 raw transfer subset without claiming full Ethereum typed transaction compatibility.
- Transactions can include a native paymaster authorization. The user signs the operation and consumes their own nonce, the paymaster signs the exact user-signed transaction, and execution charges gas to the paymaster while preserving replay-deterministic receipts through `fee_payer` and a `paymaster.sponsored` event. RPC exposes `chain_sendUserOperation`, and CLI transfers support `--paymaster-private-key`. This models sponsored gas/account-abstraction UX without claiming full ERC-4337 EntryPoint compatibility.
- Transactions can use native `batch` user operations. A batch consumes one sender nonce, executes at most 128 transfer/call operations atomically on one transaction-owned working state, charges one transaction-level fee, supports paymaster sponsorship, and exposes operation count in RPC/explorer views. CLI `tx batch-transfer` builds a repeated `--to address:amount` batch. This models one-click account-abstraction UX without claiming full ERC-4337 EntryPoint compatibility.
- The default native runtime includes `account.v1`, a single-owner smart contract account. A transaction with `from=<account contract>` and `signer=<owner>` is authorized by the stored owner; nonce, value transfers, and default fee payment belong to the contract account, while the owner EOA is only the authorization key. CLI transfer and batch-transfer commands expose this through `--from`, and RPC/explorer projections include `signer`.
- The default native runtime includes `multisig.v1`, a threshold smart contract account. It stores comma-separated owners and a threshold, verifies distinct owner signatures from transaction `authorizations`, consumes the multisig account nonce, spends the multisig account balance, supports paymaster sponsorship, and exposes authorization count through RPC/explorer projections. CLI transfer and batch-transfer commands add repeated `--auth-private-key` flags for owner signatures.
- EOAs can submit a native `set_code` transaction to set `DelegatedCodeID=account.v1` and store an `owner`. The delegated EOA keeps its original address, balance, and nonce, but future transfers can be signed by the owner through the transaction `signer`. `set_code` can also clear the delegation, and replacing/clearing delegation removes stale owner/session/recovery authority. REST account responses, explorer account pages, and `eth_getCode` expose delegated code state. This is a ChainLab-native EIP-7702-style mechanism, not Ethereum type-4 raw transaction decoding, authorization tuple RLP compatibility, arbitrary uploaded delegation code, or ERC-4337 EntryPoint support.
- `account.v1` accounts and delegated EOAs support ChainLab-native session keys through `account.session_key`. The owner installs or revokes a session key, and the session key can sign value transfers within a stored value limit, optional recipient allowlist, and optional block-height expiry, or call one configured contract address plus method. Recovery advances a session epoch so pre-rotation policies cannot authorize future actions. This is not ERC-4337 `UserOperation`, ERC-7579 modular account compatibility, batch session-key policy, parameter-level call policy, or a programmable policy engine.
- `account.v1` accounts and delegated EOAs support `account.recovery`: up to 16 EOA guardians vote in bounded rounds, threshold agreement freezes a target and starts a block delay, guardian-funded execution rotates owner, and owner/session/delegation bypasses clear stale active authority. Recovery events are account-addressed in the canonical event index. This native protocol does not claim Safe/ERC-7579/ERC-1271/passkey compatibility and does not recover the delegated EOA root key.
- Public genesis files contain an explicit timestamp, validator set, and balances but no secret. `init` and `keygen` create separate non-overwriting validator key files; `chainlab node --key-file` requires the key address to belong to the shared genesis set. Unix mode `0600` is enforced outside Windows, while encrypted/remote signing and Windows DACL enforcement remain production work.
- PoA now enforces deterministic proposer rotation and treats repeated imports of already-known canonical blocks as idempotent peer sync events.
- The local harness validator set is fixed by genesis. `validator.join` and `validator.leave` are rejected at schema/admission until the production CometBFT lifecycle implements certified epoch transitions; stake state never changes the current proposer schedule implicitly.
- `validator.slash` accepts only two different canonical block hashes at the same chain/height with valid low-s signatures from the target validator. The evidence ID canonicalizes the sorted pair, while one persistent offence tombstone per chain/validator/height prevents alternate old vote pairs from slashing newly staked funds. Slashing clears current stake and deliberately does not mutate the fixed validator set. Production evidence aging, unbonding, voting-power updates, and set transitions remain CometBFT/epoch work.
- Governance transaction and query names remain reserved, but `proposal.submit`, `vote`, and `proposal.execute` fail before admission and do not enter the txpool or change state, nonce, or fees, including across restart. Re-enabling them requires finalized voting-power snapshots, unbonding or vote locks, deposits, quorum/timelock rules, protocol-versioned activation, and migration vectors.

Phase 3 production path:

- make CometBFT ABCI++ the authoritative consensus, P2P, evidence, proposal, block-sync, and state-sync lifecycle
- replace JSON snapshots with atomic versioned KV storage and verified pruned/full/archive snapshots
- finish WASM multi-target/release/audit evidence; add protocol upgrades/migrations, inclusion proofs, protected validator signing, monitoring, backups, and incident operations
- execute multi-process fault, partition, Byzantine, race, fuzz, property, load, soak, upgrade, and disaster-recovery validation before staged public networks
