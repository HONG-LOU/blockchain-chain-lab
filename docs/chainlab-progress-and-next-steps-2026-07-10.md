# ChainLab Progress And Next Steps

Date: 2026-07-10
Branch: `feature/own-chain-mvp`
Project path: `D:\blockchain-chain-lab`

## Current Status

ChainLab is currently about 70-75% complete as a local, fully verifiable blockchain lab that demonstrates most common mainstream-chain capabilities in small, honest slices.

As a production mainnet candidate, it is only about 25-35% complete. The missing production work is still large: real P2P gossip, production BFT consensus, durable KV storage with pruning/archive modes, stronger security testing, monitoring, audits, bridge strategy, economic design, and ecosystem tooling.

The repository is clean at the time this document was written. The latest ChainLab commit before this document is:

```text
82bf4e8 docs: design account social recovery
```

The ChainLab repository currently has no configured remote, so code commits have not been pushed from this repository.

## Completed Capabilities

### Core Chain

- Go monorepo with deterministic state transition logic.
- Account state with balance, nonce, code id, delegated code id, and storage.
- Blocks with transaction root, receipt root, state root, gas limit, gas used, base fee, proposer signature, and optional finality certificate.
- Receipts with success/error, gas, fee fields, events, contract address, proposal id, and code id.
- Genesis generation and local node startup.
- Persistent chain snapshot and transaction index rebuild.

### Consensus And Validator Lifecycle

- PoA block production.
- Multi-validator round-robin proposer schedule.
- Dynamic validator join, leave, and slash transactions.
- Fork-choice/reorg support for longer imported branches.
- BFT-style finality certificates over PoA blocks.
- HTTP peer relay for finality votes.
- Double-vote evidence capture and automatic local evidence-to-slashing transactions.

### Transactions, Fees, And Txpool

- Signed native ChainLab transactions.
- Native raw transaction encoding and submission.
- Limited Ethereum EIP-1559 type-2 raw value transfers.
- EIP-1559-style local fee market with base fee, priority fee, and `eth_feeHistory`.
- Pending and queued txpool.
- Future-nonce queued transactions with automatic promotion.
- Geth-style 10% same-sender/same-nonce replacement for pending or queued transactions.
- Pending nonce support through `eth_getTransactionCount(..., "pending")`.

### Contracts And Execution

- Native contract runtime.
- Built-in `counter.v1`, `token.v1`, `account.v1`, and `multisig.v1`.
- Contract deploy and call transactions.
- Read-only contract calls through `eth_call`.
- Minimal ABI-shaped return values for `uint256`, `address`, and dynamic `string`.
- Limited Solidity-style calldata selector parsing for `get()`, `symbol()`, `owner()`, and `balanceOf(address)`.
- WASM contract runtime using wazero.
- On-chain WASM code upload.
- Deterministic WASM upload/resource gas slices.
- Host-side WASM runtime timeout protection.

### Account Abstraction

- Native paymaster-sponsored gas.
- Atomic batch transactions with transfer and contract-call operations.
- `account.v1` single-owner smart accounts.
- `multisig.v1` threshold accounts.
- Native EIP-7702-style delegated EOAs using `set_code` and `DelegatedCodeID=account.v1`.
- Transfer-scoped session keys for `account.v1` and delegated EOAs.
- Contract-call session keys limited to one contract address and one method.

### RPC, Explorer, And Indexing

- REST APIs for tx submission, chain head, txpool, validators, finality, and explorer pages.
- EVM-style JSON-RPC subset including account, block, transaction, receipt, logs, fee, txpool, debug, and probe methods.
- JSON-RPC batch request support.
- `eth_getCode` and `eth_getStorageAt` projections over ChainLab code/storage.
- `eth_getBlockByHash`, transaction count, and block-indexed transaction reads.
- `eth_getBlockReceipts`.
- `debug_traceTransaction` receipt-backed trace summaries.
- Node-local canonical event index used by logs, filters, and explorer events.
- Polling filters for logs, new blocks, and pending transactions.
- WebSocket `eth_subscribe("newHeads")`.
- WebSocket `eth_subscribe("logs")`.
- WebSocket `eth_subscribe("newPendingTransactions")`.
- Local block explorer overview and detail pages.

### Governance

- Proposal submit, vote, and execute transactions.
- Parameter-change proposal lifecycle.
- Stake-weighted voting.

## Most Recent Completed Feature

The latest implemented feature is contract-call session keys:

- Design commit: `9fa7aba`
- Plan commit: `d7a9d83`
- Core implementation commit: `b240d75`
- CLI implementation commit: `de3eb7b`
- Documentation commit: `7ade620`

What it added:

- `account.session_key` can now install `call_to` and `call_method` policy fields.
- `account.v1` and delegated EOAs can authorize a session key to call exactly one contract method.
- Session-key call authorization validates target contract, method, expiry, and signature.
- Successful calls emit `account.session_key_used` with `type=call`.
- `tx session-key` supports `--call-to` and `--call-method`.
- `tx call` supports `--from`, so a session key can sign for an account/delegated EOA.

