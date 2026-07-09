# ChainLab Txpool Queued Pool Design

## Purpose

ChainLab currently keeps a single executable pending mempool. A wallet or peer cannot submit a valid future-nonce transaction before the missing nonce arrives, because `submitTxLocked` replays the pending state and the executor rejects the nonce gap.

This design adds a node-local queued pool for nonce-gap transactions. It moves ChainLab closer to mainstream Ethereum-style txpool behavior while keeping consensus unchanged.

## Scope

Implemented in this slice:

- Store future-nonce transactions in `queued` instead of rejecting them with `bad nonce`.
- Keep `pending` as the only block-production pool.
- Promote queued transactions to pending when earlier nonces arrive or canonical state advances through an imported block.
- Expose queued transactions through `GET /txpool`, `txpool_status`, and `txpool_content`.
- Allow same-sender/same-nonce replacement in both pending and queued pools with the existing 10 percent fee bump rule.

Not implemented in this slice:

- Txpool capacity limits, eviction, local exemptions, journaling, or disk persistence.
- MEV-aware ordering, fee sorting, or per-account slot limits.
- Durable pending/queued transaction gossip.
- Promotion notifications that distinguish queued acceptance from pending promotion.

## Semantics

`pending` means every transaction can execute sequentially from the current canonical state plus earlier pending transactions. Pending transactions affect `eth_getTransactionCount(..., "pending")`, pending filters, block production, and local fee simulation.

`queued` means the transaction has a nonce higher than the sender's current pending nonce. Queued transactions are accepted into the txpool, exposed for inspection, and queryable by hash, but they do not advance pending nonce and are not included in a block until promoted.

When a new transaction is submitted:

1. Search pending and queued for the same normalized `from` and `nonce`.
2. If found, require the existing 10 percent fee bump policy and replace in that same pool.
3. Otherwise build pending state by replaying pending transactions only.
4. If the transaction nonce equals the pending account nonce, execute it on the pending state, append it to pending, and attempt queued promotion.
5. If the transaction nonce is greater than the pending account nonce, validate it conservatively on a cloned state with the account nonce advanced to the transaction nonce, then append it to queued.
6. If the transaction nonce is lower than the pending account nonce, reject it with the executor's nonce error unless it was a valid replacement.

Promotion scans queued transactions repeatedly. Any queued transaction whose nonce matches the current pending account nonce and executes successfully on the current pending state is moved to pending. A queued transaction that matches the nonce but fails execution is left queued, so later incoming funds or replacements can make it promotable.

## RPC Shape

`GET /txpool` adds:

```json
{
  "pending": [],
  "queued": [],
  "pending_count": 1,
  "queued_count": 1
}
```

`txpool_status` returns both counters as hex quantities. `txpool_content` returns:

```json
{
  "pending": {
    "0xsender": {
      "0x0": { "hash": "0x..." }
    }
  },
  "queued": {
    "0xsender": {
      "0x2": { "hash": "0x..." }
    }
  }
}
```

`eth_getTransactionByHash` searches committed transactions first, then pending, then queued, returning null block fields for non-committed transactions.

## Tests

- Node accepts a nonce gap transaction into queued, keeps pending nonce unchanged, and promotes it when the missing nonce is submitted.
- Node replaces queued same-sender/same-nonce transactions only when the fee bump is sufficient.
- Imported blocks can advance canonical state and promote local queued transactions.
- RPC exposes pending and queued counts/content, and queued transactions are available through hash lookup.
- CLI `query mempool` decodes and prints queued transactions through the existing snapshot type.

## Documentation

README, the current technology roadmap, and the main ChainLab design doc must state that ChainLab now has a node-local pending/queued txpool with automatic nonce-gap promotion, while capacity policy, eviction, journaling, local exemptions, and MEV ordering remain future milestones.
