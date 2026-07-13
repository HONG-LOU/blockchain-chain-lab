package abci

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"testing"

	"chainlab/internal/core"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"
	"chainlab/internal/types"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmtsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
)

func TestGenesisCertifiedValidatorAdmissionActivatesCandidate(t *testing.T) {
	fixture := newValidatorV2Fixture(t, "")
	candidateKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	genesisStore, err := state.NewStoreFromSnapshot(fixture.genesis.State)
	if err != nil {
		t.Fatal(err)
	}
	if err := genesisStore.AddStake(chaincrypto.AddressFromPrivateKey(candidateKey), state.ValidatorStakePerPower); err != nil {
		t.Fatal(err)
	}
	genesisStore.SetBalance(chaincrypto.AddressFromPrivateKey(candidateKey), 1_000_000)
	fixture.genesis.State = genesisStore.Snapshot()
	certificate := signedValidatorAdmission(t, fixture.genesis.ChainID, candidateKey, 5, fixture.keys)
	fixture.genesis.Admissions = []ValidatorAdmissionCertificate{certificate}
	fixture.genesisBytes, err = fixture.genesis.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	_ = fixture.app.Close()
	dataDir := t.TempDir()
	fixture.app, err = NewApplication(Config{Genesis: fixture.genesis, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fixture.app.Close() })
	fixture.initialize(t)
	unstake := signFixtureTransaction(t, candidateKey, types.Transaction{
		ChainID: fixture.genesis.ChainID, Type: types.TxUnstake,
		From: certificate.Identity.Account, Nonce: 0, Value: 1, GasLimit: 100_000, GasPrice: 1,
	})
	unstakeRaw := rawFixtureTransaction(t, unstake)
	checked, err := fixture.app.CheckTx(context.Background(), &abcitypes.RequestCheckTx{
		Tx: unstakeRaw,
	})
	if err != nil || checked.Code != CodeOK || !strings.Contains(string(checked.Data), `"failure_code":"execution_reverted"`) ||
		fixture.app.committed.store.StakeOf(certificate.Identity.Account) != state.ValidatorStakePerPower {
		t.Fatalf("validator unstake response=%+v err=%v", checked, err)
	}

	identity, exists := fixture.app.committed.store.ValidatorIdentityByAccount(certificate.Identity.Account)
	if !exists || identity != certificate.Identity {
		t.Fatalf("certified identity=%+v exists=%t", identity, exists)
	}
	heightOne, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(1), Height: 1, Time: validatorV2BlockTime(1),
		ProposerAddress: fixture.proposerAddresses[0], Txs: [][]byte{unstakeRaw},
	})
	if err != nil || len(heightOne.TxResults) != 1 || heightOne.TxResults[0].Code != CodeExecutionFailed {
		t.Fatalf("validator unstake block response=%+v err=%v", heightOne, err)
	}
	if _, err := fixture.app.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.app.committed.store.StakeOf(certificate.Identity.Account); got != state.ValidatorStakePerPower {
		t.Fatalf("validator stake after rejected unstake = %d", got)
	}
	fixture.finalizeAndCommit(t, 2, 0, nil)
	heightThree := fixture.finalizeAndCommit(t, 3, 0, nil)
	if len(heightThree.ValidatorUpdates) != 1 || heightThree.ValidatorUpdates[0].Power != certificate.Identity.Power ||
		!bytes.Equal(heightThree.ValidatorUpdates[0].PubKey.GetSecp256K1(), candidateKey.PubKey().SerializeCompressed()) {
		t.Fatalf("height-3 admission update = %+v", heightThree.ValidatorUpdates)
	}
	if err := fixture.app.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.app, err = NewApplication(Config{Genesis: fixture.genesis, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	restored, exists := fixture.app.committed.store.ValidatorIdentityByAccount(certificate.Identity.Account)
	if !exists || restored != certificate.Identity {
		t.Fatalf("restored certified identity=%+v exists=%t", restored, exists)
	}
	fixture.finalizeAndCommit(t, 4, 0, nil)
	candidateAddress := cmtsecp256k1.PubKey(candidateKey.PubKey().SerializeCompressed()).Address()
	response, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(5), Height: 5, Time: validatorV2BlockTime(5), ProposerAddress: candidateAddress,
	})
	if err != nil {
		t.Fatalf("candidate proposer at activation: %v", err)
	}
	if response == nil || len(response.ValidatorUpdates) != 0 {
		t.Fatalf("candidate activation block response = %+v", response)
	}
}

