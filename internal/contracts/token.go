package contracts

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"chainlab/internal/types"
)

type Token struct{}

func (Token) Deploy(ctx Context, args map[string]string) ([]types.Event, error) {
	symbol := args["symbol"]
	if symbol == "" {
		return nil, errors.New("token symbol is required")
	}
	ctx.Store.SetStorage(ctx.Address, "symbol", symbol)
	ctx.Store.SetStorage(ctx.Address, "owner", ctx.Caller)
	return []types.Event{{Type: "token.initialized", Attributes: map[string]string{"symbol": symbol}}}, nil
}

func (Token) Call(ctx Context, method string, args map[string]string) ([]types.Event, error) {
	switch method {
	case "mint":
		if ctx.Store.GetStorage(ctx.Address, "owner") != ctx.Caller {
			return nil, errors.New("only token owner can mint")
		}
		to := strings.ToLower(args["to"])
		amount, err := parseAmount(args["amount"])
		if err != nil {
			return nil, err
		}
		if err := addTokenBalance(ctx, to, amount); err != nil {
			return nil, err
		}
		return []types.Event{{Type: "token.minted", Attributes: map[string]string{"to": to, "amount": args["amount"]}}}, nil
	case "transfer":
		to := strings.ToLower(args["to"])
		amount, err := parseAmount(args["amount"])
		if err != nil {
			return nil, err
		}
		if err := subTokenBalance(ctx, ctx.Caller, amount); err != nil {
			return nil, err
		}
		if err := addTokenBalance(ctx, to, amount); err != nil {
			return nil, err
		}
		return []types.Event{{Type: "token.transferred", Attributes: map[string]string{"from": ctx.Caller, "to": to, "amount": args["amount"]}}}, nil
	default:
		return nil, fmt.Errorf("unknown token method %q", method)
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
	return amount, nil
}

func addTokenBalance(ctx Context, address string, amount uint64) error {
	current := tokenBalance(ctx, address)
	if math.MaxUint64-current < amount {
		return errors.New("token balance overflow")
	}
	ctx.Store.SetStorage(ctx.Address, "balance:"+address, strconv.FormatUint(current+amount, 10))
	return nil
}

func subTokenBalance(ctx Context, address string, amount uint64) error {
	current := tokenBalance(ctx, address)
	if current < amount {
		return errors.New("insufficient token balance")
	}
	ctx.Store.SetStorage(ctx.Address, "balance:"+address, strconv.FormatUint(current-amount, 10))
	return nil
}

func tokenBalance(ctx Context, address string) uint64 {
	raw := ctx.Store.GetStorage(ctx.Address, "balance:"+strings.ToLower(address))
	if raw == "" {
		return 0
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0
	}
	return value
}
