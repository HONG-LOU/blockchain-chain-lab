# Txpool Replacement Design

## Goal

Add Ethereum-style pending transaction replacement to ChainLab's node-local txpool so wallets and raw transaction clients can resubmit a stuck transaction with the same sender and nonce after increasing the fee.

This moves ChainLab closer to mainstream EVM node behavior while keeping the current single pending pool architecture.

## Current Context

ChainLab already has:

- A node-local mempool stored as `[]types.Transaction`.
- `PendingAccount` and `eth_getTransactionCount(..., "pending")`, which replay pending transactions on a cloned state.
- `GET /txpool`, `txpool_status`, and `txpool_content`.
- Pending transaction lookup through `eth_getTransactionByHash`.
- Pending transaction polling through `eth_newPendingTransactionFilter`.
- WebSocket `eth_subscribe("newPendingTransactions")`.
- Native signed raw transactions and limited Ethereum EIP-1559 type-2 raw value transfers.

Today `Node.submitTxLocked` always builds pending state by replaying the full mempool. If a new transaction has the same sender and nonce as an existing pending transaction, replaying the old transaction increments the sender nonce first, and the new transaction fails with a nonce error. That blocks the common wallet workflow of replacing a pending transaction with a higher-fee transaction.

## External Compatibility Target

The behavior should follow the practical shape of Geth-style txpool replacement:

- Geth exposes pending and queued txpool inspection through `txpool_content` and `txpool_status`.
- Geth has a `--txpool.pricebump` setting whose default is 10 percent for replacing an existing EVM transaction.
- ChainLab will use a fixed 10 percent bump for this slice.

References:

- https://geth.ethereum.org/docs/interacting-with-geth/rpc/ns-txpool
- https://geth.ethereum.org/docs/fundamentals/command-line-options

## Scope

In scope:

1. Replace an existing pending transaction when the new transaction has the same normalized sender and nonce.
2. Require the new transaction to pay at least a 10 percent fee bump.
3. Validate the replacement transaction by replaying the pending pool in order with the replacement substituted at the matched index.
4. Keep the replacement in the same mempool position as the old transaction.
5. Preserve existing txpool read APIs while making them show only the active replacement.
6. Emit the accepted replacement hash through existing pending transaction notification paths.
7. Document the behavior and current limits.

Out of scope:

- Queued transaction pool for nonce gaps.
- Pool capacity limits, eviction, TTL, journaling, local transaction exemptions, or remote/local priority.
- Multiple pending alternatives for the same sender and nonce.
- Transaction ordering by gas price.
- Replacing already committed transactions.
- EVM blob pool or EIP-4844 semantics.

## Replacement Identity

A transaction is a replacement candidate when:

- `strings.ToLower(strings.TrimSpace(oldTx.From)) == strings.ToLower(strings.TrimSpace(newTx.From))`
- `oldTx.Nonce == newTx.Nonce`

`Signer`, `To`, `Value`, transaction type, payload, and hash may differ. This lets users cancel or speed up a transaction by sending a different transaction with the same account nonce.

Chain ID is already enforced by execution validation, so it does not need to be part of the replacement key in this slice.

## Fee Bump Policy

ChainLab will use a fixed 10 percent bump.

Legacy-style transactions:

- If either transaction uses only `GasPrice`, the new `GasPrice` must be at least `oldGasPrice + ceil(oldGasPrice * 10 / 100)`.

EIP-1559-style transactions:

- If either transaction uses `MaxFeePerGas` or `MaxPriorityFeePerGas`, both caps must be bumped:
  - `new.MaxFeePerGas >= old.MaxFeePerGas + ceil(old.MaxFeePerGas * 10 / 100)`
  - `new.MaxPriorityFeePerGas >= old.MaxPriorityFeePerGas + ceil(old.MaxPriorityFeePerGas * 10 / 100)`

Mixed transactions:

- If the old transaction uses legacy `GasPrice` and the new one uses EIP-1559 fields, compare the old `GasPrice` against both new caps.
- If the old transaction uses EIP-1559 fields and the new one uses only `GasPrice`, compare the new `GasPrice` against both old caps.