func TestGenesisCertifiedValidatorAdmissionRejectsInvalidCertificates(t *testing.T) {
	fixture := newValidatorV2Fixture(t, "")
	candidateKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	genesisStore, err := state.NewStoreFromSnapshot(fixture.genesis.State)
	if err != nil {
		t.Fatal(err)
	}
	if err := genesisStore.AddStake(chaincrypto.AddressFromPrivateKey(candidateKey), state.ValidatorStakePerPower); err != nil {
		t.Fatal(err)
	}
	fixture.genesis.State = genesisStore.Snapshot()
	valid := signedValidatorAdmission(t, fixture.genesis.ChainID, candidateKey, 5, fixture.keys)

	for _, test := range []struct {
		name   string
		mutate func(*GenesisDocument)
		want   string
	}{
		{
			name: "certificate capacity",
			mutate: func(genesis *GenesisDocument) {
				genesis.Admissions = make([]ValidatorAdmissionCertificate, maxValidatorAdmissionCertificates+1)
			},
			want: "exceed 64 certificates",
		},
		{
			name: "protocol version 1",
			mutate: func(genesis *GenesisDocument) {
				genesis.Protocol = ProtocolVersion
				genesis.ValidatorPolicy = nil
			},
			want: "require protocol version 2",
		},
		{
			name: "below quorum",
			mutate: func(genesis *GenesisDocument) {
				genesis.Admissions[0].Signatures = genesis.Admissions[0].Signatures[:1]
			},
			want: "requires between 2 and 2 signatures",
		},
		{
			name: "duplicate candidate",
			mutate: func(genesis *GenesisDocument) {
				genesis.Admissions = append(genesis.Admissions, genesis.Admissions[0])
			},
			want: "duplicates an account",
		},
		{
			name: "insufficient stake",
			mutate: func(genesis *GenesisDocument) {
				store, err := state.NewStoreFromSnapshot(genesis.State)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.SubStake(valid.Identity.Account, state.ValidatorStakePerPower); err != nil {
					t.Fatal(err)
				}
				genesis.State = store.Snapshot()
			},
			want: "requires at least 1000 stake",
		},
		{
			name: "tampered power",
			mutate: func(genesis *GenesisDocument) {
				genesis.Admissions[0].Identity.Power++
			},
			want: "does not match stake-derived power",
		},
		{
			name: "tampered activation",
			mutate: func(genesis *GenesisDocument) {
				genesis.Admissions[0].Identity.ActiveHeight = 9
			},
			want: "does not authorize",
		},
		{
			name: "cross-chain replay",
			mutate: func(genesis *GenesisDocument) {
				genesis.ChainID = "chainlab-validator-v2-other"
			},
			want: "does not authorize",
		},
		{
			name: "duplicate signer",
			mutate: func(genesis *GenesisDocument) {
				genesis.Admissions[0].Signatures[1] = genesis.Admissions[0].Signatures[0]
			},
			want: "ordered by validator",
		},
		{
			name: "misaligned activation",
			mutate: func(genesis *GenesisDocument) {
				genesis.Admissions[0].Identity.ActiveHeight = 4
			},
			want: "future epoch",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			genesis := fixture.genesis
			genesis.Admissions = cloneValidatorAdmissions([]ValidatorAdmissionCertificate{valid})
			test.mutate(&genesis)
			if _, err := genesis.CanonicalBytes(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("certificate error=%v, want %q", err, test.want)
			}
		})
	}
}

func TestRuntimeCertifiedValidatorAdmissionsShareCommittedRoot(t *testing.T) {
	fixture := newValidatorV2Fixture(t, "")
	candidateKeys := make([]chaincrypto.PrivateKey, 2)
	for index := range candidateKeys {
		key, err := chaincrypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		candidateKeys[index] = key
	}
	store, err := state.NewStoreFromSnapshot(fixture.genesis.State)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidateKey := range candidateKeys {
		candidate := chaincrypto.AddressFromPrivateKey(candidateKey)
		store.SetBalance(candidate, 1_000_000)
		if err := store.AddStake(candidate, state.ValidatorStakePerPower); err != nil {
			t.Fatal(err)
		}
	}
	fixture.genesis.State = store.Snapshot()
	fixture.genesis.ValidatorPolicy.RuntimeAdmissions = true
	fixture.genesisBytes, err = fixture.genesis.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	_ = fixture.app.Close()
	fixture.app, err = NewApplication(Config{Genesis: fixture.genesis})
	if err != nil {
		t.Fatal(err)
	}
	fixture.initialize(t)

	authorizationRoot := fixture.app.committed.store.ValidatorRoot()
	certificates := make([]core.RuntimeValidatorAdmissionCertificate, len(candidateKeys))
	joinRaw := make([][]byte, len(candidateKeys))
	for index, candidateKey := range candidateKeys {
		certificate := signedRuntimeValidatorAdmission(
			t, fixture.genesis.ChainID, candidateKey, 0, authorizationRoot, 5, fixture.keys,
		)
		encoded, err := core.EncodeRuntimeValidatorAdmission(certificate)
		if err != nil {
			t.Fatal(err)
		}
		join := signFixtureTransaction(t, candidateKey, types.Transaction{
			ChainID: fixture.genesis.ChainID, Type: types.TxValidatorJoin,
			From: certificate.Identity.Account, Nonce: 0, GasLimit: 100_000, GasPrice: 1,
			Payload: map[string]string{"certificate": encoded},
		})
		certificates[index] = certificate
		joinRaw[index] = rawFixtureTransaction(t, join)
	}
	heightOne, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(1), Height: 1, Time: validatorV2BlockTime(1),
		ProposerAddress: fixture.proposerAddresses[0], Txs: joinRaw,
	})
	if err != nil || len(heightOne.TxResults) != len(candidateKeys) {
		t.Fatalf("runtime admission response=%+v err=%v", heightOne, err)
	}
	for index, result := range heightOne.TxResults {
		if result.Code != CodeOK {
			t.Fatalf("runtime admission %d result=%+v", index, result)
		}
	}
	if _, err := fixture.app.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	for _, certificate := range certificates {
		identity, exists := fixture.app.committed.store.ValidatorIdentityByAccount(certificate.Identity.Account)
		if !exists || identity != certificate.Identity {
			t.Fatalf("runtime admitted identity=%+v exists=%t", identity, exists)
		}
	}
	fixture.finalizeAndCommit(t, 2, 0, nil)
	heightThree := fixture.finalizeAndCommit(t, 3, 0, nil)
	if len(heightThree.ValidatorUpdates) != len(candidateKeys) {
		t.Fatalf("runtime admission update=%+v", heightThree.ValidatorUpdates)
	}
	wantPublicKeys := make(map[string]struct{}, len(candidateKeys))
	for _, candidateKey := range candidateKeys {
		wantPublicKeys[bytesToHex(candidateKey.PubKey().SerializeCompressed())] = struct{}{}
	}
	for _, update := range heightThree.ValidatorUpdates {
		key := bytesToHex(update.PubKey.GetSecp256K1())
		if update.Power != 1 {
			t.Fatalf("runtime admission update=%+v", update)
		}
		delete(wantPublicKeys, key)
	}
	if len(wantPublicKeys) != 0 {
		t.Fatalf("runtime admission updates missed %d candidates", len(wantPublicKeys))
	}
	fixture.finalizeAndCommit(t, 4, 0, nil)
	publicKey := candidateKeys[0].PubKey().SerializeCompressed()
	response, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(5), Height: 5, Time: validatorV2BlockTime(5),
		ProposerAddress: cmtsecp256k1.PubKey(publicKey).Address(),
	})
	if err != nil || response == nil {
		t.Fatalf("runtime candidate proposer response=%+v err=%v", response, err)
	}
}

