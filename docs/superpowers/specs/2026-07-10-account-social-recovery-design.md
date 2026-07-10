# Account Social Recovery Design

## Status

Implemented design, revised after adversarial review.

This is ChainLab-native guardian recovery for `account.v1` smart accounts and delegated EOAs. It is not ERC-4337 EntryPoint validation, an ERC-7579 module, a Safe recovery module, EIP-7702 type-4 compatibility, or passkey/email recovery.

## Threat Model

The feature addresses a lost or unavailable current owner key when a configured guardian threshold remains honest and available.

It does not protect against an active compromised owner: the current owner can immediately cancel a pending recovery, clear or replace guardian configuration, call `setOwner`, or, for a delegated EOA, use the root EOA key to replace/clear delegation. Monitoring and delay give the owner a reaction window only when the owner remains able and willing to act.

Guardians and replacement owners are signature-verifying EOAs. Contract guardians, ERC-1271, passkeys, email identity, weighted guardians, and multisig recovery are non-goals for this slice.

## Goals

- Let the owner configure up to 16 guardian EOAs, a threshold, and a block-height delay.
- Record one vote per guardian without letting a minority guardian occupy or repeatedly reset a pending target.
- Create a unique pending recovery only when one target reaches threshold.
- Start the delay when threshold is reached, then give guardians a bounded execution window.
- Rotate the owner atomically while invalidating pre-recovery session authority.
- Keep every failed action atomic and expose canonical events through receipts, the event index, and EVM-shaped logs.

## Storage

```text
recovery:guardians
recovery:threshold
recovery:delay
recovery:votes
recovery:pending_owner
recovery:execute_after
recovery:expires_at
recovery:approvals
session:epoch
```

`recovery:guardians` is a canonical comma-separated list. Guardians are unique, non-zero 20-byte hex EOAs and cannot equal the recovered account or current owner.

`recovery:votes` stores canonical `guardian=new_owner` entries before threshold. A guardian may cast one initial vote in a voting round. A change to another target is accepted only when that change immediately reaches threshold; this permits split votes to converge without allowing unilateral vote churn.

The first vote opens a 256-block voting round. If no target reaches threshold, a later `approve` after `expires_at` clears the old votes and begins a new round.

When a target reaches threshold, its matching guardian set is copied to `recovery:approvals`, `pending_owner` is frozen, and:

```text
execute_after = threshold_reached_height + delay
expires_at = execute_after + 256
```

The equality boundaries are inclusive: execution is allowed when height equals `execute_after` or `expires_at`, and rejected after `expires_at`. Height additions are overflow checked.

## Transaction Envelope

Transaction type:

```text
account.recovery
```

Every recovery transaction requires an explicit `signer` and a signature from that signer. The recovered account supplies the transaction nonce. Guardian nonce is unchanged.

Recovery rejects multisig `authorizations`, every non-empty `signature_kind`, paymaster fields (including an orphan signature), `to`, non-zero `value`, `batch`, and action-irrelevant payload fields.

Fee policy:

- `configure`, `cancel`, and `clear`: recovered account pays gas.
- `approve` and `execute`: guardian signer pays gas; `receipt.fee_payer` records that guardian.

Guardian-funded actions prevent a malicious guardian from repeatedly draining the recovered account. Insufficient guardian balance fails atomically before committed state changes.

## Actions

### `configure`

Owner-only payload:

```json
{
  "action": "configure",
  "guardians": "0x...,0x...",
  "threshold": "2",
  "delay": "3"
}
```

`delay` is required and may explicitly be zero. Threshold is positive and no larger than guardian count. Configuration clears votes and pending recovery state but does not revoke active session keys. Event: `account.recovery_configured`.

### `approve`

Guardian-only payload:

```json
{
  "action": "approve",
  "new_owner": "0x..."
}
```

`new_owner` is a non-zero 20-byte EOA, cannot equal the recovered account or current owner, and must not currently have contract code. Before threshold, the vote is stored or a converging vote change is applied. At threshold, pending target, approvals, delay, and execution expiry are frozen. Event: `account.recovery_approved`.

