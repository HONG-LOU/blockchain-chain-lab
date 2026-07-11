# ChainLab

ChainLab is being built as a production-grade sovereign blockchain implemented in Go, not as a learning project or toy chain. The repository contains the evolving protocol implementation and a local PoA development network used to replay and verify its deterministic application state transition before the CometBFT-based production network is activated. Local block timestamps and hashes are not cross-run deterministic.

It currently implements:

- account balances, nonces, storage, and deterministic state roots
- secp256k1 signatures and Ethereum-style 20-byte addresses
- transfers plus stake accounting; governance proposal/vote/execute transactions are fail-closed until finalized voting-power snapshots, unbonding locks, deposits, and versioned activation exist
- local proof-of-authority block production with deterministic multi-validator proposer rotation
- a fixed genesis validator set committed into state roots and snapshots; `validator.join` and `validator.leave` are rejected until finalized epoch transitions are implemented through the production consensus lifecycle
- PoA finality-attestation certificates from validator commit signatures, with a monotonic highest-certificate finality lock that rejects longer conflicting branches; without a quorum certificate, `finalized` remains at genesis and the separate `depth_confirmation` result is only a local `safe` heuristic
- finality double-vote evidence detection and persistent vote memory; a valid conflicting quorum certificate triggers a persistent sticky HALT, while `validator.slash` accepts only same-chain, same-height, cryptographically verified conflicting signatures, consumes each chain/validator/height offence once regardless of which conflicting vote pair proves it, and clears the target's current stake without changing the fixed validator set
- transaction, receipt, and state roots
- native smart-contract runtime with a sealed, versioned `chainlab-native-v1` gas schedule and bounded input, storage, event, and output work
- sandboxed WASM contract runtime with built-in `wasm.echo.v1` and chain-state uploaded modules through `wasm.upload`
- versioned Wasmtime WASM execution with deterministic guest fuel, shared host gas, fixed declared memory/table resources, a maximum 64-node direct-call graph, bounded host I/O/events/writes, and atomic rollback
- EIP-1559-style local fee market with block base fee, gas used/limit, base fee burn, priority fee rewards, and legacy `gas_price` compatibility
- included deterministic execution failures with stable receipt codes, business-state rollback, sender-nonce consumption, and actual-gas fee settlement; out-of-gas consumes the full transaction gas limit and a paymaster funds sponsored failures
- native paymaster-sponsored transactions: the user signs the operation and consumes their own nonce, while a paymaster signs an authorization and pays gas
- ChainLab-native batched user operations: one signed transaction can atomically execute up to 128 transfer/call operations with one sender nonce and one fee settlement
- ChainLab-native smart contract accounts through `account.v1`: a contract account holds the balance and nonce while its stored owner signs with the transaction `signer`
- ChainLab-native multisig smart accounts through `multisig.v1`: a contract account enforces an owner threshold with multiple transaction authorizations
- ChainLab-native EIP-7702-style delegated EOAs through `set_code`: an EOA keeps its address, balance, and nonce while delegating authorization to `account.v1`
- ChainLab-native session keys for `account.v1` and delegated EOAs: an owner installs a limited key for capped transfers or one allowed contract method, with optional block-height expiry
- ChainLab-native social recovery for `account.v1` and delegated EOAs: bounded guardian voting rounds, threshold-triggered delay, guardian-funded approval/execution, owner rotation, and session-authority invalidation
- HTTP REST and JSON-RPC endpoints with bounded request concurrency, batch sizes, query ranges, result counts, and response bytes, plus halt-aware readiness and separate liveness checks
- strict persistent snapshot v3 envelopes with generation and checksum, an explicitly timestamped genesis, current state, canonical and known blocks, finality lock, partial votes, equivocation evidence, and pending/queued txpool state; restart replays block history, verifies state roots, and revalidates every persisted transaction before publishing it, while v2 snapshots migrate deterministically
- local fork-choice that stores known branches and reorgs to a longer validated branch
- EVM-shaped JSON-RPC read subset: `web3_clientVersion`, `net_version`, `net_listening`, `eth_chainId`, `eth_accounts`, `eth_coinbase`, `eth_mining`, `eth_hashrate`, `eth_syncing`, `eth_blockNumber`, `eth_getBalance`, `eth_getTransactionCount`, `eth_getCode`, `eth_getStorageAt`, `eth_getTransactionByHash`, `eth_getTransactionReceipt`, `eth_getBlockReceipts`, `eth_getBlockByNumber`, `eth_getBlockByHash`, block transaction-count/index lookups, `eth_feeHistory`, `eth_getLogs`, `eth_call`, `eth_estimateGas`
- minimal latest-state ABI-compatible `eth_call` support for native read methods, including Solidity-style calldata selectors for `get()`, `symbol()`, `owner()`, and `balanceOf(address)`, plus ABI-shaped `uint256`, `address`, and dynamic `string` return data; reads pin an immutable read-only state version so block production and reorg processing can continue concurrently
- EVM-style `safe` and `finalized` block tags for block and log reads
- bounded pending/queued txpool inspection and nonce promotion: 2 MiB per transaction, 128 MiB/4,096 transactions total with incremental byte accounting, 2,048 queued, 64 per sender, nonce gap 64, snapshot-backed restart, deterministic valid-orphan reinsertion after canonical reorg, plus principal-safe 10 percent same-nonce replacement, pending lookup/filter polling, and WebSocket notifications
- consensus-visible 2 MiB transaction and 16 MiB block limits, including a 12 MiB uncertified base-block ceiling that reserves 4 MiB for a quorum certificate, plus 4,096 transaction/receipt/finality-signature count limits
- account storage caps of 4,096 entries, 256 bytes per key, 64 KiB per value, and 8 MiB total per account with O(1) derived byte accounting; an 8 MiB RPC txpool-source ceiling; 32 concurrent ordinary HTTP requests; 1,024 random-ID filters with active five-minute expiry, 64 KiB per log-filter definition, at most 64 pending filters and 65,536 retained pending hashes; and WebSocket limits of 64 connections, 64 subscriptions per connection, 1,024 subscriptions per node, and 64 log subscriptions
- raw transaction submission for ChainLab-native signed JSON and a limited Ethereum EIP-1559 type-2 transfer subset
- devnet faucet that creates a normal proposer-signed transfer into the mempool
- local block explorer pages for head, finality, recent blocks, transactions, accounts, validators, mempool, and indexed recent contract events
- EVM-style contract event logs served from the node event index, with block range, address, and topic filtering
- EVM-style block/log/pending transaction filter polling with `eth_newBlockFilter`, `eth_newPendingTransactionFilter`, `eth_newFilter`, `eth_getFilterLogs`, `eth_getFilterChanges`, and `eth_uninstallFilter`
- local multi-node devnet sync over HTTP peers: transaction relay, block import, produced-block broadcast, and finality vote relay
- CLI commands for keys, genesis, nodes, signed transfers, block production, queries, and demos
- a pinned CometBFT v0.39.3 ABCI++ application with strict genesis/consensus-parameter binding, deterministic proposal replay, candidate-only finalize, evidence-driven `chainlab-v2` validator removal, Pebble-backed incremental history profiles, and verified snapshot restore
- a genesis-committed `chainlab-v2 -> chainlab-v3 -> chainlab-v4 -> chainlab-v5` activation path that updates Comet application versions one height before activation; V3 introduces exact-total transaction/receipt/state Merkle roots, V4 moves state to a mutation-aware sparse Merkle map, and V5 safely compacts only timestamped validator offences after both evidence-retention dimensions expire
- generated four-validator private networks with independent secp256k1 validator keys, P2P identities, homes, ABCI sockets, RPC endpoints, full-mesh persistent peers, and real CometBFT processes
- multi-process evidence for CometBFT rounds/P2P, raw-transaction commitment, progress with one of four validators stopped, targeted round-0 proposer outage with later-round commit, deterministic halt with only two of four validators, symmetric 2+2 P2P partition with side-local transaction gossip, queued-transaction commit after quorum or topology recovery, lagging-node block sync, durable application restart, fresh application replay, destructive-data state sync through two light-client RPC sources, and identical common-height block/app hashes plus current state-root/account state after recovery; exact limits are in [CometBFT Fault Network Evidence](docs/chainlab-comet-fault-network.md)

