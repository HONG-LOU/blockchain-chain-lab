package contracts

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"unicode/utf8"

	wasmtime "github.com/bytecodealliance/wasmtime-go/v46"

	"chainlab/internal/types"
)

const wasmHostModule = "chainlab"

const (
	wasmInstantiateGas       uint64 = 100
	wasmHostCallGas          uint64 = 100
	wasmByteGas              uint64 = 1
	wasmStorageGrowthGas     uint64 = 4
	wasmInstantiationByteGas uint64 = 1
	wasmMemoryPageGas        uint64 = 1000
	wasmTableElementGas      uint64 = 10
	wasmMaxModuleBytes              = 512 * 1024
	wasmMaxMemoryBytes       int64  = 16 * 1024 * 1024
	wasmMaxMemoryPages       uint64 = 256
	wasmMaxTableElements     int64  = 1024
	wasmMaxStackBytes               = 512 * 1024
	wasmMaxKeyBytes          uint32 = 256
	wasmMaxValueBytes        uint32 = 64 * 1024
	wasmMaxReturnBytes       uint32 = 64 * 1024
	wasmMaxEventTypeBytes    uint32 = 128
	wasmMaxEventKeyBytes     uint32 = 256
	wasmMaxEventValueBytes   uint32 = 16 * 1024
	wasmMaxEvents                   = 64
	wasmMaxEventBytes        uint64 = 64 * 1024
	wasmMaxStorageWrites            = 128
	wasmMaxHostIOBytes       uint64 = 1024 * 1024
	wasmMaxTypes                    = 1024
	wasmMaxFunctions                = 4096
	wasmMaxGlobals                  = 256
	wasmMaxSegments                 = 256
	wasmMaxParams                   = 64
	wasmMaxResults                  = 1
	wasmMaxLocalsPerFunction        = 1024
	wasmMaxLocalsPerModule          = 16 * 1024
)

const (
	MaxWASMModuleBytes  = wasmMaxModuleBytes
	WASMMeteringVersion = "chainlab-wasm-v1"
	WASMRuntimeVersion  = "wasmtime-go-v46.0.1"
)

var (
	ErrReadOnlyContract  = errors.New("contract read-only capability violation")
	ErrWasmResourceLimit = errors.New("wasm resource limit exceeded")
	ErrWasmGuestTrap     = errors.New("wasm guest trap")
	ErrWasmInvalidUTF8   = errors.New("wasm host data must be valid UTF-8")
	ErrWasmRuntimeFault  = errors.New("wasm runtime fault")
)

var chainlabWasmEngine = newChainlabWasmEngine()

type WasmContract struct {
	module    *wasmtime.Module
	resources wasmModuleResources
}

type wasmModuleResources struct {
	codeBytes     uint64
	memoryPages   uint64
	tableElements uint64
}

type wasmBinarySignature struct {
	params  []byte
	results []byte
}

type wasmBinaryPolicy struct {
	types                 []wasmBinarySignature
	importedFunctionTypes []uint32
	functionTypes         []uint32
	seenImports           map[string]struct{}
	seenExports           map[string]struct{}
	seenSections          map[byte]struct{}
	requiredExports       map[string]bool
	lastSectionID         byte
	functionCount         uint32
	codeBodyCount         uint32
	memoryDefined         bool
	memoryExported        bool
}

func NewWasmContract(code []byte) (WasmContract, error) {
	return newWasmContract(code, false)
}

func newAdmittedWasmContract(code []byte) (WasmContract, error) {
	return newWasmContract(code, true)
}

func newWasmContract(code []byte, admitted bool) (WasmContract, error) {
	module, resources, err := compileWasmModule(code, admitted)
	if err != nil {
		return WasmContract{}, err
	}
	return WasmContract{module: module, resources: resources}, nil
}

func NewWasmEchoContract() WasmContract {
	contract, err := NewWasmContract(wasmEchoModule())
	if err != nil {
		panic(fmt.Sprintf("invalid built-in wasm echo contract: %v", err))
	}
	return contract
}

func WasmEchoCode() []byte {
	return wasmEchoModule()
}

func ValidateWasmCode(code []byte) error {
	module, _, err := compileWasmModule(code, false)
	if err != nil {
		return err
	}
	module.Close()
	return nil
}

func newChainlabWasmEngine() *wasmtime.Engine {
	config := wasmtime.NewConfig()
	config.SetConsumeFuel(true)
	config.SetStrategy(wasmtime.StrategyCranelift)
	config.SetCraneliftOptLevel(wasmtime.OptLevelSpeed)
	config.SetCraneliftNanCanonicalization(true)
	config.SetParallelCompilation(false)
	config.SetMaxWasmStack(wasmMaxStackBytes)
	config.SetWasmSIMD(false)
	config.SetWasmRelaxedSIMD(false)
	config.SetWasmBulkMemory(false)
	config.SetWasmMultiValue(false)
	config.SetWasmMultiMemory(false)
	config.SetWasmMemory64(false)
	config.SetWasmTailCall(false)
	config.SetGCSupport(false)
	config.SetWasmWideArithmetic(false)
	config.SetWasmThreads(false)
	config.SetWasmReferenceTypes(true)
	config.SetWasmFunctionReferences(false)
	config.SetWasmComponentModel(false)
	return wasmtime.NewEngineWithConfig(config)
}

