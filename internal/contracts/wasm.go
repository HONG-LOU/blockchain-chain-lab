package contracts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"

	"chainlab/internal/types"
)

const wasmHostModule = "chainlab"

const (
	wasmInstantiateGas = 100
	wasmInstructionGas = 1
	wasmHostCallGas    = 100
	wasmByteGas        = 1
)

const wasmExecutionTimeout = 250 * time.Millisecond

type WasmContract struct {
	code []byte
}

func NewWasmContract(code []byte) (WasmContract, error) {
	if err := ValidateWasmCode(code); err != nil {
		return WasmContract{}, err
	}
	return WasmContract{code: append([]byte(nil), code...)}, nil
}

func NewWasmEchoContract() WasmContract {
	return WasmContract{code: wasmEchoModule()}
}

func WasmEchoCode() []byte {
	return wasmEchoModule()
}

func ValidateWasmCode(code []byte) error {
	if len(code) == 0 {
		return errors.New("wasm bytecode is required")
	}
	ctx := context.Background()
	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(1))
	defer runtime.Close(ctx)
	compiled, err := runtime.CompileModule(ctx, code)
	if err != nil {
		return fmt.Errorf("invalid wasm bytecode: %w", err)
	}
	defer compiled.Close(ctx)
	return nil
}

func (w WasmContract) Deploy(ctx Context, args map[string]string) ([]types.Event, error) {
	invocation := newWasmInvocation(ctx, args)
	if err := invocation.call(w.code, "deploy"); err != nil {
		return nil, err
	}
	return invocation.events, nil
}

func (w WasmContract) Call(ctx Context, method string, args map[string]string) ([]types.Event, error) {
	if method != "set" {
		return nil, fmt.Errorf("unknown wasm echo method %q", method)
	}
	invocation := newWasmInvocation(ctx, args)
	if err := invocation.call(w.code, "call"); err != nil {
		return nil, err
	}
	return invocation.events, nil
}

func (w WasmContract) Read(ctx Context, method string, args map[string]string) (string, error) {
	if method != "get" {
		return "", fmt.Errorf("unknown wasm echo read method %q", method)
	}
	invocation := newWasmInvocation(ctx, args)
	if err := invocation.call(w.code, "read"); err != nil {
		return "", err
	}
	return invocation.returnValue, nil
}

type wasmInvocation struct {
	contractCtx Context
	args        map[string]string
	events      []types.Event
	returnValue string
	err         error
}

func newWasmInvocation(ctx Context, args map[string]string) *wasmInvocation {
	if args == nil {
		args = map[string]string{}
	}
	return &wasmInvocation{contractCtx: ctx, args: args}
}

func (i *wasmInvocation) call(code []byte, export string) error {
	if !i.charge(wasmInstantiateGas + uint64(len(code))*wasmByteGas/32) {
		return i.err
	}
	instructionFuel, err := wasmExportedFunctionFuel(code, export)
	if err != nil {
		return err
	}
	if !i.charge(instructionFuel) {
		return i.err
	}
	ctx := context.Background()
	execCtx, cancel := context.WithTimeout(ctx, wasmExecutionTimeout)
	defer cancel()
	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().
		WithMemoryLimitPages(1).
		WithCloseOnContextDone(true))
	defer runtime.Close(ctx)

	if _, err := runtime.NewHostModuleBuilder(wasmHostModule).
		NewFunctionBuilder().WithFunc(i.argCopy).Export("arg_copy").
		NewFunctionBuilder().WithFunc(i.storageCopy).Export("storage_copy").
		NewFunctionBuilder().WithFunc(i.storageSet).Export("storage_set").
		NewFunctionBuilder().WithFunc(i.returnSet).Export("return_set").
		NewFunctionBuilder().WithFunc(i.emitEvent).Export("emit_event").
		Instantiate(execCtx); err != nil {
		if timeoutErr := wasmTimeoutError(execCtx); timeoutErr != nil {
			return timeoutErr
		}
		return err
	}

	module, err := runtime.Instantiate(execCtx, code)
	if err != nil {
		if timeoutErr := wasmTimeoutError(execCtx); timeoutErr != nil {
			return timeoutErr
		}
		return err
	}
	fn := module.ExportedFunction(export)
	if fn == nil {
		return fmt.Errorf("wasm export %q is missing", export)
	}
	if _, err := fn.Call(execCtx); err != nil {
		if timeoutErr := wasmTimeoutError(execCtx); timeoutErr != nil {
			return timeoutErr
		}
		return err
	}
	if timeoutErr := wasmTimeoutError(execCtx); timeoutErr != nil {
		return timeoutErr
	}
	return i.err
}

