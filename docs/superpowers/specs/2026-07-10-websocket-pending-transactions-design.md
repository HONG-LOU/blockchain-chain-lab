# WebSocket Pending Transactions Design

## Goal

Add a node-local WebSocket subscription for pending transactions so ChainLab behaves more like mainstream Ethereum-style RPC nodes for wallets, SDKs, and lightweight indexers.

The scope is deliberately narrow: `eth_subscribe("newPendingTransactions")` pushes transaction hashes for transactions accepted into the local txpool after the subscription is created.

## Recommended Approach

Use the existing WebSocket JSON-RPC handler and add a third subscription table beside `newHeads` and `logs`.

- `eth_subscribe` accepts subscription type `newPendingTransactions`.
- The subscription response is the same local subscription id format already used by other WebSocket subscriptions.
- Each notification uses the standard `eth_subscription` envelope with `params.subscription` and `params.result`.
- `params.result` is the accepted transaction hash string, not a full transaction object.
- `eth_unsubscribe` removes pending transaction subscriptions through the same unified cleanup path as `newHeads` and `logs`.

This matches the common Ethereum subscription shape while keeping ChainLab's current node-local, restart-volatile development scope.

## Alternatives Considered

1. Push only transaction hashes.
   - Best compatibility with common `newPendingTransactions` clients.
   - Small messages and a small implementation surface.
   - Clients can call `eth_getTransactionByHash` when they need details.

2. Push full pending transaction objects.
   - More convenient for a custom explorer.
   - Less aligned with the common default `newPendingTransactions` expectation.
   - Larger messages and more risk that the WebSocket object shape drifts from `eth_getTransactionByHash`.

3. Build a general txpool event bus first.
   - Cleaner if ChainLab soon needs multiple internal txpool consumers.
   - Too broad for this compatibility slice.
   - Would change more infrastructure than needed for the current roadmap.

The selected approach is option 1.

## Data Flow

1. A client opens `GET /rpc/ws`.
2. The client sends `eth_subscribe` with `["newPendingTransactions"]`.
3. The server registers a buffered hash channel and returns a subscription id.
4. A transaction is submitted through an existing successful txpool entry path:
   - `POST /tx`
   - `POST /tx/raw`
   - `POST /faucet`
   - `POST /peer/tx`
   - `eth_sendRawTransaction`
   - `chain_faucet`
   - `chain_sendUserOperation`
5. After `SubmitTx` or `RequestFaucet` succeeds, the RPC server broadcasts the transaction hash to local pending transaction subscribers.
6. The writer goroutine emits:

```json
{
  "jsonrpc": "2.0",
  "method": "eth_subscription",
  "params": {
    "subscription": "0x1",
    "result": "0x..."
  }
}
```

## Boundaries

- Existing pending transactions are not replayed when a subscription is created.
- Rejected transactions are not broadcast.
- Transactions already included in a produced block are not re-broadcast from block production.
- The subscription is node-local and in-memory. It is not a durable mempool feed.
- Peer relayed transactions are broadcast when this node accepts them into its own txpool.
- Slow subscribers use the same drop-on-full-buffer policy as current WebSocket subscriptions.

## Error Handling

- Unknown subscription types continue returning `unsupported subscription`.
- `eth_unsubscribe` returns `false` for missing or non-local subscription ids.
- Connection close unregisters all local subscriptions, including pending transaction subscriptions.

## Testing

Use TDD with a WebSocket integration test:

1. Subscribe to `newPendingTransactions`.
2. Submit a signed transaction through an HTTP path.
3. Expect one `eth_subscription` notification whose result equals the submitted transaction hash.
4. Confirm the initial RED failure is `unsupported subscription`.
5. Run adjacent WebSocket, pending filter, and txpool tests.
6. Run full repository verification before commit.
