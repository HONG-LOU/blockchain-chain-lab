# ChainLab V3 Scheduled Upgrade And Inclusion Proofs

Status date: 2026-07-11

## Scope

`chainlab-v3` is an explicitly scheduled application protocol that replaces the legacy list/snapshot hashes in ABCI commitments with domain-separated Merkle roots. It adds transaction, receipt, and account-state inclusion proofs plus a standalone verifier that does not import ABCI, state, storage, or execution code.

V1 and V2 commitments, app hashes, and fresh-process vectors are unchanged. V3 is not silently enabled on an existing genesis.

## Upgrade Schedule

The canonical V2 application genesis can contain:

```json
{"upgrades":[{"height":100,"protocol":"chainlab-v3"}]}
```

The implemented schedule rules are deliberately narrow:

- the source genesis must be `chainlab-v2`;
- the targets must follow `chainlab-v3`, optional `chainlab-v4`, optional `chainlab-v5` order;
- activation height `A` must be at least 2;
- the schedule is part of the canonical genesis hash and application database identity;
- changing or removing it after database creation fails the genesis-identity check.

At `FinalizeBlock(A-1)`, the application still commits V2 roots and returns a CometBFT consensus-parameter update setting application version 3. CometBFT applies that version to the next height. At height `A`, the application commitment protocol becomes `chainlab-v3` and all three roots use the V3 algorithms.

`Info.AppVersion` reports the version required for the next height. This matters after committing `A-1` and during replay/state sync. A binary whose configured maximum is 2 refuses proposal processing/finalization before publishing the version-3 update, and refuses restart once the next height requires version 3. This models fail-closed incompatible-node behavior. A real four-validator test now rolls V4-capped applications to V5 one at a time before activation while preserving 3-of-4 progress and replay convergence; signed release compatibility, Comet binary replacement, and a staged operator runbook remain required.

The current schedule is genesis-committed. Runtime governance scheduling remains disabled. V4 and V5 behavior are specified separately in [ChainLab V4 Sparse State](chainlab-v4-sparse-state.md) and [ChainLab V5 Validator Offence Retention](chainlab-v5-offence-retention.md).

## Merkle Protocol

Proof protocol: `chainlab-merkle-proof-v1`.

Domains:

- transactions: `chainlab-tx-v1`
- receipts: `chainlab-receipt-v1`
- flat state: `chainlab-state-v1`

Every node hash is legacy Keccak-256 over this binary sequence:

```text
kind:u8
domain_length:u8
domain:bytes
total_leaves:u64be
index:u64be
first_length:u64be
first:bytes
second_length:u64be
second:bytes
```

Kinds are `0x00` leaf, `0x01` parent, `0x02` deterministic padding, and `0x03` empty root. A non-empty tree is padded to the next power of two. Leaf and padding hashes bind their index and the exact unpadded total; every parent also binds the total. This prevents a proof from relabeling the leaf count within the same padded tree size.

Transaction leaves are canonical ChainLab transaction JSON. Receipt leaves are canonical receipt JSON. State leaves are canonical objects with `kind`, base64url `key`, and base64url canonical `value`, ordered by `kind + ":" + base64url(key)`. The state tree covers account metadata, individual account-storage keys, code, stake, proposals, parameters, the active validator slice, validator identities, and offences.

Empty transaction and receipt blocks have domain-separated V3 empty roots. Inclusion proofs do not exist for an empty tree.

## Query And Verification

The following ABCI paths are available only when the requested committed height uses V3:

- `/proof/transaction` with canonical decimal transaction index;
- `/proof/receipt` with canonical decimal receipt index;
- `/proof/account` with a canonical ChainLab account address.

They return a canonical `chainlab-inclusion-proof-v1` envelope containing kind, height, committed root, logical key, leaf, exact total/index, and sibling hashes. Retained historical V3 heights are supported through Store V2 checkpoint/delta reconstruction. A V2 height returns unsupported rather than manufacturing a proof against a legacy root.

ABCI query paths are capped at 128 bytes and query data at 1 KiB before parsing or historical reconstruction. Transaction/receipt/state leaf sizes remain bounded by the existing consensus schemas. Historical proof reconstruction still holds the application lifecycle lock and is not yet an isolated public-query service.

The standalone verifier requires a separately supplied trusted root:

```powershell
go run ./cmd/chainlab-proof --proof proof.json --root 0x<trusted-root> --height <trusted-height> --kind <expected-kind> --key <expected-key>
```

The proof file must be canonical JSON in a bounded regular non-symlink file. The verifier rejects unknown fields, non-canonical base64url/hex, wrong trusted height/root/kind/key, wrong domain/index/total/depth, and modified leaves or siblings.

The trusted root and height must come from a separately verified ChainLab application commitment/header; kind and key must be the item the caller intended to request. Reusing all four claims from an unauthenticated envelope proves nothing about chain finality or user intent.

## Recovery And Network Evidence

Automated coverage includes:

- property-style round trips for tree sizes 1 through 17 and every leaf;
- tampered root/protocol/domain/index/total/leaf/sibling/key rejection, including same-depth total changes;
- a fixed Merkle root vector and fixed V1/V3 fresh-process ABCI vectors;
- V2 root behavior before `A`, app-version update at `A-1`, and V3 roots at `A`;
- simulated version-2 binary rejection in prepare/process/finalize and restart;
- immutable schedule/policy cloning and modified-genesis database rejection;
- current and retained-height transaction/receipt/account proofs;
- Pebble restart and V3 snapshot/state-sync proof recovery;
- a real four-validator CometBFT network crossing V3 and V4 scheduled heights, converging on app version 4, and independently verifying sparse account proofs from all four nodes.

## Remaining Gates

- Upgrade scheduling is genesis-only; governance authority, deposits/timelocks, signed binary manifests, rollback windows, Comet binary replacement, and a staged operator rolling-upgrade drill are not implemented. Automated application replacement is covered separately.
- V3 state proof construction materializes the full leaf set. V4 provides mutation-aware sparse state and broader state/non-membership routes; an isolated bounded historical proof service and production proof load policy remain open.
- `chainlab-proof` is a standalone minimal verifier in this repository, not a second independently implemented client or external audit.
- A trusted-header/light-client, bridge, wallet, and explorer verification path is not yet integrated.
- External monotonic data-directory rollback protection remains open.
