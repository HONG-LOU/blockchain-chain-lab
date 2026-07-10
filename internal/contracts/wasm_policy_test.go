package contracts

import (
	"strings"
	"testing"

	wasmtime "github.com/bytecodealliance/wasmtime-go/v46"
)

func TestWASMPolicyRejectsStartSection(t *testing.T) {
	code := wasmComputeContractForTest(0)
	start := wasmSection(8, wasmU32(0))
	code = insertWasmSectionBeforeCode(t, code, start)

	if err := ValidateWasmCode(code); err == nil || !strings.Contains(err.Error(), "start section") {
		t.Fatalf("start section validation error = %v", err)
	}
}

func TestWASMPolicyRejectsUnknownImport(t *testing.T) {
	code := wasmContractWithImportForTest("wasi_snapshot_preview1", "fd_write")
	if err := ValidateWasmCode(code); err == nil || !strings.Contains(err.Error(), "import") {
		t.Fatalf("unknown import validation error = %v", err)
	}
}

func TestWASMPolicyRejectsInvalidABI(t *testing.T) {
	tests := []struct {
		name string
		wat  string
		want string
	}{
		{
			name: "start export",
			wat: `(module
  (memory (export "memory") 1 1)
  (func (export "deploy")) (func (export "call"))
  (func (export "_start")))`,
			want: "not allowed",
		},
		{
			name: "wrong deploy signature",
			wat: `(module
  (memory (export "memory") 1 1)
  (func (export "deploy") (param i32)) (func (export "call")) (func (export "read")))`,
			want: "signature",
		},
		{
			name: "missing read",
			wat: `(module
  (memory (export "memory") 1 1)
  (func (export "deploy")) (func (export "call")))`,
			want: "required",
		},
		{
			name: "imported memory",
			wat: `(module
  (import "chainlab" "memory" (memory 1 1))
  (export "memory" (memory 0))
  (func (export "deploy")) (func (export "call")) (func (export "read")))`,
			want: "import",
		},
		{
			name: "imported table",
			wat: `(module
  (import "chainlab" "table" (table 1 1 funcref))
  (memory (export "memory") 1 1)
  (func (export "deploy")) (func (export "call")) (func (export "read")))`,
			want: "import",
		},
		{
			name: "imported global",
			wat: `(module
  (import "chainlab" "global" (global i32))
  (memory (export "memory") 1 1)
  (func (export "deploy")) (func (export "call")) (func (export "read")))`,
			want: "import",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code, err := wasmtime.Wat2Wasm(test.wat)
			if err != nil {
				t.Fatal(err)
			}
			err = ValidateWasmCode(code)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ABI validation error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestWASMPolicyRejectsOversizedModuleBeforeCompilation(t *testing.T) {
	code := make([]byte, wasmMaxModuleBytes+1)
	copy(code, wasmComputeContractForTest(0))
	if err := ValidateWasmCode(code); err == nil || !strings.Contains(err.Error(), "size") {
		t.Fatalf("oversized module validation error = %v", err)
	}
}

func TestWASMPolicyRejectsMemoryAboveVersionLimit(t *testing.T) {
	code := wasmComputeContractForTest(0)
	oversizedMemory := wasmSection(5, append([]byte{0x01, 0x01}, append(wasmU32(257), wasmU32(257)...)...))
	code = replaceWasmSectionForTest(t, code, 5, oversizedMemory)
	if err := ValidateWasmCode(code); err == nil || !strings.Contains(err.Error(), "memory") {
		t.Fatalf("oversized memory validation error = %v", err)
	}
}

func TestWASMPolicyRejectsTableAboveVersionLimit(t *testing.T) {
	code, err := wasmtime.Wat2Wasm(`
(module
  (table 1025 1025 funcref)
  (memory (export "memory") 1 1)
  (func (export "deploy"))
  (func (export "call"))
  (func (export "read")))`)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateWasmCode(code); err == nil || !strings.Contains(err.Error(), "table") {
		t.Fatalf("oversized table validation error = %v", err)
	}
}

func TestWASMEnvelopeRejectsMalformedStructure(t *testing.T) {
	base := wasmComputeContractForTest(0)
	tests := []struct {
		name string
		code func(t *testing.T) []byte
		want string
	}{
		{
			name: "duplicate section",
			code: func(t *testing.T) []byte {
				return insertWasmSectionBeforeCode(t, base, wasmSection(5, []byte{0x01, 0x01, 0x01, 0x01}))
			},
			want: "duplicated",
		},
		{
			name: "out of order section",
			code: func(t *testing.T) []byte {
				return insertWasmSectionBeforeCode(t, base, wasmSection(4, []byte{0x00}))
			},
			want: "out of order",
		},
		{
			name: "unknown section",
			code: func(t *testing.T) []byte {
				return insertWasmSectionBeforeCode(t, base, wasmSection(13, nil))
			},
			want: "not allowed",
		},
		{
			name: "trailing memory payload",
			code: func(t *testing.T) []byte {
				return replaceWasmSectionForTest(t, base, 5, wasmSection(5, []byte{0x01, 0x01, 0x01, 0x01, 0x00}))
			},
			want: "trailing data",
		},
		{
			name: "function code count mismatch",
			code: func(t *testing.T) []byte {
				code := wasmSection(10, wasmVector(
					wasmCode(nil, []byte{0x0b}),
					wasmCode(nil, []byte{0x0b}),
				))
				return replaceWasmSectionForTest(t, base, 10, code)
			},
			want: "counts differ",
		},
		{
			name: "overflowing section size",
			code: func(_ *testing.T) []byte {
				code := append([]byte(nil), base[:8]...)
				return append(code, 0x01, 0xff, 0xff, 0xff, 0xff, 0x10)
			},
			want: "section size",
		},
		{
			name: "duplicate export",
			code: func(t *testing.T) []byte {
				exports := wasmSection(7, wasmVector(
					wasmExport("memory", 0x02, 0),
					wasmExport("deploy", 0x00, 0),
					wasmExport("call", 0x00, 1),
					wasmExport("deploy", 0x00, 2),
				))
				return replaceWasmSectionForTest(t, base, 7, exports)
			},
			want: "duplicated",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateWasmCode(test.code(t))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("malformed module error = %v, want %q", err, test.want)
			}
		})
	}
}

func insertWasmSectionBeforeCode(t *testing.T, code []byte, section []byte) []byte {
	t.Helper()
	offset := 8
	for offset < len(code) {
		sectionStart := offset
		sectionID := code[offset]
		offset++
		reader := wasmSectionReader{data: code, offset: offset}
		size, ok := reader.readU32()
		if !ok {
			t.Fatal("invalid test wasm section size")
		}
		offset = reader.offset + int(size)
		if sectionID == 10 {
			out := append([]byte(nil), code[:sectionStart]...)
			out = append(out, section...)
			return append(out, code[sectionStart:]...)
		}
	}
	t.Fatal("test wasm code section not found")
	return nil
}

func replaceWasmSectionForTest(t *testing.T, code []byte, wantedID byte, replacement []byte) []byte {
	t.Helper()
	offset := 8
	for offset < len(code) {
		sectionStart := offset
		sectionID := code[offset]
		offset++
		reader := wasmSectionReader{data: code, offset: offset}
		size, ok := reader.readU32()
		if !ok {
			t.Fatal("invalid test wasm section size")
		}
		sectionEnd := reader.offset + int(size)
		if sectionID == wantedID {
			out := append([]byte(nil), code[:sectionStart]...)
			out = append(out, replacement...)
			return append(out, code[sectionEnd:]...)
		}
		offset = sectionEnd
	}
	t.Fatalf("test wasm section %d not found", wantedID)
	return nil
}

func wasmContractWithImportForTest(moduleName string, importName string) []byte {
	var module []byte
	module = append(module, []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}...)
	module = append(module, wasmSection(1, wasmVector(wasmFuncType(nil, nil)))...)
	module = append(module, wasmSection(2, wasmVector(wasmImport(moduleName, importName, 0)))...)
	module = append(module, wasmSection(3, wasmU32Vector(0, 0, 0))...)
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
		wasmCode(nil, []byte{0x0b}),
	))...)
	return module
}