func wasmTimeoutError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("wasm execution timeout: %w", err)
	}
	return nil
}

func (i *wasmInvocation) argCopy(ctx context.Context, module api.Module, keyPtr uint32, keyLen uint32, dstPtr uint32) uint32 {
	key, ok := i.readString(module, keyPtr, keyLen)
	if !ok {
		return 0
	}
	value := i.args[key]
	if !i.charge(wasmHostCallGas + uint64(len(key)+len(value))*wasmByteGas) {
		return 0
	}
	return i.writeString(module, dstPtr, value)
}

func (i *wasmInvocation) storageCopy(ctx context.Context, module api.Module, keyPtr uint32, keyLen uint32, dstPtr uint32) uint32 {
	key, ok := i.readString(module, keyPtr, keyLen)
	if !ok {
		return 0
	}
	value := i.contractCtx.Store.GetStorage(i.contractCtx.Address, key)
	if !i.charge(wasmHostCallGas + uint64(len(key)+len(value))*wasmByteGas) {
		return 0
	}
	return i.writeString(module, dstPtr, value)
}

func (i *wasmInvocation) storageSet(ctx context.Context, module api.Module, keyPtr uint32, keyLen uint32, valuePtr uint32, valueLen uint32) uint32 {
	key, keyOK := i.readString(module, keyPtr, keyLen)
	value, valueOK := i.readString(module, valuePtr, valueLen)
	if !keyOK || !valueOK {
		return 1
	}
	if !i.charge(wasmHostCallGas + uint64(len(key)+len(value))*wasmByteGas) {
		return 1
	}
	i.contractCtx.Store.SetStorage(i.contractCtx.Address, key, value)
	return 0
}

func (i *wasmInvocation) returnSet(ctx context.Context, module api.Module, valuePtr uint32, valueLen uint32) uint32 {
	value, ok := i.readString(module, valuePtr, valueLen)
	if !ok {
		return 1
	}
	if !i.charge(wasmHostCallGas + uint64(len(value))*wasmByteGas) {
		return 1
	}
	i.returnValue = value
	return 0
}

func (i *wasmInvocation) emitEvent(ctx context.Context, module api.Module, typePtr uint32, typeLen uint32, keyPtr uint32, keyLen uint32, valuePtr uint32, valueLen uint32) uint32 {
	eventType, typeOK := i.readString(module, typePtr, typeLen)
	key, keyOK := i.readString(module, keyPtr, keyLen)
	value, valueOK := i.readString(module, valuePtr, valueLen)
	if !typeOK || !keyOK || !valueOK {
		return 1
	}
	if !i.charge(wasmHostCallGas + uint64(len(eventType)+len(key)+len(value))*wasmByteGas) {
		return 1
	}
	i.events = append(i.events, types.Event{Type: eventType, Attributes: map[string]string{key: value}})
	return 0
}

func (i *wasmInvocation) readString(module api.Module, ptr uint32, length uint32) (string, bool) {
	if i.err != nil {
		return "", false
	}
	memory := module.Memory()
	if memory == nil {
		i.err = errors.New("wasm module has no memory")
		return "", false
	}
	raw, ok := memory.Read(ptr, length)
	if !ok {
		i.err = fmt.Errorf("wasm memory read out of range: ptr=%d len=%d", ptr, length)
		return "", false
	}
	return string(raw), true
}

func (i *wasmInvocation) writeString(module api.Module, ptr uint32, value string) uint32 {
	if i.err != nil {
		return 0
	}
	memory := module.Memory()
	if memory == nil {
		i.err = errors.New("wasm module has no memory")
		return 0
	}
	if !memory.WriteString(ptr, value) {
		i.err = fmt.Errorf("wasm memory write out of range: ptr=%d len=%d", ptr, len(value))
		return 0
	}
	return uint32(len(value))
}

