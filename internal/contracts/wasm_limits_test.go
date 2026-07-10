package contracts

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"chainlab/internal/state"
)

func TestWASMHostFieldLimits(t *testing.T) {
	tests := []struct {
		name string
		code []byte
		read bool
	}{
		{name: "storage key", code: wasmStorageLengthsContractForTest(wasmMaxKeyBytes+1, 0)},
		{name: "storage value", code: wasmStorageLengthsContractForTest(0, wasmMaxValueBytes+1)},
		{name: "event type", code: wasmEventLengthsContractForTest(wasmMaxEventTypeBytes+1, 0, 0, 1)},
		{name: "event key", code: wasmEventLengthsContractForTest(0, wasmMaxEventKeyBytes+1, 0, 1)},
		{name: "event value", code: wasmEventLengthsContractForTest(0, 0, wasmMaxEventValueBytes+1, 1)},
		{name: "empty event type", code: wasmEventLengthsContractForTest(0, 1, 0, 1)},
		{name: "empty event key", code: wasmEventLengthsContractForTest(1, 0, 0, 1)},
		{name: "return value", code: wasmReturnLengthContractForTest(wasmMaxReturnBytes + 1), read: true},
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
			codeID := "wasm.limit." + strings.ReplaceAll(test.name, " ", "-")
			runtime.Register(codeID, contract)
			address, _, err := runtime.Deploy(store, creator, codeID, "seed", nil)
			if err != nil {
				t.Fatal(err)
			}
			if test.read {
				_, err = runtime.Read(store, address, creator, "get", nil)
			} else {
				_, _, err = runtime.CallMeteredWithLimit(store, address, creator, "set", nil, 1_000_000)
			}
			if !errors.Is(err, ErrWasmResourceLimit) {
				t.Fatalf("field limit error = %v", err)
			}
		})
	}
}

func TestWASMTotalEventAndHostIOLimits(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tests := []struct {
		name string
		code []byte
		args map[string]string
	}{
		{
			name: "event bytes",
			code: wasmEventLengthsContractForTest(0, 0, wasmMaxEventValueBytes, 5),
		},
		{
			name: "host I/O bytes",
			code: wasmArgCopyContractForTest(17),
			args: map[string]string{"": strings.Repeat("x", int(wasmMaxValueBytes))},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := state.NewStore()
			runtime := NewRuntime()
			contract, err := NewWasmContract(test.code)
			if err != nil {
				t.Fatal(err)
			}
			codeID := "wasm.total." + strings.ReplaceAll(test.name, " ", "-")
			runtime.Register(codeID, contract)
			address, _, err := runtime.Deploy(store, creator, codeID, "seed", nil)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = runtime.CallMeteredWithLimit(store, address, creator, "set", test.args, 5_000_000)
			if !errors.Is(err, ErrWasmResourceLimit) {
				t.Fatalf("aggregate limit error = %v", err)
			}
		})
	}
}

func TestWASMStorageGrowthGasIsDeterministic(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	measure := func(valueLength int) (uint64, uint64) {
		t.Helper()
		store := state.NewStore()
		runtime := NewRuntime()
		contract, err := NewWasmContract(wasmStorageValueContractForTest(valueLength))
		if err != nil {
			t.Fatal(err)
		}
		codeID := "wasm.growth." + strconv.Itoa(valueLength)
		runtime.Register(codeID, contract)
		address, _, err := runtime.Deploy(store, creator, codeID, "seed", nil)
		if err != nil {
			t.Fatal(err)
		}
		_, gasUsed, err := runtime.CallMeteredWithLimit(store, address, creator, "set", nil, 1_000_000)
		if err != nil {
			t.Fatal(err)
		}
		return gasUsed, contract.resources.instantiationGas()
	}

	small, smallInstantiation := measure(1)
	large, largeInstantiation := measure(101)
	const addedBytes = 100
	wantDelta := uint64(addedBytes) * (wasmByteGas + wasmStorageGrowthGas)
	smallExecution := small - smallInstantiation
	largeExecution := large - largeInstantiation
	if delta := largeExecution - smallExecution; delta != wantDelta {
		t.Fatalf("storage growth gas delta = %d, want %d (small=%d large=%d)", delta, wantDelta, smallExecution, largeExecution)
	}
}

