package contracts

import (
	"fmt"
	"strconv"

	"chainlab/internal/types"
)

type Counter struct{}

func (Counter) Deploy(ctx Context, args map[string]string) ([]types.Event, error) {
	initial := args["initial"]
	if initial == "" {
		initial = "0"
	}
	if _, err := strconv.ParseUint(initial, 10, 64); err != nil {
		return nil, fmt.Errorf("invalid initial counter value: %w", err)
	}
	ctx.Store.SetStorage(ctx.Address, "count", initial)
	return []types.Event{{Type: "counter.initialized", Attributes: map[string]string{"count": initial}}}, nil
}

func (Counter) Call(ctx Context, method string, args map[string]string) ([]types.Event, error) {
	switch method {
	case "increment":
		amount := args["amount"]
		if amount == "" {
			amount = "1"
		}
		delta, err := strconv.ParseUint(amount, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid increment amount: %w", err)
		}
		currentRaw := ctx.Store.GetStorage(ctx.Address, "count")
		if currentRaw == "" {
			currentRaw = "0"
		}
		current, err := strconv.ParseUint(currentRaw, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid stored counter value: %w", err)
		}
		next := current + delta
		nextRaw := strconv.FormatUint(next, 10)
		ctx.Store.SetStorage(ctx.Address, "count", nextRaw)
		return []types.Event{{Type: "counter.incremented", Attributes: map[string]string{"count": nextRaw}}}, nil
	default:
		return nil, fmt.Errorf("unknown counter method %q", method)
	}
}
