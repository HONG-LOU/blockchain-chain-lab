# ChainLab Session Key Accounts Design

## Purpose

ChainLab already supports native paymasters, batches, `account.v1` smart accounts, `multisig.v1`, and EIP-7702-style delegated EOAs. The next account-abstraction gap is session keys: a wallet owner should be able to authorize a temporary key with limited power, so routine actions can be signed without exposing the owner key.

This design adds a ChainLab-native session-key slice for `account.v1` accounts and delegated EOAs that use `DelegatedCodeID=account.v1`. It is inspired by current account-abstraction wallet patterns, but it is not full ERC-4337 EntryPoint, ERC-7579 modular account, or policy-engine compatibility.

## Scope

Implemented in this slice:

- Add an `account.session_key` transaction for owners to install or revoke a session key on an `account.v1` account or delegated EOA.
- Store session-key policy in account storage so it is included in snapshots, state roots, persistence, and reorg replay.
- Allow a session key to sign a `transfer` from the account while the account keeps nonce, balance, and fee liability.
- Enforce per-session-key transfer value limit, optional recipient allowlist, and optional expiry block height.
- Track cumulative value spent by the session key.
- Expose behavior through core tests, CLI E2E tests, README, roadmap, and the main ChainLab design spec.

Not implemented in this slice:

- ERC-4337 `UserOperation`, EntryPoint, bundler, alt mempool, aggregator, or paymaster contract APIs.
- ERC-7579 validator/executor/fallback module registry.
- General programmable policy language.
- Session-key support for contract calls, batches, governance, validator, raw Ethereum type-2, or `set_code` transactions.
- Time-based expiry; first version uses block height so replay is deterministic.
- Spending limit over gas fees; the limit covers transferred value only. Fees are still charged to the account unless a paymaster sponsors them.

## Storage Model

For each session key address `0xabc...`, the owning account stores:

- `session:<key>:limit` - positive uint64 transfer-value limit.
- `session:<key>:spent` - cumulative uint64 transferred value.
- `session:<key>:expires` - optional block height; `0` or empty means no expiry.
- `session:<key>:to` - optional recipient allowlist; empty means any recipient.

Revocation deletes those keys.

This storage lives under the account address, so the same model works for both contract accounts and delegated EOAs.

## Transaction Semantics

### `account.session_key`

Payload:

- `action`: `add` or `revoke`.
- `key`: session key address.
- `limit`: positive uint64, required for `add`.
- `expires`: optional uint64 block height.
- `to`: optional recipient address.

Authorization:

- `from` must be either an `account.v1` contract account or a delegated EOA with `DelegatedCodeID=account.v1`.
- `signer` must be the stored owner.
- The signature must verify against `signer`.
- Session keys cannot add or revoke other session keys.

Execution:

- `add` stores normalized policy fields and initializes missing `spent` to `0`.
- `revoke` removes all storage fields for that key.
- Receipt events are `account.session_key_added` and `account.session_key_revoked`.

### Session-Key Transfer

A normal `transfer` may be signed with `signer=<session-key-address>` when:

- `from` is an `account.v1` contract account or delegated EOA.
- A session policy exists for `signer`.
- `tx.Type == transfer`.
- `tx.Value` does not exceed `limit - spent`.
- `to` matches the allowlist when one is configured.
- the current block height is not past `expires`.
- the signature verifies against the session key.

After the transfer succeeds, ChainLab increments `session:<key>:spent` by `tx.Value` and emits `account.session_key_used`.

The sender account remains `from`, so account nonce, balance, default fee liability, txpool grouping, receipts, and explorer account storage all belong to the account, not the session key.

## CLI

Install a session key:

```powershell
chainlab tx session-key --rpc http://127.0.0.1:8547 --from <account-or-delegated-eoa> --private-key <owner-private-key> --key <session-key-address> --limit 100 --expires 50 --to <recipient>
```

Use the session key:

```powershell
chainlab tx transfer --rpc http://127.0.0.1:8547 --from <account-or-delegated-eoa> --private-key <session-private-key> --to <recipient> --value 25
```

Revoke it:

```powershell
chainlab tx session-key --rpc http://127.0.0.1:8547 --from <account-or-delegated-eoa> --private-key <owner-private-key> --key <session-key-address> --revoke
```

## Tests

- Core: owner can add a session key, session key can transfer within limit, spent increments, account nonce increments, and session key nonce stays unchanged.
- Core: non-owner cannot add session key.
- Core: session key cannot transfer over limit, to a disallowed recipient, after expiry, or after revocation.
- CLI: `tx session-key` installs a key through a real test server, then `tx transfer --from <account> --private-key <session-key>` succeeds within policy.
- Docs: README, roadmap, and main design spec document session keys as ChainLab-native and keep ERC-4337/ERC-7579 non-goals explicit.

## References

- ERC-4337: https://eips.ethereum.org/EIPS/eip-4337
- Ethereum account abstraction: https://ethereum.org/roadmap/account-abstraction/
- ERC-7579 modular smart accounts: https://eips.ethereum.org/EIPS/eip-7579