The ABCI++ lifecycle writes each committed height to one synchronized Pebble batch. A flat live state, mutation-journal-derived per-height delta, periodic checkpoint, commitment/app hash, transactions, receipts, current-height pointer, history boundary, and block index advance atomically. Store clones retain semantic dirty keys until durable commit, and checkpoints fail closed if the journal differs from a complete state diff. V4 ordinary commits update a copy-on-write sparse accumulator only for journaled mutations; a successful durable batch publishes the overlay, while failure leaves the committed tree unchanged. Checkpoints rebuild the complete sparse tree and compare roots. Store V2 also has deterministic V1 migration, archive/full/pruned retention profiles, verified historical reads, compaction, consistent Pebble backups, restart/profile identity checks, and fail-closed root/count/checksum validation. Verified 1 MiB-chunk snapshots are independently bound to genesis and trusted app hash, and a real CometBFT validator has recovered through state sync. Broader filesystem faults, Linux load/soak, external rollback protection, runtime-authorized upgrades, remote signing, and the remaining Byzantine network cases are still open.

The current implementation is not yet approved for public mainnet launch. [Mainstream Chain Capability And Production Gates](docs/mainstream-chain-capability-and-production-gates-2026-07-10.md) is the authoritative gate set; [the production technology roadmap](docs/current-blockchain-tech-roadmap.md) orders the implementation work. Mainnet requires deterministic execution, CometBFT ABCI++ consensus/networking, transactional storage, protocol upgrades, security testing, economics, and operational evidence.

## Verify

The minimum supported toolchain is Go 1.25.12. The patch floor is security-sensitive because earlier Go 1.25 standard libraries contain vulnerabilities reachable from ChainLab's HTTP, TLS, URL, and template paths.

```powershell
go test ./...
go run ./cmd/chainlab demo
```

## CometBFT Private Network

Generate four independent validator homes without overwriting an existing directory:

```powershell
go run ./cmd/chainlab-comet init --out data/comet-private --chain-id chainlab-private
```

Each `node0` through `node3` home contains its own private-validator key and last-sign state, P2P key, Comet genesis, canonical ChainLab application genesis, and strict `chainlab-node.json`. Start one application and one Comet process per home, using the addresses in `network.json`:

```powershell
go run ./cmd/chainlab-abci --genesis data/comet-private/node0/config/chainlab-genesis.json --data-dir data/comet-private/node0/data/chainlab-app --listen tcp://127.0.0.1:26658
go run ./cmd/chainlab-comet node --home data/comet-private/node0
```

