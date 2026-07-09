# Ethereum Type-2 Raw Transactions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Accept signed Ethereum EIP-1559 type-2 raw transfer transactions through ChainLab raw submission paths.

**Architecture:** Keep ChainLab-native raw JSON hex support intact. Add a chain-aware raw decoder that detects `0x02` typed Ethereum transactions, parses the RLP payload, translates supported type-2 transfers into ChainLab `transfer` transactions, stores Ethereum signature/hash metadata, and makes the executor verify the Ethereum signing digest during replay.

**Tech Stack:** Go, ChainLab internal secp256k1 compact signatures, Keccak, small local RLP helpers for the limited EIP-1559 payload shape, existing JSON-RPC and REST raw transaction endpoints.

---

### Task 1: Digest Signature Helpers

**Files:**
- Modify: `internal/crypto/crypto_test.go`
- Modify: `internal/crypto/crypto.go`

- [ ] **Step 1: Write failing digest signature test**

Add to `internal/crypto/crypto_test.go`:

```go
func TestSignAndVerifyDigest(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	digest := make([]byte, 32)
	for i := range digest {
		digest[i] = byte(i + 1)
	}
	signature, err := chaincrypto.SignDigest(key, digest)
	if err != nil {
		t.Fatal(err)
	}
	address := chaincrypto.AddressFromPrivateKey(key)
	if !chaincrypto.VerifyDigest(address, digest, signature) {
		t.Fatal("digest signature should verify")
	}
	digest[0] ^= 0xff
	if chaincrypto.VerifyDigest(address, digest, signature) {
		t.Fatal("tampered digest should not verify")
	}
}
```

- [ ] **Step 2: Run RED**

Run:

```powershell
go test -count=1 -run TestSignAndVerifyDigest ./internal/crypto
```

Expected: compile fail because `SignDigest` and `VerifyDigest` are undefined.

- [ ] **Step 3: Implement digest helpers**

In `internal/crypto/crypto.go`, add:

```go
func SignDigest(key PrivateKey, digest []byte) (string, error) {
	if len(digest) != 32 {
		return "", errors.New("digest must be 32 bytes")
	}
	signature := secpECDSA.SignCompact(key, digest, false)
	return "0x" + hex.EncodeToString(signature), nil
}

func VerifyDigest(address string, digest []byte, signatureHex string) bool {
	if len(digest) != 32 {
		return false
	}
	signatureBytes, err := hexToBytes(signatureHex)
	if err != nil || len(signatureBytes) != 65 {
		return false
	}
	pub, _, err := secpECDSA.RecoverCompact(signatureBytes, digest)
	if err != nil {
		return false
	}
	recovered := addressFromPublicKey(pub)
	return recovered == strings.ToLower(address)
}
```

Then update existing `Sign` and `Verify` to call these helpers after computing Keccak.

- [ ] **Step 4: Run GREEN**

Run:

```powershell
go test -count=1 -run TestSignAndVerifyDigest ./internal/crypto
go test -count=1 ./internal/crypto
```

Expected: pass.

---

### Task 2: Decode Ethereum Type-2 Raw Transfer

**Files:**
- Modify: `internal/types/types.go`
- Modify: `internal/types/raw.go`
- Modify: `internal/types/raw_test.go`

- [ ] **Step 1: Write failing type-2 decode test**

Add helpers and test to `internal/types/raw_test.go`. The helper builds the EIP-1559 unsigned payload, signs `keccak(0x02 || rlp(unsigned_payload))`, and appends y-parity, r, and s.

```go
func TestDecodeEthereumType2RawTransactionTranslatesTransfer(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	from := chaincrypto.AddressFromPrivateKey(key)
	to := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	raw, rawHash := signedEthereumType2TransferRaw(t, key, ethereumType2Transfer{
		ChainID:              31337,
		Nonce:                4,
		MaxPriorityFeePerGas: 2,
		MaxFeePerGas:         9,
		GasLimit:             21_000,
		To:                   to,
		Value:                123,
	})

	tx, err := types.DecodeRawTransactionForChain(raw, "chainlab-local")
	if err != nil {
		t.Fatal(err)
	}
	if tx.ChainID != "chainlab-local" || tx.Type != types.TxTransfer || tx.From != from || tx.To != to {
		t.Fatalf("translated tx = %+v", tx)
	}
	if tx.Nonce != 4 || tx.Value != 123 || tx.GasLimit != 21_000 || tx.MaxFeePerGas != 9 || tx.MaxPriorityFeePerGas != 2 {
		t.Fatalf("translated fee/value fields = %+v", tx)
	}
	if tx.SignatureKind != types.SignatureKindEthereumType2 || tx.EthereumRawHash != rawHash {
		t.Fatalf("signature metadata = kind %q hash %q", tx.SignatureKind, tx.EthereumRawHash)
	}
	if tx.Hash() != rawHash {
		t.Fatalf("tx hash = %s, want ethereum hash %s", tx.Hash(), rawHash)
	}
	digest, err := types.EthereumType2SigningDigest(tx)
	if err != nil {
		t.Fatal(err)
	}
	if !chaincrypto.VerifyDigest(tx.From, digest, tx.Signature) {
		t.Fatal("ethereum type2 signature should verify")
	}
}
```