func compileWasmModule(code []byte, admitted bool) (*wasmtime.Module, wasmModuleResources, error) {
	resources := wasmModuleResources{codeBytes: uint64(len(code))}
	if len(code) == 0 {
		return nil, wasmModuleResources{}, errors.New("wasm bytecode is required")
	}
	if len(code) > wasmMaxModuleBytes {
		return nil, wasmModuleResources{}, fmt.Errorf("wasm module size exceeds %d bytes", wasmMaxModuleBytes)
	}
	if err := validateWasmEnvelope(code, &resources); err != nil {
		if admitted {
			return nil, wasmModuleResources{}, fmt.Errorf("%w: admitted wasm envelope mismatch", ErrWasmRuntimeFault)
		}
		return nil, wasmModuleResources{}, err
	}
	if err := wasmtime.ModuleValidate(chainlabWasmEngine, code); err != nil {
		if admitted {
			return nil, wasmModuleResources{}, fmt.Errorf("%w: admitted wasm validation failed", ErrWasmRuntimeFault)
		}
		return nil, wasmModuleResources{}, errors.New("invalid wasm bytecode")
	}
	module, err := wasmtime.NewModule(chainlabWasmEngine, code)
	if err != nil {
		return nil, wasmModuleResources{}, fmt.Errorf("%w: wasm compilation failed", ErrWasmRuntimeFault)
	}
	if err := validateWasmABI(module, &resources); err != nil {
		module.Close()
		if admitted {
			return nil, wasmModuleResources{}, fmt.Errorf("%w: admitted wasm ABI mismatch", ErrWasmRuntimeFault)
		}
		return nil, wasmModuleResources{}, err
	}
	return module, resources, nil
}

func validateWasmEnvelope(code []byte, resources *wasmModuleResources) error {
	if len(code) < 8 || string(code[:4]) != "\x00asm" || string(code[4:8]) != "\x01\x00\x00\x00" {
		return errors.New("invalid wasm bytecode header")
	}
	policy := wasmBinaryPolicy{
		seenImports:     make(map[string]struct{}),
		seenExports:     make(map[string]struct{}),
		seenSections:    make(map[byte]struct{}),
		requiredExports: map[string]bool{"deploy": false, "call": false, "read": false},
	}
	for offset := 8; offset < len(code); {
		sectionID := code[offset]
		offset++
		reader := wasmSectionReader{data: code, offset: offset}
		sectionSize, ok := reader.readU32()
		if !ok {
			return errors.New("invalid wasm section size")
		}
		sectionEnd := uint64(reader.offset) + uint64(sectionSize)
		if sectionEnd > uint64(len(code)) {
			return errors.New("invalid wasm section bounds")
		}
		section := code[reader.offset:int(sectionEnd)]
		if sectionID != 0 {
			if sectionID > 12 {
				return fmt.Errorf("wasm section id %d is not allowed", sectionID)
			}
			if sectionID == 8 {
				return errors.New("wasm start section is forbidden")
			}
			if sectionID == 12 {
				return errors.New("wasm data-count section requires forbidden bulk memory")
			}
			if _, duplicate := policy.seenSections[sectionID]; duplicate {
				return fmt.Errorf("wasm section id %d is duplicated", sectionID)
			}
			if sectionID < policy.lastSectionID {
				return fmt.Errorf("wasm section id %d is out of order", sectionID)
			}
			policy.seenSections[sectionID] = struct{}{}
			policy.lastSectionID = sectionID
		}
		switch sectionID {
		case 1:
			if err := validateWasmTypeSection(section, &policy); err != nil {
				return err
			}
		case 2:
			if err := validateWasmImportSection(section, &policy); err != nil {
				return err
			}
		case 3:
			if err := validateWasmFunctionSection(section, &policy); err != nil {
				return err
			}
		case 4:
			if err := validateWasmTableSection(section, resources); err != nil {
				return err
			}
		case 5:
			if err := validateWasmMemorySection(section, &policy, resources); err != nil {
				return err
			}
		case 6:
			if err := validateWasmVectorCount(section, wasmMaxGlobals, "globals"); err != nil {
				return err
			}
		case 7:
			if err := validateWasmExportSection(section, &policy); err != nil {
				return err
			}
		case 9, 11:
			if err := validateWasmVectorCount(section, wasmMaxSegments, "segments"); err != nil {
				return err
			}
		case 10:
			if err := validateWasmCodeSection(section, &policy); err != nil {
				return err
			}
		}
		offset = int(sectionEnd)
	}
	return policy.validateComplete()
}

func validateWasmVectorCount(section []byte, maximum uint32, name string) error {
	reader := wasmSectionReader{data: section}
	count, ok := reader.readU32()
	if !ok {
		return fmt.Errorf("invalid wasm %s section", name)
	}
	if count > maximum {
		return fmt.Errorf("wasm %s exceed the version-1 limit", name)
	}
	return nil
}

