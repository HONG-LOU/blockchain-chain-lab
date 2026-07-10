package contracts

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/types"
)

const AccountCodeID = "account.v1"
const MultisigCodeID = "multisig.v1"
const MaxMultisigOwners = 16

type Account struct{}

func (Account) Deploy(ctx Context, args map[string]string) ([]types.Event, error) {
	owner, err := validatedAccountOwner(args["owner"], ctx.Address())
	if err != nil {
		return nil, err
	}
	if err := ctx.SetStorage("owner", owner); err != nil {
		return nil, err
	}
	return []types.Event{{Type: "account.owner_set", Attributes: map[string]string{"owner": owner}}}, nil
}

func (Account) Call(ctx Context, method string, args map[string]string) ([]types.Event, error) {
	switch method {
	case "setOwner":
		current, err := storedAccountOwner(ctx)
		if err != nil {
			return nil, err
		}
		if normalizeAddress(ctx.Caller()) != current {
			return nil, errors.New("account setOwner requires current owner")
		}
		next, err := validatedAccountOwner(args["owner"], ctx.Address())
		if err != nil {
			return nil, err
		}
		if err := ctx.SetStorage("owner", next); err != nil {
			return nil, err
		}
		for _, key := range []string{
			"recovery:pending_owner",
			"recovery:execute_after",
			"recovery:expires_at",
			"recovery:approvals",
			"recovery:votes",
		} {
			if err := ctx.DeleteStorage(key); err != nil {
				return nil, err
			}
		}
		if _, err := ctx.DeleteStoragePrefix("session:"); err != nil {
			return nil, err
		}
		return []types.Event{{Type: "account.owner_set", Attributes: map[string]string{
			"old_owner": current,
			"owner":     next,
		}}}, nil
	default:
		return nil, fmt.Errorf("unknown account method %q", method)
	}
}

func (Account) Read(ctx Context, method string, args map[string]string) (string, error) {
	switch method {
	case "owner":
		return storedAccountOwner(ctx)
	default:
		return "", fmt.Errorf("unknown account read method %q", method)
	}
}

func storedAccountOwner(ctx Context) (string, error) {
	raw, err := ctx.GetStorage("owner")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("%w: account owner is not set", ErrContractStateFault)
	}
	owner, err := chaincrypto.NormalizeAddress(raw)
	if err != nil || owner != raw {
		return "", fmt.Errorf("%w: invalid stored account owner", ErrContractStateFault)
	}
	if owner == normalizeAddress(ctx.Address()) {
		return "", fmt.Errorf("%w: stored account owner equals account", ErrContractStateFault)
	}
	return owner, nil
}

func validatedAccountOwner(raw string, account string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", errors.New("account owner is required")
	}
	owner, err := chaincrypto.NormalizeAddress(raw)
	if errors.Is(err, chaincrypto.ErrInvalidAddress) {
		return "", errors.New("account owner must be a 20-byte hex address")
	}
	if errors.Is(err, chaincrypto.ErrZeroAddress) {
		return "", errors.New("account owner must not be the zero address")
	}
	if err != nil {
		return "", err
	}
	if owner != raw {
		return "", errors.New("account owner must be canonically encoded")
	}
	if owner == normalizeAddress(account) {
		return "", errors.New("account owner must differ from account address")
	}
	return owner, nil
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
		if normalizeAddress(ctx.Caller()) != normalizeAddress(ctx.Address()) {
			return nil, errors.New("multisig setOwners requires self call")
		}
		return configureMultisig(ctx, args)
	default:
		return nil, fmt.Errorf("unknown multisig method %q", method)
	}
}

func (Multisig) Read(ctx Context, method string, args map[string]string) (string, error) {
	owners, threshold, err := storedMultisigConfiguration(ctx)
	if err != nil {
		return "", err
	}
	switch method {
	case "owners":
		return owners, nil
	case "threshold":
		return threshold, nil
	default:
		return "", fmt.Errorf("unknown multisig read method %q", method)
	}
}

func storedMultisigConfiguration(ctx Context) (string, string, error) {
	ownersRaw, err := ctx.GetStorage("owners")
	if err != nil {
		return "", "", err
	}
	thresholdRaw, err := ctx.GetStorage("threshold")
	if err != nil {
		return "", "", err
	}
	owners, err := parseMultisigOwners(ownersRaw)
	if err != nil || strings.Join(owners, ",") != ownersRaw {
		return "", "", fmt.Errorf("%w: invalid stored multisig owners", ErrContractStateFault)
	}
	threshold, err := parseMultisigThreshold(thresholdRaw, len(owners))
	if err != nil || strconv.FormatUint(threshold, 10) != thresholdRaw {
		return "", "", fmt.Errorf("%w: invalid stored multisig threshold", ErrContractStateFault)
	}
	return ownersRaw, thresholdRaw, nil
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
	if err := ctx.SetStorage("owners", ownersRaw); err != nil {
		return nil, err
	}
	if err := ctx.SetStorage("threshold", thresholdRaw); err != nil {
		return nil, err
	}
	return []types.Event{{Type: "multisig.configured", Attributes: map[string]string{
		"owners":    ownersRaw,
		"threshold": thresholdRaw,
	}}}, nil
}

func parseMultisigOwners(raw string) ([]string, error) {
	if len(raw) > MaxMultisigOwners*43-1 {
		return nil, fmt.Errorf("multisig owner count exceeds %d", MaxMultisigOwners)
	}
	parts := strings.Split(raw, ",")
	if len(parts) > MaxMultisigOwners {
		return nil, fmt.Errorf("multisig owner count exceeds %d", MaxMultisigOwners)
	}
	owners := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		owner, err := chaincrypto.NormalizeAddress(part)
		if err != nil || owner != part {
			return nil, errors.New("multisig owner must be a canonical 20-byte address")
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
