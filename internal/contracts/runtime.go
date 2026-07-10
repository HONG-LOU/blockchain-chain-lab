package contracts

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"chainlab/internal/hash"
	"chainlab/internal/state"
	"chainlab/internal/types"
)

type Contract interface {
	Deploy(ctx Context, args map[string]string) ([]types.Event, error)
	Call(ctx Context, method string, args map[string]string) ([]types.Event, error)
	Read(ctx Context, method string, args map[string]string) (string, error)
}

type Context struct {
	store        *state.Store
	reader       state.Reader
	nativeBudget *nativeInvocationBudget
	address      string
	caller       string
	meter        *Meter
	readOnly     bool
}

func (ctx Context) Address() string { return ctx.address }

func (ctx Context) Caller() string { return ctx.caller }

func (ctx Context) IsReadOnly() bool { return ctx.readOnly }

type Runtime struct {
	registry       map[string]Contract
	sealed         bool
	wasmCache      map[string]wasmCacheEntry
	wasmCacheOrder []string
	wasmCompiling  map[string]*wasmCompileCall
	mu             sync.RWMutex
}

type wasmCacheEntry struct {
	contract        WasmContract
	bytecode        string
	meteringVersion string
}

type wasmCompileCall struct {
	done     chan struct{}
	contract WasmContract
	err      error
}

const (
	DefaultContractGasLimit     uint64 = 30_000_000
	DefaultReadGasLimit         uint64 = 5_000_000
	maxWasmCompiledCacheEntries        = 256
)

func NewRuntime() *Runtime {
	return &Runtime{
		registry:      make(map[string]Contract),
		wasmCache:     make(map[string]wasmCacheEntry),
		wasmCompiling: make(map[string]*wasmCompileCall),
	}
}

func NewRuntimeWithDefaults() *Runtime {
	runtime := NewRuntime()
	defaults := []struct {
		codeID   string
		contract Contract
	}{
		{codeID: AccountCodeID, contract: Account{}},
		{codeID: MultisigCodeID, contract: Multisig{}},
		{codeID: "counter.v1", contract: Counter{}},
		{codeID: "token.v1", contract: Token{}},
		{codeID: "wasm.echo.v1", contract: NewWasmEchoContract()},
	}
	for _, entry := range defaults {
		if err := runtime.Register(entry.codeID, entry.contract); err != nil {
			panic(err)
		}
	}
	runtime.Seal()
	return runtime
}

// ValidateAndCacheWasmCode performs full version-1 admission and retains the
// compiled artifact for later transaction replay. State authorization is still
// checked independently before any cached module can execute.
func (r *Runtime) ValidateAndCacheWasmCode(code []byte) error {
	codeID := types.WASMCodeID(code)
	bytecode := "0x" + hex.EncodeToString(code)
	r.mu.RLock()
	if cached, ok := r.wasmCache[codeID]; ok && cached.bytecode == bytecode && cached.meteringVersion == WASMMeteringVersion {
		r.mu.RUnlock()
		return nil
	}
	r.mu.RUnlock()
	resources := wasmModuleResources{codeBytes: uint64(len(code))}
	if err := validateWasmEnvelope(code, &resources); err != nil {
		return err
	}
	_, err := r.compileWASM(codeID, bytecode, WASMMeteringVersion, code, resources, false)
	return err
}

func (r *Runtime) Register(codeID string, contract Contract) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sealed {
		return errors.New("contract runtime registry is sealed")
	}
	if strings.TrimSpace(codeID) == "" || contract == nil {
		return errors.New("contract registration requires code id and implementation")
	}
	if _, exists := r.registry[codeID]; exists {
		return fmt.Errorf("contract code id %q is already registered", codeID)
	}
	r.registry[codeID] = contract
	return nil
}

func (r *Runtime) Seal() {
	r.mu.Lock()
	r.sealed = true
	r.mu.Unlock()
}

func (r *Runtime) Deploy(store *state.Store, creator string, codeID string, seed string, args map[string]string) (string, []types.Event, error) {
	address, events, _, err := r.DeployMetered(store, creator, codeID, seed, args)
	return address, events, err
}

func (r *Runtime) DeployMetered(store *state.Store, creator string, codeID string, seed string, args map[string]string) (string, []types.Event, uint64, error) {
	return r.DeployMeteredWithLimit(store, creator, codeID, seed, args, DefaultContractGasLimit)
}

func (r *Runtime) DeployMeteredWithLimit(store *state.Store, creator string, codeID string, seed string, args map[string]string, gasLimit uint64) (string, []types.Event, uint64, error) {
	working := store.Clone()
	address, events, gasUsed, err := r.DeployMeteredInTransaction(working, creator, codeID, seed, args, gasLimit)
	if err != nil {
		return "", nil, gasUsed, err
	}
	store.ReplaceWith(working)
	return address, events, gasUsed, nil
}

