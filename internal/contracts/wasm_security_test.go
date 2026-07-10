package contracts

import (
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"testing"

	"chainlab/internal/state"
	"chainlab/internal/types"
)

func TestWASMConcurrentFreshRuntimeReadsAreDeterministic(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	setupRuntime := NewRuntime()
	code := WasmEchoCode()
	codeID := types.WASMCodeID(code)
	store.SetContractCode(types.ContractCode{CodeID: codeID, Runtime: "wasm", MeteringVersion: WASMMeteringVersion, Creator: creator, Bytecode: "0x" + hex.EncodeToString(code)})
	address, _, err := setupRuntime.Deploy(store, creator, codeID, "seed", map[string]string{"message": "hello"})
	if err != nil {
		t.Fatal(err)
	}

	const readers = 8
	runtime := NewRuntime()
	values := make([]string, readers)
	gasUsed := make([]uint64, readers)
	errorsSeen := make([]error, readers)
	var wait sync.WaitGroup
	wait.Add(readers)
	for index := 0; index < readers; index++ {
		go func(index int) {
			defer wait.Done()
			values[index], gasUsed[index], errorsSeen[index] = runtime.ReadMetered(store, address, creator, "get", nil, 10_000)
		}(index)
	}
	wait.Wait()
	for index := 0; index < readers; index++ {
		if errorsSeen[index] != nil {
			t.Fatalf("reader %d error = %v", index, errorsSeen[index])
		}
		if values[index] != "hello" {
			t.Fatalf("reader %d value = %q", index, values[index])
		}
		if gasUsed[index] != gasUsed[0] {
			t.Fatalf("reader %d gas = %d, first=%d", index, gasUsed[index], gasUsed[0])
		}
	}
}

func TestWASMCompiledCacheCannotAuthorizeMissingOrMismatchedStateCode(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	code := WasmEchoCode()
	codeID := types.WASMCodeID(code)
	runtime := NewRuntime()
	warmStore := state.NewStore()
	warmStore.SetContractCode(types.ContractCode{
		CodeID:          codeID,
		Runtime:         "wasm",
		MeteringVersion: WASMMeteringVersion,
		Creator:         creator,
		Bytecode:        "0x" + hex.EncodeToString(code),
	})
	if _, _, err := runtime.Deploy(warmStore, creator, codeID, "warm", map[string]string{"message": "hello"}); err != nil {
		t.Fatal(err)
	}

	emptyStore := state.NewStore()
	if _, _, err := runtime.Deploy(emptyStore, creator, codeID, "missing", nil); err == nil || !strings.Contains(err.Error(), "unknown contract code") {
		t.Fatalf("cached missing-code deploy error = %v", err)
	}

	mismatchedStore := state.NewStore()
	mismatchedStore.SetContractCode(types.ContractCode{
		CodeID:          codeID,
		Runtime:         "wasm",
		MeteringVersion: WASMMeteringVersion,
		Creator:         creator,
		Bytecode:        "0x" + hex.EncodeToString(wasmComputeContractForTest(1)),
	})
	if _, _, err := runtime.Deploy(mismatchedStore, creator, codeID, "mismatch", nil); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("cached mismatched-code deploy error = %v", err)
	}
}

