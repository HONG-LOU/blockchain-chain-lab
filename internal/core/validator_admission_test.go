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

func TestRuntimeValidatorAdmissionAuthorizationWindow(t *testing.T) {
	for _, test := range []struct {
		name                string
		authorizationHeight int64
		blockHeight         uint64
		validatorRoot       string
		window              int64
		wantError           bool
	}{
		{name: "previous height", authorizationHeight: 9, blockHeight: 10, validatorRoot: "root"},
		{name: "delayed within enabled window", authorizationHeight: 4, blockHeight: 10, validatorRoot: "root", window: 6},
		{name: "genesis authorization", authorizationHeight: 0, blockHeight: 1, validatorRoot: "root"},
		{name: "delayed with legacy window", authorizationHeight: 8, blockHeight: 10, validatorRoot: "root", wantError: true},
		{name: "same height", authorizationHeight: 10, blockHeight: 10, validatorRoot: "root", wantError: true},
		{name: "future height", authorizationHeight: 11, blockHeight: 10, validatorRoot: "root", wantError: true},
		{name: "older than enabled window", authorizationHeight: 3, blockHeight: 10, validatorRoot: "root", window: 6, wantError: true},
		{name: "changed root", authorizationHeight: 9, blockHeight: 10, validatorRoot: "other", wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateRuntimeAdmissionAuthorization(
				RuntimeValidatorAdmissionCertificate{AuthorizationHeight: test.authorizationHeight, ValidatorRoot: test.validatorRoot},
				ExecutionContext{
					BlockHeight: test.blockHeight, ValidatorEpochLength: 6,
					ValidatorRuntimeAdmissionWindow: test.window, ValidatorAdmissionAuthorizationRoot: "root",
				},
			)
			if (err != nil) != test.wantError {
				t.Fatalf("authorization error = %v, wantError=%t", err, test.wantError)
			}
		})
	}
}
