package contracts

import (
	"errors"
	"math"
	"testing"
)

func TestLimitedMeterStopsAtLimit(t *testing.T) {
	meter := NewLimitedMeter(10)
	if err := meter.Charge(4); err != nil {
		t.Fatal(err)
	}
	if got := meter.Remaining(); got != 6 {
		t.Fatalf("remaining gas = %d, want 6", got)
	}
	if err := meter.Charge(6); err != nil {
		t.Fatal(err)
	}
	if got := meter.GasUsed(); got != 10 {
		t.Fatalf("gas used = %d, want 10", got)
	}
	if err := meter.Charge(1); !errors.Is(err, ErrContractOutOfGas) {
		t.Fatalf("charge beyond limit error = %v", err)
	}
	if got := meter.GasUsed(); got != 10 {
		t.Fatalf("gas used after out of gas = %d, want 10", got)
	}
	if err := meter.Charge(0); !errors.Is(err, ErrContractOutOfGas) {
		t.Fatalf("out-of-gas meter must remain exhausted, got %v", err)
	}
}

func TestLimitedMeterHandlesZeroAndUint64Boundary(t *testing.T) {
	zero := NewLimitedMeter(0)
	if err := zero.Charge(1); !errors.Is(err, ErrContractOutOfGas) {
		t.Fatalf("zero-limit error = %v", err)
	}

	max := NewLimitedMeter(math.MaxUint64)
	if err := max.Charge(math.MaxUint64); err != nil {
		t.Fatal(err)
	}
	if err := max.Charge(1); !errors.Is(err, ErrContractOutOfGas) {
		t.Fatalf("uint64 boundary error = %v", err)
	}
}
