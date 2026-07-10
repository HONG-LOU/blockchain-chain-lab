package crypto_test

import (
	"errors"
	"testing"

	chaincrypto "chainlab/internal/crypto"
)

func TestNormalizeAddress(t *testing.T) {
	address, err := chaincrypto.NormalizeAddress(" 0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA ")
	if err != nil {
		t.Fatal(err)
	}
	if address != "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("normalized address = %q", address)
	}
	if _, err := chaincrypto.NormalizeAddress("0x1234"); !errors.Is(err, chaincrypto.ErrInvalidAddress) {
		t.Fatalf("short address error = %v", err)
	}
	if _, err := chaincrypto.NormalizeAddress("0x0000000000000000000000000000000000000000"); !errors.Is(err, chaincrypto.ErrZeroAddress) {
		t.Fatalf("zero address error = %v", err)
	}
}

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
