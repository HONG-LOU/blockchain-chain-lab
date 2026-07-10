package types_test

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/hash"
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

type ethereumType2Transfer struct {
	ChainID              uint64
	Nonce                uint64
	MaxPriorityFeePerGas uint64
	MaxFeePerGas         uint64
	GasLimit             uint64
	To                   string
	Value                uint64
}

func signedEthereumType2TransferRaw(t *testing.T, key chaincrypto.PrivateKey, tx ethereumType2Transfer) (string, string) {
	t.Helper()
	unsigned := testRLPList(
		testRLPUint(tx.ChainID),
		testRLPUint(tx.Nonce),
		testRLPUint(tx.MaxPriorityFeePerGas),
		testRLPUint(tx.MaxFeePerGas),
		testRLPUint(tx.GasLimit),
		testRLPAddress(t, tx.To),
		testRLPUint(tx.Value),
		testRLPBytes(nil),
		testRLPList(),
	)
	digest := hash.Keccak(append([]byte{0x02}, unsigned...))
	signature, err := chaincrypto.SignDigest(key, digest)
	if err != nil {
		t.Fatal(err)
	}
	signatureBytes, err := hex.DecodeString(strings.TrimPrefix(signature, "0x"))
	if err != nil {
		t.Fatal(err)
	}
	if len(signatureBytes) != 65 || signatureBytes[0] < 27 {
		t.Fatalf("compact signature = %x", signatureBytes)
	}
	yParity := uint64(signatureBytes[0] - 27)
	if yParity > 1 {
		t.Fatalf("unexpected y parity %d from compact header %d", yParity, signatureBytes[0])
	}
	r := bytes.TrimLeft(signatureBytes[1:33], "\x00")
	s := bytes.TrimLeft(signatureBytes[33:65], "\x00")
	if len(r) == 0 || len(s) == 0 {
		t.Fatal("signature contains a zero scalar")
	}
	signed := testRLPList(
		testRLPUint(tx.ChainID),
		testRLPUint(tx.Nonce),
		testRLPUint(tx.MaxPriorityFeePerGas),
		testRLPUint(tx.MaxFeePerGas),
		testRLPUint(tx.GasLimit),
		testRLPAddress(t, tx.To),
		testRLPUint(tx.Value),
		testRLPBytes(nil),
		testRLPList(),
		testRLPUint(yParity),
		testRLPBytes(r),
		testRLPBytes(s),
	)
	raw := append([]byte{0x02}, signed...)
	return "0x" + hex.EncodeToString(raw), hash.KeccakHex(raw)
}

func testRLPAddress(t *testing.T, address string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(strings.TrimPrefix(address, "0x"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) != 20 {
		t.Fatalf("address length = %d", len(raw))
	}
	return testRLPBytes(raw)
}

func testRLPUint(value uint64) []byte {
	if value == 0 {
		return testRLPBytes(nil)
	}
	var raw [8]byte
	i := len(raw)
	for value > 0 {
		i--
		raw[i] = byte(value)
		value >>= 8
	}
	return testRLPBytes(raw[i:])
}

func testRLPList(items ...[]byte) []byte {
	payloadLen := 0
	for _, item := range items {
		payloadLen += len(item)
	}
	output := testRLPLength(0xc0, payloadLen)
	for _, item := range items {
		output = append(output, item...)
	}
	return output
}

func testRLPBytes(raw []byte) []byte {
	if len(raw) == 1 && raw[0] < 0x80 {
		return append([]byte(nil), raw...)
	}
	output := testRLPLength(0x80, len(raw))
	output = append(output, raw...)
	return output
}

func testRLPLength(offset byte, length int) []byte {
	if length <= 55 {
		return []byte{offset + byte(length)}
	}
	var raw [8]byte
	i := len(raw)
	value := length
	for value > 0 {
		i--
		raw[i] = byte(value)
		value >>= 8
	}
	output := []byte{offset + 55 + byte(len(raw)-i)}
	output = append(output, raw[i:]...)
	return output
}
