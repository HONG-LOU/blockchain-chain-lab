package hash_test

import (
	"testing"

	"chainlab/internal/hash"
)

func TestCanonicalHashIsStableForMapOrder(t *testing.T) {
	left := map[string]any{
		"b": float64(2),
		"a": map[string]any{"y": "yes", "x": "ex"},
	}
	right := map[string]any{
		"a": map[string]any{"x": "ex", "y": "yes"},
		"b": float64(2),
	}

	leftHash, err := hash.Hex(left)
	if err != nil {
		t.Fatal(err)
	}
	rightHash, err := hash.Hex(right)
	if err != nil {
		t.Fatal(err)
	}

	if leftHash != rightHash {
		t.Fatalf("canonical hash should ignore map insertion order: %s != %s", leftHash, rightHash)
	}
}

func TestKeccakHex(t *testing.T) {
	got := hash.KeccakHex([]byte("chainlab"))
	if len(got) != 66 {
		t.Fatalf("hash should be 0x plus 64 hex chars, got %q", got)
	}
	if got != hash.KeccakHex([]byte("chainlab")) {
		t.Fatal("same input must hash deterministically")
	}
}
