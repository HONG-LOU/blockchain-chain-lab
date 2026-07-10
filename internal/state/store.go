package state

import (
	"errors"
	"math"
	"sort"
	"strings"

	"chainlab/internal/hash"
	"chainlab/internal/types"
)

type Store struct {
	accounts   map[string]types.Account
	codes      map[string]types.ContractCode
	stakes     map[string]uint64
	proposals  map[string]types.Proposal
	params     map[string]string
	validators []string
}

type Snapshot struct {
	Accounts   map[string]types.Account      `json:"accounts"`
	Codes      map[string]types.ContractCode `json:"codes,omitempty"`
	Stakes     map[string]uint64             `json:"stakes"`
	Proposals  map[string]types.Proposal     `json:"proposals"`
	Params     map[string]string             `json:"params,omitempty"`
	Validators []string                      `json:"validators,omitempty"`
}

func NewStore() *Store {
	return &Store{
		accounts:  make(map[string]types.Account),
		codes:     make(map[string]types.ContractCode),
		stakes:    make(map[string]uint64),
		proposals: make(map[string]types.Proposal),
		params:    make(map[string]string),
	}
}

func NewStoreFromSnapshot(snapshot Snapshot) *Store {
	store := NewStore()
	for address, account := range snapshot.Accounts {
		account.Address = normalize(account.Address)
		if account.Address == "" {
			account.Address = normalize(address)
		}
		account.Storage = cloneStringMap(account.Storage)
		store.accounts[normalize(address)] = account
	}
	for codeID, code := range snapshot.Codes {
		code.CodeID = strings.TrimSpace(code.CodeID)
		if code.CodeID == "" {
			code.CodeID = strings.TrimSpace(codeID)
		}
		store.codes[code.CodeID] = code
	}
	for address, stake := range snapshot.Stakes {
		store.stakes[normalize(address)] = stake
	}
	for id, proposal := range snapshot.Proposals {
		store.SetProposal(normalizeProposalID(id, proposal))
	}
	for key, value := range snapshot.Params {
		store.params[strings.TrimSpace(key)] = value
	}
	store.SetValidators(snapshot.Validators)
	return store
}

func (s *Store) Clone() *Store {
	clone := NewStore()
	for address, account := range s.accounts {
		account.Storage = cloneStringMap(account.Storage)
		clone.accounts[address] = account
	}
	clone.codes = cloneContractCodeMap(s.codes)
	for address, stake := range s.stakes {
		clone.stakes[address] = stake
	}
	for id, proposal := range s.proposals {
		clone.proposals[id] = cloneProposal(proposal)
	}
	clone.params = cloneStringMap(s.params)
	clone.validators = cloneStringSlice(s.validators)
	return clone
}

func (s *Store) Snapshot() Snapshot {
	snapshot := Snapshot{
		Accounts:   make(map[string]types.Account, len(s.accounts)),
		Codes:      cloneContractCodeMap(s.codes),
		Stakes:     cloneUint64Map(s.stakes),
		Proposals:  make(map[string]types.Proposal, len(s.proposals)),
		Params:     cloneStringMap(s.params),
		Validators: cloneStringSlice(s.validators),
	}
	for address, account := range s.accounts {
		account.Storage = cloneStringMap(account.Storage)
		snapshot.Accounts[address] = account
	}
	for id, proposal := range s.proposals {
		snapshot.Proposals[id] = cloneProposal(proposal)
	}
	return snapshot
}

func (s *Store) ReplaceWith(other *Store) {
	replacement := other.Clone()
	s.accounts = replacement.accounts
	s.codes = replacement.codes
	s.stakes = replacement.stakes
	s.proposals = replacement.proposals
	s.params = replacement.params
	s.validators = replacement.validators
}

func (s *Store) GetAccount(address string) types.Account {
	account := s.account(address)
	account.Storage = cloneStringMap(account.Storage)
	return account
}

func (s *Store) SetBalance(address string, amount uint64) {
	account := s.account(address)
	account.Balance = amount
	s.accounts[normalize(address)] = account
}

func (s *Store) AddBalance(address string, amount uint64) error {
	account := s.account(address)
	if math.MaxUint64-account.Balance < amount {
		return errors.New("balance overflow")
	}
	account.Balance += amount
	s.accounts[normalize(address)] = account
	return nil
}

