package types_test

import (
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/types"
)

func TestRawTransactionRoundTripsSignedTransaction(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	tx := types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Nonce:    7,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	}
	sig, err := chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = sig

	raw, err := types.EncodeRawTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= 2 || raw[:2] != "0x" {
		t.Fatalf("raw tx = %q", raw)
	}
	decoded, err := types.DecodeRawTransaction(raw)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Hash() != tx.Hash() {
		t.Fatalf("decoded hash = %s, want %s", decoded.Hash(), tx.Hash())
	}
	if !chaincrypto.Verify(decoded.From, decoded.SigningBytes(), decoded.Signature) {
		t.Fatal("decoded signature should verify")
	}
}

func TestDecodeRawTransactionRejectsInvalidInput(t *testing.T) {
	if _, err := types.DecodeRawTransaction("not-hex"); err == nil {
		t.Fatal("missing hex prefix should fail")
	}
	if _, err := types.DecodeRawTransaction("0x123"); err == nil {
		t.Fatal("invalid hex should fail")
	}
	if _, err := types.DecodeRawTransaction("0x7b7d"); err == nil {
		t.Fatal("missing transaction fields should fail")
	}
}