func (i *wasmInvocation) charge(amount uint64) bool {
	if i.err != nil {
		return false
	}
	if err := i.contractCtx.Meter.Charge(amount); err != nil {
		i.err = err
		return false
	}
	return true
}

func wasmExportedFunctionFuel(code []byte, exportName string) (uint64, error) {
	parser := wasmModuleParser{data: code}
	return parser.exportedFunctionFuel(exportName)
}

type wasmModuleParser struct {
	data             []byte
	offset           int
	importedFuncs    uint32
	exportedFuncs    map[string]uint32
	codeBodyFuelByID []uint64
}

func (p *wasmModuleParser) exportedFunctionFuel(exportName string) (uint64, error) {
	if len(p.data) < 8 || string(p.data[:4]) != "\x00asm" {
		return 0, errors.New("invalid wasm magic")
	}
	p.offset = 8
	p.exportedFuncs = make(map[string]uint32)
	for p.offset < len(p.data) {
		sectionID := p.data[p.offset]
		p.offset++
		sectionSize, ok := p.readU32()
		if !ok {
			return 0, errors.New("invalid wasm section size")
		}
		sectionStart := p.offset
		sectionEnd := sectionStart + int(sectionSize)
		if sectionEnd < sectionStart || sectionEnd > len(p.data) {
			return 0, errors.New("invalid wasm section bounds")
		}
		section := p.data[sectionStart:sectionEnd]
		switch sectionID {
		case 2:
			if err := p.parseImportSection(section); err != nil {
				return 0, err
			}
		case 7:
			if err := p.parseExportSection(section); err != nil {
				return 0, err
			}
		case 10:
			if err := p.parseCodeSection(section); err != nil {
				return 0, err
			}
		}
		p.offset = sectionEnd
	}
	funcIndex, ok := p.exportedFuncs[exportName]
	if !ok {
		return 0, nil
	}
	if funcIndex < p.importedFuncs {
		return 0, nil
	}
	bodyIndex := funcIndex - p.importedFuncs
	if int(bodyIndex) >= len(p.codeBodyFuelByID) {
		return 0, errors.New("wasm exported function has no code body")
	}
	return p.codeBodyFuelByID[bodyIndex], nil
}

func (p *wasmModuleParser) parseImportSection(section []byte) error {
	reader := wasmSectionReader{data: section}
	count, ok := reader.readU32()
	if !ok {
		return errors.New("invalid wasm import count")
	}
	for i := uint32(0); i < count; i++ {
		if _, ok := reader.readName(); !ok {
			return errors.New("invalid wasm import module name")
		}
		if _, ok := reader.readName(); !ok {
			return errors.New("invalid wasm import name")
		}
		kind, ok := reader.readByte()
		if !ok {
			return errors.New("invalid wasm import kind")
		}
		switch kind {
		case 0x00:
			if _, ok := reader.readU32(); !ok {
				return errors.New("invalid wasm imported function type")
			}
			p.importedFuncs++
		case 0x01:
			if _, ok := reader.readTableType(); !ok {
				return errors.New("invalid wasm imported table type")
			}
		case 0x02:
			if _, ok := reader.readLimits(); !ok {
				return errors.New("invalid wasm imported memory type")
			}
		case 0x03:
			if _, ok := reader.readGlobalType(); !ok {
				return errors.New("invalid wasm imported global type")
			}
		default:
			return errors.New("unsupported wasm import kind")
		}
	}
	return nil
}

func (p *wasmModuleParser) parseExportSection(section []byte) error {
	reader := wasmSectionReader{data: section}
	count, ok := reader.readU32()
	if !ok {
		return errors.New("invalid wasm export count")
	}
	for i := uint32(0); i < count; i++ {
		name, ok := reader.readName()
		if !ok {
			return errors.New("invalid wasm export name")
		}
		kind, ok := reader.readByte()
		if !ok {
			return errors.New("invalid wasm export kind")
		}
		index, ok := reader.readU32()
		if !ok {
			return errors.New("invalid wasm export index")
		}
		if kind == 0x00 {
			p.exportedFuncs[name] = index
		}
	}
	return nil
}

