package contracts

import (
	"errors"
	"strings"
	"testing"

	"chainlab/internal/state"
)

func TestWASMCallStopsInfiniteLoopWithDeterministicFuel(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	loopContract, err := NewWasmContract(wasmInfiniteLoopContractForTest())
	if err != nil {
		t.Fatal(err)
	}

	var firstGas uint64
	for run := 0; run < 2; run++ {
		store := state.NewStore()
		runtime := NewRuntime()
		runtime.Register("wasm.loop.v1", loopContract)
		addr, _, err := runtime.Deploy(store, creator, "wasm.loop.v1", "seed-loop", nil)
		if err != nil {
			t.Fatal(err)
		}

		gasLimit := loopContract.resources.instantiationGas() + 500
		if gasLimit <= loopContract.resources.instantiationGas() {
			t.Fatal("loop test must reserve guest fuel after instantiation")
		}
		_, gasUsed, err := runtime.CallMeteredWithLimit(store, addr, creator, "set", nil, gasLimit)
		if !errors.Is(err, ErrContractOutOfGas) {
			t.Fatalf("infinite loop wasm error = %v, want contract out of gas", err)
		}
		if !strings.Contains(strings.ToLower(err.Error()), "out of gas") {
			t.Fatalf("infinite loop wasm error = %q", err)
		}
		if gasUsed != gasLimit {
			t.Fatalf("infinite loop gas used = %d, want %d", gasUsed, gasLimit)
		}
		if run == 0 {
			firstGas = gasUsed
		} else if gasUsed != firstGas {
			t.Fatalf("infinite loop gas differs: first=%d second=%d", firstGas, gasUsed)
		}
	}
}

func wasmInfiniteLoopContractForTest() []byte {
	loop := []byte{0x03, 0x40, 0x0c, 0x00, 0x0b, 0x0b}

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
		wasmCode(nil, loop),
		wasmCode(nil, []byte{0x0b}),
	))...)
	return module
}
