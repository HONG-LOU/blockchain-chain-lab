package node

import (
	"math"
	"math/big"
	"testing"

	"chainlab/internal/types"
)

func TestBaseFeeChangeMatchesArbitraryPrecisionReference(t *testing.T) {
	tests := []struct {
		name        string
		baseFee     uint64
		gasDelta    uint64
		gasTarget   uint64
		denominator uint64
	}{
		{name: "default full block high fee", baseFee: math.MaxUint64 / 2, gasDelta: 15_000_000, gasTarget: 15_000_000, denominator: 8},
		{name: "maximum base fee", baseFee: math.MaxUint64, gasDelta: 15_000_000, gasTarget: 15_000_000, denominator: 8},
		{name: "quotient temporarily exceeds uint64", baseFee: math.MaxUint64, gasDelta: 2, gasTarget: 1, denominator: 8},
		{name: "large target", baseFee: math.MaxUint64, gasDelta: math.MaxUint64/2 + 1, gasTarget: math.MaxUint64 / 2, denominator: 8},
		{name: "saturating result", baseFee: math.MaxUint64, gasDelta: math.MaxUint64, gasTarget: 1, denominator: 1},
		{name: "zero delta", baseFee: math.MaxUint64, gasDelta: 0, gasTarget: 1, denominator: 8},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := baseFeeChange(test.baseFee, test.gasDelta, test.gasTarget, test.denominator)
			want := referenceBaseFeeChange(test.baseFee, test.gasDelta, test.gasTarget, test.denominator)
			if got != want {
				t.Fatalf("base fee change = %d, want %d", got, want)
			}
		})
	}
}

func TestNextBaseFeeUsesOverflowSafeArithmetic(t *testing.T) {
	const gasLimit = uint64(30_000_000)
	tests := []struct {
		name    string
		baseFee uint64
		gasUsed uint64
		want    uint64
	}{
		{
			name:    "full block at half maximum",
			baseFee: math.MaxUint64 / 2,
			gasUsed: gasLimit,
			want:    math.MaxUint64/2 + (math.MaxUint64/2)/baseFeeChangeDenominator,
		},
		{
			name:    "full block saturates maximum",
			baseFee: math.MaxUint64,
			gasUsed: gasLimit,
			want:    math.MaxUint64,
		},
		{
			name:    "empty block at maximum",
			baseFee: math.MaxUint64,
			gasUsed: 0,
			want:    math.MaxUint64 - math.MaxUint64/baseFeeChangeDenominator,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parent := types.Block{Header: types.BlockHeader{
				GasLimit:      gasLimit,
				GasUsed:       test.gasUsed,
				BaseFeePerGas: test.baseFee,
			}}
			if got := NextBaseFee(parent, 0); got != test.want {
				t.Fatalf("next base fee = %d, want %d", got, test.want)
			}
		})
	}
}

func referenceBaseFeeChange(baseFee uint64, gasDelta uint64, gasTarget uint64, denominator uint64) uint64 {
	if gasTarget == 0 || denominator == 0 {
		return 0
	}
	value := new(big.Int).SetUint64(baseFee)
	value.Mul(value, new(big.Int).SetUint64(gasDelta))
	value.Div(value, new(big.Int).SetUint64(gasTarget))
	value.Div(value, new(big.Int).SetUint64(denominator))
	if !value.IsUint64() {
		return math.MaxUint64
	}
	return value.Uint64()
}
