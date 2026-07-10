package crypto

import (
	"encoding/hex"
	"errors"
	"strings"

	"chainlab/internal/hash"

	secp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	secpECDSA "github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
)

var (
	ErrInvalidAddress = errors.New("address must be a 20-byte hex address")
	ErrZeroAddress    = errors.New("address must not be the zero address")
)

type PrivateKey = *secp.PrivateKey

func GenerateKey() (PrivateKey, error) {
	return secp.GeneratePrivateKey()
}

func PrivateKeyToHex(key PrivateKey) string {
	return "0x" + hex.EncodeToString(key.Serialize())
}

func PrivateKeyFromHex(encoded string) (PrivateKey, error) {
	keyBytes, err := hex.DecodeString(strings.TrimPrefix(encoded, "0x"))
	if err != nil {
		return nil, err
	}
	if len(keyBytes) != secp.PrivKeyBytesLen {
		return nil, errors.New("private key must be 32 bytes")
	}
	return secp.PrivKeyFromBytes(keyBytes), nil
}

func AddressFromPrivateKey(key PrivateKey) string {
	return addressFromPublicKey(key.PubKey())
}

func NormalizeAddress(encoded string) (string, error) {
	address := strings.ToLower(strings.TrimSpace(encoded))
	if len(address) != 42 || !strings.HasPrefix(address, "0x") {
		return "", ErrInvalidAddress
	}
	raw, err := hex.DecodeString(address[2:])
	if err != nil || len(raw) != 20 {
		return "", ErrInvalidAddress
	}
	for _, value := range raw {
		if value != 0 {
			return address, nil
		}
	}
	return "", ErrZeroAddress
}

func Sign(key PrivateKey, message []byte) (string, error) {
	digest := hash.Keccak(message)
	return SignDigest(key, digest)
}

func SignDigest(key PrivateKey, digest []byte) (string, error) {
	if len(digest) != 32 {
		return "", errors.New("digest must be 32 bytes")
	}
	signature := secpECDSA.SignCompact(key, digest, false)
	return "0x" + hex.EncodeToString(signature), nil
}

func Verify(address string, message []byte, signatureHex string) bool {
	return VerifyDigest(address, hash.Keccak(message), signatureHex)
}

func VerifyDigest(address string, digest []byte, signatureHex string) bool {
	recovered, err := RecoverDigestAddress(digest, signatureHex)
	if err != nil {
		return false
	}
	return recovered == strings.ToLower(address)
}

func RecoverDigestAddress(digest []byte, signatureHex string) (string, error) {
	if len(digest) != 32 {
		return "", errors.New("digest must be 32 bytes")
	}
	signatureBytes, err := hexToBytes(signatureHex)
	if err != nil || len(signatureBytes) != 65 {
		return "", errors.New("signature must be 65 bytes")
	}
	pub, _, err := secpECDSA.RecoverCompact(signatureBytes, digest)
	if err != nil {
		return "", err
	}
	return addressFromPublicKey(pub), nil
}

func hexToBytes(encoded string) ([]byte, error) {
	return hex.DecodeString(strings.TrimPrefix(encoded, "0x"))
}

func addressFromPublicKey(pub *secp.PublicKey) string {
	uncompressed := pub.SerializeUncompressed()
	digest := hash.Keccak(uncompressed[1:])
	return "0x" + hex.EncodeToString(digest[len(digest)-20:])
}
