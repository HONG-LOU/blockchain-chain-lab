package contracts_test

import (
	"testing"

	"chainlab/internal/contracts"
	"chainlab/internal/state"
)

func TestCounterContract(t *testing.T) {
	store := state.NewStore()
	runtime := contracts.NewRuntimeWithDefaults()
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	addr, events, err := runtime.Deploy(store, creator, "counter.v1", "seed-1", map[string]string{"initial": "2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("deploy should emit an event")
	}

	if _, err := runtime.Call(store, addr, creator, "increment", map[string]string{"amount": "3"}); err != nil {
		t.Fatal(err)
	}
	if got := store.GetStorage(addr, "count"); got != "5" {
		t.Fatalf("counter storage = %q", got)
	}
}

func TestTokenContract(t *testing.T) {
	store := state.NewStore()
	runtime := contracts.NewRuntimeWithDefaults()
	owner := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	token, _, err := runtime.Deploy(store, owner, "token.v1", "seed-token", map[string]string{"symbol": "LAB"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Call(store, token, owner, "mint", map[string]string{"to": owner, "amount": "1000"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Call(store, token, owner, "transfer", map[string]string{"to": bob, "amount": "250"}); err != nil {
		t.Fatal(err)
	}

	if got := store.GetStorage(token, "balance:"+owner); got != "750" {
		t.Fatalf("owner token balance = %q", got)
	}
	if got := store.GetStorage(token, "balance:"+bob); got != "250" {
		t.Fatalf("bob token balance = %q", got)
	}
}
