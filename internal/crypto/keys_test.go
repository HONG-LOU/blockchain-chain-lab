package crypto_test

import (
	"testing"

	chaincrypto "chainlab/internal/crypto"
)

func TestSignVerifyAndAddress(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	msg := []byte("chainlab")
	sig, err := chaincrypto.Sign(key, msg)
	if err != nil {
		t.Fatal(err)
	}

	addr := chaincrypto.AddressFromPrivateKey(key)
	if !chaincrypto.Verify(addr, msg, sig) {
		t.Fatal("signature should verify")
	}
	if chaincrypto.Verify(addr, []byte("tampered"), sig) {
		t.Fatal("tampered payload should not verify")
	}
	if len(addr) != 42 {
		t.Fatalf("address should be 0x plus 40 hex chars, got %q", addr)
	}
}

func TestPrivateKeyHexRoundTrip(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	encoded := chaincrypto.PrivateKeyToHex(key)
	decoded, err := chaincrypto.PrivateKeyFromHex(encoded)
	if err != nil {
		t.Fatal(err)
	}

	if chaincrypto.AddressFromPrivateKey(decoded) != chaincrypto.AddressFromPrivateKey(key) {
		t.Fatal("decoded key should derive the same address")
	}
}