func (p *wasmModuleParser) parseCodeSection(section []byte) error {
	reader := wasmSectionReader{data: section}
	count, ok := reader.readU32()
	if !ok {
		return errors.New("invalid wasm code body count")
	}
	p.codeBodyFuelByID = make([]uint64, 0, count)
	for i := uint32(0); i < count; i++ {
		size, ok := reader.readU32()
		if !ok {
			return errors.New("invalid wasm code body size")
		}
		body, ok := reader.readBytes(size)
		if !ok {
			return errors.New("invalid wasm code body")
		}
		p.codeBodyFuelByID = append(p.codeBodyFuelByID, uint64(len(body))*wasmInstructionGas)
	}
	return nil
}

func (p *wasmModuleParser) readU32() (uint32, bool) {
	reader := wasmSectionReader{data: p.data, offset: p.offset}
	value, ok := reader.readU32()
	p.offset = reader.offset
	return value, ok
}

type wasmSectionReader struct {
	data   []byte
	offset int
}

func (r *wasmSectionReader) readByte() (byte, bool) {
	if r.offset >= len(r.data) {
		return 0, false
	}
	value := r.data[r.offset]
	r.offset++
	return value, true
}

func (r *wasmSectionReader) readBytes(length uint32) ([]byte, bool) {
	end := r.offset + int(length)
	if end < r.offset || end > len(r.data) {
		return nil, false
	}
	out := r.data[r.offset:end]
	r.offset = end
	return out, true
}

func (r *wasmSectionReader) readName() (string, bool) {
	length, ok := r.readU32()
	if !ok {
		return "", false
	}
	raw, ok := r.readBytes(length)
	if !ok {
		return "", false
	}
	return string(raw), true
}

func (r *wasmSectionReader) readU32() (uint32, bool) {
	var result uint32
	var shift uint
	for i := 0; i < 5; i++ {
		b, ok := r.readByte()
		if !ok {
			return 0, false
		}
		result |= uint32(b&0x7f) << shift
		if b&0x80 == 0 {
			return result, true
		}
		shift += 7
	}
	return 0, false
}

func (r *wasmSectionReader) readLimits() (struct{}, bool) {
	flag, ok := r.readByte()
	if !ok {
		return struct{}{}, false
	}
	switch flag {
	case 0x00:
		_, ok = r.readU32()
	case 0x01:
		_, ok = r.readU32()
		if ok {
			_, ok = r.readU32()
		}
	default:
		ok = false
	}
	return struct{}{}, ok
}

func (r *wasmSectionReader) readTableType() (struct{}, bool) {
	if _, ok := r.readByte(); !ok {
		return struct{}{}, false
	}
	return r.readLimits()
}

func (r *wasmSectionReader) readGlobalType() (struct{}, bool) {
	if _, ok := r.readByte(); !ok {
		return struct{}{}, false
	}
	if _, ok := r.readByte(); !ok {
		return struct{}{}, false
	}
	return struct{}{}, true
}