func validateWasmTypeSection(section []byte, policy *wasmBinaryPolicy) error {
	reader := wasmSectionReader{data: section}
	count, ok := reader.readU32()
	if !ok || count > wasmMaxTypes {
		return errors.New("wasm types exceed the version-1 limit")
	}
	for index := uint32(0); index < count; index++ {
		form, ok := reader.readByte()
		if !ok || form != 0x60 {
			return errors.New("invalid wasm function type")
		}
		params, ok := reader.readU32()
		if !ok || params > wasmMaxParams {
			return errors.New("wasm function parameters exceed the version-1 limit")
		}
		paramTypes, ok := reader.readBytes(params)
		if !ok {
			return errors.New("invalid wasm function parameters")
		}
		results, ok := reader.readU32()
		if !ok || results > wasmMaxResults {
			return errors.New("wasm function results exceed the version-1 limit")
		}
		resultTypes, ok := reader.readBytes(results)
		if !ok {
			return errors.New("invalid wasm function results")
		}
		policy.types = append(policy.types, wasmBinarySignature{
			params:  append([]byte(nil), paramTypes...),
			results: append([]byte(nil), resultTypes...),
		})
	}
	if !reader.exhausted() {
		return errors.New("wasm type section has trailing data")
	}
	return nil
}

func validateWasmImportSection(section []byte, policy *wasmBinaryPolicy) error {
	reader := wasmSectionReader{data: section}
	count, ok := reader.readU32()
	if !ok || count > uint32(len(wasmHostSignatures)) {
		return errors.New("wasm imports exceed the version-1 limit")
	}
	for index := uint32(0); index < count; index++ {
		moduleName, ok := reader.readName()
		if !ok || moduleName != wasmHostModule {
			return errors.New("wasm imports must use the chainlab module")
		}
		name, ok := reader.readName()
		if !ok {
			return errors.New("invalid wasm import name")
		}
		if _, allowed := wasmHostSignatures[name]; !allowed {
			return fmt.Errorf("wasm import %q is not allowed", name)
		}
		if _, duplicate := policy.seenImports[name]; duplicate {
			return fmt.Errorf("wasm import %q is duplicated", name)
		}
		policy.seenImports[name] = struct{}{}
		kind, ok := reader.readByte()
		if !ok || kind != 0x00 {
			return fmt.Errorf("wasm import %q must be a function", name)
		}
		typeIndex, ok := reader.readU32()
		if !ok || int(typeIndex) >= len(policy.types) {
			return fmt.Errorf("wasm import %q has an invalid type", name)
		}
		if !wasmBinarySignatureMatches(policy.types[typeIndex], wasmHostBinarySignature(name)) {
			return fmt.Errorf("wasm import %q has an invalid signature", name)
		}
		policy.importedFunctionTypes = append(policy.importedFunctionTypes, typeIndex)
	}
	if !reader.exhausted() {
		return errors.New("wasm import section has trailing data")
	}
	return nil
}

func validateWasmFunctionSection(section []byte, policy *wasmBinaryPolicy) error {
	reader := wasmSectionReader{data: section}
	count, ok := reader.readU32()
	if !ok || count > wasmMaxFunctions {
		return errors.New("wasm functions exceed the version-1 limit")
	}
	policy.functionTypes = make([]uint32, 0, count)
	policy.functionCount = count
	for index := uint32(0); index < count; index++ {
		typeIndex, ok := reader.readU32()
		if !ok || int(typeIndex) >= len(policy.types) {
			return errors.New("wasm function has an invalid type")
		}
		policy.functionTypes = append(policy.functionTypes, typeIndex)
	}
	if !reader.exhausted() {
		return errors.New("wasm function section has trailing data")
	}
	return nil
}

func validateWasmMemorySection(section []byte, policy *wasmBinaryPolicy, resources *wasmModuleResources) error {
	reader := wasmSectionReader{data: section}
	count, ok := reader.readU32()
	if !ok || count != 1 {
		return errors.New("wasm must define exactly one memory")
	}
	minimum, maximum, hasMaximum, ok := reader.readLimitBounds()
	if !ok || !hasMaximum || minimum == 0 || minimum != maximum || uint64(maximum) > wasmMaxMemoryPages {
		return errors.New("wasm memory must declare a fixed version-1 size")
	}
	policy.memoryDefined = true
	resources.memoryPages = uint64(maximum)
	if !reader.exhausted() {
		return errors.New("wasm memory section has trailing data")
	}
	return nil
}

func validateWasmExportSection(section []byte, policy *wasmBinaryPolicy) error {
	reader := wasmSectionReader{data: section}
	count, ok := reader.readU32()
	if !ok || count > 4 {
		return errors.New("wasm exports exceed the version-1 limit")
	}
	for index := uint32(0); index < count; index++ {
		name, ok := reader.readName()
		if !ok {
			return errors.New("invalid wasm export name")
		}
		if _, duplicate := policy.seenExports[name]; duplicate {
			return fmt.Errorf("wasm export %q is duplicated", name)
		}
		policy.seenExports[name] = struct{}{}
		kind, ok := reader.readByte()
		if !ok {
			return errors.New("invalid wasm export kind")
		}
		exportIndex, ok := reader.readU32()
		if !ok {
			return errors.New("invalid wasm export index")
		}
		if name == "memory" {
			if kind != 0x02 || exportIndex != 0 || !policy.memoryDefined || policy.memoryExported {
				return errors.New("wasm must export its single memory")
			}
			policy.memoryExported = true
			continue
		}
		if _, required := policy.requiredExports[name]; !required || kind != 0x00 {
			return fmt.Errorf("wasm export %q is not allowed", name)
		}
		signature, ok := policy.functionSignature(exportIndex)
		if !ok || !wasmBinarySignatureMatches(signature, wasmBinarySignature{}) {
			return fmt.Errorf("wasm export %q must have signature () -> ()", name)
		}
		policy.requiredExports[name] = true
	}
	if !reader.exhausted() {
		return errors.New("wasm export section has trailing data")
	}
	return nil
}

