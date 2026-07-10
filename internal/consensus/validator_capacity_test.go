package consensus_test

import (
	"fmt"
	"strings"
	"testing"

	"chainlab/internal/consensus"
	"chainlab/internal/types"
)

func TestConsensusValidatorSetCapacity(t *testing.T) {
	maximum := consensusCapacityValidators(types.MaxValidators)
	if err := consensus.ValidateValidatorSet(maximum); err != nil {
		t.Fatalf("maximum validator set rejected: %v", err)
	}

	overflow := append(append([]string(nil), maximum...), consensusCapacityValidator(types.MaxValidators+1))
	if err := consensus.ValidateValidatorSet(overflow); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("exceeds %d", types.MaxValidators)) {
		t.Fatalf("overflow validator set error = %v", err)
	}
	if len(maximum) != types.MaxValidators {
		t.Fatalf("maximum validator set mutated, length = %d", len(maximum))
	}
}

func consensusCapacityValidators(count int) []string {
	validators := make([]string, count)
	for index := range validators {
		validators[index] = consensusCapacityValidator(index + 1)
	}
	return validators
}

func consensusCapacityValidator(sequence int) string {
	return fmt.Sprintf("0x%040x", sequence)
}
