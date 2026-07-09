# Session Key Contract Calls Design

## Context

ChainLab already supports `account.v1` smart accounts, delegated EOAs, native paymasters, batches, multisig accounts, and transfer-scoped session keys. A session key can currently spend value from an `account.v1` account or delegated EOA within a value limit, optional recipient allowlist, and optional block-height expiry.

The next account-abstraction gap is contract-call session keys. Current smart-account wallets commonly allow a temporary key to perform a narrow app action, such as calling one contract method, without exposing the owner key. This design adds that narrow ChainLab-native capability without claiming ERC-4337 EntryPoint compatibility, ERC-7579 module compatibility, social recovery, or a general programmable policy engine.

## Goals

- Allow an owner to install a session key that can sign a `call` transaction from an `account.v1` account or delegated EOA.
- Restrict that session key to one contract address and one method.
- Keep existing transfer session-key behavior unchanged.
- Keep nonce, balance, fee liability, receipts, txpool grouping, and explorer account state on the account, not on the session key.
- Expose the feature through CLI flags and documentation.

## Non-Goals

- No parameter-level allowlist.
- No batch session-key authorization.
- No wildcard contract-call policies.
- No contract creation, WASM upload, governance, validator, or staking authorization by session key.
- No ERC-4337 `UserOperation`, ERC-7579 module format, social recovery, or general policy engine.

## Policy Storage

Session-key policy stays in account storage under deterministic keys:

```text
session:<key>:limit
session:<key>:spent
session:<key>:expires
session:<key>:to
session:<key>:call_to
session:<key>:call_method
```

Existing transfer policies use `limit`, `spent`, optional `expires`, and optional `to`.

Contract-call policies add:

- `call_to`: lower-cased contract address that the session key may call.
- `call_method`: exact method string required in `tx.Payload["method"]`.

`expires` applies to both transfer and call policies. `limit`, `spent`, and `to` apply only to transfer spending. A policy can support transfer, call, or both:

- transfer only: `limit` is set and `call_to` / `call_method` are absent;
- call only: `call_to` and `call_method` are set and `limit` may be absent;
- transfer plus call: both transfer fields and call fields are set.

Revocation deletes all keys for the session key, including the new call policy fields.

## Transaction Semantics

### Installing A Call Policy

`account.session_key` keeps the existing owner-only install path. The `add` payload may include:

```json
{
  "action": "add",
  "key": "0x...",
  "expires": "50",
  "call_to": "0x...",
  "call_method": "increment"
}
```

At least one usable permission must be present:

- transfer permission: positive `limit`;
- call permission: both `call_to` and `call_method`.

If either `call_to` or `call_method` is present without the other, installation fails.

The `account.session_key_added` event includes `call_to` and `call_method` attributes when a call policy is installed.

### Authorizing `call`

When a transaction has `from=<account>`, `signer=<session-key>`, and type `call`, authorization succeeds only if:

- `from` is an `account.v1` account or a delegated EOA with `DelegatedCodeID=account.v1`;
- the session key signature verifies;
- the key has an installed policy;
- the policy is not expired for the current block height;
- `tx.To` equals `call_to`;
- `tx.Payload["method"]` equals `call_method`.

After a successful contract call, ChainLab emits `account.session_key_used` with attributes:

- `account`
- `key`
- `type=call`
- `to`
- `method`

Contract-call session-key use does not increment `spent` because no transfer value is consumed by the policy.

### Transfer Behavior

Existing transfer policy behavior remains unchanged:

- transfer still requires positive `limit`;
- transfer still checks optional `to`;
- transfer still increments `spent` by `tx.Value`;
- transfer still emits `account.session_key_used` with transfer value and cumulative spent.

A call-only policy cannot authorize transfers because it has no positive transfer limit.

## CLI

`chainlab tx session-key` adds:

- `--call-to <address>`
- `--call-method <method>`

Examples:

```powershell
go run ./cmd/chainlab tx session-key --rpc http://127.0.0.1:8547 --from <account> --private-key <owner-key> --key <session-address> --call-to <contract> --call-method increment --expires 50
```

`chainlab tx call` adds:

- `--from <account-or-delegated-eoa>`

When `--from` is present, the private key signs as `signer`, while the transaction `from` remains the account. This matches existing transfer and batch smart-account command behavior.

Example:

```powershell
go run ./cmd/chainlab tx call --rpc http://127.0.0.1:8547 --from <account> --private-key <session-key> --to <contract> --method increment --arg amount=1
```

## Error Handling

- Installing a session key with no transfer permission and no call permission returns `session key requires transfer limit or call policy`.
- Installing a partial call policy returns `session key call policy requires call_to and call_method`.
- Using a call-only key for transfer returns the existing not-authorized or transfer-limit error.
- Using a call key against the wrong contract returns `session key call target not allowed`.
- Using a call key against the wrong method returns `session key call method not allowed`.
- Using a call key after expiry returns `session key expired`.
- Revoked keys remain unauthorized for both transfer and call.

## Testing

Core tests must prove:

- owner can install a call-only session key;
- that session key can call the allowed contract method;
- account nonce increments while session key nonce stays unchanged;
- disallowed contract address fails;
- disallowed method fails;
- expired call policy fails;
- revoked call policy fails;
- existing transfer session-key tests still pass.

CLI tests must prove:

- `tx session-key --call-to --call-method` stores the call policy;
- `tx call --from <account> --private-key <session-key>` submits a signed account call;
- produced block mutates the target contract state through the session key.

Documentation updates must state that ChainLab supports transfer and single-contract-method session keys, while full ERC-4337, ERC-7579, social recovery, batch policies, parameter policies, and general policy engines remain separate milestones.
