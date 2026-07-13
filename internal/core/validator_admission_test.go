package core

import (
	"strings"
	"testing"

	"chainlab/internal/state"
	"chainlab/internal/types"
)

func TestRuntimeValidatorAdmissionGasAccountsForCertificateWork(t *testing.T) {
	certificate := RuntimeValidatorAdmissionCertificate{
		AuthorizationHeight: 1,
		ValidatorRoot:       "0x" + strings.Repeat("a", 64),
		Identity: state.ValidatorIdentity{
			Account:          "0x1111111111111111111111111111111111111111",
			ConsensusAddress: strings.Repeat("22", 20),
			PublicKey:        "02" + strings.Repeat("33", 32),
			Power:            1,
			ActiveHeight:     11,
		},
		Signatures: []RuntimeValidatorAdmissionSignature{
			{Validator: "0x4444444444444444444444444444444444444444", Signature: "0x" + strings.Repeat("55", 65)},
			{Validator: "0x6666666666666666666666666666666666666666", Signature: "0x" + strings.Repeat("77", 65)},
		},
	}
	raw, err := EncodeRuntimeValidatorAdmission(certificate)
	if err != nil {
		t.Fatal(err)
	}

	gas, err := estimateRuntimeValidatorAdmissionGas(raw)
	if err != nil {
		t.Fatal(err)
	}
	base, err := EstimateGas(types.TxValidatorJoin)
	if err != nil {
		t.Fatal(err)
	}
	want := base + uint64(len(raw))*runtimeAdmissionByteGas +
		uint64(len(certificate.Signatures))*runtimeAdmissionSignatureGas
	if gas != want {
		t.Fatalf("runtime validator admission gas = %d, want %d", gas, want)
	}
}

func TestRuntimeValidatorAdmissionGasRejectsNonCanonicalCertificate(t *testing.T) {
	if _, err := estimateRuntimeValidatorAdmissionGas(`{"signatures":[]}`); err == nil ||
		!strings.Contains(err.Error(), "not canonically encoded") {
		t.Fatalf("non-canonical certificate error = %v", err)
	}
}
