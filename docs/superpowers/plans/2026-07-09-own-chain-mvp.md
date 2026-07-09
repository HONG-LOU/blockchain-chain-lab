# Own Chain MVP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the first runnable local blockchain release with signed transactions, deterministic state, PoA block production, native contracts, staking/governance proposal modules, RPC, CLI, and tests.

**Architecture:** Implement a Go monorepo with small internal packages. Consensus-critical types live in `internal/types`; state transitions live in `internal/core`; signing lives in `internal/crypto`; contracts and modules are isolated behind explicit interfaces; the node package composes mempool, block production, and RPC operations.

**Tech Stack:** Go 1.25, `github.com/ethereum/go-ethereum/crypto` for secp256k1, Go standard `net/http`, Go test suite.

---

## File Structure

- Create `go.mod`: module definition and dependencies.
- Create `cmd/chainlab/main.go`: CLI entrypoint.
- Create `internal/crypto/keys.go`: keys, addresses, signing, verification.
- Create `internal/types/types.go`: accounts, transactions, receipts, blocks, genesis, validators.
- Create `internal/hash/hash.go`: canonical encoding and hash helpers.
- Create `internal/state/store.go`: in-memory state and deterministic roots.
- Create `internal/contracts/runtime.go`: contract runtime interface and registry.
- Create `internal/contracts/counter.go`: built-in counter contract.
- Create `internal/contracts/token.go`: built-in token contract.
- Create `internal/core/executor.go`: transaction state transition function.
- Create `internal/consensus/poa.go`: local proof-of-authority block validation.
- Create `internal/node/node.go`: chain state, mempool, block production.
- Create `internal/rpc/server.go`: HTTP RPC server.
- Create `examples/demo.go`: reusable demo scenario.
- Create tests next to each package: `*_test.go`.

## Task 1: Crypto And Canonical Hashing

**Files:**
- Create: `go.mod`
- Create: `internal/crypto/keys_test.go`
- Create: `internal/crypto/keys.go`
- Create: `internal/hash/hash_test.go`
- Create: `internal/hash/hash.go`

- [ ] **Step 1: Write failing crypto tests**

```go
func TestSignVerifyAndAddress(t *testing.T) {
	key, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("chainlab")
	sig, err := Sign(key, msg)
	if err != nil {
		t.Fatal(err)
	}
	addr := AddressFromPrivateKey(key)
	if !Verify(addr, msg, sig) {
		t.Fatal("signature should verify")
	}
	if Verify(addr, []byte("tampered"), sig) {
		t.Fatal("tampered payload should not verify")
	}
	if len(addr) != 42 {
		t.Fatalf("address should be 0x plus 40 hex chars, got %q", addr)
	}
}
```

- [ ] **Step 2: Run failing tests**

Run: `go test ./internal/crypto ./internal/hash`

Expected: FAIL because packages and functions do not exist.

- [ ] **Step 3: Implement minimal crypto and hash helpers**

Use go-ethereum crypto for secp256k1 keys, address derivation, Keccak256, signing, and public-key recovery.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/crypto ./internal/hash`

Expected: PASS.

## Task 2: Types And State Store

**Files:**
- Create: `internal/types/types.go`
- Create: `internal/state/store_test.go`
- Create: `internal/state/store.go`

- [ ] **Step 1: Write failing state tests**

Cover account creation, balances, nonce increments, storage isolation, clone behavior, and deterministic state root regardless of insertion order.

- [ ] **Step 2: Run failing tests**

Run: `go test ./internal/types ./internal/state`

Expected: FAIL because packages and methods do not exist.

- [ ] **Step 3: Implement account and state store**

Implement account upsert, balance movement, storage read/write, clone, and deterministic root computation.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/types ./internal/state`

Expected: PASS.

## Task 3: Transaction Executor

**Files:**
- Create: `internal/core/executor_test.go`
- Create: `internal/core/executor.go`

- [ ] **Step 1: Write failing executor tests**

Cover transfer success, invalid signature, bad nonce, insufficient funds, gas fee charging, EIP-1559-style base fee/tip accounting, stake, unstake, proposal submission, voting, and proposal execution.

- [ ] **Step 2: Run failing tests**

Run: `go test ./internal/core`

Expected: FAIL because executor does not exist.

- [ ] **Step 3: Implement executor**

Validate signatures and nonces, charge legacy or EIP-1559-style gas fees, burn base fees, reward priority fees, mutate balances, staking records, governance proposals, and governance parameters, and return receipts.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/core`

Expected: PASS.

## Task 4: Native Contract Runtime

**Files:**
- Create: `internal/contracts/runtime_test.go`
- Create: `internal/contracts/runtime.go`
- Create: `internal/contracts/counter.go`
- Create: `internal/contracts/token.go`
- Modify: `internal/core/executor.go`
- Modify: `internal/core/executor_test.go`

- [ ] **Step 1: Write failing contract tests**

Cover deploying `counter.v1`, calling `increment`, reading contract storage, deploying `token.v1`, minting, transferring, and token balance reads.

- [ ] **Step 2: Run failing tests**

Run: `go test ./internal/contracts ./internal/core`

Expected: FAIL because contract runtime does not exist.

- [ ] **Step 3: Implement runtime and contracts**

Add a registry, context, deployment address derivation, storage helpers, counter contract, and token contract.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/contracts ./internal/core`

Expected: PASS.

## Task 5: PoA Blocks And Node

**Files:**
- Create: `internal/consensus/poa_test.go`
- Create: `internal/consensus/poa.go`
- Create: `internal/node/node_test.go`
- Create: `internal/node/node.go`

- [ ] **Step 1: Write failing consensus and node tests**

Cover genesis creation, mempool acceptance, block production, root validation, unauthorized proposer rejection, parent mismatch rejection, and state commit.

- [ ] **Step 2: Run failing tests**

Run: `go test ./internal/consensus ./internal/node`

Expected: FAIL because packages do not exist.

- [ ] **Step 3: Implement PoA validator and node**

Compose executor, state, mempool, blocks, proposer signatures, and chain storage.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/consensus ./internal/node`

Expected: PASS.

## Task 6: RPC, CLI, And Demo

**Files:**
- Create: `internal/rpc/server_test.go`
- Create: `internal/rpc/server.go`
- Create: `examples/demo_test.go`
- Create: `examples/demo.go`
- Create: `cmd/chainlab/main.go`

- [ ] **Step 1: Write failing RPC and demo tests**

Cover `/health`, `/chain/head`, `/account/{address}`, `POST /tx`, JSON-RPC `chain_head`, and full demo execution.

- [ ] **Step 2: Run failing tests**

Run: `go test ./internal/rpc ./examples`

Expected: FAIL because RPC and demo do not exist.

- [ ] **Step 3: Implement RPC, CLI, and demo**

Expose local node operations over HTTP and provide CLI commands for keygen, init, demo, and node startup.

- [ ] **Step 4: Run all tests and CLI demo**

Run: `go test ./...`

Expected: PASS.

Run: `go run ./cmd/chainlab demo`

Expected: output includes produced blocks, successful transfer, counter increment, token transfer, stake, governance vote, and an executed parameter change.

## Self-Review

- The plan covers every phase 1 feature in the design.
- The plan has no placeholder tasks; future production features are explicitly out of phase 1.
- Package names and APIs are intentionally small so tests can drive exact signatures.