The test helper can live in `raw_test.go` with local RLP encode functions named `testRLPBytes`, `testRLPUint`, `testRLPList`, and `testRLPAddress`.

- [ ] **Step 2: Run RED**

Run:

```powershell
go test -count=1 -run TestDecodeEthereumType2RawTransactionTranslatesTransfer ./internal/types
```

Expected: compile fail because `DecodeRawTransactionForChain`, `SignatureKindEthereumType2`, `EthereumRawHash`, and `EthereumType2SigningDigest` are missing.

- [ ] **Step 3: Add transaction metadata**

In `internal/types/types.go`:

```go
const SignatureKindEthereumType2 = "ethereum.type2"
```

Add fields to `Transaction`:

```go
SignatureKind  string `json:"signature_kind,omitempty"`
EthereumRawHash string `json:"ethereum_raw_hash,omitempty"`
```

Update `Hash()`:

```go
func (tx Transaction) Hash() string {
	if tx.SignatureKind == SignatureKindEthereumType2 && tx.EthereumRawHash != "" {
		return tx.EthereumRawHash
	}
	return hash.MustHex(tx)
}
```

- [ ] **Step 4: Implement chain-aware raw decoder and type-2 helpers**

In `internal/types/raw.go`:

```go
func DecodeRawTransactionForChain(raw string, chainID string) (Transaction, error) {
	if !strings.HasPrefix(raw, "0x") {
		return Transaction{}, fmt.Errorf("raw transaction must be 0x-prefixed hex")
	}
	encoded, err := hex.DecodeString(strings.TrimPrefix(raw, "0x"))
	if err != nil {
		return Transaction{}, fmt.Errorf("invalid raw transaction hex")
	}
	if len(encoded) > 0 && encoded[0] == 0x02 {
		return decodeEthereumType2RawTransaction(encoded, chainID)
	}
	return decodeChainLabRawTransactionBytes(encoded)
}

func DecodeRawTransaction(raw string) (Transaction, error) {
	return DecodeRawTransactionForChain(raw, "")
}
```

Move the existing JSON decode body into `decodeChainLabRawTransactionBytes`.

Implement unexported helpers in the same file:

```text
decodeEthereumType2RawTransaction
ethereumInternalChainID
ethereumChainNumber
EthereumType2SigningDigest
ethereumType2SigningPayload
ethereumCompactSignature
rlpDecodeItem
rlpEncodeList
rlpEncodeBytes
rlpEncodeUint64
rlpUint64
rlpAddress
```

The decoder must reject:

```text
wrong item count
wrong chain id for provided ChainLab chain
contract creation
non-empty data
non-empty access list
uint values that do not fit uint64
invalid y parity
invalid r/s length
```

- [ ] **Step 5: Run GREEN**

Run:

```powershell
go test -count=1 -run "TestDecodeEthereumType2RawTransactionTranslatesTransfer|TestRawTransactionRoundTripsSignedTransaction|TestDecodeRawTransactionRejectsInvalidInput" ./internal/types
go test -count=1 ./internal/types
```

Expected: pass.

---

### Task 3: Verify Ethereum Type-2 Signatures During Execution

**Files:**
- Modify: `internal/core/executor_test.go`
- Modify: `internal/core/executor.go`

- [ ] **Step 1: Write failing executor replay test**

Add to `internal/core/executor_test.go`:

```go
func TestExecutorAcceptsEthereumType2TransferSignature(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store := state.NewStore()
	store.SetBalance(alice, 1_000_000)
	tx := ethereumType2SignedTransferForExecutorTest(t, key, bob)

	executor := core.NewExecutor("chainlab-local", alice, nil)
	receipt, err := executor.ExecuteWithContext(store, tx, core.ExecutionContext{BlockHeight: 1, BaseFeePerGas: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Success || store.GetAccount(bob).Balance != 100 {
		t.Fatalf("receipt = %+v bob = %+v", receipt, store.GetAccount(bob))
	}
}
```

Use local test helper logic equivalent to the raw decoder test helper to produce a translated type-2 transaction.

- [ ] **Step 2: Run RED**

Run:

```powershell
go test -count=1 -run TestExecutorAcceptsEthereumType2TransferSignature ./internal/core
```

Expected: fail with `invalid transaction signature`.

- [ ] **Step 3: Implement executor verification branch**

In `internal/core/executor.go`, update `validateTransactionAuthorization`:

```go
if tx.SignatureKind == types.SignatureKindEthereumType2 {
	return validateEthereumType2Authorization(tx)
}
```

Add:

```go
func validateEthereumType2Authorization(tx types.Transaction) error {
	if strings.TrimSpace(tx.Signer) != "" || len(tx.Authorizations) > 0 || strings.TrimSpace(tx.Paymaster) != "" {
		return errors.New("ethereum type2 transactions cannot use ChainLab signer, multisig, or paymaster fields")
	}
	digest, err := types.EthereumType2SigningDigest(tx)
	if err != nil {
		return err
	}
	if !chaincrypto.VerifyDigest(tx.From, digest, tx.Signature) {
		return errors.New("invalid ethereum type2 transaction signature")
	}
	return nil
}
```

- [ ] **Step 4: Run GREEN**

Run:

```powershell
go test -count=1 -run TestExecutorAcceptsEthereumType2TransferSignature ./internal/core
go test -count=1 ./internal/core
```

Expected: pass.

---

### Task 4: Wire RPC and REST Raw Submission

**Files:**
- Modify: `internal/rpc/server.go`
- Modify: `internal/rpc/server_test.go`

- [ ] **Step 1: Write failing RPC integration test**

Add to `internal/rpc/server_test.go`:

```go
func TestRPCSendEthereumType2RawTransactionSubmitsTransfer(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	raw, rawHash := signedEthereumType2TransferRaw(t, key, rpcEthereumType2Transfer{
		ChainID:              31337,
		Nonce:                0,
		MaxPriorityFeePerGas: 1,
		MaxFeePerGas:         5,
		GasLimit:             21_000,
		To:                   bob,
		Value:                100,
	})
	result := callRPC(t, server.URL, "eth_sendRawTransaction", []any{raw})
	if result != rawHash {
		t.Fatalf("raw tx result = %v, want %s", result, rawHash)
	}
	pool := n.TxPool()
	if pool.PendingCount != 1 || pool.Pending[0].Hash() != rawHash || pool.Pending[0].SignatureKind != types.SignatureKindEthereumType2 {
		t.Fatalf("txpool = %+v", pool)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	if got := n.Account(bob).Balance; got != 100 {
		t.Fatalf("bob balance = %d", got)
	}
	if _, ok := n.Transaction(rawHash); !ok {
		t.Fatalf("ethereum raw tx hash %s not indexed", rawHash)
	}
}
```

- [ ] **Step 2: Run RED**

Run:

```powershell
go test -count=1 -run TestRPCSendEthereumType2RawTransactionSubmitsTransfer ./internal/rpc
```

Expected: fail because `eth_sendRawTransaction` still uses the native-only decoder.

- [ ] **Step 3: Wire chain-aware decoder**

In `internal/rpc/server.go`:

```go
func (s *Server) decodeRawTransaction(r *http.Request) (types.Transaction, error)
```

or keep the helper as a function accepting chain id:

```go
func decodeRawTransaction(r *http.Request, chainID string) (types.Transaction, error)
```

Use:

```go
types.DecodeRawTransactionForChain(request.Raw, s.node.ChainID())
types.DecodeRawTransactionForChain(raw, n.ChainID())
```

for `POST /tx/raw` and `eth_sendRawTransaction`.

- [ ] **Step 4: Run GREEN**

Run:

```powershell
go test -count=1 -run "TestRPCSendEthereumType2RawTransactionSubmitsTransfer|TestRPCSendRawTransactionSubmitsSignedTx|TestRESTRawTransactionSubmitsSignedTx" ./internal/rpc
go test -count=1 ./internal/rpc
```

Expected: pass.

---

### Task 5: Docs and Full Verification

**Files:**
- Modify: `README.md`
- Modify: `docs/current-blockchain-tech-roadmap.md`
- Modify: `docs/superpowers/specs/2026-07-09-own-chain-design.md`

- [ ] **Step 1: Update docs**

Change text that says raw transactions are only ChainLab JSON or that Ethereum type-2 raw compatibility is future work. New wording must say:

```text
ChainLab accepts its native signed transaction JSON raw envelope and a limited Ethereum EIP-1559 type-2 raw transfer subset. Type-2 support covers value transfers with empty calldata and empty access lists; general EVM calldata, contract creation, legacy RLP, and type-1 access-list transactions remain later work.
```

- [ ] **Step 2: Run full verification**

Run:

```powershell
go test -count=1 ./internal/crypto
go test -count=1 ./internal/types
go test -count=1 ./internal/core
go test -count=1 ./internal/rpc
go test -count=1 ./...
go run ./cmd/chainlab demo
go build -o $env:TEMP\chainlab-eth-type2-raw-verify.exe ./cmd/chainlab
git diff --check
```

Expected: all test/demo/build commands exit 0. `git diff --check` may print LF/CRLF warnings, but must exit 0.

- [ ] **Step 3: Commit implementation**

Run:

```powershell
git add README.md docs/current-blockchain-tech-roadmap.md docs/superpowers/specs/2026-07-09-own-chain-design.md internal/crypto/crypto.go internal/crypto/crypto_test.go internal/types/types.go internal/types/raw.go internal/types/raw_test.go internal/core/executor.go internal/core/executor_test.go internal/rpc/server.go internal/rpc/server_test.go
git diff --cached --check
git commit -m "feat: accept ethereum type2 raw transfers"
```

Expected: one implementation commit after verification.