func wasmEchoModule() []byte {
	const dataOffset = 1024
	const bufferOffset = 2048
	data := []byte("messagelastwasm.echo.initializedwasm.echoedvalue")
	messageOffset := uint32(dataOffset)
	lastOffset := messageOffset + uint32(len("message"))
	initializedOffset := lastOffset + uint32(len("last"))
	echoedOffset := initializedOffset + uint32(len("wasm.echo.initialized"))
	valueOffset := echoedOffset + uint32(len("wasm.echoed"))

	var module []byte
	module = append(module, []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}...)
	module = append(module, wasmSection(1, wasmVector(
		wasmFuncType([]byte{0x7f, 0x7f, 0x7f}, []byte{0x7f}),
		wasmFuncType([]byte{0x7f, 0x7f, 0x7f, 0x7f}, []byte{0x7f}),
		wasmFuncType([]byte{0x7f, 0x7f}, []byte{0x7f}),
		wasmFuncType([]byte{0x7f, 0x7f, 0x7f, 0x7f, 0x7f, 0x7f}, []byte{0x7f}),
		wasmFuncType(nil, nil),
	))...)
	module = append(module, wasmSection(2, wasmVector(
		wasmImport(wasmHostModule, "arg_copy", 0),
		wasmImport(wasmHostModule, "storage_copy", 0),
		wasmImport(wasmHostModule, "storage_set", 1),
		wasmImport(wasmHostModule, "return_set", 2),
		wasmImport(wasmHostModule, "emit_event", 3),
	))...)
	module = append(module, wasmSection(3, wasmU32Vector(4, 4, 4))...)
	module = append(module, wasmSection(5, []byte{0x01, 0x01, 0x01, 0x01})...)
	module = append(module, wasmSection(7, wasmVector(
		wasmExport("memory", 0x02, 0),
		wasmExport("deploy", 0x00, 5),
		wasmExport("call", 0x00, 6),
		wasmExport("read", 0x00, 7),
	))...)
	module = append(module, wasmSection(10, wasmVector(
		wasmCode([]byte{0x7f}, wasmDeployInstructions(messageOffset, lastOffset, initializedOffset, valueOffset, bufferOffset)),
		wasmCode([]byte{0x7f}, wasmCallInstructions(messageOffset, lastOffset, echoedOffset, valueOffset, bufferOffset)),
		wasmCode([]byte{0x7f}, wasmReadInstructions(lastOffset, bufferOffset)),
	))...)
	module = append(module, wasmSection(11, wasmVector(wasmDataSegment(dataOffset, data)))...)
	return module
}

func wasmDeployInstructions(messageOffset uint32, lastOffset uint32, initializedOffset uint32, valueOffset uint32, bufferOffset uint32) []byte {
	var code []byte
	code = append(code, wasmCall3(0, messageOffset, uint32(len("message")), bufferOffset)...)
	code = append(code, wasmLocalSet(0)...)
	code = append(code, wasmCall4WithLocal(2, lastOffset, uint32(len("last")), bufferOffset, 0)...)
	code = append(code, 0x1a)
	code = append(code, wasmCall6WithLocal(4, initializedOffset, uint32(len("wasm.echo.initialized")), valueOffset, uint32(len("value")), bufferOffset, 0)...)
	code = append(code, 0x1a, 0x0b)
	return code
}

func wasmCallInstructions(messageOffset uint32, lastOffset uint32, echoedOffset uint32, valueOffset uint32, bufferOffset uint32) []byte {
	var code []byte
	code = append(code, wasmCall3(0, messageOffset, uint32(len("message")), bufferOffset)...)
	code = append(code, wasmLocalSet(0)...)
	code = append(code, wasmCall4WithLocal(2, lastOffset, uint32(len("last")), bufferOffset, 0)...)
	code = append(code, 0x1a)
	code = append(code, wasmCall6WithLocal(4, echoedOffset, uint32(len("wasm.echoed")), valueOffset, uint32(len("value")), bufferOffset, 0)...)
	code = append(code, 0x1a, 0x0b)
	return code
}

func wasmReadInstructions(lastOffset uint32, bufferOffset uint32) []byte {
	var code []byte
	code = append(code, wasmCall3(1, lastOffset, uint32(len("last")), bufferOffset)...)
	code = append(code, wasmLocalSet(0)...)
	code = append(code, wasmI32Const(bufferOffset)...)
	code = append(code, wasmLocalGet(0)...)
	code = append(code, 0x10)
	code = append(code, wasmU32(3)...)
	code = append(code, 0x1a, 0x0b)
	return code
}

func wasmCall3(functionIndex uint32, a uint32, b uint32, c uint32) []byte {
	var code []byte
	code = append(code, wasmI32Const(a)...)
	code = append(code, wasmI32Const(b)...)
	code = append(code, wasmI32Const(c)...)
	code = append(code, 0x10)
	code = append(code, wasmU32(functionIndex)...)
	return code
}

