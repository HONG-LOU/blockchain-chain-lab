package state_test

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"chainlab/internal/state"
	"chainlab/internal/types"
)

const snapshotTestValidator = "0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

func TestStorageLimitsApplyToWritesAndSnapshotRestore(t *testing.T) {
	address := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	for index := range state.MaxStorageEntriesPerAccount {
		if err := store.SetStorage(address, fmt.Sprintf("key:%04d", index), "value"); err != nil {
			t.Fatalf("write %d: %v", index, err)
		}
	}
	if err := store.SetStorage(address, "overflow", "value"); !errors.Is(err, state.ErrStorageLimit) {
		t.Fatalf("entry limit error = %v", err)
	}
	if err := store.SetStorage(address, "key:0000", "updated"); err != nil {
		t.Fatalf("existing entry update: %v", err)
	}
	if err := store.SetStorage(address, strings.Repeat("k", state.MaxStorageKeyBytes+1), "value"); !errors.Is(err, state.ErrStorageLimit) {
		t.Fatalf("key limit error = %v", err)
	}
	if err := store.SetStorage(address, "large", strings.Repeat("v", state.MaxStorageValueBytes+1)); !errors.Is(err, state.ErrStorageLimit) {
		t.Fatalf("value limit error = %v", err)
	}

	snapshot := store.Snapshot()
	account := snapshot.Accounts[address]
	account.Storage["snapshot-overflow"] = "value"
	snapshot.Accounts[address] = account
	if _, err := state.NewStoreFromSnapshot(snapshot); !errors.Is(err, state.ErrStorageLimit) {
		t.Fatalf("snapshot limit error = %v", err)
	}
}

func TestStorageTotalByteLimitAppliesToWritesAndSnapshotRestore(t *testing.T) {
	address := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	largeValue := strings.Repeat("v", state.MaxStorageValueBytes)
	for index := 0; index < 127; index++ {
		if err := store.SetStorage(address, fmt.Sprintf("key:%03d", index), largeValue); err != nil {
			t.Fatalf("write %d: %v", index, err)
		}
	}
	if err := store.SetStorage(address, "overflow", largeValue); !errors.Is(err, state.ErrStorageLimit) {
		t.Fatalf("total byte limit error = %v", err)
	}

	snapshot := store.Snapshot()
	account := snapshot.Accounts[address]
	account.Storage["overflow"] = largeValue
	snapshot.Accounts[address] = account
	if _, err := state.NewStoreFromSnapshot(snapshot); !errors.Is(err, state.ErrStorageLimit) {
		t.Fatalf("snapshot total byte limit error = %v", err)
	}
}

func TestMissingAccountReadsDoNotMutateState(t *testing.T) {
	store := state.NewStore()
	rootBefore := store.Root()
	address := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_ = store.GetAccount(address)
	_ = store.GetStorage(address, "key")
	_ = store.CodeID(address)
	_ = store.StorageEntryCount(address)
	_, _ = store.GetStorageWithExists(address, "key")
	_ = store.StorageSnapshot(address)
	_ = store.SortedStorageKeys(address)
	if store.Root() != rootBefore {
		t.Fatal("read of missing account mutated state root")
	}
}

func TestZeroValueTransferDoesNotCreateRecipientAccount(t *testing.T) {
	store := state.NewStore()
	from := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	to := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store.SetBalance(from, 100)
	rootBefore := store.Root()

	if err := store.Transfer(from, to, 0); err != nil {
		t.Fatal(err)
	}
	if store.Root() != rootBefore {
		t.Fatal("zero-value transfer changed state root")
	}
	if _, exists := store.Snapshot().Accounts[to]; exists {
		t.Fatal("zero-value transfer created recipient account")
	}
}