Final verification for that feature exited 0:

```powershell
go test -count=1 ./internal/core
go test -count=1 ./cmd/chainlab
go test -count=1 ./...
go run ./cmd/chainlab demo
go build -o $env:TEMP\chainlab-session-key-call-final.exe ./cmd/chainlab
git diff --check
git diff --cached --check
```

## Current In-Progress Design

Social recovery has been designed but not implemented.

Committed design:

```text
82bf4e8 docs: design account social recovery
```

Design file:

```text
docs/superpowers/specs/2026-07-10-account-social-recovery-design.md
```

Scope of the design:

- `account.v1` and delegated EOA owner recovery.
- Owner configures guardians, threshold, and delay.
- Guardians approve a new owner.
- A guardian executes recovery after threshold and delay.
- Current owner can cancel pending recovery or clear recovery config.

Explicit non-goals:

- No ERC-4337 EntryPoint.
- No ERC-7579 module format.
- No Safe module compatibility.
- No passkey/email/WebAuthn recovery.
- No guardian weights.
- No recovery for `multisig.v1` in this milestone.

No implementation plan or production code for social recovery has been written yet.

## Next Immediate Steps

When the goal restarts, continue from social recovery.

1. Write the implementation plan:
   - Path: `docs/superpowers/plans/2026-07-10-account-social-recovery.md`
   - Cover core tests, transaction type, executor logic, CLI command, docs, and verification.
   - Commit as `docs: plan account social recovery`.

2. Implement with TDD:
   - Add RED core tests for configure, approve, execute, cancel, unauthorized owner, unauthorized guardian, threshold, delay, and owner rotation.
   - Add `TxAccountRecovery` to `internal/types/types.go`.
   - Add recovery gas estimate in `internal/core/executor.go`.
   - Add owner/guardian authorization and recovery state transitions in `internal/core/executor.go`.
   - Add CLI command `tx recovery` in `cmd/chainlab/main.go`.
   - Add CLI E2E test in `cmd/chainlab/main_test.go`.
   - Update README and roadmap.

3. Verify social recovery:

```powershell
go test -count=1 ./internal/core
go test -count=1 ./cmd/chainlab
go test -count=1 ./...
go run ./cmd/chainlab demo
go build -o $env:TEMP\chainlab-account-recovery-verify.exe ./cmd/chainlab
git diff --check
git diff --cached --check
```

4. Commit in small commits:
   - `docs: plan account social recovery`
   - `feat: add account social recovery`
   - `feat: add account recovery cli`
   - `docs: document account social recovery`

5. After implementation, write Obsidian diary/project record and push `D:\code\obsidian-note`.

## Remaining Larger Roadmap

### Account Abstraction

- Social recovery implementation.
- Batch session-key policies.
- Parameter-level call policies.
- Policy-based paymasters.
- ERC-4337-like `UserOperation` and EntryPoint simulation.
- ERC-7579-style modular account compatibility slice.

### Execution Compatibility

- General ABI registry and calldata decoder.
- Contract creation for Ethereum raw transactions.
- Legacy and type-1 Ethereum raw transaction decoding.
- More complete EVM compatibility decision: either embed an EVM engine or keep ChainLab-native execution honest.

### Storage And Node Operations

- Replace JSON snapshot persistence with a durable KV store such as Pebble/RocksDB/LevelDB.
- Add pruning and archive modes.
- Add crash-recovery tests.
- Add node metrics and health probes beyond current dev endpoints.
- Add txpool capacity, eviction, journaling, and local transaction policy.

### Networking And Consensus

- Real peer discovery and gossip instead of configured HTTP peers.
- Peer scoring, bans, reconnect logic, and propagation tests.
- Production-grade BFT rounds/timeouts/locking, or explicitly migrate to a proven consensus stack.
- Stronger validator key management and slashing economics.

### Security And Quality

- Fuzz tests for transaction decoding, state transition, RPC input parsing, and reorg replay.
- Property tests for roots, replay determinism, txpool invariants, and fee accounting.
- More adversarial tests around malformed WASM, gas metering, and storage growth.
- Threat model for owner keys, paymasters, governance, upgrades, and bridges.

### Product Ecosystem

- Wallet connection story.
- Token standards beyond the current native token demo.
- NFT/RWA/oracle primitives if product direction requires them.
- Bridge strategy only after security model is clear.
- Deployment/ops runbooks.

## Completion Audit Needed Before Calling The Goal Done

Do not mark the long-running goal complete until a completion audit proves:

- `go test -count=1 ./...` passes.
- Demo passes.
- Build passes.
- Docs match implemented behavior.
- The roadmap clearly separates implemented features from non-goals.
- Mainstream-chain capability checklist is reviewed item by item.
- Any missing production-grade items are either implemented or explicitly accepted as out of scope for ChainLab as a learning/prototype chain.
