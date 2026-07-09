# ChainLab EIP-7702-Style Delegated EOA Design

## Purpose

ChainLab already supports native paymasters, batched user operations, single-owner contract accounts, and multisig contract accounts. Those features model important account-abstraction UX, but they require funds and nonce ownership to move into a contract account.

Modern Ethereum account abstraction is also moving through EIP-7702, where an EOA can set executable delegation code while keeping its own address, balance, and nonce. This design adds a ChainLab-native delegated EOA slice so an externally owned account can opt into account-style authorization without becoming a separate contract account.

This is an account-abstraction learning feature, not complete Ethereum `0x04` set-code raw transaction compatibility.

## Scope

Implemented in this slice:

- Add a consensus-state delegation marker for normal accounts.
- Add a `set_code` transaction that lets an EOA delegate to an allowed account contract code id.
- Support clearing the delegation by submitting an empty code id.
- Allow delegated EOAs to authorize transfers and batches with `signer=<delegate owner>` using the same account policy as `account.v1`.
- Keep the delegated EOA as the `from` account for balance, nonce, fees, txpool, receipts, explorer, and RPC projections.
- Expose delegation through account REST, `chain_getAccount`, explorer account pages, CLI query output, and transaction/event receipts.
- Add CLI support for setting and clearing delegation.

Not implemented in this slice:

- Full EIP-7702 type `0x04` raw transaction decoding or Ethereum authorization tuple RLP.
- EVM bytecode execution or Ethereum's exact `0xef0100 || address` delegated-code storage format.
- ERC-4337 EntryPoint validation, bundlers, alt mempools, or paymaster policy contracts.
- Session keys, social recovery, spending limits, or arbitrary programmable policy engines.
- Delegation to arbitrary uploaded WASM code. First version only delegates to built-in `account.v1` because its owner policy is already explicit and tested.

## Model

ChainLab accounts keep the existing fields:

- `Address`
- `Balance`
- `Nonce`
- `CodeID`
- `Storage`

Delegation should be explicit and separate from `CodeID`, because `CodeID` means the account itself is a contract account. A delegated EOA still has no contract code of its own. It keeps EOA balance and nonce semantics, but stores:

- `DelegatedCodeID`
- optional delegation storage such as `owner`

For the first version, `DelegatedCodeID` can be `account.v1`. The account's `owner` storage key defines the authorization key. If `DelegatedCodeID` is empty, the account behaves like a normal EOA.

The state root and snapshots must include delegation fields so node restart and reorg replay preserve delegation.

## Transaction Semantics

### `set_code`

`TxSetCode` is a normal ChainLab transaction signed by `from`. It consumes the `from` nonce and pays gas like other transactions.

Payload:

- `code_id`: allowed value is `account.v1`; empty string clears delegation.
- `owner`: required when setting `account.v1`; omitted when clearing.

Execution:

1. Verify the transaction is signed by `from`; `set_code` cannot use `signer`, multisig authorizations, or Ethereum type-2 signature kind.
2. Reject setting delegation on an account that already has `CodeID`, because contract accounts already execute their own code.
3. If `code_id` is empty, clear `DelegatedCodeID` and delegation storage keys managed by this feature.
4. If `code_id` is `account.v1`, store `DelegatedCodeID=account.v1` and `owner=<normalized owner>`.
5. Emit `account.delegation_set` or `account.delegation_cleared`.

### Delegated Authorization

When a transaction has `from=<delegated EOA>` and `signer=<owner>`, authorization succeeds if:

- `from` account has empty `CodeID`.
- `from` account has `DelegatedCodeID=account.v1`.
- `owner` storage matches `signer`.
- the signature verifies against `signer` and the ChainLab signing bytes.

The sender account remains `from`, so:

- sender nonce is `from.Nonce`
- value comes from `from.Balance`
- default fee payer is `from`
- paymaster sponsorship can still pay gas
- txpool replacement and queued promotion still group by `from` and nonce

If a delegated EOA transaction omits `signer`, it remains a normal EOA transaction and must be signed by `from`.

## RPC And CLI

Account JSON should include `delegated_code_id` when present.

`eth_getCode` remains honest:

- normal EOAs return `0x`
- contract accounts return deterministic `CodeID` hex
- delegated EOAs return deterministic delegated code id hex, plus account JSON/explorer shows that it is delegated

ChainLab JSON-RPC should accept `chain_sendTx` and `chain_sendUserOperation` with `type="set_code"`.

CLI:

```powershell
chainlab tx set-code --rpc http://127.0.0.1:8547 --private-key <eoa-key> --code-id account.v1 --owner <owner-address>
chainlab tx set-code --rpc http://127.0.0.1:8547 --private-key <eoa-key> --clear
chainlab tx transfer --rpc http://127.0.0.1:8547 --from <delegated-eoa-address> --private-key <owner-key> --to <recipient> --value 100
```

The existing transfer and batch commands can reuse their current `--from` / `Signer` path. The executor is responsible for recognizing delegated EOA authorization.

## Tests

- State snapshot/root includes delegated code id and survives clone/snapshot restore.
- `set_code` stores `DelegatedCodeID`, owner storage, emits a receipt event, consumes nonce, and charges fees.
- `set_code --clear` removes the delegation and rejects later delegated-owner transfers.
- Delegated EOA owner can transfer value from the EOA without spending owner balance or nonce.
- Unauthorized delegated signer fails without mutating nonce or balances.
- Delegated EOA transfers can use paymaster sponsorship.
- Node persistence or reorg replay preserves delegated EOA state through snapshots.
- RPC account, `eth_getCode`, and explorer account page expose delegation.
- CLI can set delegation and then send a delegated transfer in a real-node E2E test.

## Documentation

README, the current blockchain roadmap, and the main ChainLab design must say ChainLab supports a native EIP-7702-style delegated EOA experiment. They must also state that full Ethereum type-4 raw transactions, ERC-4337 EntryPoint compatibility, session keys, social recovery, and arbitrary policy engines remain separate milestones.
