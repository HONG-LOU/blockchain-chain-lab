# Deterministic WASM Runtime Design

## Status

Implemented pre-genesis consensus design; mainnet evidence gates remain open.

This design replaces the wazero static function-body charge and wall-clock
timeout with a version-pinned Wasmtime execution engine and deterministic fuel.
It keeps ChainLab's application state machine, transaction protocol, storage,
and host ABI. It does not claim CosmWasm, EVM, WASI, or general WebAssembly
ecosystem compatibility.

Wasmtime was selected for this version because it provides maintained engine
fuel while preserving ChainLab's existing application protocol and host ABI.
CosmWasm wasmvm remains a viable future compatibility subsystem, but adopting
it requires a separate binary-KV, message/query/reply, ordered-event, and guest
ABI migration; it is not a drop-in metering fix.

## Safety Decision

ChainLab must not execute arbitrary user WASM in a production consensus path
until every guest instruction and host resource is bounded independently of
wall-clock time, scheduler timing, validator load, or machine speed.

The consensus engine for metering version `chainlab-wasm-v1` is:

```text
github.com/bytecodealliance/wasmtime-go/v46 v46.0.1
Wasmtime fuel enabled
Cranelift backend
Cranelift optimization level: speed
NaN canonicalization enabled
parallel compilation disabled
reference types enabled for fixed funcref tables and call_indirect
SIMD, relaxed SIMD, bulk memory, multi-value, multi-memory, memory64,
threads, function references, tail calls, GC, wide arithmetic, and the
component model disabled
maximum WASM stack: 512 KiB
```

The Go module version, native Wasmtime artifact, engine configuration, module
policy, host ABI, and gas schedule are one protocol unit. Changing any of them
requires a named metering version, an activation height, replay vectors, and a
network upgrade. A node must never silently use a different runtime version.

Wasmtime fuel, rather than epoch interruption or a context deadline, decides
whether guest execution succeeds. Epoch and wall-clock interruption may be
added later as a node watchdog, but a watchdog firing is a local validator
fault: it cannot produce a committed failure receipt or state transition.

## Threat Model

The runtime assumes uploaded bytecode, arguments, storage, and call sequences
are adversarial. It must contain:

- infinite loops, recursion, branch-heavy code, indirect calls, and traps;
- oversized modules, memories, tables, inputs, outputs, events, and writes;
- unknown, privileged, or nondeterministic imports;
- initialization code that attempts side effects before an exported call;
- read methods that attempt storage writes or event emission;
- out-of-gas execution immediately before a host side effect;
- repeated execution intended to exhaust compiler, memory, receipt, or state
  resources;
- validator differences in CPU speed, load, OS scheduling, and floating-point
  NaN representation.

This milestone does not prove the Wasmtime implementation correct. The pinned
engine remains a trusted computing base that requires dependency review,
published checksums, reproducible release inputs, differential replay, fuzzing,
and external security review before mainnet.

## Module Admission

Upload validates before consensus state stores code. The policy is deterministic
and returns stable errors:

- maximum module size: 512 KiB;
- valid core WebAssembly accepted by the pinned engine configuration;
- no start section and no `_start` export;
- imports are functions from module `chainlab` only;
- imported functions are exactly the version-1 host ABI and signatures;
- imported memories, tables, globals, WASI, clocks, randomness, filesystem,
  network, and process APIs are forbidden;
- exactly one exported memory;
- required functions `deploy`, `call`, and `read`, each with `() -> ()`;
- no additional exported functions, memories, tables, or globals;
- exactly one memory with an explicit fixed minimum/maximum of 1 to 256 pages
  (64 KiB to 16 MiB); `memory.grow` therefore returns `-1`;
- at most one funcref table with an explicit fixed minimum/maximum of 0 to
  1,024 elements; `table.grow` therefore returns `-1`;
- at most one instance, one memory, and one table per invocation.

