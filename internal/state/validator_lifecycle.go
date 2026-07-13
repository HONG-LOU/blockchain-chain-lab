package state

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/hash"
	"chainlab/internal/types"

	"golang.org/x/crypto/ripemd160"
)

const MaxValidatorOffences = 1_000_000

// ValidatorIdentity permanently binds the application account to the CometBFT
// consensus identity. InactiveHeight is exclusive: the validator is active at
// h when ActiveHeight <= h < InactiveHeight, or when InactiveHeight is zero.
// UnbondingHeight is inclusive and zero means stake remains locked indefinitely.
type ValidatorIdentity struct {
	Account          string `json:"account"`
	ConsensusAddress string `json:"consensus_address"`
	PublicKey        string `json:"public_key"`
	Power            int64  `json:"power"`
	ActiveHeight     int64  `json:"active_height"`
	InactiveHeight   int64  `json:"inactive_height,omitempty"`
	UnbondingHeight  int64  `json:"unbonding_height,omitempty"`
}

type ValidatorOffence struct {
	Validator              string `json:"validator"`
	Height                 int64  `json:"height"`
	Type                   int32  `json:"type"`
	ObservedHeight         int64  `json:"observed_height"`
	SlashBasisPoints       uint32 `json:"slash_basis_points"`
	SlashAmount            uint64 `json:"slash_amount"`
	RemovalHeight          int64  `json:"removal_height"`
	EvidenceTimePresent    bool   `json:"evidence_time_present,omitempty"`
	EvidenceTimeUnix       int64  `json:"evidence_time_unix,omitempty"`
	EvidenceTimeNanosecond int32  `json:"evidence_time_nanosecond,omitempty"`
}

type ValidatorLifecycle struct {
	Validators map[string]ValidatorIdentity `json:"validators"`
	Offences   map[string]ValidatorOffence  `json:"offences"`
}

func ValidatorOffenceKey(account string, height int64) string {
	return fmt.Sprintf("%s:%020d", account, height)
}

func (s *Store) InitializeValidatorLifecycle(identities []ValidatorIdentity) error {
	if s.validatorLifecycle != nil {
		return errors.New("validator lifecycle is already initialized")
	}
	lifecycle := ValidatorLifecycle{
		Validators: make(map[string]ValidatorIdentity, len(identities)),
		Offences:   make(map[string]ValidatorOffence),
	}
	for _, identity := range identities {
		if _, exists := lifecycle.Validators[identity.ConsensusAddress]; exists {
			return fmt.Errorf("validator consensus address %q is duplicated", identity.ConsensusAddress)
		}
		lifecycle.Validators[identity.ConsensusAddress] = identity
	}
	return s.SetValidatorLifecycle(lifecycle)
}

func (s *Store) SetValidatorLifecycle(lifecycle ValidatorLifecycle) error {
	if err := validateValidatorLifecycle(lifecycle, s.validators); err != nil {
		return err
	}
	cloned := cloneValidatorLifecycle(lifecycle)
	if s.validatorLifecycle == nil {
		for key := range cloned.Validators {
			s.markValidatorIdentity(key)
		}
		for key := range cloned.Offences {
			s.markValidatorOffence(key)
		}
	} else {
		for key, next := range cloned.Validators {
			if previous, exists := s.validatorLifecycle.Validators[key]; !exists || previous != next {
				s.markValidatorIdentity(key)
			}
		}
		for key := range s.validatorLifecycle.Validators {
			if _, exists := cloned.Validators[key]; !exists {
				s.markValidatorIdentity(key)
			}
		}
		for key, next := range cloned.Offences {
			if previous, exists := s.validatorLifecycle.Offences[key]; !exists || previous != next {
				s.markValidatorOffence(key)
			}
		}
		for key := range s.validatorLifecycle.Offences {
			if _, exists := cloned.Offences[key]; !exists {
				s.markValidatorOffence(key)
			}
		}
	}
	s.validatorLifecycle = &cloned
	return nil
}

