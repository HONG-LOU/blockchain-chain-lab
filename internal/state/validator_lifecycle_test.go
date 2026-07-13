package state_test

import (
	"encoding/hex"
	"strings"
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"

	cmtsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
)

func TestValidatorLifecycleAcceptsFutureNonGenesisIdentity(t *testing.T) {
	genesisKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	candidateKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	genesis := validatorIdentity(genesisKey, 1)
	candidate := validatorIdentity(candidateKey, 11)
	store := state.NewStore()
	if err := store.SetValidators([]string{genesis.Account}); err != nil {
		t.Fatal(err)
	}
	if err := store.InitializeValidatorLifecycle([]state.ValidatorIdentity{genesis}); err != nil {
		t.Fatal(err)
	}
	lifecycle, _ := store.ValidatorLifecycle()
	lifecycle.Validators[candidate.ConsensusAddress] = candidate
	if err := store.SetValidatorLifecycle(lifecycle); err != nil {
		t.Fatalf("future candidate identity: %v", err)
	}
	got, exists := store.ValidatorIdentityByAccount(candidate.Account)
	if !exists || got != candidate {
		t.Fatalf("candidate identity = %+v exists=%t", got, exists)
	}
	wantRoot := store.ValidatorRoot()
	restored, err := state.NewStoreFromSnapshot(store.Snapshot())
	if err != nil {
		t.Fatalf("restore future candidate: %v", err)
	}
	restoredCandidate, exists := restored.ValidatorIdentityByAccount(candidate.Account)
	if !exists || restoredCandidate != candidate || restored.ValidatorRoot() != wantRoot {
		t.Fatalf("restored candidate=%+v exists=%t root=%s want=%s", restoredCandidate, exists, restored.ValidatorRoot(), wantRoot)
	}
}

func TestValidatorLifecycleRequiresEveryGenesisIdentity(t *testing.T) {
	firstKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	first := validatorIdentity(firstKey, 1)
	second := validatorIdentity(secondKey, 1)
	store := state.NewStore()
	if err := store.SetValidators([]string{first.Account, second.Account}); err != nil {
		t.Fatal(err)
	}
	if err := store.InitializeValidatorLifecycle([]state.ValidatorIdentity{first}); err == nil || !strings.Contains(err.Error(), "missing genesis account") {
		t.Fatalf("missing genesis identity error = %v", err)
	}
}

func TestValidatorLifecycleRejectsInvalidFutureIdentity(t *testing.T) {
	genesisKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	candidateKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	genesis := validatorIdentity(genesisKey, 1)
	store := state.NewStore()
	if err := store.SetValidators([]string{genesis.Account}); err != nil {
		t.Fatal(err)
	}
	if err := store.InitializeValidatorLifecycle([]state.ValidatorIdentity{genesis}); err != nil {
		t.Fatal(err)
	}

	t.Run("non-positive activation", func(t *testing.T) {
		lifecycle, _ := store.ValidatorLifecycle()
		candidate := validatorIdentity(candidateKey, 0)
		lifecycle.Validators[candidate.ConsensusAddress] = candidate
		if err := store.SetValidatorLifecycle(lifecycle); err == nil || !strings.Contains(err.Error(), "active height must be positive") {
			t.Fatalf("activation error = %v", err)
		}
	})

	t.Run("duplicate account", func(t *testing.T) {
		lifecycle, _ := store.ValidatorLifecycle()
		candidate := validatorIdentity(candidateKey, 11)
		candidate.Account = genesis.Account
		lifecycle.Validators[candidate.ConsensusAddress] = candidate
		if err := store.SetValidatorLifecycle(lifecycle); err == nil || !strings.Contains(err.Error(), "public key does not match account") {
			t.Fatalf("duplicate account error = %v", err)
		}
	})
}

func validatorIdentity(key chaincrypto.PrivateKey, activeHeight int64) state.ValidatorIdentity {
	publicKey := key.PubKey().SerializeCompressed()
	return state.ValidatorIdentity{
		Account:          chaincrypto.AddressFromPrivateKey(key),
		ConsensusAddress: hex.EncodeToString(cmtsecp256k1.PubKey(publicKey).Address()),
		PublicKey:        hex.EncodeToString(publicKey),
		Power:            1,
		ActiveHeight:     activeHeight,
	}
}
