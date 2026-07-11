# ChainLab Application Store V2

Status date: 2026-07-11

## Scope

`chainlab-app-store-v2` is the durable application-state format used by the CometBFT ABCI++ process. It replaces the V1 complete per-height disk copy with a flat current state, incremental height deltas, and periodic checkpoints while preserving one-batch commit atomicity.

This document specifies implemented behavior. It does not claim production throughput, filesystem-independent rollback protection, or production-scale proof serving.

## Atomic Layout

The database identity binds the protocol, canonical genesis hash, and exact storage profile. A profile mismatch on restart fails closed.

- `v2/live/` stores the complete current flat state.
- `v2/version/<height>/delta_set/` stores changed or created entries.
- `v2/version/<height>/delta_delete/` stores deleted-entry markers.
- `v2/version/<height>/checkpoint/` stores a complete flat state at checkpoint heights.
- Each height manifest binds the commitment, app hash, versioned flat-state root/count, delta root/counts, checkpoint root/count, proposer bindings, transaction count, receipt count, and checksum. Legacy V2/V3 heights use `chainlab-flat-root-v1`; V4 heights use `chainlab-sparse-root-v1`.
- `meta/current` and `meta/min_history` identify the published height and earliest retained height.
- `index/block/` maps each retained non-genesis block hash to its height.

One `pebble.Sync` batch updates live entries, writes the new delta/checkpoint and block results, advances both metadata pointers, writes the block index, and deletes pruned versions/indexes. A crash can expose only the complete previous batch or the complete next batch.

Accounts and account-storage keys are separate flat entries. Contract code, stake, proposals, parameters, validator identities, validator offences, and the active validator slice also have independent entries. Updating one contract storage key therefore persists one storage delta instead of rewriting its account or every state key.

The in-memory `state.Store` now keeps a detached semantic mutation journal for account metadata, individual account-storage keys, contract code, stake, proposals, parameters, the validator slice, validator identities, and validator offences. Store clones carry the journal through proposal simulation, failed-transaction ante settlement, and finalization. Only a successful durable application commit resets it.

Ordinary heights encode delta sets/deletes only from that journal and compare each touched entry with the previous flat value, so a write restored to its prior value produces no disk delta. The projected live map reuses immutable prior entry bytes and is published only after the synchronized Pebble batch succeeds. V4 also updates a copy-on-write sparse accumulator for touched entries and publishes the overlay only after persistence. Checkpoint heights flatten the complete state, compare the journal delta with a complete diff, rebuild the complete sparse tree, and require the incremental and rebuilt roots to match. Genesis creation, V1 migration, state-sync restore, checkpoints, and legacy V2/V3 flat/proof roots still require complete-state work. Ordinary V4 state-root maintenance is O(mutations * 256), but end-to-end production throughput remains unclaimed.

## Retention Profiles

### Archive

`archive` uses `retain_heights=0` and a positive checkpoint interval. It retains every height available in that database. A database created from genesis has `minimum_height=0`; a database restored by state sync begins at the trusted restored height and cannot serve earlier history.

### Full

`full` requires at least two retained heights and a positive checkpoint interval no larger than the retention setting. It retains at least `retain_heights` recent heights and moves `minimum_height` only to a retained checkpoint. It can therefore retain up to one additional checkpoint interval minus one height.

The default is 10,000 retained heights with a checkpoint every 100 heights.

### Pruned

`pruned` requires `retain_heights=1` and `checkpoint_interval=0`. Each commit removes all older version and block-index entries. The flat live state and latest manifest remain restart-verifiable.

Profile changes are not supported in place. An operator must create a separately trusted database through state sync or another explicitly verified recovery procedure.

## Historical Reads

ABCI `Query` accepts a positive retained height for `/app`, `/state/root`, `/account`, and `/validator`. The store locates the nearest retained checkpoint at or below the requested height, verifies each manifest and delta, replays forward, rebuilds the state, and verifies the target state, transaction, receipt, app-hash, and block-index commitments.

Height `0` follows the ABCI convention and means latest, so genesis-height reads are available through the internal storage API but not distinguishable from latest through the standard request field.

Requests below `minimum_height` return a pruned error. Requests above current height or otherwise unavailable return an unavailable error. The latest-only `/storage` query reports the protocol, configured profile, checkpoint/retention settings, and current history range.

