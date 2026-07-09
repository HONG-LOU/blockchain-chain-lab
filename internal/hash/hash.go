package hash

import (
	"encoding/json"

	"golang.org/x/crypto/sha3"
)

func CanonicalBytes(value any) ([]byte, error) {
	return json.Marshal(value)
}

func MustCanonicalBytes(value any) []byte {
	encoded, err := CanonicalBytes(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func Hex(value any) (string, error) {
	encoded, err := CanonicalBytes(value)
	if err != nil {
		return "", err
	}
	return KeccakHex(encoded), nil
}

func MustHex(value any) string {
	digest, err := Hex(value)
	if err != nil {
		panic(err)
	}
	return digest
}

func KeccakHex(data []byte) string {
	digest := Keccak(data)
	const hexChars = "0123456789abcdef"
	output := make([]byte, 2+len(digest)*2)
	output[0] = '0'
	output[1] = 'x'
	for i, b := range digest {
		output[2+i*2] = hexChars[b>>4]
		output[3+i*2] = hexChars[b&0x0f]
	}
	return string(output)
}

func Keccak(data []byte) []byte {
	hasher := sha3.NewLegacyKeccak256()
	_, _ = hasher.Write(data)
	return hasher.Sum(nil)
}
