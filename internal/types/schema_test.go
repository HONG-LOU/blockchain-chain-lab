package types_test

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/types"
)

func TestChainIDCapacityAndCanonicalAlphabet(t *testing.T) {
	prefix := "Aa0-_."
	maximum := prefix + strings.Repeat("z", types.MaxChainIDBytes-len(prefix))
	if len(maximum) != types.MaxChainIDBytes {
		t.Fatalf("maximum chain id bytes = %d", len(maximum))
	}
	if err := types.ValidateChainID(maximum); err != nil {
		t.Fatalf("maximum chain id rejected: %v", err)
	}

	invalid := []struct {
		name    string
		chainID string
	}{
		{name: "129 bytes", chainID: strings.Repeat("a", types.MaxChainIDBytes+1)},
		{name: "space", chainID: "chain lab"},
		{name: "slash", chainID: "chain/lab"},
		{name: "colon", chainID: "chain:lab"},
		{name: "unicode", chainID: "chain-\u94fe"},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if err := types.ValidateChainID(test.chainID); err == nil {
				t.Fatalf("invalid chain id %q was accepted", test.chainID)
			}
		})
	}
}

func TestCanonicalSignatureRejectsEquivalentHighSForm(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	signature, err := chaincrypto.Sign(key, []byte("canonical-schema"))
	if err != nil {
		t.Fatal(err)
	}
	highS := equivalentHighSSignature(t, signature)
	if err := types.ValidateCanonicalSignature("signature", highS); err == nil || !strings.Contains(err.Error(), "low-s") {
		t.Fatalf("high-s validation error = %v", err)
	}

	tx := types.Transaction{
		ChainID:   "chainlab-local",
		Type:      types.TxTransfer,
		From:      chaincrypto.AddressFromPrivateKey(key),
		To:        "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		GasLimit:  21_000,
		GasPrice:  1,
		Signature: highS,
	}
	if _, err := types.EncodeRawTransaction(tx); err == nil || !strings.Contains(err.Error(), "low-s") {
		t.Fatalf("high-s raw encoding error = %v", err)
	}
}

func TestEthereumType2HashIsDerivedAndNamedChainsFailClosed(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	tx := types.Transaction{
		ChainID:              "chainlab-local",
		Type:                 types.TxTransfer,
		From:                 chaincrypto.AddressFromPrivateKey(key),
		To:                   "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		GasLimit:             21_000,
		MaxFeePerGas:         5,
		MaxPriorityFeePerGas: 1,
		SignatureKind:        types.SignatureKindEthereumType2,
	}
	digest, err := types.EthereumType2SigningDigest(tx)
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature, err = chaincrypto.SignDigest(key, digest)
	if err != nil {
		t.Fatal(err)
	}
	derived, err := types.EthereumType2TransactionHash(tx)
	if err != nil {
		t.Fatal(err)
	}
	tx.EthereumRawHash = "0x" + strings.Repeat("a", 64)
	if tx.Hash() != derived {
		t.Fatalf("transaction hash trusted supplied metadata: got %s want %s", tx.Hash(), derived)
	}

	for _, chainID := range []string{"private-a", "private-b", "0x01", "0x0"} {
		tx.ChainID = chainID
		if _, err := types.EthereumType2SigningDigest(tx); err == nil {
			t.Fatalf("named/non-canonical chain %q accepted", chainID)
		}
	}
}

func TestChainLabRawTransactionRequiresExactCanonicalJSONSchema(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	tx := types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     chaincrypto.AddressFromPrivateKey(key),
		To:       "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		GasLimit: 21_000,
		GasPrice: 1,
	}
	tx.Signature, err = chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := types.EncodeRawTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := hex.DecodeString(strings.TrimPrefix(raw, "0x"))
	if err != nil {
		t.Fatal(err)
	}

	withWhitespace := "0x" + hex.EncodeToString(append(encoded, ' '))
	if _, err := types.DecodeRawTransaction(withWhitespace); err == nil || !strings.Contains(err.Error(), "not canonically encoded") {
		t.Fatalf("whitespace raw error = %v", err)
	}

	var object map[string]any
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	object["unknown"] = true
	withUnknown, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := types.DecodeRawTransaction("0x" + hex.EncodeToString(withUnknown)); err == nil || !strings.Contains(err.Error(), "invalid raw transaction json") {
		t.Fatalf("unknown raw field error = %v", err)
	}
}

func equivalentHighSSignature(t *testing.T, signature string) string {
	t.Helper()
	raw, err := hex.DecodeString(strings.TrimPrefix(signature, "0x"))
	if err != nil || len(raw) != 65 {
		t.Fatalf("decode signature: %v", err)
	}
	order, ok := new(big.Int).SetString("fffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141", 16)
	if !ok {
		t.Fatal("parse secp256k1 order")
	}
	s := new(big.Int).SetBytes(raw[33:65])
	high := new(big.Int).Sub(order, s).FillBytes(make([]byte, 32))
	copy(raw[33:65], high)
	raw[0] = 27 + ((raw[0] - 27) ^ 1)
	return "0x" + hex.EncodeToString(raw)
}