### `execute`

Guardian-only payload:

```json
{
  "action": "execute",
  "new_owner": "0x..."
}
```

`new_owner` is mandatory so the guardian signature binds the final target. It must equal `pending_owner`. Execution requires threshold approvals, `height >= execute_after`, and `height <= expires_at`.

Successful execution sets `owner`, clears voting/pending state, and increments `session:epoch`. Existing session-policy keys may remain in storage, but their earlier epoch can no longer authorize transfers or calls. Event: `account.recovery_executed`.

### `cancel`

Owner-only. Clears current votes and pending state, preserving guardian configuration. Event: `account.recovery_cancelled`.

### `clear`

Owner-only. Clears all `recovery:` configuration, votes, and pending state. Event: `account.recovery_cleared`.

## Owner-Rotation And Delegation Invariants

- Direct `account.v1 setOwner` clears votes/pending recovery and all `session:` authority before setting the new owner. Guardian configuration remains available to the new owner.
- Replacing or clearing delegated `account.v1` code removes prior owner, session, and recovery authorization storage so a later delegation cannot reactivate stale policy.
- Owner and guardian addresses are strictly normalized 20-byte non-zero addresses. An account cannot own itself.
- Account nonce overflow is rejected rather than wrapping to zero.
- Pending transaction replacement requires the same authorization principal; one guardian cannot fee-bump-replace another guardian's same-account/same-nonce recovery transaction.

## Events And Reorgs

Recovery events use the recovered account (`tx.from`) as the event/log address. They are available through canonical event queries, `eth_getLogs`, receipts, filters, and WebSocket log subscriptions.

Delay uses candidate block height, not finalized height. A reorg replays recovery state against canonical heights. Current WebSocket delivery does not emit `removed: true` for orphaned logs, so a recovery monitor must re-query canonical state/logs and use the local `safe`/`finalized` boundary instead of treating one notification as final.

## CLI

```powershell
chainlab tx recovery --rpc <url> --from <account> --private-key <owner-key> --action configure --guardian <guardian-a> --guardian <guardian-b> --threshold 2 --delay 3
chainlab tx recovery --rpc <url> --from <account> --private-key <guardian-a-key> --action approve --new-owner <new-owner>
chainlab tx recovery --rpc <url> --from <account> --private-key <guardian-b-key> --action approve --new-owner <new-owner>
chainlab tx recovery --rpc <url> --from <account> --private-key <guardian-a-key> --action execute --new-owner <new-owner>
chainlab tx recovery --rpc <url> --from <account> --private-key <owner-key> --action cancel
chainlab tx recovery --rpc <url> --from <account> --private-key <owner-key> --action clear
```

The CLI uses the pending nonce of `--from` and always writes `signer`, including the `from == signer` edge. Guardians need native balance for `approve` and `execute` gas.

## Required Evidence

- Smart-account and delegated-EOA configure/approve/execute/transfer lifecycles.
- Role isolation, signature/envelope validation, maximum guardian set, malformed/corrupted stored state, height/nonce overflow, exact delay/expiry boundaries, split-vote convergence, voting-round rollover, fee ownership, insufficient guardian funds, and atomic failures.
- Session transfer/call authority invalid after rotation.
- Direct owner rotation and delegation replacement clear stale authority.
- Real-node CLI workflow, mempool cross-guardian replacement rejection, persistence/replay, and account-addressed recovery logs.
- Full tests, race detector, vet, demo, build, and formatting checks.

## Non-Goals

- ERC-4337 EntryPoint, bundler, keyed nonce, or `UserOperation` compatibility.
- ERC-7579/Safe module formats or ERC-1271 contract guardians.
- EIP-7702 authorization tuples or root-key recovery for delegated EOAs.
- Guardian weights, multiple simultaneous threshold-approved proposals, finalized-height timelocks, passkeys, email, WebAuthn, or off-chain identity recovery.
- Recovery for `multisig.v1`.
