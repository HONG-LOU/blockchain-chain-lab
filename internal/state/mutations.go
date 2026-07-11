package state

import (
	"sort"
	"strings"

	"chainlab/internal/types"
)

// StorageMutation identifies one account storage entry without exposing a
// mutable view of the account.
type StorageMutation struct {
	Address string
	Key     string
}

// Mutations is the detached semantic write set since the last durable commit.
// It is intentionally excluded from snapshots and consensus hashing.
type Mutations struct {
	Accounts            []string
	AccountStorage      []StorageMutation
	ContractCodes       []string
	Stakes              []string
	Proposals           []string
	Params              []string
	Validators          bool
	ValidatorIdentities []string
	ValidatorOffences   []string
}

func (m Mutations) Empty() bool {
	return len(m.Accounts) == 0 && len(m.AccountStorage) == 0 &&
		len(m.ContractCodes) == 0 && len(m.Stakes) == 0 &&
		len(m.Proposals) == 0 && len(m.Params) == 0 && !m.Validators &&
		len(m.ValidatorIdentities) == 0 && len(m.ValidatorOffences) == 0
}

type mutationTracker struct {
	accounts            map[string]struct{}
	accountStorage      map[StorageMutation]struct{}
	contractCodes       map[string]struct{}
	stakes              map[string]struct{}
	proposals           map[string]struct{}
	params              map[string]struct{}
	validators          bool
	validatorIdentities map[string]struct{}
	validatorOffences   map[string]struct{}
}

func (s *Store) Mutations() Mutations {
	mutations := Mutations{
		Accounts:            sortedMutationKeys(s.mutations.accounts),
		ContractCodes:       sortedMutationKeys(s.mutations.contractCodes),
		Stakes:              sortedMutationKeys(s.mutations.stakes),
		Proposals:           sortedMutationKeys(s.mutations.proposals),
		Params:              sortedMutationKeys(s.mutations.params),
		Validators:          s.mutations.validators,
		ValidatorIdentities: sortedMutationKeys(s.mutations.validatorIdentities),
		ValidatorOffences:   sortedMutationKeys(s.mutations.validatorOffences),
	}
	mutations.AccountStorage = make([]StorageMutation, 0, len(s.mutations.accountStorage))
	for mutation := range s.mutations.accountStorage {
		mutations.AccountStorage = append(mutations.AccountStorage, mutation)
	}
	sort.Slice(mutations.AccountStorage, func(left int, right int) bool {
		if mutations.AccountStorage[left].Address != mutations.AccountStorage[right].Address {
			return mutations.AccountStorage[left].Address < mutations.AccountStorage[right].Address
		}
		return mutations.AccountStorage[left].Key < mutations.AccountStorage[right].Key
	})
	return mutations
}

func (s *Store) ResetMutations() {
	s.mutations = mutationTracker{}
}

func (s *Store) AccountMetadata(address string) (types.Account, bool) {
	address = normalize(address)
	account, exists := s.accounts[address]
	if !exists {
		return types.Account{}, false
	}
	account.Storage = nil
	return account, true
}

func (s *Store) StakeWithExists(address string) (uint64, bool) {
	value, exists := s.stakes[normalize(address)]
	return value, exists
}

func (s *Store) ProposalWithExists(id string) (types.Proposal, bool) {
	proposal, exists := s.proposals[id]
	if !exists {
		return types.Proposal{}, false
	}
	return cloneProposal(proposal), true
}

func (s *Store) ParamWithExists(key string) (string, bool) {
	value, exists := s.params[strings.TrimSpace(key)]
	return value, exists
}

func (s *Store) ValidatorOffence(key string) (ValidatorOffence, bool) {
	if s.validatorLifecycle == nil {
		return ValidatorOffence{}, false
	}
	offence, exists := s.validatorLifecycle.Offences[key]
	return offence, exists
}

func (s *Store) markAccount(address string) {
	markMutation(&s.mutations.accounts, normalize(address))
}

func (s *Store) markAccountStorage(address string, key string) {
	if s.mutations.accountStorage == nil {
		s.mutations.accountStorage = make(map[StorageMutation]struct{})
	}
	s.mutations.accountStorage[StorageMutation{Address: normalize(address), Key: key}] = struct{}{}
}

func (s *Store) markContractCode(codeID string) {
	markMutation(&s.mutations.contractCodes, strings.TrimSpace(codeID))
}

func (s *Store) markStake(address string) {
	markMutation(&s.mutations.stakes, normalize(address))
}

func (s *Store) markProposal(id string) {
	markMutation(&s.mutations.proposals, strings.TrimSpace(id))
}

func (s *Store) markParam(key string) {
	markMutation(&s.mutations.params, strings.TrimSpace(key))
}

func (s *Store) markValidatorIdentity(consensusAddress string) {
	markMutation(&s.mutations.validatorIdentities, consensusAddress)
}

func (s *Store) markValidatorOffence(key string) {
	markMutation(&s.mutations.validatorOffences, key)
}

func markMutation(target *map[string]struct{}, key string) {
	if *target == nil {
		*target = make(map[string]struct{})
	}
	(*target)[key] = struct{}{}
}

func sortedMutationKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func cloneMutationTracker(input mutationTracker) mutationTracker {
	return mutationTracker{
		accounts:            cloneMutationKeys(input.accounts),
		accountStorage:      cloneStorageMutationKeys(input.accountStorage),
		contractCodes:       cloneMutationKeys(input.contractCodes),
		stakes:              cloneMutationKeys(input.stakes),
		proposals:           cloneMutationKeys(input.proposals),
		params:              cloneMutationKeys(input.params),
		validators:          input.validators,
		validatorIdentities: cloneMutationKeys(input.validatorIdentities),
		validatorOffences:   cloneMutationKeys(input.validatorOffences),
	}
}

func cloneMutationKeys(input map[string]struct{}) map[string]struct{} {
	if input == nil {
		return nil
	}
	cloned := make(map[string]struct{}, len(input))
	for key := range input {
		cloned[key] = struct{}{}
	}
	return cloned
}

func cloneStorageMutationKeys(input map[StorageMutation]struct{}) map[StorageMutation]struct{} {
	if input == nil {
		return nil
	}
	cloned := make(map[StorageMutation]struct{}, len(input))
	for key := range input {
		cloned[key] = struct{}{}
	}
	return cloned
}
