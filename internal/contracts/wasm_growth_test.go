package contracts

import (
	"strings"
	"testing"

	wasmtime "github.com/bytecodealliance/wasmtime-go/v46"

	"chainlab/internal/state"
)

func TestWASMPolicyRejectsGrowableMemory(t *testing.T) {
	code := wasmMemoryGrowContractForTest(1)
	growableMemory := wasmSection(5, append([]byte{0x01, 0x01, 0x01}, wasmU32(256)...))
	code = replaceWasmSectionForTest(t, code, 5, growableMemory)
	if err := ValidateWasmCode(code); err == nil || !strings.Contains(err.Error(), "fixed") {
		t.Fatalf("growable memory validation error = %v", err)
	}
}

func TestWASMFixedMemoryCannotGrow(t *testing.T) {
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	runtime := NewRuntime()
	contract, err := NewWasmContract(wasmMemoryGrowContractForTest(1))
	if err != nil {
		t.Fatal(err)
	}
	runtime.Register("wasm.memory-grow.v1", contract)
	address, _, err := runtime.Deploy(store, creator, "wasm.memory-grow.v1", "seed", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtime.CallMeteredWithLimit(store, address, creator, "set", nil, 100_000); err != nil {
		t.Fatalf("fixed memory.grow must return -1 without trapping: %v", err)
	}
}

func TestWASMPolicyRejectsGrowableTable(t *testing.T) {
	code, err := wasmtime.Wat2Wasm(`
(module
  (table 1 1024 funcref)
  (memory (export "memory") 1 1)
  (func (export "deploy"))
  (func (export "call"))
  (func (export "read")))`)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateWasmCode(code); err == nil || !strings.Contains(err.Error(), "fixed") {
		t.Fatalf("growable table validation error = %v", err)
	}
}

func TestWASMFixedTableCannotGrow(t *testing.T) {
	code, err := wasmtime.Wat2Wasm(`
(module
  (table 1024 1024 funcref)
  (memory (export "memory") 1 1)
  (func (export "deploy"))
  (func (export "call")
    (if (i32.ne
          (table.grow (ref.null func) (i32.const 1))
          (i32.const -1))
      (then unreachable)))
  (func (export "read")))`)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := NewWasmContract(code)
	if err != nil {
		t.Fatal(err)
	}
	creator := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	runtime := NewRuntime()
	runtime.Register("wasm.table-grow.v1", contract)
	address, _, err := runtime.Deploy(store, creator, "wasm.table-grow.v1", "seed", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runtime.CallMeteredWithLimit(store, address, creator, "set", nil, 100_000)
	if err != nil {
		t.Fatalf("fixed table.grow must return -1 without trapping: %v", err)
	}
}

func wasmMemoryGrowContractForTest(delta uint32) []byte {
	call := append([]byte{}, wasmI32Const(delta)...)
	call = append(call, 0x40, 0x00)
	call = append(call, 0x41, 0x7f, 0x47, 0x04, 0x40, 0x00, 0x0b)
	call = append(call, 0x0b)

	var module []byte
	module = append(module, []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}...)
	module = append(module, wasmSection(1, wasmVector(wasmFuncType(nil, nil)))...)
	module = append(module, wasmSection(3, wasmU32Vector(0, 0, 0))...)
	module = append(module, wasmSection(5, []byte{0x01, 0x01, 0x02, 0x02})...)
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
