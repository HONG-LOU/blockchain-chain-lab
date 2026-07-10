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

func TestAccountContractStoresOwner(t *testing.T) {
	store := state.NewStore()
	runtime := contracts.NewRuntimeWithDefaults()
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	owner := "0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

	addr, events, err := runtime.Deploy(store, creator, contracts.AccountCodeID, "seed-account", map[string]string{"owner": owner})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.GetStorage(addr, "owner"); got != "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("owner storage = %q", got)
	}
	value, err := runtime.Read(store, addr, creator, "owner", nil)
	if err != nil {
		t.Fatal(err)
	}
	if value != "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("owner read = %q", value)
	}
	if len(events) != 2 || events[1].Type != "account.owner_set" {
		t.Fatalf("events = %#v", events)
	}
}

func TestAccountOwnerRotationClearsPendingRecoveryAndSessionAuthority(t *testing.T) {
	store := state.NewStore()
	runtime := contracts.NewRuntimeWithDefaults()
	owner := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	nextOwner := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	account, _, err := runtime.Deploy(store, owner, contracts.AccountCodeID, "seed-owner-rotation", map[string]string{"owner": owner})
	if err != nil {
		t.Fatal(err)
	}
	storage := map[string]string{
		"recovery:guardians":     "0xcccccccccccccccccccccccccccccccccccccccc",
		"recovery:threshold":     "1",
		"recovery:delay":         "3",
		"recovery:pending_owner": nextOwner,
		"recovery:execute_after": "10",
		"recovery:expires_at":    "266",
		"recovery:approvals":     "0xcccccccccccccccccccccccccccccccccccccccc",
		"recovery:votes":         "0xcccccccccccccccccccccccccccccccccccccccc=0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"session:0xdddddddddddddddddddddddddddddddddddddddd:limit": "100",
		"session:0xdddddddddddddddddddddddddddddddddddddddd:spent": "20",
		"profile:name": "alice",
	}
	for key, value := range storage {
		store.SetStorage(account, key, value)
	}

	events, err := runtime.Call(store, account, owner, "setOwner", map[string]string{"owner": nextOwner})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.GetStorage(account, "owner"); got != nextOwner {
		t.Fatalf("owner = %q", got)
	}
	for _, key := range []string{"recovery:pending_owner", "recovery:execute_after", "recovery:expires_at", "recovery:approvals", "recovery:votes"} {
		if got := store.GetStorage(account, key); got != "" {
			t.Fatalf("pending recovery key %q = %q", key, got)
		}
	}
	if got := store.GetStorage(account, "recovery:guardians"); got == "" {
		t.Fatal("owner rotation should preserve recovery configuration")
	}
	if got := store.GetStorage(account, "session:0xdddddddddddddddddddddddddddddddddddddddd:limit"); got != "" {
		t.Fatalf("old session authority = %q", got)
	}
	if got := store.GetStorage(account, "profile:name"); got != "alice" {
		t.Fatalf("unrelated storage = %q", got)
	}
	if len(events) != 1 || events[0].Type != "account.owner_set" || events[0].Attributes["old_owner"] != owner {
		t.Fatalf("owner rotation events = %#v", events)
	}
	if _, err := runtime.Call(store, account, nextOwner, "setOwner", map[string]string{"owner": account}); err == nil {
		t.Fatal("account must not be set as its own owner")
	}
}

func TestMultisigContractStoresOwnersAndThreshold(t *testing.T) {
	store := state.NewStore()
	runtime := contracts.NewRuntimeWithDefaults()
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ownerA := "0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	ownerB := "0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

	addr, events, err := runtime.Deploy(store, creator, contracts.MultisigCodeID, "seed-multisig", map[string]string{
		"owners":    ownerA + "," + ownerB,
		"threshold": "2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.GetStorage(addr, "owners"); got != "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa,0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("owners storage = %q", got)
	}
	if got := store.GetStorage(addr, "threshold"); got != "2" {
		t.Fatalf("threshold storage = %q", got)
	}
	owners, err := runtime.Read(store, addr, creator, "owners", nil)
	if err != nil {
		t.Fatal(err)
	}
	if owners != "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa,0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" {
		t.Fatalf("owners read = %q", owners)
	}
	threshold, err := runtime.Read(store, addr, creator, "threshold", nil)
	if err != nil {
		t.Fatal(err)
	}
	if threshold != "2" {
		t.Fatalf("threshold read = %q", threshold)
	}
	if len(events) != 2 || events[1].Type != "multisig.configured" {
		t.Fatalf("events = %#v", events)
	}
}
