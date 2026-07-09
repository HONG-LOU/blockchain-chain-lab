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

	value, err := runtime.Read(store, addr, creator, "get", nil)
	if err != nil {
		t.Fatal(err)
	}
	if value != "5" {
		t.Fatalf("counter read = %q", value)
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

	bobBalance, err := runtime.Read(store, token, owner, "balanceOf", map[string]string{"address": bob})
	if err != nil {
		t.Fatal(err)
	}
	if bobBalance != "250" {
		t.Fatalf("bob token read balance = %q", bobBalance)
	}
	symbol, err := runtime.Read(store, token, owner, "symbol", nil)
	if err != nil {
		t.Fatal(err)
	}
	if symbol != "LAB" {
		t.Fatalf("token symbol = %q", symbol)
	}
}

func TestWASMContractEchoLifecycle(t *testing.T) {
	store := state.NewStore()
	runtime := contracts.NewRuntimeWithDefaults()
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	addr, events, err := runtime.Deploy(store, creator, "wasm.echo.v1", "seed-wasm", map[string]string{"message": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got := events[len(events)-1].Type; got != "wasm.echo.initialized" {
		t.Fatalf("deploy event = %q", got)
	}

	value, err := runtime.Read(store, addr, creator, "get", nil)
	if err != nil {
		t.Fatal(err)
	}
	if value != "hello" {
		t.Fatalf("initial wasm read = %q", value)
	}

	events, err = runtime.Call(store, addr, creator, "set", map[string]string{"message": "world"})
	if err != nil {
		t.Fatal(err)
	}
	if got := events[0].Type; got != "wasm.echoed" {
		t.Fatalf("call event = %q", got)
	}
	if got := store.GetStorage(addr, "last"); got != "world" {
		t.Fatalf("wasm storage = %q", got)
	}

	value, err = runtime.Read(store, addr, creator, "get", nil)
	if err != nil {
		t.Fatal(err)
	}
	if value != "world" {
		t.Fatalf("updated wasm read = %q", value)
	}
}