Version 1 also caps the binary structure at 1,024 types, 4,096 declared
functions/code bodies, 256 globals, 256 element/data segments, 64 parameters
per function type, one result per function type, 1,024 locals per function,
and 16,384 locals per module. There are exactly four permitted exports.
Standard sections must be unique and ordered, function and code-body counts
must match, and parsed type/import/function/memory/table/export/code payloads
must be fully consumed.

The ChainLab envelope parser is a deterministic resource and ABI prefilter,
not an independent implementation of the full WebAssembly validator. The
pinned Wasmtime engine remains authoritative for instruction typing, global,
element and data expressions, and complete core-module validity before code is
stored or executed. This dependency is part of the trusted computing base.

The start section is rejected even though fuel could meter it. Running hidden
initialization on every call makes capability review and gas reasoning harder,
and can attempt writes before the selected export runs.

Code ID remains the hash of the admitted original bytecode. Because this is a
pre-genesis rule change, existing local uploaded modules are revalidated under
version 1. After genesis, contract code records must include their metering
version and activation/migration rules.

## Gas Model

One Wasmtime fuel unit equals one ChainLab WASM compute gas unit in metering
version 1. The exact fuel inserted and consumed by Wasmtime `v46.0.1` is part of
the pinned protocol behavior, not a stable promise across Wasmtime releases.

An invocation receives only the transaction gas remaining after transaction
base gas and earlier batch operations. Direct runtime and query helpers use a
fixed maximum invocation budget. Before JIT compilation or instantiation, the
runtime verifies that the remaining budget covers the module's deterministic
instantiation-resource charge. The balance is then installed as Wasmtime fuel,
so module initialization and the requested export share one budget.

Guest instructions and host work use the same Wasmtime store fuel balance:

1. Wasmtime deducts guest fuel.
2. Before a host side effect, the host reads remaining fuel and deducts its
   versioned charge with `SetFuel`.
3. Insufficient fuel traps before mutation, allocation proportional to guest
   input, event append, or return-value update.
4. Invocation gas is `instantiation_resource_gas + initial_fuel -
   remaining_fuel`, including failed calls.
5. The contract meter records consumed fuel even after a host trap, without
   exceeding its caller-provided limit.

Version-1 host charges retain these base constants and add bounded byte costs:

```text
instantiation base          100 gas
admitted original module byte  1 gas per byte
declared memory page       1,000 gas per 64 KiB page
declared table element        10 gas per element
host call base              100 gas
input/output byte             1 gas
positive storage growth       4 gas per byte
```

Memory and table declarations are paid on every fresh instance and fixed at
admission, so guest growth cannot allocate beyond the paid amount. Stack depth
is stopped by both fuel and the configured native WASM stack ceiling.
Compilation is not consensus gas. A state-bound compiled-module cache uses
singleflight on concurrent misses and is capped at 256 logical entries without
changing cold/warm gas. Native JIT memory reclamation after eviction is still
GC/finalizer-backed rather than a hard resident-memory bound and remains a
production gate.

## Host Resource Limits

Every host length is checked before reading guest memory. Version 1 limits:

```text
host key                         256 bytes
host value / argument         65,536 bytes
return value                  65,536 bytes
event type                       128 bytes
event attribute key              256 bytes
event attribute value         16,384 bytes
events per invocation              64
total event bytes             65,536 bytes
storage writes per invocation      128
total host I/O bytes        1,048,576 bytes
```

Length arithmetic is overflow checked. Limits are enforced before string or
slice copies. Storage writes charge key/value I/O plus positive growth relative
to the previous value. Reads charge returned bytes. Events and return data
charge all copied bytes. No host call receives filesystem, network, time,
randomness, environment, process, or validator-local data.

Version 1 is a UTF-8 string ABI because the current ChainLab state, event, and
snapshot formats are string-based. Guest keys, values, event fields, and return
data, plus host values copied into guest memory, must be valid UTF-8. Invalid
bytes trap before state/event mutation. Arbitrary binary contract storage
requires a separately versioned binary-KV protocol and migration.