Repeat with the generated node-specific addresses for `node1` through `node3`. Generated keys are for isolated private-network testing, not production custody. The runtime locks Comet to the `flood` mempool because v0.39.3's socket server does not dispatch `InsertTx`/`ReapTxs`; the application-side mempool remains covered through direct lifecycle tests but is not claimed as socket-transport evidence. ChainLab's app hash commits block identity and therefore changes on empty blocks, so the generated Comet config deliberately creates blocks continuously with a bounded commit interval. Each application data directory is single-writer Pebble storage. Production validators still require remote signing, broader fault/load evidence, and the remaining security gates.

### Application Storage Profiles

The default is `full`, retaining at least 10,000 recent heights with checkpoints every 100 heights. Profile identity is stored in the database and must match on restart. Select another profile when first creating the application database:

```powershell
# Keep every available height.
go run ./cmd/chainlab-abci --genesis genesis.json --data-dir app-data --storage-mode archive

# Keep only the latest committed height.
go run ./cmd/chainlab-abci --genesis genesis.json --data-dir app-data --storage-mode pruned

# Set an explicit bounded history window and checkpoint interval.
go run ./cmd/chainlab-abci --genesis genesis.json --data-dir app-data --storage-mode full --retain-heights 50000 --checkpoint-interval 500
```

Create a consistent offline-openable backup in a new directory outside the data directory, or compact before serving:

```powershell
go run ./cmd/chainlab-abci --genesis genesis.json --data-dir app-data --backup-to backups/app-2026-07-11
go run ./cmd/chainlab-abci --genesis genesis.json --data-dir app-data --compact
```

See [Application Store V2](docs/chainlab-application-storage-v2.md) for exact retention, migration, historical-query, state-sync, backup, and remaining-performance guarantees.

### Scheduled V3/V4/V5 Protocols

Generate a V2 private network committed to activate V3 at height 100, V4 sparse state at height 200, and V5 offence retention at height 300:

```powershell
go run ./cmd/chainlab-comet init --out data/comet-v5 --chain-id chainlab-v5 --application-protocol chainlab-v2 --upgrade-v3-height 100 --upgrade-v4-height 200 --upgrade-v5-height 300
```

V3 serves inclusion proofs through `/proof/transaction`, `/proof/receipt`, and `/proof/account`. V4 keeps the transaction/receipt envelope and serves sparse membership or non-membership from `/proof/account`; `/proof/state` accepts `kind:base64url-key` for account storage, code, stake, proposals, parameters, validators, identities, and offences. Decode the ABCI response value into `proof.json`, obtain the corresponding trusted root from an independently verified application commitment/header, and verify it without application/state code:

```powershell
go run ./cmd/chainlab-proof --proof proof.json --root 0x<trusted-tx-root> --height 100 --kind transaction --key 0
```

V5 retains V4 proof formats and sparse-root semantics. The verifier automatically recognizes V3 inclusion and V4/V5 sparse envelopes. The root, height, kind, and key arguments are the requested trusted item; do not copy them blindly from the proof response. See [V3 Scheduled Upgrade And Inclusion Proofs](docs/chainlab-v3-proofs-and-upgrades.md), [V4 Sparse State](docs/chainlab-v4-sparse-state.md), and [V5 Validator Offence Retention](docs/chainlab-v5-offence-retention.md).

## CLI

Generate an additional validator key file without printing the secret:

```powershell
go run ./cmd/chainlab keygen --out config/validator-2-key.json
```

Create a public local genesis and a separate validator key file:

```powershell
go run ./cmd/chainlab init --out config/genesis.json --key-out config/validator-1-key.json
```

The genesis contains only public consensus data, including an explicit `genesis_time_unix`, validator set, and balances. It never contains a private key and is the artifact every node shares. Key files are created with exclusive-create semantics and Unix mode `0600`; never share one validator key between nodes. Windows remains a development target because Unix mode bits do not prove a restricted Windows DACL. For a multi-validator devnet, generate additional key files, add their public addresses to `validators` before first start, and keep each key on only its owning validator.

Start a local node:

```powershell
go run ./cmd/chainlab node --genesis config/genesis.json --key-file config/validator-1-key.json --listen :8547
```

Open the local explorer at `http://127.0.0.1:8547/explorer`. Block, transaction, account, and recent event pages are linked from the overview.

Start a persistent local node:

```powershell
go run ./cmd/chainlab node --genesis config/genesis.json --key-file config/validator-1-key.json --listen :8547 --data-dir data/localnet
```

An initialized data directory contains a checksummed `manifest.json` and snapshot v3 `chain.json`. V3 atomically binds pending/queued transactions to the same generation as canonical state, revalidates their signatures, nonces, fees, byte/count/sender limits, and execution on restart, and deterministically migrates v2 snapshots. If either file later disappears, the node refuses to silently construct a new genesis over the directory. Snapshot reads and writes share a 512 MiB ceiling; loading rejects unknown or non-canonical fields, verifies generation/checksum, bounds WASM code and account storage before decoding large values, and replays canonical and known branches from genesis before publishing state. If rename succeeds but directory synchronization fails, the process treats the result as commit-uncertain and enters sticky HALT instead of continuing from rolled-back memory. This remains development JSON persistence and rewrites the full snapshot on persistent txpool changes; a production WAL-backed txpool and transactional database remain separate roadmap gates.

Start a follower node:

```powershell
go run ./cmd/chainlab node --genesis config/genesis.json --key-file config/validator-2-key.json --listen :8548 --data-dir data/validator-2
```

Start a producing node that broadcasts to the follower:

```powershell
go run ./cmd/chainlab node --genesis config/genesis.json --key-file config/validator-1-key.json --listen :8547 --data-dir data/validator-1 --peer http://127.0.0.1:8548
```

Start a second validator from the same genesis:

```powershell
go run ./cmd/chainlab node --genesis config/genesis.json --key-file config/validator-2-key.json --listen :8548 --data-dir data/validator-2 --peer http://127.0.0.1:8547
```

Run the built-in demo:

```powershell
go run ./cmd/chainlab demo
```

Submit a signed transfer through a running node:

```powershell
go run ./cmd/chainlab tx transfer --rpc http://127.0.0.1:8547 --private-key <hex-private-key> --to <address> --value 100
```

Query fee market recommendations or submit an EIP-1559-style capped transfer:

```powershell
go run ./cmd/chainlab query fees --rpc http://127.0.0.1:8547
go run ./cmd/chainlab tx transfer --rpc http://127.0.0.1:8547 --private-key <hex-private-key> --to <address> --value 100 --max-fee-per-gas 5 --max-priority-fee-per-gas 2
```

The block header records `base_fee_per_gas`, `gas_limit`, and `gas_used`. Receipts record `effective_gas_price`, burned base fee, and paid priority fee. ChainLab accepts its native signed transaction JSON raw envelope and a limited Ethereum EIP-1559 type-2 raw transfer subset. Type-2 support covers value transfers with empty calldata and empty access lists; general EVM calldata, contract creation, legacy RLP, and type-1 access-list transactions remain later work.

Submit a sponsored transfer where a paymaster pays gas:

```powershell
go run ./cmd/chainlab tx transfer --rpc http://127.0.0.1:8547 --private-key <user-private-key> --to <address> --value 100 --gas-price 2 --paymaster-private-key <paymaster-private-key>
```

The user's signature covers the operation, the paymaster signature covers the user-signed transaction, and the receipt records `fee_payer`. This models the account-abstraction/paymaster workflow in a ChainLab-native way; it is not a full ERC-4337 EntryPoint implementation. ChainLab's EIP-7702-style delegated EOA experiment is exposed separately through `tx set-code`.

Submit a batched transfer with one user signature and one nonce:

```powershell
go run ./cmd/chainlab tx batch-transfer --rpc http://127.0.0.1:8547 --private-key <hex-private-key> --to <address-1>:10 --to <address-2>:20
```

Batch transactions use type `batch` and carry a `batch` array of operations. The current format supports `transfer` and contract `call` operations. Execution is atomic: if an operation fails during included transaction execution, every business-state change from the batch rolls back, while the transaction produces a failed receipt, consumes the sender nonce, and pays for gas already consumed. A batch can also include `--paymaster-private-key`, in which case the sponsor pays the single transaction-level fee, including for an included failure. Signature, nonce, intrinsic-gas, and fee-capacity admission failures are rejected before inclusion and do not consume nonce or fees.

Deploy a smart contract account and send from it with the owner key:

```powershell
go run ./cmd/chainlab tx deploy --rpc http://127.0.0.1:8547 --private-key <owner-private-key> --code-id account.v1 --arg owner=<owner-address>
go run ./cmd/chainlab tx transfer --rpc http://127.0.0.1:8547 --from <account-contract-address> --private-key <owner-private-key> --to <recipient> --value 100
```

For a smart account transaction, `from` is the contract account that owns the balance, nonce, and fee liability. `signer` is the EOA owner that authorizes the operation. The owner key can also build sponsored smart-account transfers or batches with `--paymaster-private-key`, so the paymaster pays gas while the contract account sends value. This is a native single-owner account model, separate from delegated EOAs, threshold accounts, full ERC-4337 EntryPoint compatibility, and policy-engine compatibility.

Deploy a 2-of-2 multisig smart account and send from it:

```powershell
go run ./cmd/chainlab tx deploy --rpc http://127.0.0.1:8547 --private-key <owner-a-private-key> --code-id multisig.v1 --arg owners=<owner-a-address>,<owner-b-address> --arg threshold=2
go run ./cmd/chainlab tx transfer --rpc http://127.0.0.1:8547 --from <multisig-contract-address> --private-key <owner-a-private-key> --auth-private-key <owner-b-private-key> --to <recipient> --value 100
```

Multisig transactions use `authorizations`, one per owner signature. The transaction still consumes the multisig account nonce, spends the multisig account balance, and can use `--paymaster-private-key` for sponsored gas. This is a native threshold-account model, not Gnosis Safe or full ERC-4337 compatibility. Guardian recovery and session keys are separate `account.v1` / delegated EOA authorization paths.

Delegate an EOA to `account.v1` and then transfer from that same EOA address with the owner key:

```powershell
go run ./cmd/chainlab tx set-code --rpc http://127.0.0.1:8547 --private-key <eoa-private-key> --code-id account.v1 --owner <owner-address>
go run ./cmd/chainlab chain produce --rpc http://127.0.0.1:8547
go run ./cmd/chainlab tx transfer --rpc http://127.0.0.1:8547 --from <delegated-eoa-address> --private-key <owner-private-key> --to <recipient> --value 100
```

The `set_code` transaction must be signed directly by the EOA being delegated. After inclusion, the EOA keeps its address, balance, and nonce, while `DelegatedCodeID=account.v1` makes the stored `owner` authorize future transfers through the transaction `signer`. `tx set-code --clear` removes the delegation. This is a ChainLab-native EIP-7702-style experiment, not Ethereum type-4 raw transaction decoding, authorization tuple RLP compatibility, arbitrary uploaded delegation code, or ERC-4337 EntryPoint support.

