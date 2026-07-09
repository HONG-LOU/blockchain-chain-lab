package state

import (
	"errors"
	"math"
	"strings"

	"chainlab/internal/hash"
	"chainlab/internal/types"
)

type Store struct {
	accounts  map[string]types.Account
	stakes    map[string]uint64
	proposals map[string]types.Proposal
}

func NewStore() *Store {
	return &Store{
		accounts:  make(map[string]types.Account),
		stakes:    make(map[string]uint64),
		proposals: make(map[string]types.Proposal),
	}
}

func (s *Store) Clone() *Store {
	clone := NewStore()
	for address, account := range s.accounts {
		account.Storage = cloneStringMap(account.Storage)
		clone.accounts[address] = account
	}
	for address, stake := range s.stakes {
		clone.stakes[address] = stake
	}
	for id, proposal := range s.proposals {
		proposal.Votes = cloneUint64Map(proposal.Votes)
		proposal.Voters = cloneStringMap(proposal.Voters)
		clone.proposals[id] = proposal
	}
	return clone
}

func (s *Store) ReplaceWith(other *Store) {
	replacement := other.Clone()
	s.accounts = replacement.accounts
	s.stakes = replacement.stakes
	s.proposals = replacement.proposals
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

func (s *Store) IncrementNonce(address string) {
	account := s.account(address)
	account.Nonce++
	s.accounts[normalize(address)] = account
}

func (s *Store) SetCodeID(address string, codeID string) {
	account := s.account(address)
	account.CodeID = codeID
	s.accounts[normalize(address)] = account
}

func (s *Store) SetStorage(address string, key string, value string) {
	account := s.account(address)
	if account.Storage == nil {
		account.Storage = make(map[string]string)
	}
	account.Storage[key] = value
	s.accounts[normalize(address)] = account
}

func (s *Store) GetStorage(address string, key string) string {
	account := s.account(address)
	return account.Storage[key]
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
	s.proposals[proposalID] = proposal
}

func (s *Store) Proposal(id string) types.Proposal {
	proposal, ok := s.proposals[id]
	if !ok {
		return types.Proposal{
			ID:     id,
			Votes:  make(map[string]uint64),
			Voters: make(map[string]string),
		}
	}
	proposal.Votes = cloneUint64Map(proposal.Votes)
	proposal.Voters = cloneStringMap(proposal.Voters)
	return proposal
}

func (s *Store) Root() string {
	snapshot := struct {
		Accounts  map[string]types.Account  `json:"accounts"`
		Stakes    map[string]uint64         `json:"stakes"`
		Proposals map[string]types.Proposal `json:"proposals"`
	}{
		Accounts:  make(map[string]types.Account, len(s.accounts)),
		Stakes:    cloneUint64Map(s.stakes),
		Proposals: make(map[string]types.Proposal, len(s.proposals)),
	}
	for address, account := range s.accounts {
		account.Storage = cloneStringMap(account.Storage)
		snapshot.Accounts[address] = account
	}
	for id, proposal := range s.proposals {
		proposal.Votes = cloneUint64Map(proposal.Votes)
		proposal.Voters = cloneStringMap(proposal.Voters)
		snapshot.Proposals[id] = proposal
	}
	return hash.MustHex(snapshot)
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
	return strings.ToLower(address)
}

func cloneStringMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneUint64Map(input map[string]uint64) map[string]uint64 {
	output := make(map[string]uint64, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
