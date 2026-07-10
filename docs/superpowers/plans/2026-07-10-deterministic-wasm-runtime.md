# Deterministic WASM Runtime Implementation Plan

## Goal

Replace consensus-unsafe wazero static body charging and wall-clock timeout
with a version-pinned, deterministically fueled Wasmtime runtime, strict module
admission, bounded host resources, read isolation, and atomic direct calls.

## Test-Driven Sequence

- [x] Add module-policy tests for maximum size, start section, `_start`, WASI
  and unknown imports, imported resources, required export signatures, and
  memory/table limits.
- [x] Replace the timeout test with an infinite-loop fuel test that asserts a
  stable out-of-gas class and deterministic consumed gas.
- [x] Add dynamic execution tests for loops, callees/recursion, memory growth,
  stack exhaustion, and repeated fresh-runtime gas equality.
- [x] Add read-isolation tests using malicious WASM and native contracts that
  attempt writes and events.
- [x] Add host-limit tests for memory bounds, key/value/event/return sizes,
  event count/bytes, storage write count, total I/O, and storage growth gas.
- [x] Add direct rollback tests for write-then-trap, host out-of-gas before
  mutation, failed native deploy/call, read isolation, and low-gas transaction
  calls.
- [x] Add executor and batch vectors for WASM deploy/call guest traps, a later
  operation failure, and failed-transaction fee/nonce semantics.
- [x] Pin `github.com/bytecodealliance/wasmtime-go/v46 v46.0.1`; remove wazero.
- [x] Build a single version-1 Wasmtime engine configuration with fuel, NaN
  canonicalization, disabled unsupported proposals, a stack limit, and stable
  resource limits.
- [x] Implement deterministic module admission and normalized errors.
- [x] Implement a shared guest/host fuel balance and pre-side-effect host
  charging with bounded memory access.
- [x] Make runtime deploy/call atomic and read calls disposable/read-only.
- [x] Add limited meters, pre-JIT instantiation-resource gates, early fee-cap
  and balance checks, and pass remaining transaction gas through deploy, call,
  upload, and batch execution paths.
- [x] Cache state-bound validated modules with concurrent-miss singleflight and
  cold/warm consensus-gas equality.
- [x] Classify unexpected engine failures as `ErrWasmRuntimeFault` and make the
  local node enter a process-local sticky halt across transaction admission,
  block production/import, finality votes, revalidation, and queued promotion.
- [ ] Define, persist, fault-inject, and operationally test protocol-wide fatal
  runtime-fault halt plus deterministic recovery/upgrade behavior on every
  CometBFT ABCI++ validator path.
- [ ] Replace GC-backed cache eviction with a concurrency-safe hard native/JIT
  memory lifecycle bound and stop replay-compiling pending uploads.
- [x] Update production-roadmap and current-design claims with exact runtime
  status and residual release/architecture gates.
- [x] Run focused tests after each RED/GREEN slice, then full tests, race, vet,
  build, demo, deterministic replay, formatting, and diff checks.
- [x] Implement and test a maximum-64 acyclic direct-call-depth policy that is
  independent of the native stack layout.
- [ ] Commit Linux/amd64 golden block-replay vectors; Windows remains a
  development target and arm64/macOS validators stay unsupported until
  matching evidence exists.
- [x] Version and meter native-contract work, and define charged failed-
  transaction receipts so signed OOG/trap calls cannot be replayed for free.
- [x] Commit the implementation in reviewable commits.
- [x] Write the Obsidian diary and ChainLab project record, commit them, and
  push the notes repository.

## Explicit Non-Goals

- CosmWasm ABI or wasmd module compatibility.
- WASI or nondeterministic host facilities.
- Claiming Ethereum-compatible failed-transaction, gas, or receipt semantics.
- Claiming multi-architecture validator support without native artifacts and
  matching replay vectors.
- Claiming the chain is production-ready from this runtime milestone alone.
