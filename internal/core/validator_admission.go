package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/hash"
	"chainlab/internal/state"
	"chainlab/internal/types"
)

const (
	runtimeValidatorAdmissionDomain     = "chainlab-runtime-validator-admission-v1"
	maxRuntimeAdmissionCertificateBytes = 512 * 1024
	runtimeAdmissionByteGas             = uint64(16)
	runtimeAdmissionSignatureGas        = uint64(5_000)
)

type RuntimeValidatorAdmissionSignature struct {
	Validator string `json:"validator"`
	Signature string `json:"signature"`
}

type RuntimeValidatorAdmissionCertificate struct {
	AuthorizationHeight int64                                `json:"authorization_height"`
	ValidatorRoot       string                               `json:"validator_root"`
	Identity            state.ValidatorIdentity              `json:"identity"`
	Signatures          []RuntimeValidatorAdmissionSignature `json:"signatures"`
}

type runtimeValidatorAdmissionPayload struct {
	Domain              string                  `json:"domain"`
	ChainID             string                  `json:"chain_id"`
	AuthorizationHeight int64                   `json:"authorization_height"`
	ValidatorRoot       string                  `json:"validator_root"`
	Identity            state.ValidatorIdentity `json:"identity"`
}

func RuntimeValidatorAdmissionSigningBytes(
	chainID string,
	certificate RuntimeValidatorAdmissionCertificate,
) ([]byte, error) {
	if err := types.ValidateChainID(chainID); err != nil {
		return nil, err
	}
	if certificate.AuthorizationHeight < 0 {
		return nil, errors.New("runtime validator admission authorization height must be non-negative")
	}
	if err := types.ValidateCanonicalHash("runtime validator admission root", certificate.ValidatorRoot); err != nil {
		return nil, err
	}
	if err := state.ValidateValidatorIdentity(certificate.Identity); err != nil {
		return nil, err
	}
	return hash.CanonicalBytes(runtimeValidatorAdmissionPayload{
		Domain: runtimeValidatorAdmissionDomain, ChainID: chainID,
		AuthorizationHeight: certificate.AuthorizationHeight,
		ValidatorRoot:       certificate.ValidatorRoot, Identity: certificate.Identity,
	})
}

func EncodeRuntimeValidatorAdmission(certificate RuntimeValidatorAdmissionCertificate) (string, error) {
	raw, err := hash.CanonicalBytes(certificate)
	if err != nil {
		return "", err
	}
	if len(raw) > maxRuntimeAdmissionCertificateBytes {
		return "", fmt.Errorf("runtime validator admission exceeds %d bytes", maxRuntimeAdmissionCertificateBytes)
	}
	return string(raw), nil
}