func TestSortedStorageKeysAreDetachedAndDoNotExposeValues(t *testing.T) {
	store := state.NewStore()
	address := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := store.SetStorage(address, "beta", "large-value-not-returned"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStorage(address, "alpha", "another-value"); err != nil {
		t.Fatal(err)
	}

	keys := store.SortedStorageKeys(address)
	if len(keys) != 2 || keys[0] != "alpha" || keys[1] != "beta" {
		t.Fatalf("sorted storage keys = %#v", keys)
	}
	keys[0] = "mutated"
	if !store.HasStorage(address, "alpha") || store.HasStorage(address, "mutated") {
		t.Fatal("mutating returned storage keys changed state")
	}
}

func TestBalancesNonceAndStorage(t *testing.T) {
	store := state.NewStore()
	alice := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	store.SetBalance(alice, 100)
	if err := store.Transfer(alice, bob, 35); err != nil {
		t.Fatal(err)
	}
	if err := store.IncrementNonce(alice); err != nil {
		t.Fatal(err)
	}
	store.SetStorage(alice, "role", "admin")

	if got := store.GetAccount(alice).Balance; got != 65 {
		t.Fatalf("alice balance = %d", got)
	}
	if got := store.GetAccount(alice).Nonce; got != 1 {
		t.Fatalf("alice nonce = %d", got)
	}
	if got := store.GetAccount(bob).Balance; got != 35 {
		t.Fatalf("bob balance = %d", got)
	}
	if got := store.GetStorage(alice, "role"); got != "admin" {
		t.Fatalf("storage role = %q", got)
	}
}

func TestIncrementNonceRejectsOverflow(t *testing.T) {
	store := state.NewStore()
	account := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store.SetNonce(account, math.MaxUint64)
	rootBefore := store.Root()

	if err := store.IncrementNonce(account); err == nil || err.Error() != "nonce overflow" {
		t.Fatalf("increment error = %v", err)
	}
	if got := store.GetAccount(account).Nonce; got != math.MaxUint64 {
		t.Fatalf("nonce after overflow = %d", got)
	}
	if store.Root() != rootBefore {
		t.Fatal("nonce overflow changed state root")
	}
}

func TestCloneIsIsolated(t *testing.T) {
	store := state.NewStore()
	alice := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store.SetBalance(alice, 100)

	clone := store.Clone()
	clone.SetBalance(alice, 1)
	clone.SetStorage(alice, "k", "v")

	if got := store.GetAccount(alice).Balance; got != 100 {
		t.Fatalf("original balance should remain 100, got %d", got)
	}
	if got := store.GetStorage(alice, "k"); got != "" {
		t.Fatalf("original storage should remain empty, got %q", got)
	}
}

func TestDeleteStorageRemovesKeyFromSnapshotAndRoot(t *testing.T) {
	store := newSnapshotTestStore()
	account := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	key := "session:0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb:limit"
	store.SetStorage(account, key, "100")
	rootWithKey := store.Root()

	store.DeleteStorage(account, key)
	if got := store.GetStorage(account, key); got != "" {
		t.Fatalf("deleted storage = %q", got)
	}
	if store.Root() == rootWithKey {
		t.Fatal("root did not change after deleting storage")
	}
	restored := restoredStore(t, store.Snapshot())
	if got := restored.GetStorage(account, key); got != "" {
		t.Fatalf("restored deleted storage = %q", got)
	}
}

func TestDeleteStoragePrefixIsolatedAcrossCloneSnapshotAndRoot(t *testing.T) {
	store := newSnapshotTestStore()
	account := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	sessionKeys := []string{
		"session:0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb:limit",
		"session:0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb:spent",
		"session:0xcccccccccccccccccccccccccccccccccccccccc:expires",
	}
	for _, key := range sessionKeys {
		store.SetStorage(account, key, "value")
	}
	store.SetStorage(account, "recovery:guardian", "enabled")
	store.SetStorage(account, "profile:name", "alice")
	rootWithSessions := store.Root()

	clone := store.Clone()
	if got := clone.DeleteStoragePrefix(account, "session:"); got != len(sessionKeys) {
		t.Fatalf("deleted session keys = %d, want %d", got, len(sessionKeys))
	}
	if got := clone.DeleteStoragePrefix(account, "session:"); got != 0 {
		t.Fatalf("deleted session keys on second call = %d, want 0", got)
	}
	if clone.Root() == rootWithSessions {
		t.Fatal("clone root did not change after deleting session storage")
	}
	for _, key := range sessionKeys {
		if got := clone.GetStorage(account, key); got != "" {
			t.Fatalf("clone storage %q = %q after prefix deletion", key, got)
		}
		if got := store.GetStorage(account, key); got != "value" {
			t.Fatalf("original storage %q = %q after clone deletion", key, got)
		}
	}
	if got := clone.GetStorage(account, "recovery:guardian"); got != "enabled" {
		t.Fatalf("unmatched recovery storage = %q", got)
	}
	if got := clone.GetStorage(account, "profile:name"); got != "alice" {
		t.Fatalf("unmatched profile storage = %q", got)
	}

	restored := restoredStore(t, clone.Snapshot())
	if restored.Root() != clone.Root() {
		t.Fatalf("restored root = %s, want %s", restored.Root(), clone.Root())
	}
	for _, key := range sessionKeys {
		if got := restored.GetStorage(account, key); got != "" {
			t.Fatalf("restored storage %q = %q after prefix deletion", key, got)
		}
	}
}

func TestRootIsDeterministic(t *testing.T) {
	alice := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	left := state.NewStore()
	left.SetBalance(alice, 100)
	left.SetBalance(bob, 20)
	left.SetStorage(alice, "x", "1")

	right := state.NewStore()
	right.SetStorage(alice, "x", "1")
	right.SetBalance(bob, 20)
	right.SetBalance(alice, 100)

	if left.Root() != right.Root() {
		t.Fatalf("same logical state must have same root: %s != %s", left.Root(), right.Root())
	}
}

func TestValidatorsArePartOfSnapshotAndRoot(t *testing.T) {
	validatorA := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	validatorB := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	store := state.NewStore()
	store.SetValidators([]string{validatorA})
	if err := store.AddValidator(validatorB); err != nil {
		t.Fatal(err)
	}

	validators := store.Validators()
	if len(validators) != 2 || validators[0] != validatorA || validators[1] != validatorB {
		t.Fatalf("validators = %#v", validators)
	}

	restored := restoredStore(t, store.Snapshot())
	restoredValidators := restored.Validators()
	if len(restoredValidators) != 2 || restoredValidators[0] != validatorA || restoredValidators[1] != validatorB {
		t.Fatalf("restored validators = %#v", restoredValidators)
	}
	if restored.Root() != store.Root() {
		t.Fatal("validator set should be included in state root")
	}

	changed := store.Clone()
	if err := changed.AddValidator("0xcccccccccccccccccccccccccccccccccccccccc"); err != nil {
		t.Fatal(err)
	}
	if changed.Root() == store.Root() {
		t.Fatal("changing validators should change state root")
	}
}

func TestRemoveValidatorUpdatesSnapshotAndRoot(t *testing.T) {
	validatorA := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	validatorB := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	store := state.NewStore()
	store.SetValidators([]string{validatorA, validatorB})
	before := store.Root()

	if err := store.RemoveValidator(validatorB); err != nil {
		t.Fatal(err)
	}
	validators := store.Validators()
	if len(validators) != 1 || validators[0] != validatorA {
		t.Fatalf("validators = %#v", validators)
	}
	if store.Root() == before {
		t.Fatal("removing validator should change state root")
	}

	restored := restoredStore(t, store.Snapshot())
	restoredValidators := restored.Validators()
	if len(restoredValidators) != 1 || restoredValidators[0] != validatorA {
		t.Fatalf("restored validators = %#v", restoredValidators)
	}
	if err := restored.RemoveValidator(validatorA); err == nil {
		t.Fatal("removing the last validator should fail")
	}
}

func TestDelegatedCodeIDPersistsThroughCloneSnapshotAndRoot(t *testing.T) {
	store := newSnapshotTestStore()
	alice := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	owner := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store.SetBalance(alice, 100)
	store.SetDelegatedCodeID(alice, "account.v1")
	store.SetStorage(alice, "owner", owner)
	root := store.Root()

	clone := store.Clone()
	if got := clone.GetAccount(alice).DelegatedCodeID; got != "account.v1" {
		t.Fatalf("clone delegated code id = %q", got)
	}
	if got := clone.GetStorage(alice, "owner"); got != owner {
		t.Fatalf("clone owner = %q", got)
	}

	restored := restoredStore(t, store.Snapshot())
	if got := restored.GetAccount(alice).DelegatedCodeID; got != "account.v1" {
		t.Fatalf("snapshot delegated code id = %q", got)
	}
	if restored.Root() != root {
		t.Fatalf("restored root = %s, want %s", restored.Root(), root)
	}

	restored.ClearDelegation(alice)
	if got := restored.GetAccount(alice).DelegatedCodeID; got != "" {
		t.Fatalf("cleared delegated code id = %q", got)
	}
	if got := restored.GetStorage(alice, "owner"); got != "" {
		t.Fatalf("cleared owner = %q", got)
	}
}

func TestContractCodeMeteringVersionPersistsThroughCloneSnapshotAndRoot(t *testing.T) {
	store := newSnapshotTestStore()
	bytecode := []byte{0x00, 0x61, 0x73, 0x6d}
	code := types.ContractCode{
		CodeID:          types.WASMCodeID(bytecode),
		Runtime:         "wasm",
		MeteringVersion: "chainlab-wasm-v1",
		Creator:         "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Bytecode:        "0x0061736d",
	}
	store.SetContractCode(code)
	root := store.Root()

	clone := store.Clone()
	cloned, ok := clone.ContractCode(code.CodeID)
	if !ok || cloned.MeteringVersion != code.MeteringVersion || clone.Root() != root {
		t.Fatalf("cloned contract code = %+v, root=%s", cloned, clone.Root())
	}
	restored := restoredStore(t, store.Snapshot())
	restoredCode, ok := restored.ContractCode(code.CodeID)
	if !ok || restoredCode.MeteringVersion != code.MeteringVersion || restored.Root() != root {
		t.Fatalf("restored contract code = %+v, root=%s", restoredCode, restored.Root())
	}

	legacy := store.Snapshot()
	legacyCode := legacy.Codes[code.CodeID]
	legacyCode.MeteringVersion = ""
	legacy.Codes[code.CodeID] = legacyCode
	if _, err := state.NewStoreFromSnapshot(legacy); err == nil || !strings.Contains(err.Error(), "metering version") {
		t.Fatalf("missing metering version error = %v", err)
	}
	unknown := store.Snapshot()
	unknownCode := unknown.Codes[code.CodeID]
	unknownCode.MeteringVersion = "chainlab-wasm-v2"
	unknown.Codes[code.CodeID] = unknownCode
	if _, err := state.NewStoreFromSnapshot(unknown); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unknown metering version error = %v", err)
	}
}

