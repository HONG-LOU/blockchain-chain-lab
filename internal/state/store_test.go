package state_test

import (
	"testing"

	"chainlab/internal/state"
)

func TestBalancesNonceAndStorage(t *testing.T) {
	store := state.NewStore()
	alice := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	store.SetBalance(alice, 100)
	if err := store.Transfer(alice, bob, 35); err != nil {
		t.Fatal(err)
	}
	store.IncrementNonce(alice)
	store.SetStorage(alice, "role", "admin")

	if got := store.GetAccount(alice).Balance; got != 65 {
		t.Fatalf("alice balance = %d", got)
	}
	if got := store.GetAccount(alice).Nonce; got != 1 {
		t.Fatalf("alice nonce = %d", got)
	}
	if got := store.GetAccount(bob).Balance; got != 35 {
		t.Fatalf("bob balance = %d", got)
	}
	if got := store.GetStorage(alice, "role"); got != "admin" {
		t.Fatalf("storage role = %q", got)
	}
}

func TestCloneIsIsolated(t *testing.T) {
	store := state.NewStore()
	alice := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store.SetBalance(alice, 100)

	clone := store.Clone()
	clone.SetBalance(alice, 1)
	clone.SetStorage(alice, "k", "v")

	if got := store.GetAccount(alice).Balance; got != 100 {
		t.Fatalf("original balance should remain 100, got %d", got)
	}
	if got := store.GetStorage(alice, "k"); got != "" {
		t.Fatalf("original storage should remain empty, got %q", got)
	}
}

func TestRootIsDeterministic(t *testing.T) {
	alice := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	left := state.NewStore()
	left.SetBalance(alice, 100)
	left.SetBalance(bob, 20)
	left.SetStorage(alice, "x", "1")

	right := state.NewStore()
	right.SetStorage(alice, "x", "1")
	right.SetBalance(bob, 20)
	right.SetBalance(alice, 100)

	if left.Root() != right.Root() {
		t.Fatalf("same logical state must have same root: %s != %s", left.Root(), right.Root())
	}
}