Install a transfer-scoped session key on an `account.v1` account or delegated EOA, then spend with that temporary key:

```powershell
go run ./cmd/chainlab tx session-key --rpc http://127.0.0.1:8547 --from <account-or-delegated-eoa> --private-key <owner-private-key> --key <session-key-address> --limit 100 --expires 50 --to <recipient>
go run ./cmd/chainlab chain produce --rpc http://127.0.0.1:8547
go run ./cmd/chainlab tx transfer --rpc http://127.0.0.1:8547 --from <account-or-delegated-eoa> --private-key <session-private-key> --to <recipient> --value 25
go run ./cmd/chainlab tx session-key --rpc http://127.0.0.1:8547 --from <account-or-delegated-eoa> --private-key <owner-private-key> --key <session-key-address> --revoke
```

Install a contract-call session key that can only call one contract method:

```powershell
go run ./cmd/chainlab tx session-key --rpc http://127.0.0.1:8547 --from <account-or-delegated-eoa> --private-key <owner-private-key> --key <session-key-address> --call-to <contract-address> --call-method increment --expires 50
go run ./cmd/chainlab chain produce --rpc http://127.0.0.1:8547
go run ./cmd/chainlab tx call --rpc http://127.0.0.1:8547 --from <account-or-delegated-eoa> --private-key <session-private-key> --to <contract-address> --method increment --arg amount=1
```

Session key policy is stored in account storage under `session:<key>:...` keys and is committed into state roots and snapshots. Transfer policies track transferred value against `limit`, optionally restrict the recipient with `--to`, and optionally expire after a block height. Contract-call policies restrict the key to one `--call-to` contract address and one `--call-method`; the session key cannot call other contracts or methods. Owner recovery advances `session:epoch`, so every session policy installed before the rotation becomes unauthorized. ChainLab does not yet implement ERC-4337 `UserOperation`, ERC-7579 modules, batch session-key policies, parameter-level call policies, or a general policy engine.

Configure two recovery guardians, approve a replacement owner, wait for the threshold delay, and execute:

```powershell
go run ./cmd/chainlab tx recovery --rpc http://127.0.0.1:8547 --from <account-or-delegated-eoa> --private-key <owner-key> --action configure --guardian <guardian-a> --guardian <guardian-b> --threshold 2 --delay 3
go run ./cmd/chainlab tx recovery --rpc http://127.0.0.1:8547 --from <account-or-delegated-eoa> --private-key <guardian-a-key> --action approve --new-owner <new-owner>
go run ./cmd/chainlab tx recovery --rpc http://127.0.0.1:8547 --from <account-or-delegated-eoa> --private-key <guardian-b-key> --action approve --new-owner <new-owner>
go run ./cmd/chainlab tx recovery --rpc http://127.0.0.1:8547 --from <account-or-delegated-eoa> --private-key <guardian-a-key> --action execute --new-owner <new-owner>
```

Before threshold, each guardian votes for one target. A vote can change only when the change immediately reaches threshold, preventing unilateral vote churn. The voting round expires after 256 blocks. Threshold freezes one pending target, starts `delay`, and opens an inclusive 256-block execution window. `execute --new-owner` is mandatory so the signature binds the final owner.

All actions consume the recovered account nonce. The recovered account pays `configure`, `cancel`, and `clear`; the signing guardian pays `approve` and `execute`, while guardian nonce remains unchanged. Guardians therefore need native gas balance. Recovery events are indexed at the recovered account address and can be monitored with `eth_getLogs`; because WebSocket logs do not yet emit `removed: true` on reorg, automation must re-query canonical state and honor the network safe/finalized boundary.

The current owner can cancel an active round without removing guardian configuration, or clear it completely:

```powershell
go run ./cmd/chainlab tx recovery --rpc http://127.0.0.1:8547 --from <account-or-delegated-eoa> --private-key <owner-key> --action cancel
go run ./cmd/chainlab tx recovery --rpc http://127.0.0.1:8547 --from <account-or-delegated-eoa> --private-key <owner-key> --action clear
```

This is a ChainLab-native recovery protocol for EOA guardians. It does not claim ERC-4337, ERC-7579, Safe module, ERC-1271 contract guardian, passkey, email, or EIP-7702 type-4 compatibility. For delegated EOAs it rotates the delegated `account.v1` owner; the root EOA key can still replace or clear delegation.

Build a signed raw ChainLab transaction without broadcasting, then submit it later:

```powershell
go run ./cmd/chainlab tx transfer --rpc http://127.0.0.1:8547 --private-key <hex-private-key> --to <address> --value 100 --raw-only
go run ./cmd/chainlab tx raw-submit --rpc http://127.0.0.1:8547 --raw <0x-raw-transaction>
```

The native raw format is a `0x`-prefixed hex encoding of ChainLab's signed transaction JSON. `tx raw-submit`, `POST /tx/raw`, and `eth_sendRawTransaction` also accept signed Ethereum EIP-1559 type-2 raw value transfers (`0x02 || rlp(payload)`) when calldata and access list are empty. Legacy RLP, type-1 access-list transactions, contract creation, and general calldata remain later work.

Request devnet funds from the local proposer account:

```powershell
go run ./cmd/chainlab faucet request --rpc http://127.0.0.1:8547 --to <address> --amount 100
go run ./cmd/chainlab chain produce --rpc http://127.0.0.1:8547
```