func TestClearDelegationRemovesAllAuthorizationStorage(t *testing.T) {
	store := newSnapshotTestStore()
	account := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store.SetDelegatedCodeID(account, "account.v1")
	storage := map[string]string{
		"owner": "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"session:0xcccccccccccccccccccccccccccccccccccccccc:limit": "100",
		"session:0xcccccccccccccccccccccccccccccccccccccccc:spent": "25",
		"recovery:guardian": "0xdddddddddddddddddddddddddddddddddddddddd",
		"recovery:delay":    "10",
		"profile:name":      "alice",
	}
	for key, value := range storage {
		store.SetStorage(account, key, value)
	}
	rootWithDelegation := store.Root()

	store.ClearDelegation(account)
	cleared := store.GetAccount(account)
	if cleared.DelegatedCodeID != "" {
		t.Fatalf("cleared delegated code id = %q", cleared.DelegatedCodeID)
	}
	for key := range cleared.Storage {
		if key == "owner" || strings.HasPrefix(key, "session:") || strings.HasPrefix(key, "recovery:") {
			t.Fatalf("authorization storage %q remained after clearing delegation", key)
		}
	}
	if got := store.GetStorage(account, "profile:name"); got != "alice" {
		t.Fatalf("unrelated profile storage = %q", got)
	}
	if store.Root() == rootWithDelegation {
		t.Fatal("root did not change after clearing delegation")
	}

	restored := restoredStore(t, store.Snapshot())
	if restored.Root() != store.Root() {
		t.Fatalf("restored root = %s, want %s", restored.Root(), store.Root())
	}
	restoredAccount := restored.GetAccount(account)
	for key := range restoredAccount.Storage {
		if key == "owner" || strings.HasPrefix(key, "session:") || strings.HasPrefix(key, "recovery:") {
			t.Fatalf("restored authorization storage %q remained after clearing delegation", key)
		}
	}
}

