package contracts

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

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
	Store   *state.Store
	Address string
	Caller  string
	Meter   *Meter
}

type Runtime struct {
	registry map[string]Contract
}

func NewRuntime() *Runtime {
	return &Runtime{registry: make(map[string]Contract)}
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

func (r *Runtime) Register(codeID string, contract Contract) {
	r.registry[codeID] = contract
}

func (r *Runtime) Deploy(store *state.Store, creator string, codeID string, seed string, args map[string]string) (string, []types.Event, error) {
	address, events, _, err := r.DeployMetered(store, creator, codeID, seed, args)
	return address, events, err
}

func (r *Runtime) DeployMetered(store *state.Store, creator string, codeID string, seed string, args map[string]string) (string, []types.Event, uint64, error) {
	contract, err := r.contractFor(store, codeID)
	if err != nil {
		return "", nil, 0, err
	}
	address := contractAddress(creator, codeID, seed)
	store.SetCodeID(address, codeID)
	meter := NewMeter()
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
	account := store.GetAccount(address)
	if account.CodeID == "" {
		return nil, 0, errors.New("target account is not a contract")
	}
	contract, err := r.contractFor(store, account.CodeID)
	if err != nil {
		return nil, 0, err
	}
	meter := NewMeter()
	ctx := Context{Store: store, Address: strings.ToLower(address), Caller: strings.ToLower(caller), Meter: meter}
	events, err := contract.Call(ctx, method, args)
	return events, meter.GasUsed(), err
}

func (r *Runtime) Read(store *state.Store, address string, caller string, method string, args map[string]string) (string, error) {
	account := store.GetAccount(address)
	if account.CodeID == "" {
		return "", errors.New("target account is not a contract")
	}
	contract, err := r.contractFor(store, account.CodeID)
	if err != nil {
		return "", err
	}
	ctx := Context{Store: store, Address: strings.ToLower(address), Caller: strings.ToLower(caller)}
	return contract.Read(ctx, method, args)
}

func (r *Runtime) contractFor(store *state.Store, codeID string) (Contract, error) {
	if contract, ok := r.registry[codeID]; ok {
		return contract, nil
	}
	code, ok := store.ContractCode(codeID)
	if !ok {
		return nil, fmt.Errorf("unknown contract code id %q", codeID)
	}
	if code.Runtime != "wasm" {
		return nil, fmt.Errorf("unknown contract runtime %q", code.Runtime)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(code.Bytecode, "0x"))
	if err != nil {
		return nil, fmt.Errorf("invalid wasm bytecode for %q: %w", codeID, err)
	}
	return NewWasmContract(raw)
}

func contractAddress(creator string, codeID string, seed string) string {
	digest := strings.TrimPrefix(hash.KeccakHex([]byte(strings.ToLower(creator)+"|"+codeID+"|"+seed)), "0x")
	return "0x" + digest[len(digest)-40:]
}