The faucet is a local-devnet helper. It submits a normal signed transfer to the mempool, so balances only change after block production.

Deploy and write-call a native contract:

```powershell
go run ./cmd/chainlab tx deploy --rpc http://127.0.0.1:8547 --private-key <hex-private-key> --code-id counter.v1 --arg initial=0
go run ./cmd/chainlab tx call --rpc http://127.0.0.1:8547 --private-key <hex-private-key> --to <contract-address> --method increment --arg amount=1
```

Upload, deploy, and write-call a sandboxed WASM module:

```powershell
go run ./cmd/chainlab tx wasm-upload --rpc http://127.0.0.1:8547 --private-key <hex-private-key> --example echo
go run ./cmd/chainlab chain produce --rpc http://127.0.0.1:8547
go run ./cmd/chainlab query tx --rpc http://127.0.0.1:8547 --hash <upload-tx-hash>
go run ./cmd/chainlab tx deploy --rpc http://127.0.0.1:8547 --private-key <hex-private-key> --code-id <uploaded-code-id> --arg message=hello
go run ./cmd/chainlab tx call --rpc http://127.0.0.1:8547 --private-key <hex-private-key> --to <contract-address> --method set --arg message=world
go run ./cmd/chainlab query call --rpc http://127.0.0.1:8547 --to <contract-address> --method get
```

The upload receipt contains `receipt.code_id`; use that value in the deploy command. Use `--wasm-file <path>` or `--bytecode <0x...>` to upload your own module instead of the built-in `--example echo` module. The built-in `wasm.echo.v1` code id is still available for quick local tests without an upload transaction.

Uploaded WASM runs inside the version-pinned Wasmtime-Go v46.0.1 runtime and only receives ChainLab host functions for args, contract storage, return data, and events. Metering version `chainlab-wasm-v1` requires fixed-size memory and tables, rejects start/WASI/unknown ABI, and requires `deploy`, `call`, and `read` exports. Admission parses validated function bodies, rejects recursion, `call_indirect`, tail calls, function-reference calls, and unsupported opcodes, and limits the longest defined-plus-import direct-call path to 64 function nodes. The native Wasmtime stack ceiling remains defense in depth rather than the consensus call-depth rule. This is a ChainLab-specific ABI, not CosmWasm compatibility.

WASM upload gas scales with bytecode size. Every invocation charges the admitted original module bytes plus declared memory pages and table elements before instantiation, then uses Wasmtime fuel for guest execution and the same remaining budget for host calls and copied bytes. Infinite loops terminate by deterministic out-of-fuel, not a wall-clock deadline. Direct runtime calls are atomic; transaction execution owns an outer rollback store and avoids a second whole-state copy per contract call.

The deterministic call-depth rule, included-failure settlement, and `chainlab-native-v1` schedule close local implementation sub-gates; they are not a mainnet-readiness claim. Production activation still requires a supported Linux/amd64 artifact and golden block replay vectors, hard JIT-memory lifecycle bounds, protocol-wide fatal runtime-fault halt and deterministic recovery/upgrade behavior through ABCI++, reproducible builds/checksums/SBOM, broader property/fuzz/load evidence, scheduled protocol activation, and external review.

Stake and submit cryptographic validator-equivocation evidence:

```powershell
go run ./cmd/chainlab tx stake --rpc http://127.0.0.1:8547 --private-key <hex-private-key> --value 500
go run ./cmd/chainlab tx validator-slash --rpc http://127.0.0.1:8547 --private-key <reporter-private-key> --target <validator-address> --height <height> --first-block-hash <hash-a> --first-signature <signature-a> --second-block-hash <hash-b> --second-signature <signature-b>
```

The genesis file fixes validator membership for the current protocol version. Staking and unstaking change stake accounting but do not add or remove consensus validators. The `validator-join` and `validator-leave` commands intentionally return a disabled error until certified epoch transitions exist. A slash reporter and target must both be genesis validators, and the two canonical low-s target signatures must verify for distinct block hashes at the same positive height and chain ID. The canonical evidence ID includes the sorted vote pair, while the persistent offence tombstone binds only chain, validator, and height, so a third old vote cannot slash newly staked funds again. Inclusion clears all current stake but deliberately leaves the fixed validator set unchanged.

Governance transaction names remain reserved in the development schema and CLI, but `proposal.submit`, `vote`, and `proposal.execute` currently return a disabled error before admission. The previous current-stake voting demo was unsafe because stake could be unstaked, transferred, and counted again from another address. Re-enabling governance requires a versioned protocol with finalized voting-power snapshots, unbonding or vote locks, proposal deposits, quorum/timelock rules, and migration vectors.

Produce a block:

```powershell
go run ./cmd/chainlab chain produce --rpc http://127.0.0.1:8547
```

Submit a validator finality vote for a produced block:

```powershell
go run ./cmd/chainlab chain finality-vote --rpc http://127.0.0.1:8547 --private-key <validator-private-key> --height <block-height>
```

When more than two thirds of the fixed genesis validators sign the same block hash, the local harness attaches a `finality_certificate` to that block and `query finality` reports `safe_source` / `finalized_source` as `bft_certificate`. The highest certified block becomes a monotonic finality lock: a competing branch, even if longer, cannot replace it. A later compatible certificate can advance the lock, and additional valid signatures for the same block are merged and persisted.

