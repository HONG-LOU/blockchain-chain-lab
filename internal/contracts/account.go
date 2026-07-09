package contracts

import (
	"errors"
	"fmt"
	"strings"

	"chainlab/internal/types"
)

const AccountCodeID = "account.v1"

type Account struct{}

func (Account) Deploy(ctx Context, args map[string]string) ([]types.Event, error) {
	owner := normalizeAddress(args["owner"])
	if owner == "" {
		return nil, errors.New("account owner is required")
	}
	ctx.Store.SetStorage(ctx.Address, "owner", owner)
	return []types.Event{{Type: "account.owner_set", Attributes: map[string]string{"owner": owner}}}, nil
}

func (Account) Call(ctx Context, method string, args map[string]string) ([]types.Event, error) {
	switch method {
	case "setOwner":
		current := normalizeAddress(ctx.Store.GetStorage(ctx.Address, "owner"))
		if current == "" {
			return nil, errors.New("account owner is not set")
		}
		if normalizeAddress(ctx.Caller) != current {
			return nil, errors.New("account setOwner requires current owner")
		}
		next := normalizeAddress(args["owner"])
		if next == "" {
			return nil, errors.New("account owner is required")
		}
		ctx.Store.SetStorage(ctx.Address, "owner", next)
		return []types.Event{{Type: "account.owner_set", Attributes: map[string]string{"owner": next}}}, nil
	default:
		return nil, fmt.Errorf("unknown account method %q", method)
	}
}

func (Account) Read(ctx Context, method string, args map[string]string) (string, error) {
	switch method {
	case "owner":
		return normalizeAddress(ctx.Store.GetStorage(ctx.Address, "owner")), nil
	default:
		return "", fmt.Errorf("unknown account read method %q", method)
	}
}

func normalizeAddress(address string) string {
	return strings.ToLower(strings.TrimSpace(address))
}