func (p *wasmBinaryPolicy) functionSignature(functionIndex uint32) (wasmBinarySignature, bool) {
	if functionIndex < uint32(len(p.importedFunctionTypes)) {
		typeIndex := p.importedFunctionTypes[functionIndex]
		return p.types[typeIndex], true
	}
	definedIndex := functionIndex - uint32(len(p.importedFunctionTypes))
	if int(definedIndex) >= len(p.functionTypes) {
		return wasmBinarySignature{}, false
	}
	return p.types[p.functionTypes[definedIndex]], true
}

func (p *wasmBinaryPolicy) validateComplete() error {
	if !p.memoryDefined || !p.memoryExported {
		return errors.New("wasm memory export is required")
	}
	for _, name := range []string{"deploy", "call", "read"} {
		if !p.requiredExports[name] {
			return fmt.Errorf("wasm export %q is required", name)
		}
	}
	if p.functionCount != p.codeBodyCount {
		return errors.New("wasm function and code body counts differ")
	}
	return nil
}

func wasmHostBinarySignature(name string) wasmBinarySignature {
	signature := wasmHostSignatures[name]
	params := make([]byte, len(signature.params))
	for index := range params {
		params[index] = 0x7f
	}
	results := make([]byte, len(signature.results))
	for index := range results {
		results[index] = 0x7f
	}
	return wasmBinarySignature{params: params, results: results}
}

func wasmBinarySignatureMatches(actual wasmBinarySignature, expected wasmBinarySignature) bool {
	return bytes.Equal(actual.params, expected.params) && bytes.Equal(actual.results, expected.results)
}

func validateWasmTableSection(section []byte, resources *wasmModuleResources) error {
	reader := wasmSectionReader{data: section}
	count, ok := reader.readU32()
	if !ok || count > 1 {
		return errors.New("wasm tables exceed the version-1 limit")
	}
	for index := uint32(0); index < count; index++ {
		elementType, ok := reader.readByte()
		if !ok || elementType != 0x70 {
			return errors.New("wasm table element type is not allowed")
		}
		minimum, maximum, hasMaximum, ok := reader.readLimitBounds()
		if !ok || !hasMaximum || minimum != maximum {
			return errors.New("wasm table must declare a fixed version-1 size")
		}
		if maximum > uint32(wasmMaxTableElements) {
			return errors.New("wasm table exceeds the version-1 limit")
		}
		resources.tableElements = uint64(maximum)
	}
	if !reader.exhausted() {
		return errors.New("wasm table section has trailing data")
	}
	return nil
}

func validateWasmCodeSection(section []byte, policy *wasmBinaryPolicy) error {
	reader := wasmSectionReader{data: section}
	count, ok := reader.readU32()
	if !ok || count > wasmMaxFunctions {
		return errors.New("wasm code bodies exceed the version-1 limit")
	}
	policy.codeBodyCount = count
	var moduleLocals uint64
	for functionIndex := uint32(0); functionIndex < count; functionIndex++ {
		bodySize, ok := reader.readU32()
		if !ok {
			return errors.New("invalid wasm code body size")
		}
		body, ok := reader.readBytes(bodySize)
		if !ok {
			return errors.New("invalid wasm code body")
		}
		bodyReader := wasmSectionReader{data: body}
		localGroups, ok := bodyReader.readU32()
		if !ok || localGroups > wasmMaxLocalsPerFunction {
			return errors.New("wasm local groups exceed the version-1 limit")
		}
		var functionLocals uint64
		for group := uint32(0); group < localGroups; group++ {
			localCount, ok := bodyReader.readU32()
			if !ok {
				return errors.New("invalid wasm local declaration")
			}
			if _, ok := bodyReader.readByte(); !ok {
				return errors.New("invalid wasm local type")
			}
			functionLocals += uint64(localCount)
			if functionLocals > wasmMaxLocalsPerFunction {
				return errors.New("wasm locals exceed the per-function limit")
			}
		}
		moduleLocals += functionLocals
		if moduleLocals > wasmMaxLocalsPerModule {
			return errors.New("wasm locals exceed the module limit")
		}
	}
	if !reader.exhausted() {
		return errors.New("wasm code section has trailing data")
	}
	return nil
}

type wasmSignature struct {
	params  []wasmtime.ValKind
	results []wasmtime.ValKind
}

