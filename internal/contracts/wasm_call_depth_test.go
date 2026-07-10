package contracts

import (
	"strings"
	"testing"
)

func TestWASMDirectCallDepthAdmissionBoundary(t *testing.T) {
	if err := ValidateWasmCode(wasmDirectCallChainForTest(int(MaxWASMCallDepth))); err != nil {
		t.Fatalf("depth %d validation error = %v", MaxWASMCallDepth, err)
	}
	err := ValidateWasmCode(wasmDirectCallChainForTest(int(MaxWASMCallDepth) + 1))
	if err == nil || !strings.Contains(err.Error(), "direct call depth") {
		t.Fatalf("depth %d validation error = %v", MaxWASMCallDepth+1, err)
	}
}

func TestWASMDirectCallGraphRejectsRecursion(t *testing.T) {
	tests := []struct {
		name string
		code []byte
	}{
		{name: "self", code: wasmRecursiveContractForTest()},
		{name: "mutual", code: wasmMutuallyRecursiveContractForTest()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateWasmCode(test.code)
			if err == nil || !strings.Contains(err.Error(), "recursion") {
				t.Fatalf("recursive graph validation error = %v", err)
			}
		})
	}
}

func TestWASMCallDepthPolicyRejectsIndirectAndTailCalls(t *testing.T) {
	tests := []struct {
		name         string
		code         []byte
		instructions []byte
		want         string
	}{
		{name: "call_indirect", code: wasmIndirectLoopContractForTest(), instructions: []byte{0x11, 0x00, 0x00}, want: "call_indirect"},
		{name: "return_call", code: wasmModuleWithCallOpcodeForTest(0x12), instructions: []byte{0x12, 0x00}, want: "forbidden"},
		{name: "return_call_indirect", code: wasmModuleWithCallOpcodeForTest(0x13), instructions: []byte{0x13, 0x00, 0x00}, want: "forbidden"},
		{name: "call_ref", code: wasmModuleWithCallOpcodeForTest(0x14), instructions: []byte{0x14, 0x00}, want: "forbidden"},
		{name: "return_call_ref", code: wasmModuleWithCallOpcodeForTest(0x15), instructions: []byte{0x15, 0x00}, want: "forbidden"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateWasmCode(test.code)
			if err == nil {
				t.Fatalf("%s validation error = %v", test.name, err)
			}
			_, err = parseWasmDirectCalls(test.instructions)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("%s instruction policy error = %v", test.name, err)
			}
		})
	}
}

func TestWASMCallDepthParserSkipsInstructionImmediates(t *testing.T) {
	body := []byte{0x41, 0x10, 0x1a, 0x41, 0x11, 0x1a, 0x0b}
	code := wasmModuleWithFunctionsForTest(
		[]uint32{0, 0, 0},
		[]uint32{0, 1, 2},
		[][]byte{body, {0x0b}, {0x0b}},
		nil,
		nil,
	)
	if err := ValidateWasmCode(code); err != nil {
		t.Fatalf("instruction immediate validation error = %v", err)
	}
}

func TestWASMCallDepthParserDecodesAllowedImmediates(t *testing.T) {
	instructions := []byte{
		0x02, 0x40,
		0x0c, 0x10,
		0x0e, 0x01, 0x10, 0x11,
		0x1c, 0x01, 0x7f,
		0x20, 0x10,
		0x28, 0x10, 0x11,
		0x3f, 0x10,
		0x41, 0x10,
		0x42, 0x11,
		0x43, 0x10, 0x11, 0xfc, 0xff,
		0x44, 0x10, 0x11, 0xfc, 0xff, 0x10, 0x11, 0xfc, 0xff,
		0xd0, 0x70,
		0xd2, 0x10,
		0xfc, 0x0f, 0x10,
		0x10, 0x02,
	}
	directCalls, err := parseWasmDirectCalls(instructions)
	if err != nil {
		t.Fatal(err)
	}
	if len(directCalls) != 1 || directCalls[0] != 2 {
		t.Fatalf("decoded direct calls = %v, want [2]", directCalls)
	}
}