func TestWASMInvocationBudgetIsCheckedBeforeJITCompilation(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	invalidCode := append([]byte(nil), wasmComputeContractForTest(0)...)
	invalidCode[len(invalidCode)-1] = 0xff
	resources := wasmModuleResources{codeBytes: uint64(len(invalidCode))}
	if err := validateWasmEnvelope(invalidCode, &resources); err != nil {
		t.Fatalf("test fixture must pass the pre-JIT resource filter: %v", err)
	}
	codeID := types.WASMCodeID(invalidCode)
	store := state.NewStore()
	store.SetContractCode(types.ContractCode{
		CodeID:          codeID,
		Runtime:         "wasm",
		MeteringVersion: WASMMeteringVersion,
		Creator:         creator,
		Bytecode:        "0x" + hex.EncodeToString(invalidCode),
	})
	runtime := NewRuntime()
	rootBefore := store.Root()
	gasLimit := resources.instantiationGas() - 1
	_, _, gasUsed, err := runtime.DeployMeteredWithLimit(store, creator, codeID, "low-gas", nil, gasLimit)
	if !errors.Is(err, ErrContractOutOfGas) {
		t.Fatalf("pre-JIT budget error = %v, want out of gas", err)
	}
	if gasUsed != gasLimit {
		t.Fatalf("pre-JIT budget gas = %d, want %d", gasUsed, gasLimit)
	}
	if len(runtime.wasmCache) != 0 {
		t.Fatalf("underfunded module reached compiled cache: %+v", runtime.wasmCache)
	}
	if store.Root() != rootBefore {
		t.Fatal("underfunded deploy changed state")
	}

	_, _, _, err = runtime.DeployMeteredWithLimit(store, creator, codeID, "enough-gas", nil, 100_000)
	if !errors.Is(err, ErrWasmRuntimeFault) {
		t.Fatalf("admitted invalid module error = %v, want runtime fault", err)
	}
}

func TestWASMUploadInvalidModuleIsNotRuntimeFault(t *testing.T) {
	invalidCode := append([]byte(nil), wasmComputeContractForTest(0)...)
	invalidCode[len(invalidCode)-1] = 0xff
	resources := wasmModuleResources{codeBytes: uint64(len(invalidCode))}
	if err := validateWasmEnvelope(invalidCode, &resources); err != nil {
		t.Fatalf("test fixture must pass the pre-JIT resource filter: %v", err)
	}

	err := NewRuntime().ValidateAndCacheWasmCode(invalidCode)
	if err == nil || !strings.Contains(err.Error(), "invalid wasm bytecode") {
		t.Fatalf("invalid upload error = %v", err)
	}
	if errors.Is(err, ErrWasmRuntimeFault) {
		t.Fatalf("invalid upload was classified as runtime fault: %v", err)
	}
}

func TestWASMColdAndWarmCacheUseIdenticalGas(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	code := WasmEchoCode()
	codeID := types.WASMCodeID(code)
	store := state.NewStore()
	store.SetContractCode(types.ContractCode{
		CodeID:          codeID,
		Runtime:         "wasm",
		MeteringVersion: WASMMeteringVersion,
		Creator:         creator,
		Bytecode:        "0x" + hex.EncodeToString(code),
	})
	runtime := NewRuntime()
	_, _, coldGas, err := runtime.DeployMeteredWithLimit(store, creator, codeID, "cold", map[string]string{"message": "hello"}, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	_, _, warmGas, err := runtime.DeployMeteredWithLimit(store, creator, codeID, "warm", map[string]string{"message": "hello"}, 100_000)
	if err != nil {
		t.Fatal(err)
	}
	if coldGas != warmGas {
		t.Fatalf("cache changed consensus gas: cold=%d warm=%d", coldGas, warmGas)
	}
}

func TestWASMUploadValidationReusesCompiledArtifact(t *testing.T) {
	runtime := NewRuntime()
	code := WasmEchoCode()
	if err := runtime.ValidateAndCacheWasmCode(code); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ValidateAndCacheWasmCode(code); err != nil {
		t.Fatal(err)
	}
	codeID := types.WASMCodeID(code)
	if len(runtime.wasmCache) != 1 {
		t.Fatalf("upload validation cache entries = %d", len(runtime.wasmCache))
	}
	entry, ok := runtime.wasmCache[codeID]
	if !ok || entry.bytecode != "0x"+hex.EncodeToString(code) || entry.meteringVersion != WASMMeteringVersion {
		t.Fatalf("upload validation cache entry = %+v, found=%v", entry, ok)
	}
}

func TestWASMFuelMetersRecursiveAndIndirectCalls(t *testing.T) {
	tests := []struct {
		name string
		code []byte
	}{
		{name: "recursive", code: wasmRecursiveContractForTest()},
		{name: "indirect", code: wasmIndirectLoopContractForTest()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			store := state.NewStore()
			runtime := NewRuntime()
			contract, err := NewWasmContract(test.code)
			if err != nil {
				t.Fatal(err)
			}
			runtime.Register("wasm."+test.name+".v1", contract)
			address, _, err := runtime.Deploy(store, creator, "wasm."+test.name+".v1", "seed", nil)
			if err != nil {
				t.Fatal(err)
			}
			gasLimit := contract.resources.instantiationGas() + 750
			if gasLimit <= contract.resources.instantiationGas() {
				t.Fatal("fuel test must reserve guest fuel after instantiation")
			}
			_, gasUsed, err := runtime.CallMeteredWithLimit(store, address, creator, "set", nil, gasLimit)
			if !errors.Is(err, ErrContractOutOfGas) {
				t.Fatalf("%s error = %v, want out of gas", test.name, err)
			}
			if gasUsed != gasLimit {
				t.Fatalf("%s gas used = %d, want %d", test.name, gasUsed, gasLimit)
			}
		})
	}
}