var wasmHostSignatures = map[string]wasmSignature{
	"arg_copy":     {params: []wasmtime.ValKind{wasmtime.KindI32, wasmtime.KindI32, wasmtime.KindI32}, results: []wasmtime.ValKind{wasmtime.KindI32}},
	"storage_copy": {params: []wasmtime.ValKind{wasmtime.KindI32, wasmtime.KindI32, wasmtime.KindI32}, results: []wasmtime.ValKind{wasmtime.KindI32}},
	"storage_set":  {params: []wasmtime.ValKind{wasmtime.KindI32, wasmtime.KindI32, wasmtime.KindI32, wasmtime.KindI32}, results: []wasmtime.ValKind{wasmtime.KindI32}},
	"return_set":   {params: []wasmtime.ValKind{wasmtime.KindI32, wasmtime.KindI32}, results: []wasmtime.ValKind{wasmtime.KindI32}},
	"emit_event":   {params: []wasmtime.ValKind{wasmtime.KindI32, wasmtime.KindI32, wasmtime.KindI32, wasmtime.KindI32, wasmtime.KindI32, wasmtime.KindI32}, results: []wasmtime.ValKind{wasmtime.KindI32}},
}

func validateWasmABI(module *wasmtime.Module, resources *wasmModuleResources) error {
	seenImports := make(map[string]struct{})
	for _, imported := range module.Imports() {
		name := imported.Name()
		if imported.Module() != wasmHostModule || name == nil {
			return errors.New("wasm imports must be named chainlab host functions")
		}
		signature, ok := wasmHostSignatures[*name]
		if !ok {
			return fmt.Errorf("wasm import %q is not allowed", *name)
		}
		if _, duplicate := seenImports[*name]; duplicate {
			return fmt.Errorf("wasm import %q is duplicated", *name)
		}
		seenImports[*name] = struct{}{}
		functionType := imported.Type().FuncType()
		if functionType == nil || !wasmFunctionTypeMatches(functionType, signature) {
			return fmt.Errorf("wasm import %q has an invalid signature", *name)
		}
	}

	requiredFunctions := map[string]bool{"deploy": false, "call": false, "read": false}
	memoryFound := false
	for _, exported := range module.Exports() {
		name := exported.Name()
		typeInfo := exported.Type()
		if _, required := requiredFunctions[name]; required {
			functionType := typeInfo.FuncType()
			if functionType == nil || !wasmFunctionTypeMatches(functionType, wasmSignature{}) {
				return fmt.Errorf("wasm export %q must have signature () -> ()", name)
			}
			requiredFunctions[name] = true
			continue
		}
		if name == "memory" {
			memoryType := typeInfo.MemoryType()
			if memoryType == nil || memoryFound {
				return errors.New("wasm must export exactly one memory")
			}
			if memoryType.Is64() || memoryType.IsShared() || memoryType.Minimum() == 0 || memoryType.Minimum() > wasmMaxMemoryPages {
				return errors.New("wasm memory type exceeds the version-1 policy")
			}
			hasMaximum, maximum := memoryType.Maximum()
			if !hasMaximum || memoryType.Minimum() != maximum || maximum > wasmMaxMemoryPages {
				return errors.New("wasm memory must declare a fixed version-1 size")
			}
			resources.memoryPages = maximum
			memoryFound = true
			continue
		}
		return fmt.Errorf("wasm export %q is not allowed", name)
	}
	if !memoryFound {
		return errors.New("wasm memory export is required")
	}
	for _, name := range []string{"deploy", "call", "read"} {
		if !requiredFunctions[name] {
			return fmt.Errorf("wasm export %q is required", name)
		}
	}
	return nil
}

func wasmFunctionTypeMatches(functionType *wasmtime.FuncType, expected wasmSignature) bool {
	params := functionType.Params()
	results := functionType.Results()
	if len(params) != len(expected.params) || len(results) != len(expected.results) {
		return false
	}
	for index := range params {
		if params[index].Kind() != expected.params[index] {
			return false
		}
	}
	for index := range results {
		if results[index].Kind() != expected.results[index] {
			return false
		}
	}
	return true
}

func (w WasmContract) Deploy(ctx Context, args map[string]string) ([]types.Event, error) {
	invocation := newWasmInvocation(ctx, args)
	if err := invocation.call(w.module, w.resources, "deploy"); err != nil {
		return nil, err
	}
	return invocation.events, nil
}

func (w WasmContract) Call(ctx Context, method string, args map[string]string) ([]types.Event, error) {
	if method != "set" {
		return nil, fmt.Errorf("unknown wasm echo method %q", method)
	}
	invocation := newWasmInvocation(ctx, args)
	if err := invocation.call(w.module, w.resources, "call"); err != nil {
		return nil, err
	}
	return invocation.events, nil
}

func (w WasmContract) Read(ctx Context, method string, args map[string]string) (string, error) {
	if method != "get" {
		return "", fmt.Errorf("unknown wasm echo read method %q", method)
	}
	invocation := newWasmInvocation(ctx, args)
	if err := invocation.call(w.module, w.resources, "read"); err != nil {
		return "", err
	}
	return invocation.returnValue, nil
}

type wasmInvocation struct {
	contractCtx   Context
	args          map[string]string
	events        []types.Event
	returnValue   string
	err           error
	store         *wasmtime.Store
	hostIOBytes   uint64
	eventBytes    uint64
	storageWrites int
}