Rationale: this is stricter than an effective-price-only check but easier to reason about and avoids replacements that raise one cap while weakening the other. It also matches common EIP-1559 replacement expectations where both fee cap and tip cap need to move upward.

Overflow handling:

- If computing the bumped fee threshold overflows `uint64`, the replacement is not acceptable and returns `replacement transaction underpriced`.

## Node Data Flow

`Node.submitTxLocked(tx)` will:

1. Search `n.mempool` for the first transaction with the same normalized sender and nonce.
2. If no match exists, keep the current path:
   - Replay all pending transactions with `pendingStateLocked`.
   - Execute the new transaction against the resulting pending state.
   - Append the new transaction.
3. If a match exists:
   - Check the replacement fee bump against the matched transaction.
   - Replay the mempool from canonical state in current order, but execute the new transaction at the matched index instead of the old transaction.
   - Reject the replacement if the replacement itself or any later pending transaction becomes invalid after the substitution.
   - Replace `n.mempool[index]` with the new transaction.

The old transaction is not retained anywhere in the pending pool after a successful replacement. Replaying with substitution preserves later same-sender nonces that depend on the replaced transaction advancing the nonce.

## API Behavior

Existing API shapes stay the same:

- `Node.Mempool()` returns the active pending transactions.
- `Node.TxPool()` returns the same pending slice and `QueuedCount: 0`.
- `GET /txpool`, `txpool_status`, and `txpool_content` show the replacement transaction only.
- `eth_getTransactionByHash(oldHash)` returns `null` after replacement unless the old transaction was already committed earlier.
- `eth_getTransactionByHash(newHash)` returns the pending replacement with null block fields.
- `eth_getTransactionCount(address, "pending")` remains correct because replaying the active pending pool consumes the nonce once.
- `eth_newPendingTransactionFilter` sees the replacement hash as a newly accepted pending transaction.
- `eth_subscribe("newPendingTransactions")` receives the replacement hash after successful acceptance.

## Error Handling

Replacement attempts that do not meet the fee bump return:

```text
replacement transaction underpriced
```

Replacement attempts that meet the fee bump but fail normal transaction execution return the existing execution errors such as bad nonce, insufficient funds, invalid signature, gas errors, or chain ID mismatch.

The mempool must remain unchanged on every failed replacement attempt.

## Tests

Node tests:

1. Higher-fee same sender and nonce replacement updates the mempool in place.
2. Producing a block after replacement includes only the replacement transaction and applies only its effects.
3. Insufficient fee bump returns `replacement transaction underpriced` and keeps the old transaction.
4. EIP-1559-style replacement requires both max fee and priority fee to meet the 10 percent bump.
5. Replacing nonce N preserves a later pending nonce N+1 transaction when the replacement leaves the later transaction valid.

RPC tests:

1. `POST /tx` accepts a higher-fee replacement, returns the new hash, and `txpool_content` exposes only the replacement.
2. `eth_getTransactionByHash` no longer finds the old pending hash and does find the replacement hash.
3. A pending transaction filter registered before the replacement returns the replacement hash.

Regression coverage:

- Existing pending nonce, faucet, raw transaction, txpool, pending lookup, pending filter, and WebSocket pending subscription tests must continue to pass.

## Documentation

Update:

- `README.md`
- `docs/current-blockchain-tech-roadmap.md`
- `docs/superpowers/specs/2026-07-09-own-chain-design.md`

The docs should state that ChainLab supports Geth-style 10 percent pending tx replacement for same sender and nonce. At the time of this replacement design, queued pools, txpool eviction, local exemptions, and capacity policy were still separate milestones; queued nonce-gap handling is later covered by `2026-07-10-txpool-queued-pool-design.md`.

## Acceptance Criteria

- A same sender and nonce replacement with a sufficient fee bump succeeds.
- A same sender and nonce replacement without a sufficient fee bump fails with `replacement transaction underpriced`.
- Failed replacement attempts leave the mempool unchanged.
- Replaced transactions disappear from pending txpool reads and pending transaction lookup.
- Replacement transactions are included normally in produced blocks.
- Relevant node/RPC tests and the full Go test suite pass.
