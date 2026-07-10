package contracts

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/types"
)

type Token struct{}

func (Token) Deploy(ctx Context, args map[string]string) ([]types.Event, error) {
	symbol := args["symbol"]
	if !validTokenSymbol(symbol) {
		return nil, errors.New("token symbol must be 2-16 uppercase ASCII letters or digits")
	}
	if err := ctx.SetStorage("symbol", symbol); err != nil {
		return nil, err
	}
	if err := ctx.SetStorage("owner", ctx.Caller()); err != nil {
		return nil, err
	}
	return []types.Event{{Type: "token.initialized", Attributes: map[string]string{"symbol": symbol}}}, nil
}

func validTokenSymbol(symbol string) bool {
	if len(symbol) < 2 || len(symbol) > 16 {
		return false
	}
	for index := range len(symbol) {
		value := symbol[index]
		if (value < 'A' || value > 'Z') && (value < '0' || value > '9') {
			return false
		}
	}
	return true
}

func (Token) Call(ctx Context, method string, args map[string]string) ([]types.Event, error) {
	switch method {
	case "mint":
		owner, err := storedTokenOwner(ctx)
		if err != nil {
			return nil, err
		}
		if owner != ctx.Caller() {
			return nil, errors.New("only token owner can mint")
		}
		to, err := canonicalTokenAddress(args["to"])
		if err != nil {
			return nil, err
		}
		amount, err := parseAmount(args["amount"])
		if err != nil {
			return nil, err
		}
		if err := addTokenBalance(ctx, to, amount); err != nil {
			return nil, err
		}
		return []types.Event{{Type: "token.minted", Attributes: map[string]string{"to": to, "amount": args["amount"]}}}, nil
	case "transfer":
		to, err := canonicalTokenAddress(args["to"])
		if err != nil {
			return nil, err
		}
		amount, err := parseAmount(args["amount"])
		if err != nil {
			return nil, err
		}
		if err := subTokenBalance(ctx, ctx.Caller(), amount); err != nil {
			return nil, err
		}
		if err := addTokenBalance(ctx, to, amount); err != nil {
			return nil, err
		}
		return []types.Event{{Type: "token.transferred", Attributes: map[string]string{"from": ctx.Caller(), "to": to, "amount": args["amount"]}}}, nil
	default:
		return nil, fmt.Errorf("unknown token method %q", method)
	}
}

func (Token) Read(ctx Context, method string, args map[string]string) (string, error) {
	switch method {
	case "balanceOf":
		address := args["address"]
		if address == "" {
			address = args["owner"]
		}
		if address == "" {
			return "", errors.New("address is required")
		}
		address, err := canonicalTokenAddress(address)
		if err != nil {
			return "", err
		}
		balance, err := tokenBalance(ctx, address)
		if err != nil {
			return "", err
		}
		return strconv.FormatUint(balance, 10), nil
	case "symbol":
		symbol, err := ctx.GetStorage("symbol")
		if err != nil {
			return "", err
		}
		if !validTokenSymbol(symbol) {
			return "", fmt.Errorf("%w: invalid stored token symbol", ErrContractStateFault)
		}
		return symbol, nil
	case "owner":
		return storedTokenOwner(ctx)
	default:
		return "", fmt.Errorf("unknown token read method %q", method)
	}
}

func parseAmount(raw string) (uint64, error) {
	if raw == "" {
		return 0, errors.New("amount is required")
	}
	amount, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid amount: %w", err)
	}
	if amount == 0 {
		return 0, errors.New("amount must be positive")
	}
	if strconv.FormatUint(amount, 10) != raw {
		return 0, errors.New("amount must be canonically encoded")
	}
	return amount, nil
}

func addTokenBalance(ctx Context, address string, amount uint64) error {
	current, err := tokenBalance(ctx, address)
	if err != nil {
		return err
	}
	if math.MaxUint64-current < amount {
		return errors.New("token balance overflow")
	}
	return ctx.SetStorage("balance:"+address, strconv.FormatUint(current+amount, 10))
}

func subTokenBalance(ctx Context, address string, amount uint64) error {
	current, err := tokenBalance(ctx, address)
	if err != nil {
		return err
	}
	if current < amount {
		return errors.New("insufficient token balance")
	}
	key := "balance:" + address
	remaining := current - amount
	if remaining == 0 {
		return ctx.DeleteStorage(key)
	}
	return ctx.SetStorage(key, strconv.FormatUint(remaining, 10))
}

func tokenBalance(ctx Context, address string) (uint64, error) {
	raw, err := ctx.GetStorage("balance:" + strings.ToLower(address))
	if err != nil {
		return 0, err
	}
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 || strconv.FormatUint(value, 10) != raw {
		return 0, fmt.Errorf("%w: invalid stored token balance", ErrContractStateFault)
	}
	return value, nil
}

func storedTokenOwner(ctx Context) (string, error) {
	raw, err := ctx.GetStorage("owner")
	if err != nil {
		return "", err
	}
	owner, err := chaincrypto.NormalizeAddress(raw)
	if err != nil || owner != raw {
		return "", fmt.Errorf("%w: invalid stored token owner", ErrContractStateFault)
	}
	return owner, nil
}

func canonicalTokenAddress(raw string) (string, error) {
	address, err := chaincrypto.NormalizeAddress(raw)
	if err != nil || address != raw {
		return "", errors.New("token address must be a canonical 20-byte address")
	}
	return address, nil
}
