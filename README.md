# ChainLab

ChainLab is a local-first blockchain implementation for learning and prototyping an appchain.

It currently implements:

- account balances, nonces, storage, and deterministic state roots
- secp256k1 signatures and Ethereum-style 20-byte addresses
- transfers, staking, unstaking, and a governance proposal lifecycle for parameter changes
- local proof-of-authority block production with deterministic multi-validator proposer rotation
- dynamic validator joins, leaves, and slashing through `validator.join` / `validator.leave` / `validator.slash` transactions, with validator set committed into state roots and snapshots
- deterministic local `safe` and `finalized` chain checkpoints using conservative block-depth rules
- transaction, receipt, and state roots
- native smart-contract runtime with `counter.v1` and `token.v1`
- sandboxed WASM contract runtime with built-in `wasm.echo.v1` and chain-state uploaded modules through `wasm.upload`
- deterministic WASM resource metering for uploaded bytecode size and ChainLab host ABI storage/event/arg/return usage
- EIP-1559-style local fee market with block base fee, gas used/limit, base fee burn, priority fee rewards, and legacy `gas_price` compatibility
- native paymaster-sponsored transactions: the user signs the operation and consumes their own nonce, while a paymaster signs an authorization and pays gas
- ChainLab-native batched user operations: one signed transaction can atomically execute multiple transfer/call operations with one sender nonce and one fee settlement
- HTTP REST endpoints and a small JSON-RPC-style endpoint
- persistent node snapshots with committed blocks, state, and transaction index
- local fork-choice that stores known branches and reorgs to a longer validated branch
- EVM-compatible JSON-RPC read subset: `eth_chainId`, `eth_blockNumber`, `eth_getBalance`, `eth_getTransactionCount`, `eth_getTransactionByHash`, `eth_getTransactionReceipt`, `eth_getBlockByNumber`, `eth_getLogs`, `eth_call`, `eth_estimateGas`
- EVM-style `safe` and `finalized` block tags for block and log reads
- pending nonce calculation and txpool inspection for uncommitted transactions
- ChainLab-native raw transaction envelope for offline signing and later broadcast
- devnet faucet that creates a normal proposer-signed transfer into the mempool
- local block explorer pages for head, finality, recent blocks, transactions, accounts, validators, and mempool
- EVM-style contract event logs projected from native receipts, with block range, address, and topic filtering
- local multi-node devnet sync over HTTP peers: transaction relay, block import, and produced-block broadcast
- CLI commands for keys, genesis, nodes, signed transfers, block production, queries, and demos

This is not a production mainnet. It is a verified development chain designed so the consensus, storage, runtime, and RPC layers can be replaced or expanded.

## Verify

```powershell
go test ./...
go run ./cmd/chainlab demo
```

## CLI

Generate a key:

```powershell
go run ./cmd/chainlab keygen
```

Create a local genesis file:

```powershell
go run ./cmd/chainlab init --out config/genesis.json
```

The generated genesis includes a `validators` array. For a multi-validator devnet, generate additional keys with `keygen`, add their addresses to `validators`, and start each validator with its own `--private-key`.

Start a local node:

```powershell
go run ./cmd/chainlab node --genesis config/genesis.json --listen :8547
```

Open the local explorer at `http://127.0.0.1:8547/explorer`. Block, transaction, and account detail pages are linked from the overview.

Start a persistent local node:

```powershell
go run ./cmd/chainlab node --genesis config/genesis.json --listen :8547 --data-dir data/localnet
```

Start a follower node:

```powershell
go run ./cmd/chainlab node --genesis config/genesis.json --listen :8548 --data-dir data/follower
```

Start a producing node that broadcasts to the follower:

```powershell
go run ./cmd/chainlab node --genesis config/genesis.json --listen :8547 --data-dir data/producer --peer http://127.0.0.1:8548
```

Start a second validator from the same genesis:

