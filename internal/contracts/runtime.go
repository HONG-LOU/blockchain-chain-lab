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
	Store    *state.Store
	Address  string
	Caller   string
	Meter    *Meter
	ReadOnly bool
}

type Runtime struct {
	registry       map[string]Contract
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
	runtime.Register(AccountCodeID, Account{})
	runtime.Register(MultisigCodeID, Multisig{})
	runtime.Register("counter.v1", Counter{})
	runtime.Register("token.v1", Token{})
	runtime.Register("wasm.echo.v1", NewWasmEchoContract())
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

func (r *Runtime) Register(codeID string, contract Contract) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registry[codeID] = contract
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
	store.SetCodeID(address, codeID)
	ctx := Context{Store: store, Address: address, Caller: strings.ToLower(creator), Meter: meter}
	events, err := contract.Deploy(ctx, args)
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
	account := store.GetAccount(address)
	if account.CodeID == "" {
		return nil, 0, errors.New("target account is not a contract")
	}
	meter := NewLimitedMeter(gasLimit)
	contract, err := r.contractFor(store, account.CodeID, meter)
	if err != nil {
		return nil, meter.GasUsed(), err
	}
	ctx := Context{Store: store, Address: strings.ToLower(address), Caller: strings.ToLower(caller), Meter: meter}
	events, err := contract.Call(ctx, method, args)
	if err != nil {
		return nil, meter.GasUsed(), err
	}
	return events, meter.GasUsed(), nil
}

func (r *Runtime) Read(store *state.Store, address string, caller string, method string, args map[string]string) (string, error) {
	value, _, err := r.ReadMetered(store, address, caller, method, args, DefaultReadGasLimit)
	return value, err
}

func (r *Runtime) ReadMetered(store *state.Store, address string, caller string, method string, args map[string]string, gasLimit uint64) (string, uint64, error) {
	working := store.Clone()
	account := working.GetAccount(address)
	if account.CodeID == "" {
		return "", 0, errors.New("target account is not a contract")
	}
	meter := NewLimitedMeter(gasLimit)
	contract, err := r.contractFor(working, account.CodeID, meter)
	if err != nil {
		return "", meter.GasUsed(), err
	}
	ctx := Context{
		Store:    working,
		Address:  strings.ToLower(address),
		Caller:   strings.ToLower(caller),
		Meter:    meter,
		ReadOnly: true,
	}
	value, err := contract.Read(ctx, method, args)
	return value, meter.GasUsed(), err
}

func (r *Runtime) contractFor(store *state.Store, codeID string, meter *Meter) (Contract, error) {
	r.mu.RLock()
	if contract, ok := r.registry[codeID]; ok {
		r.mu.RUnlock()
		if resources, isWASM := wasmContractResources(contract); isWASM {
			if err := ensureWASMInvocationBudget(meter, resources); err != nil {
				return nil, err
			}
		}
		return contract, nil
	}
	r.mu.RUnlock()
	code, ok := store.ContractCode(codeID)
	if !ok {
		return nil, fmt.Errorf("unknown contract code id %q", codeID)
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

func contractAddress(creator string, codeID string, seed string) string {
	digest := strings.TrimPrefix(hash.KeccakHex([]byte(strings.ToLower(creator)+"|"+codeID+"|"+seed)), "0x")
	return "0x" + digest[len(digest)-40:]
}