func (s *Store) ValidatorLifecycle() (ValidatorLifecycle, bool) {
	if s.validatorLifecycle == nil {
		return ValidatorLifecycle{}, false
	}
	return cloneValidatorLifecycle(*s.validatorLifecycle), true
}

func (s *Store) ValidatorIdentityByConsensusAddress(consensusAddress string) (ValidatorIdentity, bool) {
	if s.validatorLifecycle == nil {
		return ValidatorIdentity{}, false
	}
	identity, exists := s.validatorLifecycle.Validators[consensusAddress]
	return identity, exists
}

func (s *Store) ValidatorIdentityByAccount(account string) (ValidatorIdentity, bool) {
	if s.validatorLifecycle == nil {
		return ValidatorIdentity{}, false
	}
	for _, identity := range s.validatorLifecycle.Validators {
		if identity.Account == account {
			return identity, true
		}
	}
	return ValidatorIdentity{}, false
}

func (s *Store) ValidatorRoot() string {
	if s.validatorLifecycle == nil {
		return ""
	}
	return hash.MustHex(*s.validatorLifecycle)
}

func validatorActiveAtHeight(identity ValidatorIdentity, height int64) bool {
	return height >= identity.ActiveHeight && (identity.InactiveHeight == 0 || height < identity.InactiveHeight)
}

func validateValidatorLifecycle(lifecycle ValidatorLifecycle, validators []string) error {
	if len(lifecycle.Validators) == 0 {
		return errors.New("validator lifecycle must contain identities")
	}
	if len(lifecycle.Validators) > types.MaxValidators {
		return fmt.Errorf("validator lifecycle exceeds %d identities", types.MaxValidators)
	}
	if lifecycle.Offences == nil {
		return errors.New("validator lifecycle offences map is missing")
	}
	if len(lifecycle.Offences) > MaxValidatorOffences {
		return fmt.Errorf("validator lifecycle exceeds %d offences", MaxValidatorOffences)
	}
	genesisAccounts := make(map[string]struct{}, len(validators))
	for _, validator := range validators {
		genesisAccounts[validator] = struct{}{}
	}
	seenAccounts := make(map[string]struct{}, len(lifecycle.Validators))
	identitiesByAccount := make(map[string]ValidatorIdentity, len(lifecycle.Validators))
	seenPublicKeys := make(map[string]struct{}, len(lifecycle.Validators))
	var totalPower int64
	for key, identity := range lifecycle.Validators {
		if key != identity.ConsensusAddress {
			return fmt.Errorf("validator lifecycle key %q does not match consensus address %q", key, identity.ConsensusAddress)
		}
		if err := validateValidatorIdentity(identity); err != nil {
			return err
		}
		if _, exists := seenAccounts[identity.Account]; exists {
			return fmt.Errorf("validator lifecycle account %q is duplicated", identity.Account)
		}
		if _, exists := seenPublicKeys[identity.PublicKey]; exists {
			return fmt.Errorf("validator lifecycle public key %q is duplicated", identity.PublicKey)
		}
		seenAccounts[identity.Account] = struct{}{}
		identitiesByAccount[identity.Account] = identity
		seenPublicKeys[identity.PublicKey] = struct{}{}
		if totalPower > math.MaxInt64/8-identity.Power {
			return errors.New("validator lifecycle exceeds CometBFT maximum total voting power")
		}
		totalPower += identity.Power
	}
	for account := range genesisAccounts {
		if _, exists := seenAccounts[account]; !exists {
			return fmt.Errorf("validator lifecycle is missing genesis account %q", account)
		}
	}
	for key, offence := range lifecycle.Offences {
		if err := validateValidatorOffence(key, offence, identitiesByAccount); err != nil {
			return err
		}
	}
	return nil
}