func TestCanonicalSnapshotRoundTripPreservesStateRoot(t *testing.T) {
	snapshot := canonicalSnapshotFixture()
	restored, err := state.NewStoreFromSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	if restoredSnapshot := restored.Snapshot(); !reflect.DeepEqual(restoredSnapshot, snapshot) {
		t.Fatalf("restored snapshot changed canonical state:\n got: %#v\nwant: %#v", restoredSnapshot, snapshot)
	}
	if got := restored.Validators(); len(got) != 1 || got[0] != snapshotTestValidator {
		t.Fatalf("restored validators = %#v", got)
	}
}

func TestSnapshotRestoreRejectsNonCanonicalConsensusState(t *testing.T) {
	account := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	other := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	codeID := types.WASMCodeID([]byte{0x00, 0x61, 0x73, 0x6d})
	otherCodeID := types.WASMCodeID([]byte{0x00, 0x61, 0x73, 0x6e})
	proposalID := types.ProposalID("0x" + strings.Repeat("1", 64))
	otherProposalID := types.ProposalID("0x" + strings.Repeat("2", 64))

	tests := []struct {
		name   string
		mutate func(*state.Snapshot)
	}{
		{
			name: "account normalized key collision",
			mutate: func(snapshot *state.Snapshot) {
				duplicate := snapshot.Accounts[account]
				duplicate.Address = strings.ToUpper(account)
				snapshot.Accounts[strings.ToUpper(account)] = duplicate
			},
		},
		{
			name: "account embedded address mismatch",
			mutate: func(snapshot *state.Snapshot) {
				entry := snapshot.Accounts[account]
				entry.Address = other
				snapshot.Accounts[account] = entry
			},
		},
		{
			name: "account embedded address missing",
			mutate: func(snapshot *state.Snapshot) {
				entry := snapshot.Accounts[account]
				entry.Address = ""
				snapshot.Accounts[account] = entry
			},
		},
		{
			name: "stake normalized key collision",
			mutate: func(snapshot *state.Snapshot) {
				snapshot.Stakes[strings.ToUpper(account)] = 2
			},
		},
		{
			name: "contract code embedded id mismatch",
			mutate: func(snapshot *state.Snapshot) {
				entry := snapshot.Codes[codeID]
				entry.CodeID = otherCodeID
				snapshot.Codes[codeID] = entry
			},
		},
		{
			name: "contract code duplicate embedded id",
			mutate: func(snapshot *state.Snapshot) {
				snapshot.Codes[otherCodeID] = snapshot.Codes[codeID]
			},
		},
		{
			name: "contract code id not canonical",
			mutate: func(snapshot *state.Snapshot) {
				entry := snapshot.Codes[codeID]
				delete(snapshot.Codes, codeID)
				entry.CodeID += " "
				snapshot.Codes[entry.CodeID] = entry
			},
		},
		{
			name: "contract code runtime mismatch",
			mutate: func(snapshot *state.Snapshot) {
				entry := snapshot.Codes[codeID]
				entry.Runtime = "native"
				snapshot.Codes[codeID] = entry
			},
		},
		{
			name: "contract code metering version missing",
			mutate: func(snapshot *state.Snapshot) {
				entry := snapshot.Codes[codeID]
				entry.MeteringVersion = ""
				snapshot.Codes[codeID] = entry
			},
		},
		{
			name: "contract code bytecode exceeds protocol limit",
			mutate: func(snapshot *state.Snapshot) {
				entry := snapshot.Codes[codeID]
				entry.Bytecode = "0x" + strings.Repeat("00", types.MaxWASMModuleBytes+1)
				snapshot.Codes[codeID] = entry
			},
		},
		{
			name: "contract code creator not canonical",
			mutate: func(snapshot *state.Snapshot) {
				entry := snapshot.Codes[codeID]
				entry.Creator = strings.ToUpper(entry.Creator)
				snapshot.Codes[codeID] = entry
			},
		},
		{
			name: "contract code bytecode hash mismatch",
			mutate: func(snapshot *state.Snapshot) {
				entry := snapshot.Codes[codeID]
				entry.Bytecode = "0x0061736e"
				snapshot.Codes[codeID] = entry
			},
		},
		{
			name: "proposal embedded id mismatch",
			mutate: func(snapshot *state.Snapshot) {
				entry := snapshot.Proposals[proposalID]
				entry.ID = otherProposalID
				snapshot.Proposals[proposalID] = entry
			},
		},
		{
			name: "proposal duplicate embedded id",
			mutate: func(snapshot *state.Snapshot) {
				snapshot.Proposals[otherProposalID] = snapshot.Proposals[proposalID]
			},
		},
		{
			name: "proposal status invalid",
			mutate: func(snapshot *state.Snapshot) {
				entry := snapshot.Proposals[proposalID]
				entry.Status = "pending"
				snapshot.Proposals[proposalID] = entry
			},
		},
		{
			name: "proposal vote choice invalid",
			mutate: func(snapshot *state.Snapshot) {
				entry := snapshot.Proposals[proposalID]
				entry.Votes["maybe"] = 1
				snapshot.Proposals[proposalID] = entry
			},
		},
		{
			name: "proposal voter normalized key collision",
			mutate: func(snapshot *state.Snapshot) {
				entry := snapshot.Proposals[proposalID]
				entry.Voters[strings.ToUpper(account)] = "yes"
				snapshot.Proposals[proposalID] = entry
			},
		},
		{
			name: "proposal voter choice invalid",
			mutate: func(snapshot *state.Snapshot) {
				entry := snapshot.Proposals[proposalID]
				entry.Voters[account] = "maybe"
				snapshot.Proposals[proposalID] = entry
			},
		},
		{
			name: "parameter key empty",
			mutate: func(snapshot *state.Snapshot) {
				snapshot.Params[""] = "value"
			},
		},
		{
			name: "parameter key not canonical",
			mutate: func(snapshot *state.Snapshot) {
				snapshot.Params[" block.gas "] = "value"
			},
		},
		{
			name: "validator set empty",
			mutate: func(snapshot *state.Snapshot) {
				snapshot.Validators = nil
			},
		},
		{
			name: "validator not canonical",
			mutate: func(snapshot *state.Snapshot) {
				snapshot.Validators[0] = strings.ToUpper(snapshot.Validators[0])
			},
		},
		{
			name: "validator duplicated",
			mutate: func(snapshot *state.Snapshot) {
				snapshot.Validators = append(snapshot.Validators, snapshot.Validators[0])
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			snapshot := canonicalSnapshotFixture()
			test.mutate(&snapshot)
			if _, err := state.NewStoreFromSnapshot(snapshot); err == nil {
				t.Fatal("invalid snapshot was accepted")
			}
		})
	}
}