func (s *Store) SubBalance(address string, amount uint64) error {
	account := s.account(address)
	if account.Balance < amount {
		return errors.New("insufficient funds")
	}
	account.Balance -= amount
	s.accounts[normalize(address)] = account
	return nil
}

func (s *Store) Transfer(from string, to string, amount uint64) error {
	if err := s.SubBalance(from, amount); err != nil {
		return err
	}
	if err := s.AddBalance(to, amount); err != nil {
		_ = s.AddBalance(from, amount)
		return err
	}
	return nil
}

func (s *Store) IncrementNonce(address string) error {
	account := s.account(address)
	if account.Nonce == math.MaxUint64 {
		return errors.New("nonce overflow")
	}
	account.Nonce++
	s.accounts[normalize(address)] = account
	return nil
}

func (s *Store) SetNonce(address string, nonce uint64) {
	account := s.account(address)
	account.Nonce = nonce
	s.accounts[normalize(address)] = account
}

func (s *Store) SetCodeID(address string, codeID string) {
	account := s.account(address)
	account.CodeID = codeID
	s.accounts[normalize(address)] = account
}

func (s *Store) SetDelegatedCodeID(address string, codeID string) {
	account := s.account(address)
	account.DelegatedCodeID = strings.TrimSpace(codeID)
	s.accounts[normalize(address)] = account
}

func (s *Store) ClearDelegation(address string) {
	account := s.account(address)
	account.DelegatedCodeID = ""
	if account.Storage != nil {
		delete(account.Storage, "owner")
	}
	s.accounts[normalize(address)] = account
	s.DeleteStoragePrefix(address, "session:")
	s.DeleteStoragePrefix(address, "recovery:")
}

func (s *Store) SetStorage(address string, key string, value string) {
	account := s.account(address)
	if account.Storage == nil {
		account.Storage = make(map[string]string)
	}
	account.Storage[key] = value
	s.accounts[normalize(address)] = account
}

func (s *Store) DeleteStorage(address string, key string) {
	account := s.account(address)
	if account.Storage != nil {
		delete(account.Storage, key)
	}
	s.accounts[normalize(address)] = account
}

// DeleteStoragePrefix removes storage entries whose keys start with prefix and returns the number removed.
func (s *Store) DeleteStoragePrefix(address string, prefix string) int {
	account := s.account(address)
	keys := make([]string, 0)
	for key := range account.Storage {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		delete(account.Storage, key)
	}
	s.accounts[normalize(address)] = account
	return len(keys)
}

func (s *Store) GetStorage(address string, key string) string {
	account := s.account(address)
	return account.Storage[key]
}

func (s *Store) HasStorage(address string, key string) bool {
	account := s.account(address)
	_, ok := account.Storage[key]
	return ok
}

func (s *Store) SetContractCode(code types.ContractCode) {
	code.CodeID = strings.TrimSpace(code.CodeID)
	if code.CodeID == "" {
		return
	}
	s.codes[code.CodeID] = code
}

func (s *Store) ContractCode(codeID string) (types.ContractCode, bool) {
	code, ok := s.codes[strings.TrimSpace(codeID)]
	return code, ok
}

func (s *Store) ContractCodes() []types.ContractCode {
	codes := make([]types.ContractCode, 0, len(s.codes))
	for _, code := range s.codes {
		codes = append(codes, code)
	}
	return codes
}

func (s *Store) AddStake(address string, amount uint64) error {
	address = normalize(address)
	if math.MaxUint64-s.stakes[address] < amount {
		return errors.New("stake overflow")
	}
	s.stakes[address] += amount
	return nil
}

func (s *Store) SubStake(address string, amount uint64) error {
	address = normalize(address)
	if s.stakes[address] < amount {
		return errors.New("insufficient stake")
	}
	s.stakes[address] -= amount
	return nil
}

func (s *Store) StakeOf(address string) uint64 {
	return s.stakes[normalize(address)]
}

func (s *Store) SetValidators(validators []string) {
	s.validators = nil
	for _, validator := range validators {
		_ = s.addValidator(validator)
	}
}

func (s *Store) AddValidator(address string) error {
	return s.addValidator(address)
}

func (s *Store) RemoveValidator(address string) error {
	address = normalize(address)
	if address == "" {
		return errors.New("validator address is required")
	}
	if len(s.validators) <= 1 {
		return errors.New("cannot remove the last validator")
	}
	for i, validator := range s.validators {
		if validator == address {
			s.validators = append(s.validators[:i], s.validators[i+1:]...)
			return nil
		}
	}
	return errors.New("validator is not active")
}

