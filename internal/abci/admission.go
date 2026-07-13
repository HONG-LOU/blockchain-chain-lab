package abci

import (
	"errors"
	"fmt"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/hash"
	"chainlab/internal/state"
	"chainlab/internal/types"
)

const (
	validatorAdmissionDomain          = "chainlab-validator-admission-v1"
	maxValidatorAdmissionCertificates = 64
	maxValidatorAdmissionSignatures   = types.MaxValidators
)

type ValidatorAdmissionSignature struct {
	Validator string `json:"validator"`
	Signature string `json:"signature"`
}

type ValidatorAdmissionCertificate struct {
	Identity   state.ValidatorIdentity       `json:"identity"`
	Signatures []ValidatorAdmissionSignature `json:"signatures"`
}

type validatorAdmissionPayload struct {
	Domain   string                  `json:"domain"`
	ChainID  string                  `json:"chain_id"`
	Identity state.ValidatorIdentity `json:"identity"`
}

func ValidatorAdmissionSigningBytes(chainID string, identity state.ValidatorIdentity) ([]byte, error) {
	if err := types.ValidateChainID(chainID); err != nil {
		return nil, err
	}
	if err := state.ValidateValidatorIdentity(identity); err != nil {
		return nil, err
	}
	return hash.CanonicalBytes(validatorAdmissionPayload{
		Domain: validatorAdmissionDomain, ChainID: chainID, Identity: identity,
	})
}

func validateGenesisAdmissions(document GenesisDocument, store *state.Store) error {
	if len(document.Admissions) == 0 {
		return nil
	}
	genesisValidators := store.Validators()
	if document.Protocol != ProtocolVersionV2 || document.ValidatorPolicy == nil {
		return errors.New("validator admissions require protocol version 2")
	}
	if len(document.Admissions) > maxValidatorAdmissionCertificates {
		return fmt.Errorf("validator admissions exceed %d certificates", maxValidatorAdmissionCertificates)
	}
	if len(document.Admissions) > types.MaxValidators-len(genesisValidators) {
		return fmt.Errorf("validator admissions exceed the %d identity limit", types.MaxValidators)
	}
	genesis := make(map[string]struct{}, len(genesisValidators))
	for _, account := range genesisValidators {
		genesis[account] = struct{}{}
	}
	seenAccounts := make(map[string]struct{}, len(document.Admissions))
	seenConsensusAddresses := make(map[string]struct{}, len(document.Admissions))
	previousConsensusAddress := ""
	totalSignatures := 0
	for index, certificate := range document.Admissions {
		if len(certificate.Signatures) > maxValidatorAdmissionSignatures-totalSignatures {
			return fmt.Errorf("validator admissions exceed %d total signatures", maxValidatorAdmissionSignatures)
		}
		totalSignatures += len(certificate.Signatures)
		identity := certificate.Identity
		if err := state.ValidateValidatorIdentity(identity); err != nil {
			return fmt.Errorf("validator admission %d identity: %w", index, err)
		}
		if _, exists := genesis[identity.Account]; exists {
			return fmt.Errorf("validator admission %d duplicates a genesis account", index)
		}
		if _, exists := seenAccounts[identity.Account]; exists {
			return fmt.Errorf("validator admission %d duplicates an account", index)
		}
		if _, exists := seenConsensusAddresses[identity.ConsensusAddress]; exists {
			return fmt.Errorf("validator admission %d duplicates a consensus address", index)
		}
		if identity.ConsensusAddress <= previousConsensusAddress {
			return errors.New("validator admissions must be ordered by consensus address")
		}
		if identity.ActiveHeight < 3 ||
			(identity.ActiveHeight-1)%document.ValidatorPolicy.EpochLength != 0 ||
			identity.InactiveHeight != 0 {
			return fmt.Errorf("validator admission %d must activate on a future epoch without a removal", index)
		}
		power, err := state.ValidatorPowerFromStake(store.StakeOf(identity.Account))
		if err != nil {
			return fmt.Errorf("validator admission %d: %w", index, err)
		}
		if identity.Power != power {
			return fmt.Errorf(
				"validator admission %d power %d does not match stake-derived power %d",
				index, identity.Power, power,
			)
		}
		if err := validateAdmissionSignatures(document.ChainID, identity, certificate.Signatures, genesis); err != nil {
			return fmt.Errorf("validator admission %d: %w", index, err)
		}
		seenAccounts[identity.Account] = struct{}{}
		seenConsensusAddresses[identity.ConsensusAddress] = struct{}{}
		previousConsensusAddress = identity.ConsensusAddress
	}
	return nil
}

func validateAdmissionSignatures(
	chainID string,
	identity state.ValidatorIdentity,
	signatures []ValidatorAdmissionSignature,
	genesis map[string]struct{},
) error {
	quorum := len(genesis)*2/3 + 1
	if len(signatures) < quorum || len(signatures) > len(genesis) {
		return fmt.Errorf("certificate requires between %d and %d signatures", quorum, len(genesis))
	}
	payload, err := ValidatorAdmissionSigningBytes(chainID, identity)
	if err != nil {
		return err
	}
	previous := ""
	for index, item := range signatures {
		validator, err := chaincrypto.NormalizeAddress(item.Validator)
		if err != nil || validator != item.Validator {
			return fmt.Errorf("signature %d validator is not canonical", index)
		}
		if validator <= previous {
			return errors.New("certificate signatures must be ordered by validator")
		}
		if _, exists := genesis[validator]; !exists {
			return fmt.Errorf("signature %d is not from a genesis validator", index)
		}
		if err := types.ValidateCanonicalSignature("validator admission signature", item.Signature); err != nil {
			return fmt.Errorf("signature %d: %w", index, err)
		}
		if !chaincrypto.Verify(validator, payload, item.Signature) {
			return fmt.Errorf("signature %d does not authorize the admission", index)
		}
		previous = validator
	}
	return nil
}

func cloneValidatorAdmissions(input []ValidatorAdmissionCertificate) []ValidatorAdmissionCertificate {
	cloned := make([]ValidatorAdmissionCertificate, len(input))
	for index, certificate := range input {
		cloned[index] = certificate
		cloned[index].Signatures = append([]ValidatorAdmissionSignature(nil), certificate.Signatures...)
	}
	return cloned
}
