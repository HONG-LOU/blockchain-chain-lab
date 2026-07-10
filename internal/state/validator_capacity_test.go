package state_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"chainlab/internal/state"
	"chainlab/internal/types"
)

func TestStateValidatorCapacityAndAtomicFailures(t *testing.T) {
	maximum := stateCapacityValidators(types.MaxValidators)
	store := state.NewStore()
	if err := store.SetValidators(maximum); err != nil {
		t.Fatalf("SetValidators maximum: %v", err)
	}
	if got := store.Validators(); !reflect.DeepEqual(got, maximum) {
		t.Fatalf("maximum validator set mismatch: got %d entries", len(got))
	}

	rootBefore := store.Root()
	validatorsBefore := store.Validators()
	overflowValidator := stateCapacityValidator(types.MaxValidators + 1)
	overflow := append(append([]string(nil), maximum...), overflowValidator)
	if err := store.SetValidators(overflow); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("exceeds %d", types.MaxValidators)) {
		t.Fatalf("SetValidators overflow error = %v", err)
	}
	assertStateValidatorSetUnchanged(t, store, rootBefore, validatorsBefore, "SetValidators overflow")

	if err := store.AddValidator(overflowValidator); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("exceeds %d", types.MaxValidators)) {
		t.Fatalf("AddValidator overflow error = %v", err)
	}
	assertStateValidatorSetUnchanged(t, store, rootBefore, validatorsBefore, "AddValidator overflow")

	snapshot := store.Snapshot()
	restored, err := state.NewStoreFromSnapshot(snapshot)
	if err != nil {
		t.Fatalf("restore maximum validator snapshot: %v", err)
	}
	if got := restored.Validators(); !reflect.DeepEqual(got, maximum) {
		t.Fatalf("restored maximum validator set mismatch: got %d entries", len(got))
	}

	overflowSnapshot := snapshot
	overflowSnapshot.Validators = overflow
	if _, err := state.NewStoreFromSnapshot(overflowSnapshot); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("exceeds %d", types.MaxValidators)) {
		t.Fatalf("overflow snapshot restore error = %v", err)
	}
	assertStateValidatorSetUnchanged(t, store, rootBefore, validatorsBefore, "snapshot overflow")
}

func assertStateValidatorSetUnchanged(t *testing.T, store *state.Store, root string, validators []string, operation string) {
	t.Helper()
	if store.Root() != root {
		t.Fatalf("%s changed the state root", operation)
	}
	if got := store.Validators(); !reflect.DeepEqual(got, validators) {
		t.Fatalf("%s changed validators: got %d entries", operation, len(got))
	}
}

func stateCapacityValidators(count int) []string {
	validators := make([]string, count)
	for index := range validators {
		validators[index] = stateCapacityValidator(index + 1)
	}
	return validators
}

func stateCapacityValidator(sequence int) string {
	return fmt.Sprintf("0x%040x", sequence)
}
