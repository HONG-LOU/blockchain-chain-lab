package node

import (
	"math"
	"math/bits"
)

// baseFeeChange computes floor(baseFee*gasDelta/gasTarget/denominator)
// without allowing the intermediate product or quotient to wrap uint64.
func baseFeeChange(baseFee uint64, gasDelta uint64, gasTarget uint64, denominator uint64) uint64 {
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
