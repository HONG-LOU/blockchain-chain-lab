# ChainLab

ChainLab is a local-first blockchain implementation for learning and prototyping an appchain.

It currently implements:

- account balances, nonces, storage, and deterministic state roots
- secp256k1 signatures and Ethereum-style 20-byte addresses
- transfers, staking, unstaking, and governance voting
- local proof-of-authority block production and validation
- transaction, receipt, and state roots
- native smart-contract runtime with `counter.v1` and `token.v1`
- HTTP REST endpoints and a small JSON-RPC-style endpoint
- persistent node snapshots with committed blocks, state, and transaction index
- EVM-compatible JSON-RPC read subset: `eth_chainId`, `eth_blockNumber`, `eth_getBalance`, `eth_getTransactionCount`, `eth_getTransactionByHash`, `eth_getTransactionReceipt`, `eth_getBlockByNumber`
- CLI commands for keys, genesis, nodes, and demos

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

Start a local node:

```powershell
go run ./cmd/chainlab node --genesis config/genesis.json --listen :8547
```

Start a persistent local node:

```powershell
go run ./cmd/chainlab node --genesis config/genesis.json --listen :8547 --data-dir data/localnet
```

Run the built-in demo:

```powershell
go run ./cmd/chainlab demo
```

## HTTP API

- `GET /health`
- `GET /chain/head`
- `GET /chain/block/{height}`
- `GET /account/{address}`
- `GET /tx/{hash}`
- `POST /tx`
- `POST /rpc` with methods `chain_head`, `chain_getAccount`, and `chain_sendTx`
- `POST /rpc` with EVM-style methods `eth_chainId`, `eth_blockNumber`, `eth_getBalance`, `eth_getTransactionCount`, `eth_getTransactionByHash`, `eth_getTransactionReceipt`, and `eth_getBlockByNumber`

## Roadmap

Next useful milestones:

- multi-node devnet
- WASM contract runtime
- block explorer UI
- production-framework migration decision: OP Stack, Cosmos SDK, Avalanche L1, or another appchain stack
