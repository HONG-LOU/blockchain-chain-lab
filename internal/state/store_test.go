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

func TestDeleteStorageRemovesKeyFromSnapshotAndRoot(t *testing.T) {
	store := state.NewStore()
	account := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	key := "session:0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb:limit"
	store.SetStorage(account, key, "100")
	rootWithKey := store.Root()

	store.DeleteStorage(account, key)
	if got := store.GetStorage(account, key); got != "" {
		t.Fatalf("deleted storage = %q", got)
	}
	if store.Root() == rootWithKey {
		t.Fatal("root did not change after deleting storage")
	}
	restored := state.NewStoreFromSnapshot(store.Snapshot())
	if got := restored.GetStorage(account, key); got != "" {
		t.Fatalf("restored deleted storage = %q", got)
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

func TestValidatorsArePartOfSnapshotAndRoot(t *testing.T) {
	validatorA := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	validatorB := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	store := state.NewStore()
	store.SetValidators([]string{validatorA})
	if err := store.AddValidator(validatorB); err != nil {
		t.Fatal(err)
	}

	validators := store.Validators()
	if len(validators) != 2 || validators[0] != validatorA || validators[1] != validatorB {
		t.Fatalf("validators = %#v", validators)
	}

	restored := state.NewStoreFromSnapshot(store.Snapshot())
	restoredValidators := restored.Validators()
	if len(restoredValidators) != 2 || restoredValidators[0] != validatorA || restoredValidators[1] != validatorB {
		t.Fatalf("restored validators = %#v", restoredValidators)
	}
	if restored.Root() != store.Root() {
		t.Fatal("validator set should be included in state root")
	}

	changed := store.Clone()
	if err := changed.AddValidator("0xcccccccccccccccccccccccccccccccccccccccc"); err != nil {
		t.Fatal(err)
	}
	if changed.Root() == store.Root() {
		t.Fatal("changing validators should change state root")
	}
}

func TestRemoveValidatorUpdatesSnapshotAndRoot(t *testing.T) {
	validatorA := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	validatorB := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	store := state.NewStore()
	store.SetValidators([]string{validatorA, validatorB})
	before := store.Root()

	if err := store.RemoveValidator(validatorB); err != nil {
		t.Fatal(err)
	}
	validators := store.Validators()
	if len(validators) != 1 || validators[0] != validatorA {
		t.Fatalf("validators = %#v", validators)
	}
	if store.Root() == before {
		t.Fatal("removing validator should change state root")
	}

	restored := state.NewStoreFromSnapshot(store.Snapshot())
	restoredValidators := restored.Validators()
	if len(restoredValidators) != 1 || restoredValidators[0] != validatorA {
		t.Fatalf("restored validators = %#v", restoredValidators)
	}
	if err := restored.RemoveValidator(validatorA); err == nil {
		t.Fatal("removing the last validator should fail")
	}
}

func TestDelegatedCodeIDPersistsThroughCloneSnapshotAndRoot(t *testing.T) {
	store := state.NewStore()
	alice := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	owner := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store.SetBalance(alice, 100)
	store.SetDelegatedCodeID(alice, "account.v1")
	store.SetStorage(alice, "owner", owner)
	root := store.Root()

	clone := store.Clone()
	if got := clone.GetAccount(alice).DelegatedCodeID; got != "account.v1" {
		t.Fatalf("clone delegated code id = %q", got)
	}
	if got := clone.GetStorage(alice, "owner"); got != owner {
		t.Fatalf("clone owner = %q", got)
	}

	restored := state.NewStoreFromSnapshot(store.Snapshot())
	if got := restored.GetAccount(alice).DelegatedCodeID; got != "account.v1" {
		t.Fatalf("snapshot delegated code id = %q", got)
	}
	if restored.Root() != root {
		t.Fatalf("restored root = %s, want %s", restored.Root(), root)
	}

	restored.ClearDelegation(alice)
	if got := restored.GetAccount(alice).DelegatedCodeID; got != "" {
		t.Fatalf("cleared delegated code id = %q", got)
	}
	if got := restored.GetStorage(alice, "owner"); got != "" {
		t.Fatalf("cleared owner = %q", got)
	}
}