func TestWASMGuestTrapRollsBackStorage(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	runtime := NewRuntime()
	contract, err := NewWasmContract(wasmStorageWriteContractForTest(true, 1))
	if err != nil {
		t.Fatal(err)
	}
	runtime.Register("wasm.write.guest-trap", contract)
	address, _, err := runtime.Deploy(store, creator, "wasm.write.guest-trap", "seed", nil)
	if err != nil {
		t.Fatal(err)
	}
	rootBefore := store.Root()
	_, gasUsed, err := runtime.CallMeteredWithLimit(store, address, creator, "set", nil, 10_000)
	if !errors.Is(err, ErrWasmGuestTrap) {
		t.Fatalf("call error = %v, want guest trap", err)
	}
	if gasUsed == 0 || gasUsed > 10_000 {
		t.Fatalf("failed call gas used = %d", gasUsed)
	}
	if got := store.GetStorage(address, "owned"); got != "" {
		t.Fatalf("failed call persisted storage = %q", got)
	}
	if got := store.Root(); got != rootBefore {
		t.Fatalf("failed call changed root: before=%s after=%s", rootBefore, got)
	}
}

func TestWASMHostOutOfGasOccursBeforeStorageMutation(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	address := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store := state.NewStore()
	contract, err := NewWasmContract(wasmStorageWriteContractForTest(false, 1))
	if err != nil {
		t.Fatal(err)
	}
	type attempt struct {
		store      *state.Store
		meter      *Meter
		invocation *wasmInvocation
		err        error
	}
	run := func(gasLimit uint64) attempt {
		attemptStore := state.NewStore()
		meter := NewLimitedMeter(gasLimit)
		invocation := newWasmInvocation(Context{
			Store:   attemptStore,
			Address: address,
			Caller:  creator,
			Meter:   meter,
		}, nil)
		return attempt{
			store:      attemptStore,
			meter:      meter,
			invocation: invocation,
			err:        invocation.call(contract.module, contract.resources, "call"),
		}
	}

	low := contract.resources.instantiationGas()
	high := uint64(100_000)
	for low < high {
		mid := low + (high-low)/2
		if run(mid).invocation.hostIOBytes > 0 {
			high = mid
		} else {
			low = mid + 1
		}
	}
	gasLimit := low
	result := run(gasLimit)
	store = result.store
	meter := result.meter
	invocation := result.invocation
	err = result.err
	if !errors.Is(err, ErrContractOutOfGas) {
		t.Fatalf("host out-of-gas error = %v", err)
	}
	if invocation.hostIOBytes == 0 {
		t.Fatal("test exhausted gas before entering the storage host function")
	}
	if meter.GasUsed() != gasLimit {
		t.Fatalf("host out-of-gas used %d, want full budget %d", meter.GasUsed(), gasLimit)
	}
	if got := store.GetStorage(address, "owned"); got != "" {
		t.Fatalf("host out-of-gas mutated storage = %q", got)
	}
}