func wasmCall4WithLocal(functionIndex uint32, a uint32, b uint32, c uint32, local uint32) []byte {
	var code []byte
	code = append(code, wasmI32Const(a)...)
	code = append(code, wasmI32Const(b)...)
	code = append(code, wasmI32Const(c)...)
	code = append(code, wasmLocalGet(local)...)
	code = append(code, 0x10)
	code = append(code, wasmU32(functionIndex)...)
	return code
}

func wasmCall6WithLocal(functionIndex uint32, a uint32, b uint32, c uint32, d uint32, e uint32, local uint32) []byte {
	var code []byte
	code = append(code, wasmI32Const(a)...)
	code = append(code, wasmI32Const(b)...)
	code = append(code, wasmI32Const(c)...)
	code = append(code, wasmI32Const(d)...)
	code = append(code, wasmI32Const(e)...)
	code = append(code, wasmLocalGet(local)...)
	code = append(code, 0x10)
	code = append(code, wasmU32(functionIndex)...)
	return code
}

func wasmSection(id byte, payload []byte) []byte {
	out := []byte{id}
	out = append(out, wasmU32(uint32(len(payload)))...)
	out = append(out, payload...)
	return out
}

func wasmVector(items ...[]byte) []byte {
	out := wasmU32(uint32(len(items)))
	for _, item := range items {
		out = append(out, item...)
	}
	return out
}

func wasmU32Vector(values ...uint32) []byte {
	out := wasmU32(uint32(len(values)))
	for _, value := range values {
		out = append(out, wasmU32(value)...)
	}
	return out
}

func wasmFuncType(params []byte, results []byte) []byte {
	out := []byte{0x60}
	out = append(out, wasmU32(uint32(len(params)))...)
	out = append(out, params...)
	out = append(out, wasmU32(uint32(len(results)))...)
	out = append(out, results...)
	return out
}

func wasmImport(module string, name string, typeIndex uint32) []byte {
	var out []byte
	out = append(out, wasmName(module)...)
	out = append(out, wasmName(name)...)
	out = append(out, 0x00)
	out = append(out, wasmU32(typeIndex)...)
	return out
}

func wasmExport(name string, kind byte, index uint32) []byte {
	var out []byte
	out = append(out, wasmName(name)...)
	out = append(out, kind)
	out = append(out, wasmU32(index)...)
	return out
}

func wasmCode(locals []byte, instructions []byte) []byte {
	var body []byte
	if len(locals) == 0 {
		body = append(body, 0x00)
	} else {
		body = append(body, 0x01)
		body = append(body, wasmU32(uint32(len(locals)))...)
		body = append(body, locals...)
	}
	body = append(body, instructions...)
	out := wasmU32(uint32(len(body)))
	out = append(out, body...)
	return out
}

func wasmDataSegment(offset int32, data []byte) []byte {
	out := []byte{0x00}
	out = append(out, 0x41)
	out = append(out, wasmI32(offset)...)
	out = append(out, 0x0b)
	out = append(out, wasmU32(uint32(len(data)))...)
	out = append(out, data...)
	return out
}

func wasmName(value string) []byte {
	out := wasmU32(uint32(len(value)))
	out = append(out, value...)
	return out
}

func wasmI32Const(value uint32) []byte {
	out := []byte{0x41}
	out = append(out, wasmI32(int32(value))...)
	return out
}

func wasmLocalSet(index uint32) []byte {
	out := []byte{0x21}
	out = append(out, wasmU32(index)...)
	return out
}

func wasmLocalGet(index uint32) []byte {
	out := []byte{0x20}
	out = append(out, wasmU32(index)...)
	return out
}

func wasmU32(value uint32) []byte {
	var out []byte
	for {
		b := byte(value & 0x7f)
		value >>= 7
		if value != 0 {
			b |= 0x80
		}
		out = append(out, b)
		if value == 0 {
			return out
		}
	}
}

func wasmI32(value int32) []byte {
	var out []byte
	for {
		b := byte(value & 0x7f)
		value >>= 7
		done := (value == 0 && b&0x40 == 0) || (value == -1 && b&0x40 != 0)
		if !done {
			b |= 0x80
		}
		out = append(out, b)
		if done {
			return out
		}
	}
}