Without a quorum certificate, `safe_source=depth_confirmation` reports only a local depth-based confirmation and must not be treated as Byzantine finality. `finalized` remains at genesis with `finalized_source=genesis_without_certificate` until a certificate exists.

Nodes started with `--peer` relay submitted finality votes to their configured HTTP peers through `/peer/finality-vote`, so a devnet peer that already imported the block can independently assemble the same certificate.

If a validator submits finality votes for two different block hashes at the same height, the second vote is rejected and signed `FinalityEquivocationEvidence` is persisted. Partial votes are also persisted so restarting does not erase the node's double-sign memory. Evidence identity canonicalizes the sorted hash pair; the one-time offence identity binds chain, validator, and height. If a crash occurs after evidence persistence but before inclusion, restart reconstructs the automatic slash transaction when it is still applicable. If the node encounters two independently valid conflicting quorum certificates, it writes a checksummed `HALT.json` marker and remains halted across restarts rather than choosing either history.

Query chain state:

```powershell
go run ./cmd/chainlab query head --rpc http://127.0.0.1:8547
go run ./cmd/chainlab query finality --rpc http://127.0.0.1:8547
go run ./cmd/chainlab query finality-evidence --rpc http://127.0.0.1:8547
go run ./cmd/chainlab query fees --rpc http://127.0.0.1:8547
go run ./cmd/chainlab query mempool --rpc http://127.0.0.1:8547
go run ./cmd/chainlab query account --rpc http://127.0.0.1:8547 --address <address>
go run ./cmd/chainlab query tx --rpc http://127.0.0.1:8547 --hash <tx-hash>
go run ./cmd/chainlab query logs --rpc http://127.0.0.1:8547 --from-block 0x1 --to-block latest --address <contract-address> --topic <topic0>
go run ./cmd/chainlab query validators --rpc http://127.0.0.1:8547
go run ./cmd/chainlab query proposal --rpc http://127.0.0.1:8547 --id <proposal-id>
go run ./cmd/chainlab query param --rpc http://127.0.0.1:8547 --key <param-key>
go run ./cmd/chainlab query call --rpc http://127.0.0.1:8547 --to <contract-address> --method <read-method> --arg address=<address>
go run ./cmd/chainlab query estimate-gas --rpc http://127.0.0.1:8547 --type call --to <contract-address>
```

`estimate-gas` / `eth_estimateGas` currently returns static/base schedules plus byte-scaled WASM upload cost. It does not simulate dynamic WASM deploy, call, or batch-call execution, so the 50,000-gas call estimate can be below actual `chainlab-wasm-v1` consumption.

## HTTP API

All ordinary HTTP routes share 32 execution slots; `/health/live` and valid WebSocket upgrades use independent paths. Every POST body is capped at 16 MiB before JSON decoding, and `/rpc` has a tighter 5 MiB request limit, 32 inner execution slots, at most 100 calls per batch, and a 16 MiB encoded batch-response limit enforced while each child response is recorded. Txpool content queries reject a source above 8 MiB before cloning it. `eth_feeHistory` accepts at most 1,024 blocks. Log queries accept at most a 10,000-block range, 1,000 results, 8 MiB of encoded results, 256 canonical addresses, four topic positions, and 256 canonical alternatives per topic position; a failed bounded filter poll does not advance its cursor. Installed filters use unpredictable IDs, are globally capped at 1,024, actively expire after five idle minutes, and limit each log definition to 64 KiB. Pending filters are capped at 64 and collectively retain at most 65,536 hashes. WebSockets allow 64 connections, 64 subscriptions per connection, 1,024 per node, and 64 log subscriptions; idle reads expire after 60 seconds, Ping runs every 25 seconds, writes time out after 10 seconds, delivery queues are bounded, and log matching stops after 100,000 checks per block. The CLI HTTP server also sets 5-second header, 15-second read, 30-second write, and 60-second idle timeouts.

