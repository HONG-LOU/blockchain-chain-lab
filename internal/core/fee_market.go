package core

import (
	"math"
	"math/bits"

	"chainlab/internal/types"
)

const baseFeeChangeDenominator uint64 = 8

func NextBaseFee(parentBaseFee uint64, parentGasUsed uint64, gasLimit uint64) uint64 {
	if parentBaseFee == 0 {
		parentBaseFee = types.InitialBaseFeePerGas
	}
	if gasLimit == 0 {
		gasLimit = types.DefaultBlockGasLimit
	}
	target := gasLimit / 2
	if target == 0 {
		target = 1
	}
	if parentGasUsed == target {
		return parentBaseFee
	}
	if parentGasUsed > target {
		increase := BaseFeeChange(parentBaseFee, parentGasUsed-target, target, baseFeeChangeDenominator)
		if increase == 0 {
			increase = 1
		}
		if math.MaxUint64-parentBaseFee < increase {
			return math.MaxUint64
		}
		return parentBaseFee + increase
	}

	decrease := BaseFeeChange(parentBaseFee, target-parentGasUsed, target, baseFeeChangeDenominator)
	if decrease >= parentBaseFee {
		return types.InitialBaseFeePerGas
	}
	next := parentBaseFee - decrease
	if next < types.InitialBaseFeePerGas {
		return types.InitialBaseFeePerGas
	}
	return next
}

// BaseFeeChange computes floor(baseFee*gasDelta/gasTarget/denominator)
// without allowing the intermediate product or quotient to wrap uint64.
func BaseFeeChange(baseFee uint64, gasDelta uint64, gasTarget uint64, denominator uint64) uint64 {
	if gasTarget == 0 || denominator == 0 || baseFee == 0 || gasDelta == 0 {
		return 0
	}

	productHigh, productLow := bits.Mul64(baseFee, gasDelta)
	quotientHigh := productHigh / gasTarget
	remainderHigh := productHigh % gasTarget
	quotientLow, _ := bits.Div64(remainderHigh, productLow, gasTarget)

	resultHigh := quotientHigh / denominator
	resultLow, _ := bits.Div64(quotientHigh%denominator, quotientLow, denominator)
	if resultHigh != 0 {
		return math.MaxUint64
	}
	return resultLow
}
