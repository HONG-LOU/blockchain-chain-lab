package contracts

import (
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
}

type Context struct {
	Store   *state.Store
	Address string
	Caller  string
}

type Runtime struct {
	registry map[string]Contract
}

func NewRuntime() *Runtime {
	return &Runtime{registry: make(map[string]Contract)}
}

func NewRuntimeWithDefaults() *Runtime {
	runtime := NewRuntime()
	runtime.Register("counter.v1", Counter{})
	runtime.Register("token.v1", Token{})
	return runtime
}

func (r *Runtime) Register(codeID string, contract Contract) {
	r.registry[codeID] = contract
}

func (r *Runtime) Deploy(store *state.Store, creator string, codeID string, seed string, args map[string]string) (string, []types.Event, error) {
	contract, ok := r.registry[codeID]
	if !ok {
		return "", nil, fmt.Errorf("unknown contract code id %q", codeID)
	}
	address := contractAddress(creator, codeID, seed)
	store.SetCodeID(address, codeID)
	ctx := Context{Store: store, Address: address, Caller: strings.ToLower(creator)}
	events, err := contract.Deploy(ctx, args)
	if err != nil {
		return "", nil, err
	}
	events = append([]types.Event{{
		Type: "contract.deployed",
		Attributes: map[string]string{
			"address": address,
			"code_id": codeID,
			"creator": strings.ToLower(creator),
		},
	}}, events...)
	return address, events, nil
}

func (r *Runtime) Call(store *state.Store, address string, caller string, method string, args map[string]string) ([]types.Event, error) {
	account := store.GetAccount(address)
	if account.CodeID == "" {
		return nil, errors.New("target account is not a contract")
	}
	contract, ok := r.registry[account.CodeID]
	if !ok {
		return nil, fmt.Errorf("unknown contract code id %q", account.CodeID)
	}
	ctx := Context{Store: store, Address: strings.ToLower(address), Caller: strings.ToLower(caller)}
	return contract.Call(ctx, method, args)
}

func contractAddress(creator string, codeID string, seed string) string {
	digest := strings.TrimPrefix(hash.KeccakHex([]byte(strings.ToLower(creator)+"|"+codeID+"|"+seed)), "0x")
	return "0x" + digest[len(digest)-40:]
}
