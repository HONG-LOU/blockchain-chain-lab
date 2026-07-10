package contracts

import (
	"fmt"
	"math"
	"strconv"

	"chainlab/internal/types"
)

type Counter struct{}

func (Counter) Deploy(ctx Context, args map[string]string) ([]types.Event, error) {
	initial := args["initial"]
	if initial == "" {
		initial = "0"
	}
	parsed, err := strconv.ParseUint(initial, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid initial counter value: %w", err)
	}
	if strconv.FormatUint(parsed, 10) != initial {
		return nil, fmt.Errorf("invalid initial counter value")
	}
	if err := ctx.SetStorage("count", initial); err != nil {
		return nil, err
	}
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
		if strconv.FormatUint(delta, 10) != amount {
			return nil, fmt.Errorf("invalid increment amount")
		}
		currentRaw, err := ctx.GetStorage("count")
		if err != nil {
			return nil, err
		}
		if currentRaw == "" {
			return nil, fmt.Errorf("%w: stored counter value is missing", ErrContractStateFault)
		}
		current, err := strconv.ParseUint(currentRaw, 10, 64)
		if err != nil || strconv.FormatUint(current, 10) != currentRaw {
			return nil, fmt.Errorf("%w: invalid stored counter value", ErrContractStateFault)
		}
		if math.MaxUint64-current < delta {
			return nil, fmt.Errorf("counter overflow")
		}
		next := current + delta
		nextRaw := strconv.FormatUint(next, 10)
		if err := ctx.SetStorage("count", nextRaw); err != nil {
			return nil, err
		}
		return []types.Event{{Type: "counter.incremented", Attributes: map[string]string{"count": nextRaw}}}, nil
	default:
		return nil, fmt.Errorf("unknown counter method %q", method)
	}
}

func (Counter) Read(ctx Context, method string, args map[string]string) (string, error) {
	switch method {
	case "get":
		current, err := ctx.GetStorage("count")
		if err != nil {
			return "", err
		}
		if current == "" {
			return "", fmt.Errorf("%w: stored counter value is missing", ErrContractStateFault)
		}
		parsed, err := strconv.ParseUint(current, 10, 64)
		if err != nil || strconv.FormatUint(parsed, 10) != current {
			return "", fmt.Errorf("%w: invalid stored counter value", ErrContractStateFault)
		}
		return current, nil
	default:
		return "", fmt.Errorf("unknown counter read method %q", method)
	}
}