func TestWASMRejectsInvalidUTF8BeforeStateWrite(t *testing.T) {
	const dataOffset uint32 = 1024
	call := append([]byte{}, wasmI32Const(dataOffset)...)
	call = append(call, wasmI32Const(1)...)
	call = append(call, wasmI32Const(dataOffset+1)...)
	call = append(call, wasmI32Const(1)...)
	call = append(call, 0x10, 0x00, 0x1a, 0x0b)
	code := wasmHostContractForTest("storage_set", 4, call, []byte{0x0b}, []byte{'k', 0xff})
	contract, err := NewWasmContract(code)
	if err != nil {
		t.Fatal(err)
	}
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	runtime := NewRuntime()
	runtime.Register("wasm.invalid-utf8.v1", contract)
	address, _, err := runtime.Deploy(store, creator, "wasm.invalid-utf8.v1", "seed", nil)
	if err != nil {
		t.Fatal(err)
	}
	rootBefore := store.Root()
	_, _, err = runtime.CallMeteredWithLimit(store, address, creator, "set", nil, 100_000)
	if !errors.Is(err, ErrWasmInvalidUTF8) {
		t.Fatalf("invalid UTF-8 error = %v", err)
	}
	if got := store.GetStorage(address, "k"); got != "" {
		t.Fatalf("invalid UTF-8 persisted storage = %q", got)
	}
	if got := store.Root(); got != rootBefore {
		t.Fatalf("invalid UTF-8 changed root: before=%s after=%s", rootBefore, got)
	}
}

func wasmStorageLengthsContractForTest(keyLength uint32, valueLength uint32) []byte {
	call := append([]byte{}, wasmI32Const(0)...)
	call = append(call, wasmI32Const(keyLength)...)
	call = append(call, wasmI32Const(0)...)
	call = append(call, wasmI32Const(valueLength)...)
	call = append(call, 0x10, 0x00, 0x1a, 0x0b)
	return wasmHostContractForTest("storage_set", 4, call, []byte{0x0b}, nil)
}

func wasmStorageValueContractForTest(valueLength int) []byte {
	const dataOffset uint32 = 1024
	call := append([]byte{}, wasmI32Const(dataOffset)...)
	call = append(call, wasmI32Const(1)...)
	call = append(call, wasmI32Const(dataOffset+1)...)
	call = append(call, wasmI32Const(uint32(valueLength))...)
	call = append(call, 0x10, 0x00, 0x1a, 0x0b)
	data := append([]byte{'k'}, []byte(strings.Repeat("v", valueLength))...)
	return wasmHostContractForTest("storage_set", 4, call, []byte{0x0b}, data)
}

func wasmEventLengthsContractForTest(typeLength uint32, keyLength uint32, valueLength uint32, calls int) []byte {
	var call []byte
	for index := 0; index < calls; index++ {
		call = append(call, wasmI32Const(0)...)
		call = append(call, wasmI32Const(typeLength)...)
		call = append(call, wasmI32Const(0)...)
		call = append(call, wasmI32Const(keyLength)...)
		call = append(call, wasmI32Const(0)...)
		call = append(call, wasmI32Const(valueLength)...)
		call = append(call, 0x10, 0x00, 0x1a)
	}
	call = append(call, 0x0b)
	return wasmHostContractForTest("emit_event", 6, call, []byte{0x0b}, nil)
}

func wasmReturnLengthContractForTest(valueLength uint32) []byte {
	read := append([]byte{}, wasmI32Const(0)...)
	read = append(read, wasmI32Const(valueLength)...)
	read = append(read, 0x10, 0x00, 0x1a, 0x0b)
	return wasmHostContractForTest("return_set", 2, []byte{0x0b}, read, nil)
}

func wasmArgCopyContractForTest(calls int) []byte {
	var call []byte
	for index := 0; index < calls; index++ {
		call = append(call, wasmI32Const(0)...)
		call = append(call, wasmI32Const(0)...)
		call = append(call, wasmI32Const(0)...)
		call = append(call, 0x10, 0x00, 0x1a)
	}
	call = append(call, 0x0b)
	return wasmHostContractForTest("arg_copy", 3, call, []byte{0x0b}, nil)
}

func wasmHostContractForTest(importName string, parameterCount int, call []byte, read []byte, data []byte) []byte {
	params := make([]byte, parameterCount)
	for index := range params {
		params[index] = 0x7f
	}
	var module []byte
	module = append(module, []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}...)
	module = append(module, wasmSection(1, wasmVector(
		wasmFuncType(params, []byte{0x7f}),
		wasmFuncType(nil, nil),
	))...)
	module = append(module, wasmSection(2, wasmVector(wasmImport(wasmHostModule, importName, 0)))...)
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
		wasmCode(nil, read),
	))...)
	if len(data) != 0 {
		module = append(module, wasmSection(11, wasmVector(wasmDataSegment(1024, data)))...)
	}
	return module
}
