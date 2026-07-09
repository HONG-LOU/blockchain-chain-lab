# Account Social Recovery Design

## Context

ChainLab already supports `account.v1` smart accounts, delegated EOAs that use `DelegatedCodeID=account.v1`, multisig accounts, paymasters, batches, and transfer or single-contract-method session keys. The remaining account-abstraction gap is owner recovery: if the owner key is lost, the account currently has no on-chain path to rotate to a new owner.

This design adds a ChainLab-native social recovery slice for `account.v1` accounts and delegated EOAs. It is intentionally smaller than ERC-4337, ERC-7579 modules, Safe modules, passkeys, email recovery, or a general programmable policy engine.

## Goals

- Let the account owner configure guardian addresses, a threshold, and a block-height delay.
- Let configured guardians approve one pending `new_owner`.
- Let a guardian execute the recovery after threshold approvals and the delay have passed.
- Let the current owner cancel a pending recovery or clear recovery configuration.
- Keep nonce, fee payment, storage, and receipts on the account being recovered.
- Expose the workflow through CLI and docs.

## Non-Goals

- No ERC-4337 EntryPoint or `UserOperation` compatibility.
- No ERC-7579 module format.
- No Safe module compatibility.
- No off-chain identity recovery, passkeys, email recovery, or WebAuthn.
- No guardian weights, rotating guardian sets by guardian vote, or multi-pending recovery queue.
- No recovery for `multisig.v1` accounts in this milestone.

## Storage

Recovery state is stored under deterministic account storage keys:

```text
recovery:guardians
recovery:threshold
recovery:delay
recovery:pending_owner
recovery:execute_after
recovery:approvals
```

`recovery:guardians` is a comma-separated list of lower-cased guardian addresses. `recovery:threshold` is a positive integer that cannot exceed guardian count. `recovery:delay` is a block count. `recovery:pending_owner` stores the only active recovery target. `recovery:execute_after` stores the first block height at which execution is allowed. `recovery:approvals` is a comma-separated unique list of guardians that approved the pending owner.

Starting a new approval for a different `new_owner` replaces the pending owner and resets approvals to the current approving guardian.

## Transaction

Add transaction type:

```text
account.recovery
```

Payload actions:

### configure

Owner-only. Required payload:

```json
{
  "action": "configure",
  "guardians": "0x...,0x...",
  "threshold": "2",
  "delay": "3"
}
```

Validation:

- `from` must be an `account.v1` account or delegated EOA.
- `signer` must be the current owner.
- guardians must be unique, non-empty addresses.
- threshold must be positive and `<= len(guardians)`.
- delay can be zero but should be explicit.

Execution writes config and clears any pending recovery. Event: `account.recovery_configured`.

### approve

Guardian-only. Required payload:

```json
{
  "action": "approve",
  "new_owner": "0x..."
}
```

Validation:

- `signer` must be a configured guardian.
- `new_owner` must be non-empty.

Execution starts or updates the pending recovery. If `new_owner` differs from the current pending owner, approvals reset to only the current guardian and `execute_after = block_height + delay`. If it matches, the signer is appended to approvals. Duplicate approvals fail. Event: `account.recovery_approved`.

### execute

Guardian-only. Payload may include `new_owner`; if present it must match the pending owner.

Execution checks:

- pending owner exists;
- approvals count is at least threshold;
- current block height is `>= execute_after`.

If valid, it sets `owner = pending_owner`, clears pending recovery state, and emits `account.recovery_executed`.

### cancel

Owner-only. Clears pending recovery state without changing configured guardians. Event: `account.recovery_cancelled`.

### clear

Owner-only. Clears recovery config and pending recovery state. Event: `account.recovery_cleared`.

## Authorization

`account.recovery` does not use multisig `authorizations`, session keys, paymasters for authorization, or Ethereum type-2 signatures.

The transaction must include `signer` and a signature from that signer. The validator then checks:

- `from` is `account.v1` or delegated EOA with `DelegatedCodeID=account.v1`;
- owner-only actions are signed by current `owner`;
- guardian-only actions are signed by a configured guardian.

The recovered account consumes the nonce and pays fees. The owner or guardian signer nonce is unchanged.

## CLI

Add:

```powershell
chainlab tx recovery --rpc <url> --from <account> --private-key <owner-key> --action configure --guardian <guardian-a> --guardian <guardian-b> --threshold 2 --delay 3
chainlab tx recovery --rpc <url> --from <account> --private-key <guardian-a-key> --action approve --new-owner <new-owner>
chainlab tx recovery --rpc <url> --from <account> --private-key <guardian-b-key> --action approve --new-owner <new-owner>
chainlab tx recovery --rpc <url> --from <account> --private-key <guardian-a-key> --action execute --new-owner <new-owner>
chainlab tx recovery --rpc <url> --from <account> --private-key <owner-key> --action cancel
chainlab tx recovery --rpc <url> --from <account> --private-key <owner-key> --action clear
```

The CLI reuses pending nonce lookup for the recovered account. It signs as the owner or guardian and sets the transaction `signer` field because `from != signer`.

## Errors

- Non-`account.v1` accounts return `account recovery requires account.v1 from account`.
- Missing signer returns `account recovery requires signer`.
- Non-owner config/cancel/clear returns `account recovery owner action requires current owner`.
- Non-guardian approve/execute returns `account recovery guardian action requires configured guardian`.
- Invalid guardian config returns explicit guardian or threshold errors.
- Duplicate guardian approval returns `recovery approval already recorded`.
- Execute before threshold returns `recovery approval threshold not met`.
- Execute before delay returns `recovery delay has not elapsed`.

## Testing

Core tests must prove:

- owner can configure guardians, threshold, and delay;
- guardian approvals are recorded on the account and do not increment guardian nonce;
- execution before threshold or delay fails without consuming account nonce;
- execution after threshold and delay rotates owner and clears pending state;
- old owner can no longer authorize account transfer;
- new owner can authorize account transfer;
- non-owner cannot configure;
- non-guardian cannot approve;
- owner can cancel pending recovery.

CLI tests must prove:

- `tx recovery --action configure` stores guardian config;
- guardian approvals and execute rotate owner on a real node;
- a subsequent `tx transfer --from <account> --private-key <new-owner-key>` succeeds.

Documentation must state that this is ChainLab-native guardian recovery, not ERC-4337, ERC-7579, Safe modules, or passkey recovery.