## Atomicity And Read Isolation

`Runtime.Deploy` and `Runtime.Call` execute on a state clone and replace the
caller's store only after the contract returns successfully. This makes direct
runtime use atomic. The transaction executor already owns a transaction-level
working store and uses explicit in-transaction runtime methods, avoiding an
additional whole-state clone for every contract operation.

`Runtime.Read` always executes on a disposable clone with a bounded query fuel
budget. The read invocation exposes trapping implementations of write-only host
capabilities. A read cannot persist storage, code, account, event, or other
state even if a malicious native or WASM contract attempts mutation.

The transaction executor still owns the outer transaction clone. A trap,
out-of-gas result, invalid return, event/resource-limit breach, fee failure, or
later batch-operation failure discards all contract effects.

The outer store is still an in-memory whole-state clone. Replacing it with a
transactional versioned KV overlay/savepoint is a separate P0 storage gate; the
current clone cost is not claimed to be production-bounded by contract gas.

Failed-transaction receipts and fee semantics remain a separate protocol item.
The current executor rejects failed contract transactions without committing a
nonce or fee. This runtime change must not silently redefine that behavior.

## Deterministic Errors And Observability

Consensus-visible errors are normalized ChainLab errors, not raw platform/JIT
messages. At minimum the runtime distinguishes:

- invalid module policy;
- contract out of gas;
- resource limit exceeded;
- forbidden read-only capability;
- guest trap;
- missing or invalid export.

Only structured guest traps become deterministic contract outcomes. A linker,
JIT, native-library, fuel-accounting, or other engine-internal error is a local
runtime fault: the node must stop the affected consensus processing path and
must not manufacture a failure receipt from that error.

The adapter classifies those errors as `ErrWasmRuntimeFault`. The development
node records a sticky halt when consensus-adjacent submit, produce, import,
finality-vote, revalidation, or queued-promotion paths observe that class and
rejects subsequent submit/produce/import/finality operations. Persistent nodes
write the halt as a strict, checksummed `HALT.json` record and restore it before
resuming consensus entry points; non-persistent development nodes keep the halt
in memory only. This is not yet protocol-wide across CometBFT ABCI++ validators.
Cross-validator fault injection and deterministic recovery/upgrade behavior
remain required before production activation.

Receipts continue to record total transaction gas. A later protocol version may
add metering version and guest/host breakdown, but nodes must already expose
these in non-consensus diagnostics and metrics without changing execution.

## Verification Gates

Implementation is not complete until all of these pass:

- small-body infinite loop stops by fuel with no time-based assertion;
- loops, recursion, callees, indirect calls, memory growth, and traps consume
  deterministic fuel;
- start, WASI, unknown imports, invalid ABI, and oversized modules are rejected;
- read attempts to write or emit leave storage and state root unchanged;
- host out-of-gas occurs before storage/event/return side effects;
- guest trap after writes rolls back all effects in direct and executor paths;
- event, storage-write, host-I/O, memory, table, and stack limits are enforced;
- identical invocations in fresh runtimes produce identical result, gas,
  events, storage, receipt, and state root;
- a pinned Linux/amd64 release artifact matches committed golden vectors in
  independent processes; Windows is development-only for version 1, and arm64
  or macOS validators are unsupported until native artifacts and adversarial
  replay vectors prove identical behavior;
- full tests, race detector, vet, vulnerability scan, fuzz corpus, dependency
  license review, release checksum verification, and `git diff --check` pass.

Passing the local implementation milestone removes wall-clock time from the
WASM transaction outcome. It does not activate the runtime for mainnet and does
not make the chain production-ready. Production activation and evidence for the
implemented deterministic call-depth, failed-transaction charging, and native
contract metering rules remain open alongside hard JIT memory bounds,
supported-target artifacts/vectors, CometBFT integration, transactional
storage, protocol upgrades, operations, and independent review.
