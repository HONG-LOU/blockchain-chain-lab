package contracts

import (
	"testing"

	"chainlab/internal/state"
)

func TestWASMCallGasChargesInstructionFuel(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	runtime := NewRuntime()

	emptyContract, err := NewWasmContract(wasmNoopContractForTest(0))
	if err != nil {
		t.Fatal(err)
	}
	heavyContract, err := NewWasmContract(wasmNoopContractForTest(128))
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

func wasmNoopContractForTest(noops int) []byte {
	call := make([]byte, 0, noops+1)
	for i := 0; i < noops; i++ {
		call = append(call, 0x01)
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