func validateValidatorIdentity(identity ValidatorIdentity) error {
	account, err := chaincrypto.NormalizeAddress(identity.Account)
	if err != nil || account != identity.Account {
		return fmt.Errorf("validator lifecycle account %q is not canonical", identity.Account)
	}
	publicKey, err := hex.DecodeString(identity.PublicKey)
	if err != nil || len(publicKey) != 33 || hex.EncodeToString(publicKey) != identity.PublicKey {
		return errors.New("validator lifecycle public key is not canonical compressed secp256k1")
	}
	derivedAccount, err := chaincrypto.AddressFromCompressedPublicKey(publicKey)
	if err != nil || derivedAccount != identity.Account {
		return errors.New("validator lifecycle public key does not match account")
	}
	consensusAddress, err := hex.DecodeString(identity.ConsensusAddress)
	if err != nil || len(consensusAddress) != 20 || hex.EncodeToString(consensusAddress) != identity.ConsensusAddress {
		return errors.New("validator lifecycle consensus address is not canonical")
	}
	digest := sha256.Sum256(publicKey)
	addressHasher := ripemd160.New()
	_, _ = addressHasher.Write(digest[:])
	if hex.EncodeToString(addressHasher.Sum(nil)) != identity.ConsensusAddress {
		return errors.New("validator lifecycle public key does not match consensus address")
	}
	if identity.Power <= 0 {
		return errors.New("validator lifecycle power must be positive")
	}
	if identity.ActiveHeight <= 0 {
		return errors.New("validator lifecycle active height must be positive")
	}
	if identity.InactiveHeight != 0 && identity.InactiveHeight <= identity.ActiveHeight {
		return errors.New("validator lifecycle inactive height must exceed active height")
	}
	if identity.UnbondingHeight != 0 &&
		(identity.InactiveHeight == 0 || identity.UnbondingHeight <= identity.InactiveHeight) {
		return errors.New("validator lifecycle unbonding height must exceed inactive height")
	}
	return nil
}

func ValidateValidatorIdentity(identity ValidatorIdentity) error {
	return validateValidatorIdentity(identity)
}

func validateValidatorOffence(key string, offence ValidatorOffence, identitiesByAccount map[string]ValidatorIdentity) error {
	account, err := chaincrypto.NormalizeAddress(offence.Validator)
	if err != nil || account != offence.Validator {
		return fmt.Errorf("validator offence %q has a non-canonical validator", key)
	}
	if key != ValidatorOffenceKey(offence.Validator, offence.Height) {
		return fmt.Errorf("validator offence key %q is not canonical", key)
	}
	identity, found := identitiesByAccount[offence.Validator]
	if !found {
		return fmt.Errorf("validator offence %q references an unknown validator", key)
	}
	if !validatorActiveAtHeight(identity, offence.Height) {
		return fmt.Errorf("validator offence %q occurred outside the validator's active interval", key)
	}
	if offence.Height <= 0 || offence.ObservedHeight <= offence.Height {
		return fmt.Errorf("validator offence %q heights are invalid", key)
	}
	if offence.Type != 1 && offence.Type != 2 {
		return fmt.Errorf("validator offence %q type is invalid", key)
	}
	if offence.SlashBasisPoints == 0 || offence.SlashBasisPoints > 10_000 {
		return fmt.Errorf("validator offence %q slash ratio is invalid", key)
	}
	if offence.RemovalHeight < offence.ObservedHeight+2 {
		return fmt.Errorf("validator offence %q removal height violates the validator update delay", key)
	}
	if !offence.EvidenceTimePresent {
		if offence.EvidenceTimeUnix != 0 || offence.EvidenceTimeNanosecond != 0 {
			return fmt.Errorf("validator offence %q has unmarked evidence time fields", key)
		}
	} else if offence.EvidenceTimeNanosecond < 0 || offence.EvidenceTimeNanosecond >= 1_000_000_000 {
		return fmt.Errorf("validator offence %q evidence nanosecond is invalid", key)
	}
	return nil
}

func cloneValidatorLifecycle(input ValidatorLifecycle) ValidatorLifecycle {
	cloned := ValidatorLifecycle{
		Validators: make(map[string]ValidatorIdentity, len(input.Validators)),
		Offences:   make(map[string]ValidatorOffence, len(input.Offences)),
	}
	for key, value := range input.Validators {
		cloned.Validators[key] = value
	}
	for key, value := range input.Offences {
		cloned.Offences[key] = value
	}
	return cloned
}
