package contracts_test

import (
	"errors"
	"strconv"
	"testing"

	"chainlab/internal/contracts"
	"chainlab/internal/state"
	"chainlab/internal/types"
)

var errMutatingContract = errors.New("mutating contract failed")

type mutatingContract struct{}

func (mutatingContract) Deploy(ctx contracts.Context, _ map[string]string) ([]types.Event, error) {
	if err := ctx.SetStorage("partial", "deploy"); err != nil {
		return nil, err
	}
	return nil, errMutatingContract
}

func (mutatingContract) Call(ctx contracts.Context, _ string, _ map[string]string) ([]types.Event, error) {
	if err := ctx.SetStorage("partial", "call"); err != nil {
		return nil, err
	}
	return nil, errMutatingContract
}

func (mutatingContract) Read(ctx contracts.Context, _ string, _ map[string]string) (string, error) {
	if err := ctx.SetStorage("partial", "read"); err != nil {
		return "", err
	}
	return "mutated", nil
}

type callFailingContract struct{}

type panickingContract struct{}

func (panickingContract) Deploy(ctx contracts.Context, _ map[string]string) ([]types.Event, error) {
	if err := ctx.SetStorage("ready", "true"); err != nil {
		return nil, err
	}
	return nil, nil
}

func (panickingContract) Call(contracts.Context, string, map[string]string) ([]types.Event, error) {
	panic("native panic must become a fatal runtime error")
}

func (panickingContract) Read(contracts.Context, string, map[string]string) (string, error) {
	panic("native read panic must become a fatal runtime error")
}

func (callFailingContract) Deploy(ctx contracts.Context, _ map[string]string) ([]types.Event, error) {
	if err := ctx.SetStorage("ready", "true"); err != nil {
		return nil, err
	}
	return nil, nil
}

func (callFailingContract) Call(ctx contracts.Context, _ string, _ map[string]string) ([]types.Event, error) {
	if err := ctx.SetStorage("partial", "call"); err != nil {
		return nil, err
	}
	return nil, errMutatingContract
}

func (callFailingContract) Read(ctx contracts.Context, _ string, _ map[string]string) (string, error) {
	if err := ctx.SetStorage("partial", "read"); err != nil {
		return "", err
	}
	return "mutated", nil
}