func parseRuntimeValidatorAdmission(raw string) (RuntimeValidatorAdmissionCertificate, error) {
	if raw == "" || len(raw) > maxRuntimeAdmissionCertificateBytes {
		return RuntimeValidatorAdmissionCertificate{}, fmt.Errorf(
			"runtime validator admission certificate must contain between 1 and %d bytes",
			maxRuntimeAdmissionCertificateBytes,
		)
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var certificate RuntimeValidatorAdmissionCertificate
	if err := decoder.Decode(&certificate); err != nil {
		return RuntimeValidatorAdmissionCertificate{}, fmt.Errorf("decode runtime validator admission: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return RuntimeValidatorAdmissionCertificate{}, errors.New("runtime validator admission must contain one JSON value")
	}
	canonical, err := EncodeRuntimeValidatorAdmission(certificate)
	if err != nil {
		return RuntimeValidatorAdmissionCertificate{}, err
	}
	if raw != canonical {
		return RuntimeValidatorAdmissionCertificate{}, errors.New("runtime validator admission is not canonically encoded")
	}
	return certificate, nil
}

func validateRuntimeValidatorAdmissionMessage(tx types.Transaction) error {
	if tx.To != "" || tx.Value != 0 || len(tx.Batch) != 0 || len(tx.Payload) != 1 {
		return errors.New("validator join supports only one certificate payload")
	}
	_, err := parseRuntimeValidatorAdmission(tx.Payload["certificate"])
	return err
}

func executeRuntimeValidatorAdmission(
	store *state.Store,
	tx types.Transaction,
	context ExecutionContext,
) (types.Event, error) {
	if !context.ValidatorRuntimeAdmissions {
		return types.Event{}, errors.New("runtime validator admissions are disabled")
	}
	if context.BlockHeight == 0 || context.BlockHeight > math.MaxInt64 || context.ValidatorEpochLength < 2 {
		return types.Event{}, errors.New("runtime validator admission context is invalid")
	}
	certificate, err := parseRuntimeValidatorAdmission(tx.Payload["certificate"])
	if err != nil {
		return types.Event{}, err
	}
	identity := certificate.Identity
	if identity.Account != tx.From {
		return types.Event{}, errors.New("validator join sender must equal the candidate account")
	}
	if certificate.AuthorizationHeight != int64(context.BlockHeight)-1 ||
		certificate.ValidatorRoot != context.ValidatorAdmissionAuthorizationRoot {
		return types.Event{}, errors.New("runtime validator admission does not authorize the current state")
	}
	if _, exists := store.ValidatorIdentityByAccount(identity.Account); exists {
		return types.Event{}, errors.New("runtime validator admission candidate already exists")
	}
	minimumActivation := int64(context.BlockHeight) + 2
	if identity.ActiveHeight < minimumActivation ||
		(identity.ActiveHeight-1)%context.ValidatorEpochLength != 0 ||
		identity.InactiveHeight != 0 || identity.UnbondingHeight != 0 {
		return types.Event{}, errors.New("runtime validator admission activation schedule is invalid")
	}
	power, err := state.ValidatorPowerFromStake(store.StakeOf(identity.Account))
	if err != nil || identity.Power != power {
		return types.Event{}, errors.New("runtime validator admission power does not match candidate stake")
	}
	lifecycle, exists := store.ValidatorLifecycle()
	if !exists {
		return types.Event{}, errors.New("runtime validator admission requires validator lifecycle state")
	}
	if err := validateRuntimeAdmissionQuorum(tx.ChainID, certificate, lifecycle); err != nil {
		return types.Event{}, err
	}
	lifecycle.Validators[identity.ConsensusAddress] = identity
	if err := store.SetValidatorLifecycle(lifecycle); err != nil {
		return types.Event{}, err
	}
	return types.Event{Type: "validator.admission_scheduled", Attributes: map[string]string{
		"validator": identity.Account, "consensus_address": identity.ConsensusAddress,
		"power":         fmt.Sprintf("%d", identity.Power),
		"active_height": fmt.Sprintf("%d", identity.ActiveHeight),
	}}, nil
}

func estimateRuntimeValidatorAdmissionGas(raw string) (uint64, error) {
	certificate, err := parseRuntimeValidatorAdmission(raw)
	if err != nil {
		return 0, err
	}
	base, err := EstimateGas(types.TxValidatorJoin)
	if err != nil {
		return 0, err
	}
	byteGas, err := checkedMul(uint64(len(raw)), runtimeAdmissionByteGas)
	if err != nil {
		return 0, err
	}
	signatureGas, err := checkedMul(uint64(len(certificate.Signatures)), runtimeAdmissionSignatureGas)
	if err != nil {
		return 0, err
	}
	total, err := checkedAdd(base, byteGas)
	if err != nil {
		return 0, err
	}
	return checkedAdd(total, signatureGas)
}

func validateRuntimeAdmissionQuorum(
	chainID string,
	certificate RuntimeValidatorAdmissionCertificate,
	lifecycle state.ValidatorLifecycle,
) error {
	if len(certificate.Signatures) == 0 || len(certificate.Signatures) > len(lifecycle.Validators) {
		return errors.New("runtime validator admission signature count is invalid")
	}
	payload, err := RuntimeValidatorAdmissionSigningBytes(chainID, certificate)
	if err != nil {
		return err
	}
	byAccount := make(map[string]state.ValidatorIdentity, len(lifecycle.Validators))
	var totalPower int64
	signerHeight := certificate.AuthorizationHeight
	if signerHeight == 0 {
		signerHeight = 1
	}
	for _, identity := range lifecycle.Validators {
		if signerHeight < identity.ActiveHeight ||
			(identity.InactiveHeight != 0 && signerHeight >= identity.InactiveHeight) {
			continue
		}
		byAccount[identity.Account] = identity
		totalPower += identity.Power
	}
	var signedPower int64
	previous := ""
	for index, item := range certificate.Signatures {
		validator, err := chaincrypto.NormalizeAddress(item.Validator)
		if err != nil || validator != item.Validator || validator <= previous {
			return fmt.Errorf("runtime validator admission signature %d signer ordering is invalid", index)
		}
		identity, active := byAccount[validator]
		if !active {
			return fmt.Errorf("runtime validator admission signature %d is not from an active validator", index)
		}
		if err := types.ValidateCanonicalSignature("runtime validator admission signature", item.Signature); err != nil ||
			!chaincrypto.Verify(validator, payload, item.Signature) {
			return fmt.Errorf("runtime validator admission signature %d is invalid", index)
		}
		signedPower += identity.Power
		previous = validator
	}
	if totalPower <= 0 || signedPower*3 <= totalPower*2 {
		return errors.New("runtime validator admission does not have more than two-thirds voting power")
	}
	return nil
}
