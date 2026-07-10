package contracts_test

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"chainlab/internal/contracts"
	"chainlab/internal/state"
	"chainlab/internal/types"
)

const prefixDeleteCodeID = "prefix-delete.test.v1"

type prefixDeleteContract struct{}

func (prefixDeleteContract) Deploy(contracts.Context, map[string]string) ([]types.Event, error) {
	return nil, nil
}

func (prefixDeleteContract) Call(ctx contracts.Context, _ string, args map[string]string) ([]types.Event, error) {
	reads := 0
	if raw := args["reads"]; raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return nil, errors.New("invalid test read count")
		}
		reads = parsed
	}
	for range reads {
		if _, err := ctx.GetStorage("probe"); err != nil {
			return nil, err
		}
	}
	_, err := ctx.DeleteStoragePrefix(args["prefix"])
	return nil, err
}

func (prefixDeleteContract) Read(contracts.Context, string, map[string]string) (string, error) {
	return "", errors.New("unsupported test read")
}

func TestNativeV1ExactGasVectors(t *testing.T) {
	if contracts.NativeMeteringVersion != "chainlab-native-v1" {
		t.Fatalf("native metering version = %q", contracts.NativeMeteringVersion)
	}
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	runtime := contracts.NewRuntimeWithDefaults()
	address, _, deployGas, err := runtime.DeployMeteredWithLimit(
		store,
		creator,
		"counter.v1",
		"native-v1",
		map[string]string{"initial": "2"},
		100_000,
	)
	if err != nil {
		t.Fatal(err)
	}
	if deployGas != 2_322 {
		t.Fatalf("counter deploy gas = %d, want 2322", deployGas)
	}

	beforeCall := store.Clone()
	_, callGas, err := runtime.CallMeteredWithLimit(
		store,
		address,
		creator,
		"increment",
		map[string]string{"amount": "3"},
		100_000,
	)
	if err != nil {
		t.Fatal(err)
	}
	if callGas != 1_423 {
		t.Fatalf("counter call gas = %d, want 1423", callGas)
	}
	value, readGas, err := runtime.ReadMetered(store, address, creator, "get", nil, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	if value != "5" || readGas != 613 {
		t.Fatalf("counter read value=%q gas=%d, want 5/613", value, readGas)
	}

	_, replayGas, err := runtime.CallMeteredWithLimit(
		beforeCall,
		address,
		creator,
		"increment",
		map[string]string{"amount": "3"},
		100_000,
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayGas != callGas {
		t.Fatalf("cold/warm gas mismatch: first=%d replay=%d", callGas, replayGas)
	}
}

func TestNativeV1OutOfGasIsExactAndAtomic(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	runtime := contracts.NewRuntimeWithDefaults()
	address, _, err := runtime.Deploy(store, creator, "counter.v1", "native-oog", map[string]string{"initial": "2"})
	if err != nil {
		t.Fatal(err)
	}
	rootBefore := store.Root()
	_, gasUsed, err := runtime.CallMeteredWithLimit(store, address, creator, "increment", map[string]string{"amount": "3"}, 600)
	if !errors.Is(err, contracts.ErrContractOutOfGas) {
		t.Fatalf("call error = %v", err)
	}
	if gasUsed != 600 {
		t.Fatalf("gas used = %d, want 600", gasUsed)
	}
	if store.Root() != rootBefore || store.GetStorage(address, "count") != "2" {
		t.Fatal("out-of-gas native call mutated state")
	}
}

func TestNativeRegistrySealsAndRejectsDuplicateImplementations(t *testing.T) {
	runtime := contracts.NewRuntime()
	if err := runtime.Register("counter.custom.v1", contracts.Counter{}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Register("counter.custom.v1", contracts.Counter{}); err == nil {
		t.Fatal("duplicate native code id was accepted")
	}
	store := state.NewStore()
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, _, err := runtime.Deploy(store, creator, "counter.custom.v1", "seal", map[string]string{"initial": "0"}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Register("after.execution.v1", contracts.Counter{}); err == nil {
		t.Fatal("runtime registry accepted registration after execution")
	}
}

func TestNativeV1RejectsOversizedMethodBeforeContractExecution(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	runtime := contracts.NewRuntimeWithDefaults()
	address, _, err := runtime.Deploy(store, creator, "counter.v1", "method-limit", map[string]string{"initial": "0"})
	if err != nil {
		t.Fatal(err)
	}
	rootBefore := store.Root()
	_, gasUsed, err := runtime.CallMeteredWithLimit(store, address, creator, strings.Repeat("m", contracts.MaxNativeMethodBytes+1), nil, 100_000)
	if !errors.Is(err, contracts.ErrNativeResourceLimit) || gasUsed != 0 {
		t.Fatalf("oversized method error=%v gas=%d", err, gasUsed)
	}
	if store.Root() != rootBefore {
		t.Fatal("oversized method mutated state")
	}
}

func TestNativePrefixScanIgnoresNonMatchingValueBytes(t *testing.T) {
	store, runtime, address := newPrefixDeleteFixture(t, "large-values")
	empty := store.Clone()
	largeValue := strings.Repeat("v", state.MaxStorageValueBytes)
	for index := range state.MaxStorageEntriesPerAccount {
		value := "v"
		if index == 0 || index == state.MaxStorageEntriesPerAccount-1 {
			value = largeValue
		}
		if err := store.SetStorage(address, fmt.Sprintf("other:%04d", index), value); err != nil {
			t.Fatal(err)
		}
	}
	args := map[string]string{"prefix": "match:"}
	_, emptyGas, err := runtime.CallMeteredWithLimit(empty, address, testNativeCaller, "deletePrefix", args, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	_, scanGas, err := runtime.CallMeteredWithLimit(store, address, testNativeCaller, "deletePrefix", args, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if scanGas-emptyGas != uint64(state.MaxStorageEntriesPerAccount)*25 {
		t.Fatalf("scan gas delta = %d, want %d", scanGas-emptyGas, state.MaxStorageEntriesPerAccount*25)
	}
	if store.StorageEntryCount(address) != state.MaxStorageEntriesPerAccount ||
		store.GetStorage(address, "other:0000") != largeValue ||
		store.GetStorage(address, "other:4095") != largeValue {
		t.Fatal("non-matching prefix scan changed storage")
	}
}

func TestNativePrefixDeletePreservesScanAndDeleteGasFormula(t *testing.T) {
	store, runtime, address := newPrefixDeleteFixture(t, "gas-formula")
	nonMatching := store.Clone()
	matching := store.Clone()
	if err := nonMatching.SetStorage(address, "other:0", "v"); err != nil {
		t.Fatal(err)
	}
	if err := matching.SetStorage(address, "match:0", "v"); err != nil {
		t.Fatal(err)
	}
	args := map[string]string{"prefix": "match:"}
	_, nonMatchingGas, err := runtime.CallMeteredWithLimit(nonMatching, address, testNativeCaller, "deletePrefix", args, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	_, matchingGas, err := runtime.CallMeteredWithLimit(matching, address, testNativeCaller, "deletePrefix", args, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	wantDeleteGas := uint64(300 + 2*(len("match:0")+len("v")))
	if matchingGas-nonMatchingGas != wantDeleteGas {
		t.Fatalf("matched delete gas delta = %d, want %d", matchingGas-nonMatchingGas, wantDeleteGas)
	}
	if matching.HasStorage(address, "match:0") || !nonMatching.HasStorage(address, "other:0") {
		t.Fatal("prefix delete gas vector produced unexpected storage")
	}
}

func TestNativePrefixScanKeyIOBoundary(t *testing.T) {
	tests := []struct {
		name           string
		finalKeyLength int
		wantLimit      bool
	}{
		{name: "exact limit", finalKeyLength: state.MaxStorageKeyBytes - 1},
		{name: "one byte over", finalKeyLength: state.MaxStorageKeyBytes, wantLimit: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, runtime, address := newPrefixDeleteFixture(t, "key-io-"+test.name)
			for index := range state.MaxStorageEntriesPerAccount {
				length := state.MaxStorageKeyBytes
				if index == state.MaxStorageEntriesPerAccount-1 {
					length = test.finalKeyLength
				}
				key := fmt.Sprintf("%04x", index) + strings.Repeat("k", length-4)
				if err := store.SetStorage(address, key, "v"); err != nil {
					t.Fatal(err)
				}
			}
			rootBefore := store.Root()
			_, _, err := runtime.CallMeteredWithLimit(
				store,
				address,
				testNativeCaller,
				"deletePrefix",
				map[string]string{"prefix": "z"},
				1_000_000,
			)
			if test.wantLimit {
				if !errors.Is(err, contracts.ErrNativeResourceLimit) {
					t.Fatalf("scan error = %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if store.Root() != rootBefore {
				t.Fatal("key I/O boundary scan mutated state")
			}
		})
	}
}

func TestNativePrefixScanOperationBoundary(t *testing.T) {
	store, runtime, address := newPrefixDeleteFixture(t, "operation-boundary")
	if err := store.SetStorage(address, "probe", "v"); err != nil {
		t.Fatal(err)
	}
	for index := 1; index < state.MaxStorageEntriesPerAccount; index++ {
		if err := store.SetStorage(address, fmt.Sprintf("other:%04d", index), "v"); err != nil {
			t.Fatal(err)
		}
	}
	rootBefore := store.Root()
	for reads, wantLimit := range map[int]bool{4096: false, 4097: true} {
		_, _, err := runtime.CallMeteredWithLimit(
			store,
			address,
			testNativeCaller,
			"deletePrefix",
			map[string]string{"prefix": "match:", "reads": strconv.Itoa(reads)},
			2_000_000,
		)
		if wantLimit {
			if !errors.Is(err, contracts.ErrNativeResourceLimit) {
				t.Fatalf("%d reads error = %v", reads, err)
			}
		} else if err != nil {
			t.Fatalf("%d reads: %v", reads, err)
		}
		if store.Root() != rootBefore {
			t.Fatalf("%d reads mutated state", reads)
		}
	}
}

func TestNativePrefixDeleteRollsBackAfterMatchedValueIOLimit(t *testing.T) {
	store, runtime, address := newPrefixDeleteFixture(t, "matched-value-rollback")
	largeValue := strings.Repeat("v", state.MaxStorageValueBytes)
	for index := range 17 {
		if err := store.SetStorage(address, fmt.Sprintf("match:%02d", index), largeValue); err != nil {
			t.Fatal(err)
		}
	}
	rootBefore := store.Root()
	_, _, err := runtime.CallMeteredWithLimit(
		store,
		address,
		testNativeCaller,
		"deletePrefix",
		map[string]string{"prefix": "match:"},
		5_000_000,
	)
	if !errors.Is(err, contracts.ErrNativeResourceLimit) {
		t.Fatalf("matched value I/O error = %v", err)
	}
	if store.Root() != rootBefore || store.StorageEntryCount(address) != 17 {
		t.Fatal("partial prefix deletion escaped runtime rollback")
	}
}

const testNativeCaller = "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func newPrefixDeleteFixture(t *testing.T, seed string) (*state.Store, *contracts.Runtime, string) {
	t.Helper()
	store := state.NewStore()
	runtime := contracts.NewRuntime()
	if err := runtime.Register(prefixDeleteCodeID, prefixDeleteContract{}); err != nil {
		t.Fatal(err)
	}
	address, _, err := runtime.Deploy(store, testNativeCaller, prefixDeleteCodeID, seed, nil)
	if err != nil {
		t.Fatal(err)
	}
	return store, runtime, address
}
