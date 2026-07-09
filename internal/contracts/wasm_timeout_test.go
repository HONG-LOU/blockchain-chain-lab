package contracts

import (
	"strings"
	"testing"
	"time"

	"chainlab/internal/state"
)

func TestWASMCallInterruptsInfiniteLoop(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	runtime := NewRuntime()

	loopContract, err := NewWasmContract(wasmInfiniteLoopContractForTest())
	if err != nil {
		t.Fatal(err)
	}
	runtime.Register("wasm.loop.v1", loopContract)

	addr, _, err := runtime.Deploy(store, creator, "wasm.loop.v1", "seed-loop", nil)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, _, callErr := runtime.CallMetered(store, addr, creator, "set", nil)
		done <- callErr
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("infinite loop wasm call returned nil error")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "wasm execution timeout") {
			t.Fatalf("infinite loop wasm error = %q, want wasm execution timeout", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("infinite loop wasm call did not return before timeout guard")
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