// DeployMeteredInTransaction executes against a caller-owned working store.
// The caller must discard that store if this method or a later operation fails.
func (r *Runtime) DeployMeteredInTransaction(store *state.Store, creator string, codeID string, seed string, args map[string]string, gasLimit uint64) (string, []types.Event, uint64, error) {
	meter := NewLimitedMeter(gasLimit)
	contract, err := r.contractFor(store, codeID, meter)
	if err != nil {
		return "", nil, meter.GasUsed(), err
	}
	address := contractAddress(creator, codeID, seed)
	_, isWASM := wasmContractResources(contract)
	if !isWASM {
		if err := chargeNativeInvocation(meter, "deploy", args); err != nil {
			return "", nil, meter.GasUsed(), err
		}
		if err := chargeNativeLinear(meter, nativeAccountWriteGas, nativeStorageWriteByteGas, len(codeID)); err != nil {
			return "", nil, meter.GasUsed(), err
		}
	}
	store.SetCodeID(address, codeID)
	ctx := Context{store: store, reader: store, address: address, caller: strings.ToLower(creator), meter: meter}
	if !isWASM {
		ctx.nativeBudget = &nativeInvocationBudget{}
	}
	events, err := invokeContractDeploy(contract, ctx, args, isWASM)
	if err != nil {
		return "", nil, meter.GasUsed(), err
	}
	events = append([]types.Event{{
		Type: "contract.deployed",
		Attributes: map[string]string{
			"address": address,
			"code_id": codeID,
			"creator": strings.ToLower(creator),
		},
	}}, events...)
	if !isWASM {
		if err := chargeNativeEvents(meter, events); err != nil {
			return "", nil, meter.GasUsed(), err
		}
	}
	return address, events, meter.GasUsed(), nil
}

func (r *Runtime) Call(store *state.Store, address string, caller string, method string, args map[string]string) ([]types.Event, error) {
	events, _, err := r.CallMetered(store, address, caller, method, args)
	return events, err
}

func (r *Runtime) CallMetered(store *state.Store, address string, caller string, method string, args map[string]string) ([]types.Event, uint64, error) {
	return r.CallMeteredWithLimit(store, address, caller, method, args, DefaultContractGasLimit)
}

func (r *Runtime) CallMeteredWithLimit(store *state.Store, address string, caller string, method string, args map[string]string, gasLimit uint64) ([]types.Event, uint64, error) {
	working := store.Clone()
	events, gasUsed, err := r.CallMeteredInTransaction(working, address, caller, method, args, gasLimit)
	if err != nil {
		return nil, gasUsed, err
	}
	store.ReplaceWith(working)
	return events, gasUsed, nil
}

// CallMeteredInTransaction executes against a caller-owned working store.
// The caller must discard that store if this method or a later operation fails.
func (r *Runtime) CallMeteredInTransaction(store *state.Store, address string, caller string, method string, args map[string]string, gasLimit uint64) ([]types.Event, uint64, error) {
	codeID := store.CodeID(address)
	if codeID == "" {
		return nil, 0, errors.New("target account is not a contract")
	}
	meter := NewLimitedMeter(gasLimit)
	contract, err := r.contractFor(store, codeID, meter)
	if err != nil {
		if errors.Is(err, errUnknownContractCode) {
			err = fmt.Errorf("%w: persisted unknown contract code id", ErrContractStateFault)
		}
		return nil, meter.GasUsed(), err
	}
	ctx := Context{store: store, reader: store, address: strings.ToLower(address), caller: strings.ToLower(caller), meter: meter}
	_, isWASM := wasmContractResources(contract)
	if !isWASM {
		ctx.nativeBudget = &nativeInvocationBudget{}
		if err := chargeNativeInvocation(meter, method, args); err != nil {
			return nil, meter.GasUsed(), err
		}
	}
	events, err := invokeContractCall(contract, ctx, method, args, isWASM)
	if err != nil {
		return nil, meter.GasUsed(), err
	}
	if !isWASM {
		if err := chargeNativeEvents(meter, events); err != nil {
			return nil, meter.GasUsed(), err
		}
	}
	return events, meter.GasUsed(), nil
}

func (r *Runtime) Read(store *state.Store, address string, caller string, method string, args map[string]string) (string, error) {
	value, _, err := r.ReadMetered(store, address, caller, method, args, DefaultReadGasLimit)
	return value, err
}

