package node

import "chainlab/internal/core"

// baseFeeChange computes floor(baseFee*gasDelta/gasTarget/denominator)
// without allowing the intermediate product or quotient to wrap uint64.
func baseFeeChange(baseFee uint64, gasDelta uint64, gasTarget uint64, denominator uint64) uint64 {
	return core.BaseFeeChange(baseFee, gasDelta, gasTarget, denominator)
}