func canonicalSnapshotFixture() state.Snapshot {
	account := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bytecode := []byte{0x00, 0x61, 0x73, 0x6d}
	proposalID := types.ProposalID("0x" + strings.Repeat("1", 64))
	store := newSnapshotTestStore()
	store.SetBalance(account, 100)
	_ = store.AddStake(account, 25)
	store.SetContractCode(types.ContractCode{
		CodeID:          types.WASMCodeID(bytecode),
		Runtime:         "wasm",
		MeteringVersion: "chainlab-wasm-v1",
		Creator:         account,
		Bytecode:        "0x0061736d",
	})
	store.SetProposal(types.Proposal{
		ID:              proposalID,
		Proposer:        account,
		Title:           "Raise gas limit",
		Description:     "Bounded capacity change",
		Kind:            "param.change",
		Param:           "block.gas_limit",
		Value:           "40000000",
		Status:          types.ProposalStatusOpen,
		SubmitHeight:    10,
		VotingEndHeight: 20,
		Votes:           map[string]uint64{"yes": 25},
		Voters:          map[string]string{account: "yes"},
	})
	store.SetParam("block.gas_limit", "30000000")
	return store.Snapshot()
}

func newSnapshotTestStore() *state.Store {
	store := state.NewStore()
	store.SetValidators([]string{snapshotTestValidator})
	return store
}

func restoredStore(t *testing.T, snapshot state.Snapshot) *state.Store {
	t.Helper()
	store, err := state.NewStoreFromSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