func (r *Runtime) ReadMetered(store *state.Store, address string, caller string, method string, args map[string]string, gasLimit uint64) (string, uint64, error) {
	working := store.Clone()
	return r.readMeteredSnapshot(working.ReadView(), address, caller, method, args, gasLimit)
}

// ReadImmutableSnapshot executes against a Store that its owner has published
// as immutable. Read-only contract contexts reject every state mutation, so
// callers can retain an O(1) version handle without cloning the whole state.
func (r *Runtime) ReadImmutableSnapshot(view state.ReadView, address string, caller string, method string, args map[string]string) (string, error) {
	value, _, err := r.readMeteredSnapshot(view, address, caller, method, args, DefaultReadGasLimit)
	return value, err
}

func (r *Runtime) readMeteredSnapshot(reader state.Reader, address string, caller string, method string, args map[string]string, gasLimit uint64) (string, uint64, error) {
	codeID := reader.CodeID(address)
	if codeID == "" {
		return "", 0, errors.New("target account is not a contract")
	}
	meter := NewLimitedMeter(gasLimit)
	contract, err := r.contractFor(reader, codeID, meter)
	if err != nil {
		if errors.Is(err, errUnknownContractCode) {
			err = fmt.Errorf("%w: persisted unknown contract code id", ErrContractStateFault)
		}
		return "", meter.GasUsed(), err
	}
	ctx := Context{
		reader:   reader,
		address:  strings.ToLower(address),
		caller:   strings.ToLower(caller),
		meter:    meter,
		readOnly: true,
	}
	_, isWASM := wasmContractResources(contract)
	if !isWASM {
		ctx.nativeBudget = &nativeInvocationBudget{}
		if err := chargeNativeInvocation(meter, method, args); err != nil {
			return "", meter.GasUsed(), err
		}
	}
	value, err := invokeContractRead(contract, ctx, method, args, isWASM)
	if err == nil && !isWASM {
		err = chargeNativeOutput(meter, value)
	}
	return value, meter.GasUsed(), err
}

func invokeContractDeploy(contract Contract, ctx Context, args map[string]string, isWASM bool) (events []types.Event, err error) {
	defer recoverContractPanic(isWASM, &err)
	return contract.Deploy(ctx, args)
}

func invokeContractCall(contract Contract, ctx Context, method string, args map[string]string, isWASM bool) (events []types.Event, err error) {
	defer recoverContractPanic(isWASM, &err)
	return contract.Call(ctx, method, args)
}

func invokeContractRead(contract Contract, ctx Context, method string, args map[string]string, isWASM bool) (value string, err error) {
	defer recoverContractPanic(isWASM, &err)
	return contract.Read(ctx, method, args)
}

func recoverContractPanic(isWASM bool, err *error) {
	if recover() == nil {
		return
	}
	if isWASM {
		*err = fmt.Errorf("%w: contract adapter panic", ErrWasmRuntimeFault)
		return
	}
	*err = fmt.Errorf("%w: contract implementation panic", ErrNativeRuntimeFault)
}

func (r *Runtime) contractFor(store state.Reader, codeID string, meter *Meter) (Contract, error) {
	r.mu.Lock()
	r.sealed = true
	if contract, ok := r.registry[codeID]; ok {
		r.mu.Unlock()
		if resources, isWASM := wasmContractResources(contract); isWASM {
			if err := ensureWASMInvocationBudget(meter, resources); err != nil {
				return nil, err
			}
		}
		return contract, nil
	}
	r.mu.Unlock()
	code, ok := store.ContractCode(codeID)
	if !ok {
		return nil, fmt.Errorf("%w %q", errUnknownContractCode, codeID)
	}
	if code.CodeID != codeID {
		return nil, fmt.Errorf("%w: admitted wasm code record id mismatch", ErrWasmRuntimeFault)
	}
	if code.Runtime != "wasm" {
		return nil, fmt.Errorf("%w: admitted wasm runtime mismatch", ErrWasmRuntimeFault)
	}
	if code.MeteringVersion != WASMMeteringVersion {
		return nil, fmt.Errorf("%w: admitted wasm metering version mismatch", ErrWasmRuntimeFault)
	}
	r.mu.RLock()
	if cached, ok := r.wasmCache[codeID]; ok && cached.bytecode == code.Bytecode && cached.meteringVersion == code.MeteringVersion {
		r.mu.RUnlock()
		if err := ensureWASMInvocationBudget(meter, cached.contract.resources); err != nil {
			return nil, err
		}
		return cached.contract, nil
	}
	r.mu.RUnlock()
	if err := ensureWASMEncodedCodeBudget(meter, code.Bytecode); err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(code.Bytecode, "0x"))
	if err != nil {
		return nil, fmt.Errorf("%w: admitted wasm bytecode encoding is invalid", ErrWasmRuntimeFault)
	}
	if actualCodeID := types.WASMCodeID(raw); actualCodeID != codeID {
		return nil, fmt.Errorf("%w: admitted wasm bytecode hash mismatch", ErrWasmRuntimeFault)
	}
	resources := wasmModuleResources{codeBytes: uint64(len(raw))}
	if err := validateWasmEnvelope(raw, &resources); err != nil {
		return nil, fmt.Errorf("%w: admitted wasm envelope mismatch", ErrWasmRuntimeFault)
	}
	if err := ensureWASMInvocationBudget(meter, resources); err != nil {
		return nil, err
	}
	return r.compileWASM(codeID, code.Bytecode, code.MeteringVersion, raw, resources, true)
}

