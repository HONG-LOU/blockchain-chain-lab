# ChainLab V5 Validator Offence Retention

Status date: 2026-07-11

## Scope

`chainlab-v5` extends the genesis-committed V2 -> V3 -> V4 sequence with an explicitly activated validator-offence retention rule. It does not change V4 transaction, receipt, sparse-state, or proof formats.

V5 closes forward tombstone compaction only. Certified validator admission/re-entry, stake-derived power, unbonding, rewards, key rotation, governance authority, and runtime-authorized scheduling remain separate gates.

## Evidence Time Commitment

V2-V4 offence state records offence and observation heights but not the original evidence timestamp. Those tombstones can prove block age but cannot prove CometBFT's duration-age condition, so they remain permanent.

For evidence first consumed at a V5 height, the offence entry additionally commits:

- `evidence_time_present=true`
- canonical Unix seconds
- nanoseconds in `[0, 1_000_000_000)`

Unmarked entries carrying time fields are invalid. Marked timestamps survive Pebble restart, history reconstruction, application snapshot/state sync, and sparse proofs.

## Compaction Rule

Before proposal transaction execution at V5 height `H` and block time `T`, a timestamped offence is removed only when both are true:

```text
H - offence_height > evidence_max_age_num_blocks
T - evidence_time > evidence_max_age_duration
```

The strict greater-than and logical AND match CometBFT v0.39.3 evidence expiry. If only one dimension has expired, the tombstone remains. Evidence older than both dimensions is rejected during proposal validation before compaction can expose the key, so an expired proof cannot slash again.

Compaction is applied to the same detached proposal state in PrepareProposal, ProcessProposal, and FinalizeBlock. Deletions enter the mutation journal, V5 sparse root, validator root, Pebble delta/checkpoint, application snapshot, and state-sync snapshot. FinalizeBlock emits deterministic `chainlab.validator_offence_pruned` events.

## Activation

The only accepted order is:

```text
chainlab-v2 -> chainlab-v3 -> chainlab-v4 -> chainlab-v5
```

Comet app version 5 is published at `A-1` and applies at V5 activation height `A`. A binary capped at app version 4 rejects height `A-1` before publishing a candidate.

Generate a scheduled private network with:

```powershell
go run ./cmd/chainlab-comet init --out data/comet-v5 --chain-id chainlab-v5 --application-protocol chainlab-v2 --upgrade-v3-height 100 --upgrade-v4-height 200 --upgrade-v5-height 300
```

## Evidence

Automated coverage proves one-dimension retention, dual-expiry deletion, legacy retention, expired-evidence rejection, timestamp schema validation, sparse membership/non-membership, Pebble restart, snapshot/state sync, incompatible-binary rejection, schedule validation, and fixed fresh-process app/state/validator roots.

A real four-validator CometBFT network crosses V3, V4, and V5 activation and converges on app version 5 with valid sparse account proofs. A second network starts all applications capped at V4, replaces them one at a time before V5 activation, commits a transaction with each 3-of-4 remainder, replays every restarted node to the same block/application state, and then crosses V5.

## Remaining Work

- Runtime-authorized scheduling, signed binary manifests, rollback limits, Comet binary upgrades, and staged operator runbook drills.
- Certified admission/re-entry, stake-to-power, unbonding/rewards, evidence economics, and key rotation.
- Linux load/soak and state-growth measurements, external security review, remote signing, and staged public networks.
