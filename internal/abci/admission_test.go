package abci

import (
	"bytes"
	"context"
	"sort"
	"strings"
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmtsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
)

func TestGenesisCertifiedValidatorAdmissionActivatesCandidate(t *testing.T) {
	fixture := newValidatorV2Fixture(t, "")
	candidateKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
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
	fixture.initialize(t)

	identity, exists := fixture.app.committed.store.ValidatorIdentityByAccount(certificate.Identity.Account)
	if !exists || identity != certificate.Identity {
		t.Fatalf("certified identity=%+v exists=%t", identity, exists)
	}
	fixture.finalizeAndCommit(t, 1, 0, nil)
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
	t.Cleanup(func() { _ = fixture.app.Close() })
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
			name: "tampered power",
			mutate: func(genesis *GenesisDocument) {
				genesis.Admissions[0].Identity.Power++
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