func (r *Runtime) compileWASM(codeID string, bytecode string, meteringVersion string, raw []byte, resources wasmModuleResources, admitted bool) (Contract, error) {
	compilationKey := codeID + ":upload"
	if admitted {
		compilationKey = codeID + ":state"
	}
	r.mu.Lock()
	if cached, ok := r.wasmCache[codeID]; ok && cached.bytecode == bytecode && cached.meteringVersion == meteringVersion {
		r.mu.Unlock()
		return cached.contract, nil
	}
	if inFlight, ok := r.wasmCompiling[compilationKey]; ok {
		r.mu.Unlock()
		<-inFlight.done
		if inFlight.err != nil {
			return nil, inFlight.err
		}
		return inFlight.contract, nil
	}
	inFlight := &wasmCompileCall{done: make(chan struct{})}
	r.wasmCompiling[compilationKey] = inFlight
	r.mu.Unlock()

	var contract WasmContract
	var err error
	if admitted {
		contract, err = newAdmittedWasmContract(raw)
	} else {
		contract, err = NewWasmContract(raw)
	}
	if err == nil && contract.resources != resources {
		err = fmt.Errorf("%w: wasm resource prefilter mismatch", ErrWasmRuntimeFault)
	}

	r.mu.Lock()
	if err == nil {
		r.cacheWASMLocked(codeID, wasmCacheEntry{
			contract:        contract,
			bytecode:        bytecode,
			meteringVersion: meteringVersion,
		})
	}
	inFlight.contract = contract
	inFlight.err = err
	delete(r.wasmCompiling, compilationKey)
	close(inFlight.done)
	r.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return contract, nil
}

func (r *Runtime) cacheWASMLocked(codeID string, entry wasmCacheEntry) {
	if _, exists := r.wasmCache[codeID]; exists {
		r.wasmCache[codeID] = entry
		return
	}
	if len(r.wasmCacheOrder) >= maxWasmCompiledCacheEntries {
		evicted := r.wasmCacheOrder[0]
		delete(r.wasmCache, evicted)
		r.wasmCacheOrder = r.wasmCacheOrder[1:]
	}
	r.wasmCache[codeID] = entry
	r.wasmCacheOrder = append(r.wasmCacheOrder, codeID)
}

func wasmContractResources(contract Contract) (wasmModuleResources, bool) {
	switch typed := contract.(type) {
	case WasmContract:
		return typed.resources, true
	case *WasmContract:
		return typed.resources, true
	default:
		return wasmModuleResources{}, false
	}
}

func ensureWASMInvocationBudget(meter *Meter, resources wasmModuleResources) error {
	if meter == nil || meter.Remaining() >= resources.instantiationGas() {
		return nil
	}
	return meter.Charge(resources.instantiationGas())
}

func ensureWASMEncodedCodeBudget(meter *Meter, bytecode string) error {
	if !strings.HasPrefix(bytecode, "0x") || len(bytecode) < 2 || (len(bytecode)-2)%2 != 0 {
		return fmt.Errorf("%w: admitted wasm bytecode encoding is invalid", ErrWasmRuntimeFault)
	}
	codeBytes := (len(bytecode) - 2) / 2
	if codeBytes > MaxWASMModuleBytes {
		return fmt.Errorf("%w: admitted wasm bytecode exceeds module limit", ErrWasmRuntimeFault)
	}
	return ensureWASMInvocationBudget(meter, wasmModuleResources{codeBytes: uint64(codeBytes)})
}

func contractAddress(creator string, codeID string, seed string) string {
	digest := strings.TrimPrefix(hash.KeccakHex([]byte(strings.ToLower(creator)+"|"+codeID+"|"+seed)), "0x")
	return "0x" + digest[len(digest)-40:]
}
