# ChainLab

ChainLab is a local-first blockchain implementation for learning and prototyping an appchain.

It currently implements:

- account balances, nonces, storage, and deterministic state roots
- secp256k1 signatures and Ethereum-style 20-byte addresses
- transfers, staking, unstaking, and governance voting
- local proof-of-authority block production with deterministic multi-validator proposer rotation
- transaction, receipt, and state roots
- native smart-contract runtime with `counter.v1` and `token.v1`
- HTTP REST endpoints and a small JSON-RPC-style endpoint
- persistent node snapshots with committed blocks, state, and transaction index
- EVM-compatible JSON-RPC read subset: `eth_chainId`, `eth_blockNumber`, `eth_getBalance`, `eth_getTransactionCount`, `eth_getTransactionByHash`, `eth_getTransactionReceipt`, `eth_getBlockByNumber`, `eth_getLogs`
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

Produce a block:

```powershell
go run ./cmd/chainlab chain produce --rpc http://127.0.0.1:8547
```

Query chain state:

```powershell
go run ./cmd/chainlab query head --rpc http://127.0.0.1:8547
go run ./cmd/chainlab query account --rpc http://127.0.0.1:8547 --address <address>
go run ./cmd/chainlab query tx --rpc http://127.0.0.1:8547 --hash <tx-hash>
go run ./cmd/chainlab query logs --rpc http://127.0.0.1:8547 --from-block 0x1 --to-block latest --address <contract-address> --topic <topic0>
```

## HTTP API

- `GET /health`
- `GET /chain/head`
- `GET /chain/block/{height}`
- `GET /account/{address}`
- `GET /tx/{hash}`
- `POST /tx`
- `POST /chain/produce`
- `POST /peer/tx`
- `POST /peer/block`
- `POST /rpc` with methods `chain_head`, `chain_getAccount`, and `chain_sendTx`
- `POST /rpc` with EVM-style methods `eth_chainId`, `eth_blockNumber`, `eth_getBalance`, `eth_getTransactionCount`, `eth_getTransactionByHash`, `eth_getTransactionReceipt`, `eth_getBlockByNumber`, and `eth_getLogs`

## Roadmap

Next useful milestones:

- validator-set change transactions and stronger fork-choice or finality rules
- WASM contract runtime
- block explorer UI
- production-framework migration decision: OP Stack, Cosmos SDK, Avalanche L1, or another appchain stack