- `GET /health` and `GET /health/ready` are readiness checks; both return HTTP 503 while the node is persistently halted
- `GET /health/live` is a process-liveness check and remains HTTP 200 while halted
- `GET /explorer`
- `GET /explorer/events`
- `GET /explorer/block/{height}`
- `GET /explorer/tx/{hash}`
- `GET /explorer/account/{address}`
- `GET /chain/head`
- `GET /chain/finality`
- `GET /chain/finality/evidence`
- `GET /chain/block/{height}`
- `GET /account/{address}`
- `GET /validators`
- `GET /proposal/{id}`
- `GET /param/{key}`
- `GET /tx/{hash}`
- `GET /txpool`
- `POST /tx` for signed transactions, including `transfer`, `batch`, `set_code`, `account.session_key`, `account.recovery`, `deploy`, `call`, `wasm.upload`, staking, and evidence-backed validator slashing. Governance proposal/vote/execute and validator join/leave transactions fail admission. Future-nonce transactions within a gap of 64 are accepted into a bounded node-local queued pool and promoted when earlier nonces arrive. If a pending or queued transaction already has the same sender and nonce, ChainLab accepts a replacement only when the authorization principal matches, the new legacy gas price or both EIP-1559 fee caps are bumped by at least 10 percent, and the replacement remains within the pool byte limit.
- `POST /tx/raw`
- `POST /faucet`
- `POST /chain/produce`
- `POST /peer/tx`
- `POST /peer/block`
- `POST /peer/finality-vote`
- `POST /rpc` accepts either one JSON-RPC-style request object or a batch array within the limits above. Batch responses preserve request order and isolate per-request errors.
- `GET /rpc/ws` supports WebSocket JSON-RPC `eth_subscribe` for `newHeads`, filtered `logs`, and `newPendingTransactions`, plus `eth_unsubscribe`. New block headers and matching EVM-style logs are pushed after local block production and canonical peer block import; pending transaction hashes are pushed after local txpool acceptance. This is a node-local development subscription path.
- `POST /rpc` with methods `chain_head`, `chain_finality`, `chain_finalityEvidence`, `chain_sendFinalityVote`, `chain_feeMarket`, `chain_getAccount`, `chain_validators`, `chain_proposal`, `chain_param`, `chain_sendTx`, `chain_sendUserOperation`, and `chain_faucet`
- `POST /rpc` with EVM-style methods `web3_clientVersion`, `net_version`, `net_listening`, `eth_chainId`, `eth_accounts`, `eth_coinbase`, `eth_mining`, `eth_hashrate`, `eth_syncing`, `eth_blockNumber`, `eth_gasPrice`, `eth_maxPriorityFeePerGas`, `eth_feeHistory`, `eth_getBalance`, `eth_getTransactionCount`, `eth_getCode`, `eth_getStorageAt`, `eth_getTransactionByHash`, `eth_getTransactionReceipt`, `debug_traceTransaction`, `eth_getBlockReceipts`, `eth_getBlockByNumber`, `eth_getBlockByHash`, `eth_getBlockTransactionCountByHash`, `eth_getBlockTransactionCountByNumber`, `eth_getTransactionByBlockHashAndIndex`, `eth_getTransactionByBlockNumberAndIndex`, `eth_getLogs`, `eth_newBlockFilter`, `eth_newPendingTransactionFilter`, `eth_newFilter`, `eth_getFilterLogs`, `eth_getFilterChanges`, `eth_uninstallFilter`, `eth_call`, `eth_estimateGas`, and `eth_sendRawTransaction`. Block and log reads support `earliest`, `latest`, `safe`, `finalized`, and hex quantities where applicable; `eth_call` deliberately supports only `latest`. `eth_syncing` currently returns `false` because ChainLab's HTTP devnet import path is not a staged sync pipeline. `eth_accounts` and `eth_coinbase` expose only the node's local proposer address; ChainLab does not yet include a multi-account wallet manager. `eth_mining` reflects whether that local proposer is currently a genesis validator, while `eth_hashrate` returns `0x0` because ChainLab uses PoA, not proof of work. `eth_getTransactionCount` also supports `pending` for mempool-aware nonce calculation, counting executable pending transactions but not nonce-gap queued transactions. `eth_getTransactionByHash` returns committed transactions first and falls back to txpool pending or queued transactions with null block fields; if a pending or queued transaction is replaced by a higher-fee same-sender/same-nonce transaction, only the replacement remains visible through txpool reads. `eth_getTransactionReceipt` remains `null` until block inclusion. `debug_traceTransaction` returns a receipt-backed ChainLab trace summary plus empty opcode `structLogs`; it is not a full EVM opcode tracer or replay tracer. `eth_getBlockReceipts` accepts a block number/tag or block hash and returns all receipts in that block using the same EVM-style receipt projection. `eth_feeHistory` returns canonical block base fees, gas used ratios, the next base fee, and optional gas-weighted priority-fee rewards from receipts. `eth_newBlockFilter` tracks canonical block hashes produced after filter creation; `eth_newPendingTransactionFilter` tracks pending txpool transaction hashes first seen after filter creation, including accepted pending replacements and queued transactions after they are promoted; log filters continue to track EVM-style log projections. `eth_getCode` returns `0x` for accounts without code and a deterministic hex projection of ChainLab `CodeID` for contract accounts or `DelegatedCodeID` for delegated EOAs. `eth_getStorageAt` reads the current canonical ChainLab storage by plain key or hex-encoded key and returns one 32-byte word; it is not a full EVM 256-bit storage-slot or archive-state implementation. `eth_getBlockByHash` and block+index transaction reads project known ChainLab blocks into the same EVM-style shape as number/hash transaction reads. `eth_call` accepts ChainLab-native `payload` read calls or a limited Solidity-style `data` / `input` selector for `get()`, `symbol()`, `owner()`, and `balanceOf(address)`, pins the current immutable read-only state version in O(1), releases the node lock before execution, and returns ABI-shaped data. ChainLab does not yet expose historical contract-state execution, a full contract ABI registry, or a general calldata decoder.
- `POST /rpc` with txpool-style methods `txpool_status` and `txpool_content`

## Roadmap

The production sequence is:

- production activation and Linux/amd64 evidence for the implemented deterministic WASM, included-failure, and native-contract metering rules
- CometBFT evidence slashing, validator updates/epochs, dynamic message faults/asymmetric partitions, Comet binary rolling upgrades, staged operator drills, and broader multi-process fault tests on the implemented block/state-sync path
- isolated historical proof reads, Linux retention/migration/compaction/load evidence, restore drills, and rollback protection
- runtime-authorized upgrades, trusted light-client integration, protected validator signing, metrics/alerts, fuzz/property/race/fault/load/soak validation
- economics, governance security, wallet/SDK/indexer/token/oracle/interoperability ecosystem and staged public testnets

See [Mainstream Chain Capability And Production Gates](docs/mainstream-chain-capability-and-production-gates-2026-07-10.md) for the authoritative gates and upstream references. [ChainLab Production Technology Roadmap](docs/current-blockchain-tech-roadmap.md) is the navigational implementation order.
