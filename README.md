# ChainLab

ChainLab is a local-first blockchain implementation for learning and prototyping an appchain.

It currently implements:

- account balances, nonces, storage, and deterministic state roots
- secp256k1 signatures and Ethereum-style 20-byte addresses
- transfers, staking, unstaking, and governance voting
- local proof-of-authority block production with deterministic multi-validator proposer rotation
- dynamic validator joins, leaves, and slashing through `validator.join` / `validator.leave` / `validator.slash` transactions, with validator set committed into state roots and snapshots
- deterministic local `safe` and `finalized` chain checkpoints using conservative block-depth rules
- transaction, receipt, and state roots
- native smart-contract runtime with `counter.v1` and `token.v1`
- HTTP REST endpoints and a small JSON-RPC-style endpoint
- persistent node snapshots with committed blocks, state, and transaction index
- EVM-compatible JSON-RPC read subset: `eth_chainId`, `eth_blockNumber`, `eth_getBalance`, `eth_getTransactionCount`, `eth_getTransactionByHash`, `eth_getTransactionReceipt`, `eth_getBlockByNumber`, `eth_getLogs`, `eth_call`, `eth_estimateGas`
- EVM-style `safe` and `finalized` block tags for block and log reads
- pending nonce calculation and txpool inspection for uncommitted transactions
- ChainLab-native raw transaction envelope for offline signing and later broadcast
- devnet faucet that creates a normal proposer-signed transfer into the mempool
- local block explorer page for head, finality, recent blocks, validators, and mempool
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

Open the local explorer at `http://127.0.0.1:8547/explorer`.

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

Stake, join, and leave the validator set:

```powershell
go run ./cmd/chainlab tx stake --rpc http://127.0.0.1:8547 --private-key <hex-private-key> --value 500
go run ./cmd/chainlab tx validator-join --rpc http://127.0.0.1:8547 --private-key <hex-private-key>
go run ./cmd/chainlab tx validator-leave --rpc http://127.0.0.1:8547 --private-key <hex-private-key>
go run ./cmd/chainlab tx validator-slash --rpc http://127.0.0.1:8547 --private-key <reporter-private-key> --target <validator-address> --amount 100 --evidence <evidence-ref>
```

Produce a block:

```powershell
go run ./cmd/chainlab chain produce --rpc http://127.0.0.1:8547
```

Query chain state:

```powershell
go run ./cmd/chainlab query head --rpc http://127.0.0.1:8547
go run ./cmd/chainlab query finality --rpc http://127.0.0.1:8547
go run ./cmd/chainlab query mempool --rpc http://127.0.0.1:8547
go run ./cmd/chainlab query account --rpc http://127.0.0.1:8547 --address <address>
go run ./cmd/chainlab query tx --rpc http://127.0.0.1:8547 --hash <tx-hash>
go run ./cmd/chainlab query logs --rpc http://127.0.0.1:8547 --from-block 0x1 --to-block latest --address <contract-address> --topic <topic0>
go run ./cmd/chainlab query validators --rpc http://127.0.0.1:8547
go run ./cmd/chainlab query call --rpc http://127.0.0.1:8547 --to <contract-address> --method <read-method> --arg address=<address>
go run ./cmd/chainlab query estimate-gas --rpc http://127.0.0.1:8547 --type call --to <contract-address>
```

## HTTP API

- `GET /health`
- `GET /explorer`
- `GET /chain/head`
- `GET /chain/finality`
- `GET /chain/block/{height}`
- `GET /account/{address}`
- `GET /validators`
- `GET /tx/{hash}`
- `GET /txpool`
- `POST /tx`
- `POST /tx/raw`
- `POST /faucet`
- `POST /chain/produce`
- `POST /peer/tx`
- `POST /peer/block`
- `POST /rpc` with methods `chain_head`, `chain_finality`, `chain_getAccount`, `chain_validators`, `chain_sendTx`, and `chain_faucet`
- `POST /rpc` with EVM-style methods `eth_chainId`, `eth_blockNumber`, `eth_getBalance`, `eth_getTransactionCount`, `eth_getTransactionByHash`, `eth_getTransactionReceipt`, `eth_getBlockByNumber`, `eth_getLogs`, `eth_call`, `eth_estimateGas`, and `eth_sendRawTransaction`. Block range tags support `earliest`, `latest`, `safe`, `finalized`, and hex quantities. `eth_getTransactionCount` also supports `pending` for mempool-aware nonce calculation.
- `POST /rpc` with txpool-style methods `txpool_status` and `txpool_content`

## Roadmap

Next useful milestones:

- stronger fork-choice rules and real BFT finality
- WASM contract runtime
- richer explorer detail pages for accounts, blocks, transactions, and contracts
- production-framework migration decision: OP Stack, Cosmos SDK, Avalanche L1, or another appchain stack
