# ChainLab V4 Sparse State

Status date: 2026-07-11

## Activation And Compatibility

V4 is an explicit second upgrade in a canonical V2 genesis:

```json
{"upgrades":[{"height":100,"protocol":"chainlab-v3"},{"height":200,"protocol":"chainlab-v4"}]}
```

Heights must be strictly increasing, V4 cannot skip V3, and the schedule is part of the genesis/database identity. At height 199 the application still commits V3 roots and publishes Comet application version 4 for the next height. Height 200 uses V4. A binary capped at version 3 refuses before publishing that transition.

V1, V2, and V3 vectors and roots are unchanged. V4 transaction and receipt roots remain the V3 exact-total Merkle roots. Only the application state root changes.

## Sparse State Protocol

The state map is a fixed-depth 256-bit binary sparse Merkle tree. Paths are legacy Keccak-256 of the domain `chainlab-state-v2` and the canonical logical key `kind:base64url(raw-key)`. Empty leaf, populated leaf, and branch hashes use distinct tags; branch hashes bind their depth. Populated leaves bind path, logical key, and canonical state leaf bytes.

Proof protocol `chainlab-sparse-merkle-proof-v1` carries a 32-byte bitmap plus only non-default siblings. Envelope protocol `chainlab-state-proof-v2` binds kind, committed height, root, and requested key. A leaf contains canonical `kind`, base64url `key`, and base64url canonical `value`. Non-membership carries no value and reconstructs the same trusted root from the deterministic empty leaf.

The authenticated kinds are `account`, `account_storage`, `code`, `stake`, `proposal`, `param`, `validators`, `validator_identity`, and `validator_offence`.

## Incremental Commit And Storage

The application and Store V2 keep independent in-memory sparse accumulators. FinalizeBlock creates a copy-on-write overlay and updates it from the state mutation journal. The Pebble batch persists the flat delta, results, manifest, indexes, and height atomically. Only after the synchronized batch succeeds is the overlay published into the committed base tree. A failed save leaves the committed accumulator unchanged and halts the application.

Ordinary commits update O(k * 256) authenticated nodes for k changed flat entries and do not sort or hash the complete state. At configured checkpoints, Store V2 also flattens the complete state, validates the journal delta, rebuilds a fresh sparse tree, and requires the incremental and rebuilt roots to match.

Manifest field `state_flat_root_protocol` is `chainlab-flat-root-v1` for new V2/V3 heights and `chainlab-sparse-root-v1` for V4. Older manifests without the field remain valid as legacy flat roots. Mixed histories therefore retain their original validation rules. Restart, historical reconstruction, snapshot restore, and state sync rebuild the in-memory sparse accumulator from authenticated flat state.

## Queries And Offline Verification

- `/proof/account` accepts a canonical address. V4 returns membership or non-membership.
- `/proof/state` accepts `kind:base64url-key` and returns membership or non-membership for any authenticated kind.
- `/proof/transaction` and `/proof/receipt` continue returning V3 inclusion envelopes.
- A retained V3 height still returns its original inclusion envelope; a retained V4 height returns a sparse envelope.

`chainlab-proof` detects both envelope protocols and requires separately supplied trusted root, height, kind, and key:

```powershell
go run ./cmd/chainlab-proof --proof proof.json --root 0x<trusted-state-root> --height 200 --kind state --key account:MHgxMTExMTExMTExMTExMTExMTExMTExMTExMTExMTExMTExMTExMTExMTEx
```

Successful output includes `exists=true` or `exists=false`. The verifier rejects modified roots, heights, kinds, keys, values, bitmap encodings, sibling counts, siblings, non-canonical JSON/base64url/hex, and values attached to non-membership proofs.

## Evidence And Remaining Gates

Coverage includes a fixed sparse root vector, fixed fresh-process V4 ABCI vector, order independence, update/delete/copy-on-write publication, membership/non-membership tampering, incremental-versus-full rebuild, mixed V3/V4 archive checkpoints, restart, historical proof reconstruction, snapshot/state sync, incompatible binaries, modified schedules, persistence failure isolation, and a four-validator real CometBFT V3/V4 upgrade.

Runtime-authorized upgrades, external rollback protection, a trusted-header/light-client consumer, an independent second implementation/audit, proof service isolation/rate policy, and production Linux load/soak remain separate release gates.
