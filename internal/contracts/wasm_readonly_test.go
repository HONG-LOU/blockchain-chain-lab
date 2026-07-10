package contracts

import (
	"errors"
	"testing"

	"chainlab/internal/state"
)

func TestWASMReadCannotMutateStorage(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	runtime := NewRuntime()
	contract, err := NewWasmContract(wasmReadWritesStorageContractForTest())
	if err != nil {
		t.Fatal(err)
	}
	runtime.Register("wasm.read-write.v1", contract)
	address, _, err := runtime.Deploy(store, creator, "wasm.read-write.v1", "seed", nil)
	if err != nil {
		t.Fatal(err)
	}
	rootBefore := store.Root()

	_, err = runtime.Read(store, address, creator, "get", nil)
	if !errors.Is(err, ErrReadOnlyContract) {
		t.Fatalf("read mutation error = %v, want read-only contract error", err)
	}
	if got := store.GetStorage(address, "owned"); got != "" {
		t.Fatalf("read mutation persisted storage = %q", got)
	}
	if got := store.Root(); got != rootBefore {
		t.Fatalf("read mutation changed root: before=%s after=%s", rootBefore, got)
	}
}

func TestWASMReadCannotEmitEvent(t *testing.T) {
	var read []byte
	for index := 0; index < 6; index++ {
		read = append(read, wasmI32Const(0)...)
	}
	read = append(read, 0x10, 0x00, 0x1a, 0x0b)
	contract, err := NewWasmContract(wasmHostContractForTest("emit_event", 6, []byte{0x0b}, read, nil))
	if err != nil {
		t.Fatal(err)
	}
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	runtime := NewRuntime()
	runtime.Register("wasm.read-event.v1", contract)
	address, _, err := runtime.Deploy(store, creator, "wasm.read-event.v1", "seed", nil)
	if err != nil {
		t.Fatal(err)
	}
	rootBefore := store.Root()
	_, err = runtime.Read(store, address, creator, "get", nil)
	if !errors.Is(err, ErrReadOnlyContract) {
		t.Fatalf("read event error = %v, want read-only contract error", err)
	}
	if got := store.Root(); got != rootBefore {
		t.Fatalf("read event changed root: before=%s after=%s", rootBefore, got)
	}
}

func wasmReadWritesStorageContractForTest() []byte {
	const keyOffset uint32 = 1024
	const valueOffset uint32 = 1030
	read := append([]byte{}, wasmI32Const(keyOffset)...)
	read = append(read, wasmI32Const(uint32(len("owned")))...)
	read = append(read, wasmI32Const(valueOffset)...)
	read = append(read, wasmI32Const(uint32(len("yes")))...)
	read = append(read, 0x10)
	read = append(read, wasmU32(0)...)
	read = append(read, 0x1a, 0x0b)

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
		wasmCode(nil, []byte{0x0b}),
		wasmCode(nil, read),
	))...)
	module = append(module, wasmSection(11, wasmVector(wasmDataSegment(int32(keyOffset), []byte("ownedyes"))))...)
	return module
}