func TestWASMStorageWriteLimitRollsBackInvocation(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	runtime := NewRuntime()
	contract, err := NewWasmContract(wasmStorageWriteContractForTest(false, wasmMaxStorageWrites+1))
	if err != nil {
		t.Fatal(err)
	}
	runtime.Register("wasm.write-limit.v1", contract)
	address, _, err := runtime.Deploy(store, creator, "wasm.write-limit.v1", "seed", nil)
	if err != nil {
		t.Fatal(err)
	}
	rootBefore := store.Root()
	_, _, err = runtime.CallMeteredWithLimit(store, address, creator, "set", nil, 1_000_000)
	if !errors.Is(err, ErrWasmResourceLimit) {
		t.Fatalf("storage write limit error = %v", err)
	}
	if got := store.GetStorage(address, "owned"); got != "" {
		t.Fatalf("write-limit call persisted storage = %q", got)
	}
	if got := store.Root(); got != rootBefore {
		t.Fatalf("write-limit call changed root: before=%s after=%s", rootBefore, got)
	}
}

func TestWASMEventLimitRejectsWholeInvocation(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	runtime := NewRuntime()
	contract, err := NewWasmContract(wasmEventContractForTest(wasmMaxEvents + 1))
	if err != nil {
		t.Fatal(err)
	}
	runtime.Register("wasm.event-limit.v1", contract)
	address, _, err := runtime.Deploy(store, creator, "wasm.event-limit.v1", "seed", nil)
	if err != nil {
		t.Fatal(err)
	}
	events, _, err := runtime.CallMeteredWithLimit(store, address, creator, "set", nil, 1_000_000)
	if !errors.Is(err, ErrWasmResourceLimit) {
		t.Fatalf("event limit error = %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("failed event invocation returned events = %#v", events)
	}
}

func TestWASMUnexpectedEngineErrorsAreRuntimeFaults(t *testing.T) {
	err := normalizeWasmExecutionError(errors.New("injected engine failure"))
	if !errors.Is(err, ErrWasmRuntimeFault) {
		t.Fatalf("unexpected engine error = %v, want runtime fault", err)
	}
}

func wasmRecursiveContractForTest() []byte {
	call := []byte{0x10, 0x01, 0x0b}
	return wasmModuleWithFunctionsForTest(
		[]uint32{0, 0, 0},
		[]uint32{0, 1, 2},
		[][]byte{{0x0b}, call, {0x0b}},
		nil,
		nil,
	)
}

func wasmIndirectLoopContractForTest() []byte {
	loop := []byte{0x03, 0x40, 0x0c, 0x00, 0x0b, 0x0b}
	callIndirect := []byte{0x41, 0x00, 0x11, 0x00, 0x00, 0x0b}
	table := wasmSection(4, wasmVector([]byte{0x70, 0x01, 0x01, 0x01}))
	element := wasmSection(9, wasmVector([]byte{0x00, 0x41, 0x00, 0x0b, 0x01, 0x01}))
	return wasmModuleWithFunctionsForTest(
		[]uint32{0, 0, 0, 0},
		[]uint32{0, 2, 3},
		[][]byte{{0x0b}, loop, callIndirect, {0x0b}},
		table,
		element,
	)
}

func wasmStorageWriteContractForTest(trapAfterWrite bool, writes int) []byte {
	const keyOffset uint32 = 1024
	const valueOffset uint32 = 1030
	var call []byte
	for index := 0; index < writes; index++ {
		call = append(call, wasmI32Const(keyOffset)...)
		call = append(call, wasmI32Const(uint32(len("owned")))...)
		call = append(call, wasmI32Const(valueOffset)...)
		call = append(call, wasmI32Const(uint32(len("yes")))...)
		call = append(call, 0x10, 0x00, 0x1a)
	}
	if trapAfterWrite {
		call = append(call, 0x00)
	}
	call = append(call, 0x0b)

	var module []byte
	module = append(module, []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}...)
	module = append(module, wasmSection(1, wasmVector(
		wasmFuncType([]byte{0x7f, 0x7f, 0x7f, 0x7f}, []byte{0x7f}),
		wasmFuncType(nil, nil),
	))...)
	module = append(module, wasmSection(2, wasmVector(wasmImport(wasmHostModule, "storage_set", 0)))...)
	module = append(module, wasmSection(3, wasmU32Vector(1, 1, 1))...)
	module = append(module, wasmSection(5, []byte{0x01, 0x01, 0x01, 0x01})...)
	module = append(module, wasmSection(7, wasmVector(
		wasmExport("memory", 0x02, 0),
		wasmExport("deploy", 0x00, 1),
		wasmExport("call", 0x00, 2),
		wasmExport("read", 0x00, 3),
	))...)
	module = append(module, wasmSection(10, wasmVector(
		wasmCode(nil, []byte{0x0b}),
		wasmCode(nil, call),
		wasmCode(nil, []byte{0x0b}),
	))...)
	module = append(module, wasmSection(11, wasmVector(wasmDataSegment(int32(keyOffset), []byte("ownedyes"))))...)
	return module
}