func (s *Store) Validators() []string {
	return cloneStringSlice(s.validators)
}

func (s *Store) SetProposal(proposal types.Proposal) {
	proposal.ID = strings.TrimSpace(proposal.ID)
	if proposal.ID == "" {
		return
	}
	if proposal.Votes == nil {
		proposal.Votes = make(map[string]uint64)
	}
	if proposal.Voters == nil {
		proposal.Voters = make(map[string]string)
	}
	s.proposals[proposal.ID] = cloneProposal(proposal)
}

func (s *Store) RecordVote(proposalID string, voter string, choice string, power uint64) {
	proposal := s.Proposal(proposalID)
	previousChoice, voted := proposal.Voters[normalize(voter)]
	if voted {
		previousPower := proposal.Votes[previousChoice]
		if previousPower >= power {
			proposal.Votes[previousChoice] = previousPower - power
		} else {
			proposal.Votes[previousChoice] = 0
		}
	}
	proposal.Votes[choice] += power
	proposal.Voters[normalize(voter)] = choice
	s.SetProposal(proposal)
}

func (s *Store) Proposal(id string) types.Proposal {
	proposal, ok := s.proposals[id]
	if !ok {
		return types.Proposal{
			ID:     strings.TrimSpace(id),
			Votes:  make(map[string]uint64),
			Voters: make(map[string]string),
		}
	}
	return cloneProposal(proposal)
}

func (s *Store) SetParam(key string, value string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	s.params[key] = value
}

func (s *Store) Param(key string) string {
	return s.params[strings.TrimSpace(key)]
}

func (s *Store) Params() map[string]string {
	return cloneStringMap(s.params)
}

func (s *Store) Root() string {
	snapshot := struct {
		Accounts   map[string]types.Account      `json:"accounts"`
		Codes      map[string]types.ContractCode `json:"codes"`
		Stakes     map[string]uint64             `json:"stakes"`
		Proposals  map[string]types.Proposal     `json:"proposals"`
		Params     map[string]string             `json:"params"`
		Validators []string                      `json:"validators"`
	}{
		Accounts:   make(map[string]types.Account, len(s.accounts)),
		Codes:      cloneContractCodeMap(s.codes),
		Stakes:     cloneUint64Map(s.stakes),
		Proposals:  make(map[string]types.Proposal, len(s.proposals)),
		Params:     cloneStringMap(s.params),
		Validators: cloneStringSlice(s.validators),
	}
	for address, account := range s.accounts {
		account.Storage = cloneStringMap(account.Storage)
		snapshot.Accounts[address] = account
	}
	for id, proposal := range s.proposals {
		snapshot.Proposals[id] = cloneProposal(proposal)
	}
	return hash.MustHex(snapshot)
}

func normalizeProposalID(id string, proposal types.Proposal) types.Proposal {
	proposal.ID = strings.TrimSpace(proposal.ID)
	if proposal.ID == "" {
		proposal.ID = strings.TrimSpace(id)
	}
	return proposal
}

func (s *Store) addValidator(address string) error {
	address = normalize(address)
	if address == "" {
		return errors.New("validator address is required")
	}
	for _, validator := range s.validators {
		if validator == address {
			return errors.New("validator already exists")
		}
	}
	s.validators = append(s.validators, address)
	return nil
}

func (s *Store) account(address string) types.Account {
	address = normalize(address)
	account, ok := s.accounts[address]
	if !ok {
		account = types.Account{Address: address, Storage: make(map[string]string)}
		s.accounts[address] = account
	}
	if account.Storage == nil {
		account.Storage = make(map[string]string)
	}
	return account
}

func normalize(address string) string {
	return strings.ToLower(strings.TrimSpace(address))
}

func cloneStringMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneContractCodeMap(input map[string]types.ContractCode) map[string]types.ContractCode {
	output := make(map[string]types.ContractCode, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneProposal(input types.Proposal) types.Proposal {
	input.Votes = cloneUint64Map(input.Votes)
	input.Voters = cloneStringMap(input.Voters)
	return input
}

func cloneUint64Map(input map[string]uint64) map[string]uint64 {
	output := make(map[string]uint64, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneStringSlice(input []string) []string {
	return append([]string(nil), input...)
}