func newWasmInvocation(ctx Context, args map[string]string) *wasmInvocation {
	if args == nil {
		args = map[string]string{}
	}
	if ctx.Meter == nil {
		ctx.Meter = NewLimitedMeter(DefaultContractGasLimit)
	}
	return &wasmInvocation{contractCtx: ctx, args: args}
}

func (i *wasmInvocation) call(module *wasmtime.Module, resources wasmModuleResources, export string) error {
	if module == nil {
		return fmt.Errorf("%w: wasm module is not compiled", ErrWasmRuntimeFault)
	}
	if !i.charge(resources.instantiationGas()) {
		return i.err
	}
	fuelLimit := i.contractCtx.Meter.Remaining()
	store := wasmtime.NewStoreWithData(chainlabWasmEngine, i)
	i.store = store
	defer store.Close()
	store.Limiter(wasmMaxMemoryBytes, wasmMaxTableElements, 1, 1, 1)
	if err := store.SetFuel(fuelLimit); err != nil {
		return fmt.Errorf("%w: fuel configuration", ErrWasmRuntimeFault)
	}

	linker := wasmtime.NewLinker(chainlabWasmEngine)
	defer linker.Close()
	if err := defineWasmHost(linker); err != nil {
		return err
	}
	instance, executionErr := linker.Instantiate(store, module)
	if executionErr == nil {
		function := instance.GetFunc(store, export)
		if function == nil {
			executionErr = fmt.Errorf("%w: validated export %q is missing", ErrWasmRuntimeFault, export)
		} else {
			_, executionErr = function.Call(store)
		}
	}
	remaining, fuelErr := store.GetFuel()
	if fuelErr != nil {
		return fmt.Errorf("%w: read fuel balance", ErrWasmRuntimeFault)
	}
	if remaining > fuelLimit {
		return fmt.Errorf("%w: fuel accounting underflow", ErrWasmRuntimeFault)
	}
	if wasmConsumesFullBudget(executionErr) {
		remaining = 0
	}
	if !i.charge(fuelLimit - remaining) {
		return i.err
	}
	if i.err != nil {
		return i.err
	}
	if executionErr != nil {
		return normalizeWasmExecutionError(executionErr)
	}
	return nil
}

func (r wasmModuleResources) instantiationGas() uint64 {
	return wasmInstantiateGas +
		r.codeBytes*wasmInstantiationByteGas +
		r.memoryPages*wasmMemoryPageGas +
		r.tableElements*wasmTableElementGas
}

func wasmConsumesFullBudget(err error) bool {
	var trap *wasmtime.Trap
	if !errors.As(err, &trap) {
		return false
	}
	code := trap.Code()
	return code != nil && (*code == wasmtime.OutOfFuel || *code == wasmtime.StackOverflow)
}

func defineWasmHost(linker *wasmtime.Linker) error {
	definitions := []struct {
		name     string
		function any
	}{
		{name: "arg_copy", function: wasmHostArgCopy},
		{name: "storage_copy", function: wasmHostStorageCopy},
		{name: "storage_set", function: wasmHostStorageSet},
		{name: "return_set", function: wasmHostReturnSet},
		{name: "emit_event", function: wasmHostEmitEvent},
	}
	for _, definition := range definitions {
		if err := linker.FuncWrap(wasmHostModule, definition.name, definition.function); err != nil {
			return fmt.Errorf("%w: host ABI initialization", ErrWasmRuntimeFault)
		}
	}
	return nil
}

func normalizeWasmExecutionError(err error) error {
	var trap *wasmtime.Trap
	if errors.As(err, &trap) {
		if code := trap.Code(); code != nil {
			switch *code {
			case wasmtime.OutOfFuel:
				return ErrContractOutOfGas
			case wasmtime.StackOverflow:
				return fmt.Errorf("%w: stack", ErrWasmResourceLimit)
			default:
				return fmt.Errorf("%w: trap code %d", ErrWasmGuestTrap, *code)
			}
		}
	}
	return ErrWasmRuntimeFault
}

func wasmHostArgCopy(caller *wasmtime.Caller, keyPtr int32, keyLen int32, dstPtr int32) (int32, *wasmtime.Trap) {
	invocation := wasmInvocationFromCaller(caller)
	if trap := invocation.prepareHostRead(uint32(keyLen), wasmMaxKeyBytes, "argument key", wasmHostCallGas); trap != nil {
		return 0, trap
	}
	key, trap := invocation.readString(caller, uint32(keyPtr), uint32(keyLen))
	if trap != nil {
		return 0, trap
	}
	value := invocation.args[key]
	if trap := invocation.prepareHostRead(uint32(len(value)), wasmMaxValueBytes, "argument value", 0); trap != nil {
		return 0, trap
	}
	return invocation.writeString(caller, uint32(dstPtr), value)
}

