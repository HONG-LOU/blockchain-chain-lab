package contracts

import (
	"testing"

	wasmtime "github.com/bytecodealliance/wasmtime-go/v46"

	"chainlab/internal/state"
)

func TestWASMCallGasChargesInstructionFuel(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	runtime := NewRuntime()

	emptyContract, err := NewWasmContract(wasmComputeContractForTest(0))
	if err != nil {
		t.Fatal(err)
	}
	heavyContract, err := NewWasmContract(wasmComputeContractForTest(128))
	if err != nil {
		t.Fatal(err)
	}
	runtime.Register("wasm.empty.v1", emptyContract)
	runtime.Register("wasm.heavy.v1", heavyContract)

	emptyAddr, _, err := runtime.Deploy(store, creator, "wasm.empty.v1", "seed-empty", nil)
	if err != nil {
		t.Fatal(err)
	}
	heavyAddr, _, err := runtime.Deploy(store, creator, "wasm.heavy.v1", "seed-heavy", nil)
	if err != nil {
		t.Fatal(err)
	}

	_, emptyGas, err := runtime.CallMetered(store, emptyAddr, creator, "set", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, heavyGas, err := runtime.CallMetered(store, heavyAddr, creator, "set", nil)
	if err != nil {
		t.Fatal(err)
	}

	if heavyGas-emptyGas < 128 {
		t.Fatalf("heavy wasm gas delta = %d, empty=%d heavy=%d; want at least one gas per guest instruction", heavyGas-emptyGas, emptyGas, heavyGas)
	}
}

func TestWASMInstantiationGasPricesDeclaredResources(t *testing.T) {
	code, err := wasmtime.Wat2Wasm(`
(module
  (table 3 3 funcref)
  (memory (export "memory") 2 2)
  (func (export "deploy"))
  (func (export "call"))
  (func (export "read")))`)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := NewWasmContract(code)
	if err != nil {
		t.Fatal(err)
	}
	const wantCodeBytes = 80
	const wantInstantiationGas = 2210
	if len(code) != wantCodeBytes || contract.resources.instantiationGas() != wantInstantiationGas {
		t.Fatalf("version-1 instantiation vector changed: code_bytes=%d gas=%d", len(code), contract.resources.instantiationGas())
	}
	if contract.resources.codeBytes != uint64(len(code)) || contract.resources.memoryPages != 2 || contract.resources.tableElements != 3 {
		t.Fatalf("module resources = %+v, code bytes=%d", contract.resources, len(code))
	}
	want := wasmInstantiateGas + uint64(len(code))*wasmInstantiationByteGas +
		2*wasmMemoryPageGas + 3*wasmTableElementGas
	if got := contract.resources.instantiationGas(); got != want {
		t.Fatalf("instantiation gas = %d, want %d", got, want)
	}
}

func wasmComputeContractForTest(operations int) []byte {
	call := make([]byte, 0, operations*3+1)
	for i := 0; i < operations; i++ {
		call = append(call, 0x41, 0x00, 0x1a)
	}
	call = append(call, 0x0b)

	var module []byte
	module = append(module, []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}...)
	module = append(module, wasmSection(1, wasmVector(wasmFuncType(nil, nil)))...)
	module = append(module, wasmSection(3, wasmU32Vector(0, 0, 0))...)
	module = append(module, wasmSection(5, []byte{0x01, 0x01, 0x01, 0x01})...)
	module = append(module, wasmSection(7, wasmVector(
		wasmExport("memory", 0x02, 0),
		wasmExport("deploy", 0x00, 0),
		wasmExport("call", 0x00, 1),
		wasmExport("read", 0x00, 2),
	))...)
	module = append(module, wasmSection(10, wasmVector(
		wasmCode(nil, []byte{0x0b}),
		wasmCode(nil, call),
		wasmCode(nil, []byte{0x0b}),
	))...)
	return module
}