func TestRuntimeContractFailuresAndReadsAreAtomic(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	runtime := contracts.NewRuntime()
	if err := runtime.Register("mutating.deploy.v1", mutatingContract{}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Register("mutating.call.v1", callFailingContract{}); err != nil {
		t.Fatal(err)
	}
	rootBefore := store.Root()
	if _, _, err := runtime.Deploy(store, creator, "mutating.deploy.v1", "seed", nil); !errors.Is(err, errMutatingContract) {
		t.Fatalf("mutating deploy error = %v", err)
	}
	if got := store.Root(); got != rootBefore {
		t.Fatalf("failed deploy changed root: before=%s after=%s", rootBefore, got)
	}

	address, _, err := runtime.Deploy(store, creator, "mutating.call.v1", "seed", nil)
	if err != nil {
		t.Fatal(err)
	}
	rootBefore = store.Root()
	if _, err := runtime.Call(store, address, creator, "fail", nil); !errors.Is(err, errMutatingContract) {
		t.Fatalf("mutating call error = %v", err)
	}
	if got := store.Root(); got != rootBefore {
		t.Fatalf("failed call changed root: before=%s after=%s", rootBefore, got)
	}

	if _, err := runtime.Read(store, address, creator, "mutate", nil); !errors.Is(err, contracts.ErrReadOnlyContract) {
		t.Fatalf("mutating read error = %v", err)
	}
	if got := store.Root(); got != rootBefore {
		t.Fatalf("read changed root: before=%s after=%s", rootBefore, got)
	}
}

func TestRuntimeConvertsNativePanicsToFatalErrors(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	runtime := contracts.NewRuntime()
	if err := runtime.Register("panicking.v1", panickingContract{}); err != nil {
		t.Fatal(err)
	}
	address, _, err := runtime.Deploy(store, creator, "panicking.v1", "panic", nil)
	if err != nil {
		t.Fatal(err)
	}
	rootBefore := store.Root()
	if _, err := runtime.Call(store, address, creator, "panic", nil); !errors.Is(err, contracts.ErrNativeRuntimeFault) {
		t.Fatalf("call panic error = %v", err)
	}
	if _, err := runtime.Read(store, address, creator, "panic", nil); !errors.Is(err, contracts.ErrNativeRuntimeFault) {
		t.Fatalf("read panic error = %v", err)
	}
	if store.Root() != rootBefore {
		t.Fatal("native panic mutated state")
	}
}

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

func TestTokenRejectsZeroAmountWithoutStateChanges(t *testing.T) {
	store := state.NewStore()
	runtime := contracts.NewRuntimeWithDefaults()
	owner := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	token, _, err := runtime.Deploy(store, owner, "token.v1", "seed-token-zero", map[string]string{"symbol": "LAB"})
	if err != nil {
		t.Fatal(err)
	}

	rootBefore := store.Root()
	for method, args := range map[string]map[string]string{
		"mint":     {"to": bob, "amount": "0"},
		"transfer": {"to": bob, "amount": "0"},
	} {
		if _, err := runtime.Call(store, token, owner, method, args); err == nil || err.Error() != "amount must be positive" {
			t.Fatalf("%s zero amount error = %v", method, err)
		} else if errors.Is(err, contracts.ErrContractStateFault) || errors.Is(err, contracts.ErrNativeRuntimeFault) {
			t.Fatalf("%s zero amount was classified as fatal: %v", method, err)
		}
		if store.Root() != rootBefore || store.HasStorage(token, "balance:"+owner) || store.HasStorage(token, "balance:"+bob) {
			t.Fatalf("%s zero amount mutated token state", method)
		}
	}
}

func TestTokenFullBalanceTransferReclaimsStorage(t *testing.T) {
	store := state.NewStore()
	runtime := contracts.NewRuntimeWithDefaults()
	owner := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	token, _, err := runtime.Deploy(store, owner, "token.v1", "seed-token-reclaim", map[string]string{"symbol": "LAB"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Call(store, token, owner, "mint", map[string]string{"to": owner, "amount": "10"}); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Call(store, token, owner, "transfer", map[string]string{"to": bob, "amount": "10"}); err != nil {
		t.Fatal(err)
	}
	if store.HasStorage(token, "balance:"+owner) || store.GetStorage(token, "balance:"+bob) != "10" {
		t.Fatalf("full transfer storage = %#v", store.StorageSnapshot(token))
	}
}

func TestTokenFullBalanceTransferReusesSlotAtStorageLimit(t *testing.T) {
	store := state.NewStore()
	runtime := contracts.NewRuntimeWithDefaults()
	owner := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	holder := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	nextHolder := "0xcccccccccccccccccccccccccccccccccccccccc"
	token, _, err := runtime.Deploy(store, owner, "token.v1", "seed-token-full", map[string]string{"symbol": "LAB"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Call(store, token, owner, "mint", map[string]string{"to": holder, "amount": "1"}); err != nil {
		t.Fatal(err)
	}
	for index := 0; store.StorageEntryCount(token) < state.MaxStorageEntriesPerAccount; index++ {
		if err := store.SetStorage(token, "filler:"+strconv.Itoa(index), "1"); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := runtime.Call(store, token, holder, "transfer", map[string]string{"to": nextHolder, "amount": "1"}); err != nil {
		t.Fatal(err)
	}
	if store.StorageEntryCount(token) != state.MaxStorageEntriesPerAccount {
		t.Fatalf("storage entries = %d", store.StorageEntryCount(token))
	}
	if store.HasStorage(token, "balance:"+holder) || store.GetStorage(token, "balance:"+nextHolder) != "1" {
		t.Fatal("full-balance transfer did not reuse the released storage slot")
	}
}

func TestTokenRejectsPersistedZeroBalance(t *testing.T) {
	store := state.NewStore()
	runtime := contracts.NewRuntimeWithDefaults()
	owner := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	holder := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	token, _, err := runtime.Deploy(store, owner, "token.v1", "seed-token-zero-state", map[string]string{"symbol": "LAB"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetStorage(token, "balance:"+holder, "0"); err != nil {
		t.Fatal(err)
	}
	rootBefore := store.Root()

	if _, err := runtime.Read(store, token, owner, "balanceOf", map[string]string{"address": holder}); !errors.Is(err, contracts.ErrContractStateFault) {
		t.Fatalf("persisted zero balance read error = %v", err)
	}
	if _, err := runtime.Call(store, token, holder, "transfer", map[string]string{"to": owner, "amount": "1"}); !errors.Is(err, contracts.ErrContractStateFault) {
		t.Fatalf("persisted zero balance call error = %v", err)
	}
	if store.Root() != rootBefore {
		t.Fatal("persisted zero balance fault mutated state")
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
	owner := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

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
	ownerA := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	ownerB := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

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