func wasmHostStorageCopy(caller *wasmtime.Caller, keyPtr int32, keyLen int32, dstPtr int32) (int32, *wasmtime.Trap) {
	invocation := wasmInvocationFromCaller(caller)
	if trap := invocation.prepareHostRead(uint32(keyLen), wasmMaxKeyBytes, "storage key", wasmHostCallGas); trap != nil {
		return 0, trap
	}
	key, trap := invocation.readString(caller, uint32(keyPtr), uint32(keyLen))
	if trap != nil {
		return 0, trap
	}
	value := invocation.contractCtx.Store.GetStorage(invocation.contractCtx.Address, key)
	if trap := invocation.prepareHostRead(uint32(len(value)), wasmMaxValueBytes, "storage value", 0); trap != nil {
		return 0, trap
	}
	return invocation.writeString(caller, uint32(dstPtr), value)
}

func wasmHostStorageSet(caller *wasmtime.Caller, keyPtr int32, keyLen int32, valuePtr int32, valueLen int32) (int32, *wasmtime.Trap) {
	invocation := wasmInvocationFromCaller(caller)
	if invocation.contractCtx.ReadOnly {
		return 1, invocation.fail(ErrReadOnlyContract)
	}
	if invocation.storageWrites >= wasmMaxStorageWrites {
		return 1, invocation.fail(fmt.Errorf("%w: storage writes", ErrWasmResourceLimit))
	}
	totalBytes := uint64(uint32(keyLen)) + uint64(uint32(valueLen))
	if trap := invocation.prepareHostBytes(uint32(keyLen), wasmMaxKeyBytes, "storage key", 0); trap != nil {
		return 1, trap
	}
	if trap := invocation.prepareHostBytes(uint32(valueLen), wasmMaxValueBytes, "storage value", 0); trap != nil {
		return 1, trap
	}
	if trap := invocation.consumeFuel(wasmHostCallGas + totalBytes*wasmByteGas); trap != nil {
		return 1, trap
	}
	key, trap := invocation.readString(caller, uint32(keyPtr), uint32(keyLen))
	if trap != nil {
		return 1, trap
	}
	value, trap := invocation.readString(caller, uint32(valuePtr), uint32(valueLen))
	if trap != nil {
		return 1, trap
	}
	keyExists := invocation.contractCtx.Store.HasStorage(invocation.contractCtx.Address, key)
	oldValue := invocation.contractCtx.Store.GetStorage(invocation.contractCtx.Address, key)
	var growth uint64
	if !keyExists {
		growth += uint64(len(key))
	}
	if len(value) > len(oldValue) {
		growth += uint64(len(value) - len(oldValue))
	}
	if growth > math.MaxUint64/wasmStorageGrowthGas {
		return 1, invocation.fail(ErrWasmResourceLimit)
	}
	if trap := invocation.consumeFuel(growth * wasmStorageGrowthGas); trap != nil {
		return 1, trap
	}
	invocation.storageWrites++
	invocation.contractCtx.Store.SetStorage(invocation.contractCtx.Address, key, value)
	return 0, nil
}

func wasmHostReturnSet(caller *wasmtime.Caller, valuePtr int32, valueLen int32) (int32, *wasmtime.Trap) {
	invocation := wasmInvocationFromCaller(caller)
	if trap := invocation.prepareHostRead(uint32(valueLen), wasmMaxReturnBytes, "return value", wasmHostCallGas); trap != nil {
		return 1, trap
	}
	value, trap := invocation.readString(caller, uint32(valuePtr), uint32(valueLen))
	if trap != nil {
		return 1, trap
	}
	invocation.returnValue = value
	return 0, nil
}

func wasmHostEmitEvent(caller *wasmtime.Caller, typePtr int32, typeLen int32, keyPtr int32, keyLen int32, valuePtr int32, valueLen int32) (int32, *wasmtime.Trap) {
	invocation := wasmInvocationFromCaller(caller)
	if invocation.contractCtx.ReadOnly {
		return 1, invocation.fail(ErrReadOnlyContract)
	}
	if len(invocation.events) >= wasmMaxEvents {
		return 1, invocation.fail(fmt.Errorf("%w: event count", ErrWasmResourceLimit))
	}
	totalBytes := uint64(uint32(typeLen)) + uint64(uint32(keyLen)) + uint64(uint32(valueLen))
	if totalBytes > wasmMaxEventBytes-invocation.eventBytes {
		return 1, invocation.fail(fmt.Errorf("%w: event bytes", ErrWasmResourceLimit))
	}
	for _, field := range []struct {
		length  uint32
		maximum uint32
		name    string
	}{
		{uint32(typeLen), wasmMaxEventTypeBytes, "event type"},
		{uint32(keyLen), wasmMaxEventKeyBytes, "event key"},
		{uint32(valueLen), wasmMaxEventValueBytes, "event value"},
	} {
		if trap := invocation.prepareHostBytes(field.length, field.maximum, field.name, 0); trap != nil {
			return 1, trap
		}
	}
	if trap := invocation.consumeFuel(wasmHostCallGas + totalBytes*wasmByteGas); trap != nil {
		return 1, trap
	}
	eventType, trap := invocation.readString(caller, uint32(typePtr), uint32(typeLen))
	if trap != nil {
		return 1, trap
	}
	key, trap := invocation.readString(caller, uint32(keyPtr), uint32(keyLen))
	if trap != nil {
		return 1, trap
	}
	value, trap := invocation.readString(caller, uint32(valuePtr), uint32(valueLen))
	if trap != nil {
		return 1, trap
	}
	invocation.eventBytes += totalBytes
	invocation.events = append(invocation.events, types.Event{Type: eventType, Attributes: map[string]string{key: value}})
	return 0, nil
}