func signedRuntimeValidatorAdmission(
	t *testing.T,
	chainID string,
	candidateKey chaincrypto.PrivateKey,
	authorizationHeight int64,
	validatorRoot string,
	activeHeight int64,
	signerKeys []chaincrypto.PrivateKey,
) core.RuntimeValidatorAdmissionCertificate {
	t.Helper()
	publicKey := candidateKey.PubKey().SerializeCompressed()
	certificate := core.RuntimeValidatorAdmissionCertificate{
		AuthorizationHeight: authorizationHeight,
		ValidatorRoot:       validatorRoot,
		Identity: state.ValidatorIdentity{
			Account:          chaincrypto.AddressFromPrivateKey(candidateKey),
			ConsensusAddress: bytesToHex(cmtsecp256k1.PubKey(publicKey).Address()),
			PublicKey:        bytesToHex(publicKey), Power: 1, ActiveHeight: activeHeight,
		},
	}
	payload, err := core.RuntimeValidatorAdmissionSigningBytes(chainID, certificate)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range signerKeys {
		signature, err := chaincrypto.Sign(key, payload)
		if err != nil {
			t.Fatal(err)
		}
		certificate.Signatures = append(certificate.Signatures, core.RuntimeValidatorAdmissionSignature{
			Validator: chaincrypto.AddressFromPrivateKey(key), Signature: signature,
		})
	}
	sort.Slice(certificate.Signatures, func(left int, right int) bool {
		return certificate.Signatures[left].Validator < certificate.Signatures[right].Validator
	})
	return certificate
}

func signedValidatorAdmission(
	t *testing.T,
	chainID string,
	candidateKey chaincrypto.PrivateKey,
	activeHeight int64,
	signerKeys []chaincrypto.PrivateKey,
) ValidatorAdmissionCertificate {
	t.Helper()
	publicKey := candidateKey.PubKey().SerializeCompressed()
	identity := state.ValidatorIdentity{
		Account:          chaincrypto.AddressFromPrivateKey(candidateKey),
		ConsensusAddress: bytesToHex(cmtsecp256k1.PubKey(publicKey).Address()),
		PublicKey:        bytesToHex(publicKey), Power: 1, ActiveHeight: activeHeight,
	}
	payload, err := ValidatorAdmissionSigningBytes(chainID, identity)
	if err != nil {
		t.Fatal(err)
	}
	signatures := make([]ValidatorAdmissionSignature, len(signerKeys))
	for index, key := range signerKeys {
		signature, err := chaincrypto.Sign(key, payload)
		if err != nil {
			t.Fatal(err)
		}
		signatures[index] = ValidatorAdmissionSignature{
			Validator: chaincrypto.AddressFromPrivateKey(key), Signature: signature,
		}
	}
	sort.Slice(signatures, func(left int, right int) bool {
		return signatures[left].Validator < signatures[right].Validator
	})
	return ValidatorAdmissionCertificate{Identity: identity, Signatures: signatures}
}
