package contracts

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"chainlab/internal/types"
)

const AccountCodeID = "account.v1"
const MultisigCodeID = "multisig.v1"

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

type Multisig struct{}

func (Multisig) Deploy(ctx Context, args map[string]string) ([]types.Event, error) {
	return configureMultisig(ctx, args)
}

func (Multisig) Call(ctx Context, method string, args map[string]string) ([]types.Event, error) {
	switch method {
	case "setOwners":
		if normalizeAddress(ctx.Caller) != normalizeAddress(ctx.Address) {
			return nil, errors.New("multisig setOwners requires self call")
		}
		return configureMultisig(ctx, args)
	default:
		return nil, fmt.Errorf("unknown multisig method %q", method)
	}
}

func (Multisig) Read(ctx Context, method string, args map[string]string) (string, error) {
	switch method {
	case "owners":
		return ctx.Store.GetStorage(ctx.Address, "owners"), nil
	case "threshold":
		return ctx.Store.GetStorage(ctx.Address, "threshold"), nil
	default:
		return "", fmt.Errorf("unknown multisig read method %q", method)
	}
}

func configureMultisig(ctx Context, args map[string]string) ([]types.Event, error) {
	owners, err := parseMultisigOwners(args["owners"])
	if err != nil {
		return nil, err
	}
	threshold, err := parseMultisigThreshold(args["threshold"], len(owners))
	if err != nil {
		return nil, err
	}
	ownersRaw := strings.Join(owners, ",")
	thresholdRaw := strconv.FormatUint(uint64(threshold), 10)
	ctx.Store.SetStorage(ctx.Address, "owners", ownersRaw)
	ctx.Store.SetStorage(ctx.Address, "threshold", thresholdRaw)
	return []types.Event{{Type: "multisig.configured", Attributes: map[string]string{
		"owners":    ownersRaw,
		"threshold": thresholdRaw,
	}}}, nil
}

func parseMultisigOwners(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	owners := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		owner := normalizeAddress(part)
		if owner == "" {
			continue
		}
		if _, ok := seen[owner]; ok {
			return nil, errors.New("multisig owners must be unique")
		}
		seen[owner] = struct{}{}
		owners = append(owners, owner)
	}
	if len(owners) == 0 {
		return nil, errors.New("multisig owners are required")
	}
	return owners, nil
}

func parseMultisigThreshold(raw string, ownerCount int) (uint64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, errors.New("multisig threshold is required")
	}
	threshold, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || threshold == 0 {
		return 0, errors.New("multisig threshold must be positive")
	}
	if threshold > uint64(ownerCount) {
		return 0, errors.New("multisig threshold exceeds owner count")
	}
	return threshold, nil
}