```powershell
go run ./cmd/chainlab node --genesis config/genesis.json --private-key <validator-2-private-key> --listen :8548 --data-dir data/validator-2 --peer http://127.0.0.1:8547
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

The block header records `base_fee_per_gas`, `gas_limit`, and `gas_used`. Receipts record `effective_gas_price`, burned base fee, and paid priority fee. ChainLab still uses its native signed transaction JSON; this is not full Ethereum EIP-2718 / type-2 raw transaction compatibility.

Submit a sponsored transfer where a paymaster pays gas:

```powershell
go run ./cmd/chainlab tx transfer --rpc http://127.0.0.1:8547 --private-key <user-private-key> --to <address> --value 100 --gas-price 2 --paymaster-private-key <paymaster-private-key>
```

The user's signature covers the operation, the paymaster signature covers the user-signed transaction, and the receipt records `fee_payer`. This models the account-abstraction/paymaster workflow in a ChainLab-native way; it is not a full ERC-4337 EntryPoint or EIP-7702 implementation.

Submit a batched transfer with one user signature and one nonce:

```powershell
go run ./cmd/chainlab tx batch-transfer --rpc http://127.0.0.1:8547 --private-key <hex-private-key> --to <address-1>:10 --to <address-2>:20
```

Batch transactions use type `batch` and carry a `batch` array of operations. The first version supports `transfer` and contract `call` operations. Execution is atomic on a cloned state: if any operation fails, earlier operations in the same batch roll back and the sender nonce is not consumed. A batch can also include `--paymaster-private-key`, so a sponsor pays the single transaction-level fee.

Build a signed raw ChainLab transaction without broadcasting, then submit it later:

```powershell
go run ./cmd/chainlab tx transfer --rpc http://127.0.0.1:8547 --private-key <hex-private-key> --to <address> --value 100 --raw-only
go run ./cmd/chainlab tx raw-submit --rpc http://127.0.0.1:8547 --raw <0x-raw-transaction>
```

The raw format is a `0x`-prefixed hex encoding of ChainLab's signed transaction JSON. It is not Ethereum RLP or EIP-1559 raw transaction encoding yet.

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

Uploaded WASM runs inside a restricted wazero sandbox and only receives ChainLab host functions for args, contract storage, return data, and events. The module must implement ChainLab's current `deploy`, `call`, and `read` exports. This is not CosmWasm compatibility yet.

WASM upload gas scales with bytecode size. WASM deploy and write-call receipts include deterministic extra gas for ChainLab host ABI usage, including module instantiation, argument copies, storage reads/writes, return data, and emitted event bytes.

Stake, join, and leave the validator set:

```powershell
go run ./cmd/chainlab tx stake --rpc http://127.0.0.1:8547 --private-key <hex-private-key> --value 500
go run ./cmd/chainlab tx validator-join --rpc http://127.0.0.1:8547 --private-key <hex-private-key>
go run ./cmd/chainlab tx validator-leave --rpc http://127.0.0.1:8547 --private-key <hex-private-key>
go run ./cmd/chainlab tx validator-slash --rpc http://127.0.0.1:8547 --private-key <reporter-private-key> --target <validator-address> --amount 100 --evidence <evidence-ref>
```

Submit, vote on, execute, and query a governance parameter-change proposal:

```powershell
go run ./cmd/chainlab tx proposal-submit --rpc http://127.0.0.1:8547 --private-key <hex-private-key> --title "Set quorum" --kind param.change --param governance.quorum --value majority --voting-period 2
go run ./cmd/chainlab chain produce --rpc http://127.0.0.1:8547
go run ./cmd/chainlab query tx --rpc http://127.0.0.1:8547 --hash <proposal-submit-tx-hash>
go run ./cmd/chainlab tx vote --rpc http://127.0.0.1:8547 --private-key <hex-private-key> --proposal <proposal-id> --choice yes
go run ./cmd/chainlab chain produce --rpc http://127.0.0.1:8547
go run ./cmd/chainlab tx proposal-execute --rpc http://127.0.0.1:8547 --private-key <hex-private-key> --proposal <proposal-id>
go run ./cmd/chainlab chain produce --rpc http://127.0.0.1:8547
go run ./cmd/chainlab query proposal --rpc http://127.0.0.1:8547 --id <proposal-id>
go run ./cmd/chainlab query param --rpc http://127.0.0.1:8547 --key governance.quorum
```

Proposal ids are returned in the submit transaction receipt as `receipt.proposal_id`. The current pass rule is intentionally simple for the dev chain: yes votes must be greater than no votes and greater than zero after the voting period closes.

Produce a block:

```powershell
go run ./cmd/chainlab chain produce --rpc http://127.0.0.1:8547
```

Query chain state:

```powershell
go run ./cmd/chainlab query head --rpc http://127.0.0.1:8547
go run ./cmd/chainlab query finality --rpc http://127.0.0.1:8547
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

## HTTP API

- `GET /health`
- `GET /explorer`
- `GET /explorer/block/{height}`
- `GET /explorer/tx/{hash}`
- `GET /explorer/account/{address}`
- `GET /chain/head`
- `GET /chain/finality`
- `GET /chain/block/{height}`
- `GET /account/{address}`
- `GET /validators`
- `GET /proposal/{id}`
- `GET /param/{key}`
- `GET /tx/{hash}`
- `GET /txpool`
- `POST /tx` for signed transactions, including `transfer`, `batch`, `deploy`, `call`, `wasm.upload`, staking, validator, and governance transaction types
- `POST /tx/raw`
- `POST /faucet`
- `POST /chain/produce`
- `POST /peer/tx`
- `POST /peer/block`
- `POST /rpc` with methods `chain_head`, `chain_finality`, `chain_feeMarket`, `chain_getAccount`, `chain_validators`, `chain_proposal`, `chain_param`, `chain_sendTx`, `chain_sendUserOperation`, and `chain_faucet`
- `POST /rpc` with EVM-style methods `eth_chainId`, `eth_blockNumber`, `eth_gasPrice`, `eth_maxPriorityFeePerGas`, `eth_getBalance`, `eth_getTransactionCount`, `eth_getTransactionByHash`, `eth_getTransactionReceipt`, `eth_getBlockByNumber`, `eth_getLogs`, `eth_call`, `eth_estimateGas`, and `eth_sendRawTransaction`. Block range tags support `earliest`, `latest`, `safe`, `finalized`, and hex quantities. `eth_getTransactionCount` also supports `pending` for mempool-aware nonce calculation.
- `POST /rpc` with txpool-style methods `txpool_status` and `txpool_content`

## Roadmap

Next useful milestones:

- real BFT finality and richer fork-choice safety rules
- broader WASM ABI with CPU instruction/fuel metering
- richer account abstraction, including policy-based paymasters and contract accounts
- richer contract explorer views with decoded native contract state and events
- richer governance thresholds, quorum rules, deposits, and upgrade proposal handlers
- production-framework migration decision: OP Stack, Cosmos SDK, Avalanche L1, or another appchain stack