Historical reconstruction currently runs while holding the application lifecycle lock so it cannot observe a mixed commit. This is correct but can delay consensus application work. A production public history service still needs bounded concurrency, cancellation, and an isolated Pebble snapshot/read-replica path or external indexer.

## Migration

When V1 identity is present, V1 remains authoritative until activation completes.

1. Any previous V2 shadow and migration marker are deleted synchronously.
2. Archive migrates all V1 heights, full migrates its selected recent window, and pruned migrates only current height.
3. Heights are converted in synchronized batches. The first migrated height is a full checkpoint, and a marker records progress.
4. One final synchronized batch switches identity to V2, writes the history boundary, removes the marker, and deletes V1 version entries.
5. Stale block indexes below the new boundary are removed.

If the process stops before activation, the next start discards the incomplete shadow and deterministically rebuilds it from V1. This favors a simple authoritative restart over partial-resume complexity; migration time for production-size archive databases still requires measurement and an operator runbook.

## State Sync, Backup, And Compaction

Snapshot import is independent of historical retention. Restoring a trusted application snapshot atomically replaces live/version/index data with one checkpoint at the restored height and sets `minimum_height` to that height. Later archive/full history grows forward from there.

`--backup-to` creates a Pebble checkpoint with a flushed WAL and exits. The destination must not already exist and must be outside the live data directory. The checkpoint directory opens directly with the same genesis and storage profile; tests verify retained historical reads from the backup.

`--compact` performs a full Pebble compaction before serving, or before creating a backup when both flags are supplied. Compaction does not change the logical history range.

## Integrity And Recovery Evidence

Startup verifies database identity, profile, current/minimum range, current live state, current version artifacts, and the retained checkpoint boundary. Loading a version verifies canonical keys and values, manifest identity/checksum/counts, disjoint delta sets/deletes, delta root, checkpoint root, transaction and receipt continuity, reconstructed state/app hash, and block index.

Manifests written before root versioning omit `state_flat_root_protocol` and remain canonical legacy flat-root manifests. New V2/V3 and V4 heights can coexist in one retained history, and each height is validated with its recorded protocol. Restart, historical reconstruction, and state-sync restore rebuild the in-memory sparse accumulator from the flat state.

Automated coverage includes:

- single-key mutation delta generation against 1,000 account-storage entries, no-op collapse, deletion, clone/reset isolation, and 200 sequential semantic-write/full-diff comparisons;
- checkpoint rejection of an intentionally cleared mutation journal without advancing the database, followed by a successful restart at the previous height;
- archive historical reads, restart, backup/open, and compaction;
- bounded full retention and latest-only pruned retention;
- deterministic V1 migration, interrupted-shadow replacement, pruned migration, and profile mismatch rejection;
- state-sync history rebasing and restart;
- missing live entries, receipt corruption, corrupt deltas, and invalid history boundaries;
- abrupt process exit and forced termination around a multi-megabyte synchronized batch.
- V4 incremental sparse roots versus complete checkpoint rebuilds, mixed V3/V4 archive history, persistence-failure overlay isolation, restart, and snapshot/state-sync restoration.

On Windows/amd64 development hardware, the committed benchmark for one changed storage key measured mutation-journal delta construction at about 1.3–1.7 microseconds and 1.35 KiB/12 allocations for both 1,000 and 4,096 existing entries. The former full snapshot/flatten/diff path measured about 1.46 milliseconds/1.1 MiB/8,054 allocations at 1,000 entries and 6.2 milliseconds/4.7 MiB/about 32,865 allocations at 4,096 entries. These figures isolate delta construction only; they are not Linux capacity, end-to-end commit latency, or mainnet throughput evidence.

The copy-on-write sparse single-key benchmark is approximately 225-250 microseconds and 96.6 KiB/281 allocations at both 1,000 and 4,096 leaves on the same Windows/amd64 host. This demonstrates state-size-independent update cost at those sizes, not production latency; allocation reduction remains useful. Remaining production gates include nonblocking bounded historical/proof reads, production-size migration/compaction/load/soak evidence on Linux, wider power-loss and disk-full phases, an operator restore drill, external monotonic rollback protection, and metrics/alerts.