func wasmEventContractForTest(eventCount int) []byte {
	const typeOffset uint32 = 1024
	const keyOffset uint32 = 1029
	const valueOffset uint32 = 1030
	var call []byte
	for index := 0; index < eventCount; index++ {
		call = append(call, wasmI32Const(typeOffset)...)
		call = append(call, wasmI32Const(uint32(len("event")))...)
		call = append(call, wasmI32Const(keyOffset)...)
		call = append(call, wasmI32Const(uint32(len("k")))...)
		call = append(call, wasmI32Const(valueOffset)...)
		call = append(call, wasmI32Const(uint32(len("v")))...)
		call = append(call, 0x10, 0x00, 0x1a)
	}
	call = append(call, 0x0b)

	var module []byte
	module = append(module, []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}...)
	module = append(module, wasmSection(1, wasmVector(
		wasmFuncType([]byte{0x7f, 0x7f, 0x7f, 0x7f, 0x7f, 0x7f}, []byte{0x7f}),
		wasmFuncType(nil, nil),
	))...)
	module = append(module, wasmSection(2, wasmVector(wasmImport(wasmHostModule, "emit_event", 0)))...)
	module = append(module, wasmSection(3, wasmU32Vector(1, 1, 1))...)
	module = append(module, wasmSection(5, []byte{0x01, 0x01, 0x01, 0x01})...)
	module = append(module, wasmSection(7, wasmVector(
		wasmExport("memory", 0x02, 0),
		wasmExport("deploy", 0x00, 1),
		wasmExport("call", 0x00, 2),
		wasmExport("read", 0x00, 3),
	))...)
	module = append(module, wasmSection(10, wasmVector(
		wasmCode(nil, []byte{0x0b}),
		wasmCode(nil, call),
		wasmCode(nil, []byte{0x0b}),
	))...)
	module = append(module, wasmSection(11, wasmVector(wasmDataSegment(int32(typeOffset), []byte("eventkv"))))...)
	return module
}

func wasmModuleWithFunctionsForTest(functionTypes []uint32, exportedFunctionIndexes []uint32, bodies [][]byte, tableSection []byte, elementSection []byte) []byte {
	var module []byte
	module = append(module, []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}...)
	module = append(module, wasmSection(1, wasmVector(wasmFuncType(nil, nil)))...)
	module = append(module, wasmSection(3, wasmU32Vector(functionTypes...))...)
	if len(tableSection) != 0 {
		module = append(module, tableSection...)
	}
	module = append(module, wasmSection(5, []byte{0x01, 0x01, 0x01, 0x01})...)
	module = append(module, wasmSection(7, wasmVector(
		wasmExport("memory", 0x02, 0),
		wasmExport("deploy", 0x00, exportedFunctionIndexes[0]),
		wasmExport("call", 0x00, exportedFunctionIndexes[1]),
		wasmExport("read", 0x00, exportedFunctionIndexes[2]),
	))...)
	if len(elementSection) != 0 {
		module = append(module, elementSection...)
	}
	codeBodies := make([][]byte, len(bodies))
	for index, body := range bodies {
		codeBodies[index] = wasmCode(nil, body)
	}
	module = append(module, wasmSection(10, wasmVector(codeBodies...))...)
	return module
}