func wasmInvocationFromCaller(caller *wasmtime.Caller) *wasmInvocation {
	invocation, ok := caller.Data().(*wasmInvocation)
	if !ok || invocation == nil {
		panic("wasm invocation data is missing")
	}
	return invocation
}

func (i *wasmInvocation) prepareHostRead(length uint32, maximum uint32, name string, gas uint64) *wasmtime.Trap {
	if trap := i.prepareHostBytes(length, maximum, name, 0); trap != nil {
		return trap
	}
	return i.consumeFuel(gas + uint64(length)*wasmByteGas)
}

func (i *wasmInvocation) prepareHostBytes(length uint32, maximum uint32, name string, gas uint64) *wasmtime.Trap {
	if length > maximum {
		return i.fail(fmt.Errorf("%w: %s", ErrWasmResourceLimit, name))
	}
	if uint64(length) > wasmMaxHostIOBytes-i.hostIOBytes {
		return i.fail(fmt.Errorf("%w: host I/O bytes", ErrWasmResourceLimit))
	}
	i.hostIOBytes += uint64(length)
	if gas != 0 {
		return i.consumeFuel(gas)
	}
	return nil
}

func (i *wasmInvocation) consumeFuel(amount uint64) *wasmtime.Trap {
	if i.err != nil {
		return wasmtime.NewTrap("chainlab host failure")
	}
	remaining, err := i.store.GetFuel()
	if err != nil {
		return i.fail(fmt.Errorf("%w: read host fuel balance", ErrWasmRuntimeFault))
	}
	if amount > remaining {
		if err := i.store.SetFuel(0); err != nil {
			return i.fail(fmt.Errorf("%w: exhaust host fuel", ErrWasmRuntimeFault))
		}
		return i.fail(ErrContractOutOfGas)
	}
	if err := i.store.SetFuel(remaining - amount); err != nil {
		return i.fail(fmt.Errorf("%w: deduct host fuel", ErrWasmRuntimeFault))
	}
	return nil
}

func (i *wasmInvocation) readString(caller *wasmtime.Caller, ptr uint32, length uint32) (string, *wasmtime.Trap) {
	memory, trap := i.memory(caller)
	if trap != nil {
		return "", trap
	}
	start := uint64(ptr)
	end := start + uint64(length)
	data := memory.UnsafeData(caller)
	if end < start || end > uint64(len(data)) {
		return "", i.fail(fmt.Errorf("%w: memory read", ErrWasmResourceLimit))
	}
	raw := data[int(start):int(end)]
	if !utf8.Valid(raw) {
		return "", i.fail(ErrWasmInvalidUTF8)
	}
	return string(raw), nil
}

func (i *wasmInvocation) writeString(caller *wasmtime.Caller, ptr uint32, value string) (int32, *wasmtime.Trap) {
	if !utf8.ValidString(value) {
		return 0, i.fail(ErrWasmInvalidUTF8)
	}
	memory, trap := i.memory(caller)
	if trap != nil {
		return 0, trap
	}
	start := uint64(ptr)
	end := start + uint64(len(value))
	data := memory.UnsafeData(caller)
	if end < start || end > uint64(len(data)) {
		return 0, i.fail(fmt.Errorf("%w: memory write", ErrWasmResourceLimit))
	}
	copy(data[int(start):int(end)], value)
	return int32(len(value)), nil
}

func (i *wasmInvocation) memory(caller *wasmtime.Caller) (*wasmtime.Memory, *wasmtime.Trap) {
	exported := caller.GetExport("memory")
	if exported == nil {
		return nil, i.fail(fmt.Errorf("%w: memory export", ErrWasmResourceLimit))
	}
	memory := exported.Memory()
	if memory == nil {
		return nil, i.fail(fmt.Errorf("%w: memory export", ErrWasmResourceLimit))
	}
	return memory, nil
}

func (i *wasmInvocation) fail(err error) *wasmtime.Trap {
	if i.err == nil {
		i.err = err
	}
	return wasmtime.NewTrap("chainlab host failure")
}

func (i *wasmInvocation) charge(amount uint64) bool {
	if err := i.contractCtx.Meter.Charge(amount); err != nil {
		if i.err == nil {
			i.err = err
		}
		return false
	}
	return true
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
		if i == 4 && b&0xf0 != 0 {
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

func (r *wasmSectionReader) exhausted() bool {
	return r.offset == len(r.data)
}

func (r *wasmSectionReader) readLimitBounds() (minimum uint32, maximum uint32, hasMaximum bool, ok bool) {
	flag, ok := r.readByte()
	if !ok {
		return 0, 0, false, false
	}
	minimum, ok = r.readU32()
	if !ok {
		return 0, 0, false, false
	}
	switch flag {
	case 0x00:
		return minimum, 0, false, true
	case 0x01:
		maximum, ok = r.readU32()
		return minimum, maximum, true, ok
	default:
		return 0, 0, false, false
	}
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