func TestWASMCallDepthParserFailsClosedOnUnsupportedOpcodes(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{name: "unknown", body: []byte{0xff, 0x0b}},
		{name: "SIMD proposal", body: []byte{0xfd, 0x00, 0x0b}},
		{name: "bulk memory proposal", body: []byte{0xfc, 0x08, 0x00, 0x00, 0x0b}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			code := wasmModuleWithFunctionsForTest(
				[]uint32{0, 0, 0},
				[]uint32{0, 1, 2},
				[][]byte{test.body, {0x0b}, {0x0b}},
				nil,
				nil,
			)
			err := ValidateWasmCode(code)
			if err == nil {
				t.Fatalf("unsupported opcode validation error = %v", err)
			}
			_, err = parseWasmDirectCalls(test.body)
			if err == nil || !strings.Contains(err.Error(), "not allowed") {
				t.Fatalf("unsupported opcode policy error = %v", err)
			}
		})
	}
}

func TestWASMCallDepthAdmissionIsStableAcrossFreshCompiles(t *testing.T) {
	accepted := wasmDirectCallChainForTest(int(MaxWASMCallDepth))
	rejected := wasmDirectCallChainForTest(int(MaxWASMCallDepth) + 1)
	var rejectedError string
	for attempt := 0; attempt < 3; attempt++ {
		if err := ValidateWasmCode(append([]byte(nil), accepted...)); err != nil {
			t.Fatalf("fresh compile %d accepted-module error = %v", attempt, err)
		}
		err := ValidateWasmCode(append([]byte(nil), rejected...))
		if err == nil {
			t.Fatalf("fresh compile %d accepted an over-depth module", attempt)
		}
		if attempt == 0 {
			rejectedError = err.Error()
		} else if err.Error() != rejectedError {
			t.Fatalf("fresh compile %d error = %q, first = %q", attempt, err, rejectedError)
		}
	}
}

func wasmDirectCallChainForTest(totalDepth int) []byte {
	definedFunctions := totalDepth - 1
	functionTypes := make([]uint32, definedFunctions)
	for index := range functionTypes {
		functionTypes[index] = 1
	}
	bodies := make([][]byte, definedFunctions)
	for index := 0; index < definedFunctions-1; index++ {
		body := []byte{0x10}
		body = append(body, wasmU32(uint32(index+2))...)
		bodies[index] = append(body, 0x0b)
	}
	lastBody := append([]byte{}, wasmI32Const(0)...)
	lastBody = append(lastBody, wasmI32Const(0)...)
	lastBody = append(lastBody, 0x10, 0x00, 0x1a, 0x0b)
	bodies[definedFunctions-1] = lastBody

	var module []byte
	module = append(module, []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}...)
	module = append(module, wasmSection(1, wasmVector(
		wasmFuncType([]byte{0x7f, 0x7f}, []byte{0x7f}),
		wasmFuncType(nil, nil),
	))...)
	module = append(module, wasmSection(2, wasmVector(wasmImport(wasmHostModule, "return_set", 0)))...)
	module = append(module, wasmSection(3, wasmU32Vector(functionTypes...))...)
	module = append(module, wasmSection(5, []byte{0x01, 0x01, 0x01, 0x01})...)
	module = append(module, wasmSection(7, wasmVector(
		wasmExport("memory", 0x02, 0),
		wasmExport("deploy", 0x00, 1),
		wasmExport("call", 0x00, 1),
		wasmExport("read", 0x00, 1),
	))...)
	codeBodies := make([][]byte, len(bodies))
	for index, body := range bodies {
		codeBodies[index] = wasmCode(nil, body)
	}
	module = append(module, wasmSection(10, wasmVector(codeBodies...))...)
	return module
}

func wasmMutuallyRecursiveContractForTest() []byte {
	return wasmModuleWithFunctionsForTest(
		[]uint32{0, 0, 0},
		[]uint32{0, 1, 2},
		[][]byte{{0x10, 0x01, 0x0b}, {0x10, 0x00, 0x0b}, {0x0b}},
		nil,
		nil,
	)
}

func wasmModuleWithCallOpcodeForTest(opcode byte) []byte {
	return wasmModuleWithFunctionsForTest(
		[]uint32{0, 0, 0},
		[]uint32{0, 1, 2},
		[][]byte{{opcode, 0x00, 0x00, 0x0b}, {0x0b}, {0x0b}},
		nil,
		nil,
	)
}
