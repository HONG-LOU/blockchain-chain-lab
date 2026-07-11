package state

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/hash"
	"chainlab/internal/types"
)

const (
	MaxStorageEntriesPerAccount = 4096
	MaxStorageKeyBytes          = 256
	MaxStorageValueBytes        = 64 * 1024
	MaxStorageBytesPerAccount   = 8 * 1024 * 1024
)

var ErrStorageLimit = errors.New("account storage limit exceeded")

type Store struct {
	accounts           map[string]types.Account
	storageBytes       map[string]int
	codes              map[string]types.ContractCode
	stakes             map[string]uint64
	proposals          map[string]types.Proposal
	params             map[string]string
	validators         []string
	validatorLifecycle *ValidatorLifecycle
	mutations          mutationTracker
}

// Reader is the read-only state capability exposed to contract execution.
// Implementations must return detached values for any mutable collection.
type Reader interface {
	CodeID(address string) string
	ContractCode(codeID string) (types.ContractCode, bool)
	GetStorage(address string, key string) string
	HasStorage(address string, key string) bool
	StorageEntryCount(address string) int
	GetStorageWithExists(address string, key string) (string, bool)
	SortedStorageKeys(address string) []string
}

// ReadView pins a Store version without exposing mutation methods. The owner
// must publish Stores immutably and replace the version pointer on commit.
type ReadView struct {
	store *Store
}

func (s *Store) ReadView() ReadView {
	return ReadView{store: s}
}

func (v ReadView) CodeID(address string) string {
	if v.store == nil {
		return ""
	}
	return v.store.CodeID(address)
}

func (v ReadView) ContractCode(codeID string) (types.ContractCode, bool) {
	if v.store == nil {
		return types.ContractCode{}, false
	}
	return v.store.ContractCode(codeID)
}

func (v ReadView) GetStorage(address string, key string) string {
	if v.store == nil {
		return ""
	}
	return v.store.GetStorage(address, key)
}

func (v ReadView) HasStorage(address string, key string) bool {
	if v.store == nil {
		return false
	}
	return v.store.HasStorage(address, key)
}

func (v ReadView) StorageEntryCount(address string) int {
	if v.store == nil {
		return 0
	}
	return v.store.StorageEntryCount(address)
}

func (v ReadView) GetStorageWithExists(address string, key string) (string, bool) {
	if v.store == nil {
		return "", false
	}
	return v.store.GetStorageWithExists(address, key)
}

func (v ReadView) SortedStorageKeys(address string) []string {
	if v.store == nil {
		return nil
	}
	return v.store.SortedStorageKeys(address)
}

type Snapshot struct {
	Accounts           map[string]types.Account      `json:"accounts"`
	Codes              map[string]types.ContractCode `json:"codes,omitempty"`
	Stakes             map[string]uint64             `json:"stakes"`
	Proposals          map[string]types.Proposal     `json:"proposals"`
	Params             map[string]string             `json:"params,omitempty"`
	Validators         []string                      `json:"validators,omitempty"`
	ValidatorLifecycle *ValidatorLifecycle           `json:"validator_lifecycle,omitempty"`
}

func NewStore() *Store {
	return &Store{
		accounts:     make(map[string]types.Account),
		storageBytes: make(map[string]int),
		codes:        make(map[string]types.ContractCode),
		stakes:       make(map[string]uint64),
		proposals:    make(map[string]types.Proposal),
		params:       make(map[string]string),
	}
}

func NewStoreFromSnapshot(snapshot Snapshot) (*Store, error) {
	store := NewStore()
	accountKeys, err := validateCanonicalAddressMapKeys("account", snapshot.Accounts)
	if err != nil {
		return nil, err
	}
	for _, address := range accountKeys {
		account := snapshot.Accounts[address]
		if account.Address == "" || account.Address != address {
			return nil, fmt.Errorf("snapshot account %q has mismatched embedded address %q", address, account.Address)
		}
		if account.CodeID != strings.TrimSpace(account.CodeID) {
			return nil, fmt.Errorf("snapshot account %q code id is not canonical", address)
		}
		if account.DelegatedCodeID != strings.TrimSpace(account.DelegatedCodeID) {
			return nil, fmt.Errorf("snapshot account %q delegated code id is not canonical", address)
		}
		if len(account.Storage) > MaxStorageEntriesPerAccount {
			return nil, fmt.Errorf("%w for account %q", ErrStorageLimit, address)
		}
		storageBytes := 0
		for key, value := range account.Storage {
			if key == "" || len(key) > MaxStorageKeyBytes || len(value) > MaxStorageValueBytes {
				return nil, fmt.Errorf("%w for account %q", ErrStorageLimit, address)
			}
			if storageBytes > MaxStorageBytesPerAccount-len(key) || storageBytes+len(key) > MaxStorageBytesPerAccount-len(value) {
				return nil, fmt.Errorf("%w for account %q", ErrStorageLimit, address)
			}
			storageBytes += len(key) + len(value)
		}
		account.Storage = cloneStringMap(account.Storage)
		store.accounts[address] = account
		store.storageBytes[address] = storageBytes
	}
	codeKeys := sortedMapKeys(snapshot.Codes)
	seenCodeIDs := make(map[string]string, len(codeKeys))
	for _, mapKey := range codeKeys {
		code := snapshot.Codes[mapKey]
		if err := validateCanonicalStateKey("contract code map key", mapKey); err != nil {
			return nil, err
		}
		if err := validateCanonicalStateKey("contract code id", code.CodeID); err != nil {
			return nil, err
		}
		if previous, exists := seenCodeIDs[code.CodeID]; exists {
			return nil, fmt.Errorf("snapshot contract code id %q is duplicated by keys %q and %q", code.CodeID, previous, mapKey)
		}
		seenCodeIDs[code.CodeID] = mapKey
		if mapKey != code.CodeID {
			return nil, fmt.Errorf("snapshot contract code key %q does not match embedded id %q", mapKey, code.CodeID)
		}
		if err := validateSnapshotContractCode(code); err != nil {
			return nil, fmt.Errorf("snapshot contract code %q: %w", mapKey, err)
		}
		store.codes[mapKey] = code
	}
	stakeKeys, err := validateCanonicalAddressMapKeys("stake", snapshot.Stakes)
	if err != nil {
		return nil, err
	}
	for _, address := range stakeKeys {
		store.stakes[address] = snapshot.Stakes[address]
	}
	proposalKeys := sortedMapKeys(snapshot.Proposals)
	seenProposalIDs := make(map[string]string, len(proposalKeys))
	for _, mapKey := range proposalKeys {
		proposal := snapshot.Proposals[mapKey]
		if err := validateSnapshotProposalID(mapKey); err != nil {
			return nil, fmt.Errorf("snapshot proposal map key: %w", err)
		}
		if err := validateSnapshotProposalID(proposal.ID); err != nil {
			return nil, fmt.Errorf("snapshot proposal embedded id: %w", err)
		}
		if previous, exists := seenProposalIDs[proposal.ID]; exists {
			return nil, fmt.Errorf("snapshot proposal id %q is duplicated by keys %q and %q", proposal.ID, previous, mapKey)
		}
		seenProposalIDs[proposal.ID] = mapKey
		if mapKey != proposal.ID {
			return nil, fmt.Errorf("snapshot proposal key %q does not match embedded id %q", mapKey, proposal.ID)
		}
		if err := validateSnapshotProposal(proposal); err != nil {
			return nil, fmt.Errorf("snapshot proposal %q: %w", mapKey, err)
		}
		store.proposals[mapKey] = cloneProposal(proposal)
	}
	for _, key := range sortedMapKeys(snapshot.Params) {
		if err := validateCanonicalStateKey("parameter key", key); err != nil {
			return nil, err
		}
		store.params[key] = snapshot.Params[key]
	}
	validators, err := validateSnapshotValidators(snapshot.Validators)
	if err != nil {
		return nil, err
	}
	store.validators = validators
	if snapshot.ValidatorLifecycle != nil {
		if err := store.SetValidatorLifecycle(*snapshot.ValidatorLifecycle); err != nil {
			return nil, fmt.Errorf("invalid snapshot validator lifecycle: %w", err)
		}
	}
	store.ResetMutations()
	return store, nil
}

func validateCanonicalAddressMapKeys[V any](kind string, values map[string]V) ([]string, error) {
	keys := sortedMapKeys(values)
	seen := make(map[string]string, len(keys))
	for _, key := range keys {
		normalized, err := chaincrypto.NormalizeAddress(key)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s key %q is not a valid address", kind, key)
		}
		if previous, exists := seen[normalized]; exists {
			return nil, fmt.Errorf("snapshot %s keys %q and %q normalize to the same address", kind, previous, key)
		}
		seen[normalized] = key
		if normalized != key {
			return nil, fmt.Errorf("snapshot %s key %q is not canonically encoded", kind, key)
		}
	}
	return keys, nil
}

func validateCanonicalStateKey(field string, value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("snapshot %s %q is empty or not canonical", field, value)
	}
	return nil
}

func validateSnapshotContractCode(code types.ContractCode) error {
	if code.Runtime != "wasm" {
		return fmt.Errorf("runtime %q is unsupported", code.Runtime)
	}
	if code.MeteringVersion != types.WASMMeteringVersion {
		return fmt.Errorf("metering version %q is unsupported", code.MeteringVersion)
	}
	if err := validateCanonicalAddressValue("contract code creator", code.Creator); err != nil {
		return err
	}
	if code.Bytecode == "" || code.Bytecode != strings.TrimSpace(code.Bytecode) || code.Bytecode != strings.ToLower(code.Bytecode) || !strings.HasPrefix(code.Bytecode, "0x") {
		return errors.New("bytecode is not canonical lowercase 0x-prefixed hex")
	}
	encodedBytes := len(code.Bytecode) - 2
	if encodedBytes == 0 || encodedBytes%2 != 0 || encodedBytes > types.MaxWASMModuleBytes*2 {
		return fmt.Errorf("bytecode exceeds %d bytes or is not canonical hex", types.MaxWASMModuleBytes)
	}
	raw, err := hex.DecodeString(code.Bytecode[2:])
	if err != nil || len(raw) == 0 {
		return errors.New("bytecode is not canonical lowercase 0x-prefixed hex")
	}
	if expected := types.WASMCodeID(raw); code.CodeID != expected {
		return fmt.Errorf("code id does not match bytecode hash: got %q want %q", code.CodeID, expected)
	}
	return nil
}

func validateSnapshotProposalID(id string) error {
	if id == "" || id != strings.TrimSpace(id) || !strings.HasPrefix(id, "proposal:") {
		return fmt.Errorf("proposal id %q is not canonical", id)
	}
	if err := types.ValidateCanonicalHash("proposal hash", strings.TrimPrefix(id, "proposal:")); err != nil {
		return fmt.Errorf("proposal id %q is not canonical", id)
	}
	return nil
}

func validateSnapshotProposal(proposal types.Proposal) error {
	if err := validateCanonicalAddressValue("proposal proposer", proposal.Proposer); err != nil {
		return err
	}
	if proposal.Title == "" || proposal.Title != strings.TrimSpace(proposal.Title) {
		return errors.New("title is empty or not canonical")
	}
	if proposal.Description != strings.TrimSpace(proposal.Description) {
		return errors.New("description is not canonical")
	}
	if proposal.Kind != "param.change" {
		return fmt.Errorf("kind %q is unsupported", proposal.Kind)
	}
	if proposal.Param == "" || proposal.Param != strings.TrimSpace(proposal.Param) {
		return errors.New("parameter is empty or not canonical")
	}
	if proposal.Value == "" || proposal.Value != strings.TrimSpace(proposal.Value) {
		return errors.New("value is empty or not canonical")
	}
	switch proposal.Status {
	case types.ProposalStatusOpen, types.ProposalStatusRejected, types.ProposalStatusExecuted:
	default:
		return fmt.Errorf("status %q is invalid", proposal.Status)
	}
	if proposal.VotingEndHeight <= proposal.SubmitHeight {
		return errors.New("voting end height must exceed submit height")
	}
	if proposal.Votes == nil {
		return errors.New("votes map is missing")
	}
	for _, choice := range sortedMapKeys(proposal.Votes) {
		if !validProposalChoice(choice) {
			return fmt.Errorf("vote choice %q is invalid", choice)
		}
	}
	if proposal.Voters == nil {
		return errors.New("voters map is missing")
	}
	voters, err := validateCanonicalAddressMapKeys("proposal voter", proposal.Voters)
	if err != nil {
		return err
	}
	for _, voter := range voters {
		if choice := proposal.Voters[voter]; !validProposalChoice(choice) {
			return fmt.Errorf("voter %q has invalid choice %q", voter, choice)
		}
	}
	return nil
}

func validProposalChoice(choice string) bool {
	return choice == "yes" || choice == "no" || choice == "abstain"
}

func validateSnapshotValidators(validators []string) ([]string, error) {
	if len(validators) == 0 {
		return nil, errors.New("snapshot validator set is empty")
	}
	if len(validators) > types.MaxValidators {
		return nil, fmt.Errorf("snapshot validator set exceeds %d entries", types.MaxValidators)
	}
	seen := make(map[string]struct{}, len(validators))
	result := make([]string, 0, len(validators))
	for index, validator := range validators {
		if err := validateCanonicalAddressValue(fmt.Sprintf("validator %d", index), validator); err != nil {
			return nil, err
		}
		if _, exists := seen[validator]; exists {
			return nil, fmt.Errorf("snapshot validator %q is duplicated", validator)
		}
		seen[validator] = struct{}{}
		result = append(result, validator)
	}
	return result, nil
}

func validateCanonicalAddressValue(field string, value string) error {
	normalized, err := chaincrypto.NormalizeAddress(value)
	if err != nil || normalized != value {
		return fmt.Errorf("snapshot %s %q is not a canonical address", field, value)
	}
	return nil
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *Store) Clone() *Store {
	clone := NewStore()
	for address, account := range s.accounts {
		account.Storage = cloneStringMap(account.Storage)
		clone.accounts[address] = account
	}
	clone.storageBytes = cloneIntMap(s.storageBytes)
	clone.codes = cloneContractCodeMap(s.codes)
	for address, stake := range s.stakes {
		clone.stakes[address] = stake
	}
	for id, proposal := range s.proposals {
		clone.proposals[id] = cloneProposal(proposal)
	}
	clone.params = cloneStringMap(s.params)
	clone.validators = cloneStringSlice(s.validators)
	if s.validatorLifecycle != nil {
		lifecycle := cloneValidatorLifecycle(*s.validatorLifecycle)
		clone.validatorLifecycle = &lifecycle
	}
	clone.mutations = cloneMutationTracker(s.mutations)
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
	if s.validatorLifecycle != nil {
		lifecycle := cloneValidatorLifecycle(*s.validatorLifecycle)
		snapshot.ValidatorLifecycle = &lifecycle
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
	s.storageBytes = replacement.storageBytes
	s.codes = replacement.codes
	s.stakes = replacement.stakes
	s.proposals = replacement.proposals
	s.params = replacement.params
	s.validators = replacement.validators
	s.validatorLifecycle = replacement.validatorLifecycle
	s.mutations = replacement.mutations
}

func (s *Store) GetAccount(address string) types.Account {
	account := s.account(address)
	account.Storage = cloneStringMap(account.Storage)
	return account
}

func (s *Store) SetBalance(address string, amount uint64) {
	address = normalize(address)
	account := s.account(address)
	if prior, exists := s.accounts[address]; exists && prior.Balance == amount {
		return
	}
	account.Balance = amount
	s.accounts[address] = account
	s.markAccount(address)
}

func (s *Store) AddBalance(address string, amount uint64) error {
	address = normalize(address)
	account := s.account(address)
	if math.MaxUint64-account.Balance < amount {
		return errors.New("balance overflow")
	}
	_, existed := s.accounts[address]
	account.Balance += amount
	s.accounts[address] = account
	if amount != 0 || !existed {
		s.markAccount(address)
	}
	return nil
}

func (s *Store) SubBalance(address string, amount uint64) error {
	address = normalize(address)
	account := s.account(address)
	if account.Balance < amount {
		return errors.New("insufficient funds")
	}
	_, existed := s.accounts[address]
	account.Balance -= amount
	s.accounts[address] = account
	if amount != 0 || !existed {
		s.markAccount(address)
	}
	return nil
}

func (s *Store) Transfer(from string, to string, amount uint64) error {
	if amount == 0 {
		return nil
	}
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
	address = normalize(address)
	account := s.account(address)
	if account.Nonce == math.MaxUint64 {
		return errors.New("nonce overflow")
	}
	account.Nonce++
	s.accounts[address] = account
	s.markAccount(address)
	return nil
}

func (s *Store) SetNonce(address string, nonce uint64) {
	address = normalize(address)
	account := s.account(address)
	if prior, exists := s.accounts[address]; exists && prior.Nonce == nonce {
		return
	}
	account.Nonce = nonce
	s.accounts[address] = account
	s.markAccount(address)
}

func (s *Store) SetCodeID(address string, codeID string) {
	address = normalize(address)
	account := s.account(address)
	if prior, exists := s.accounts[address]; exists && prior.CodeID == codeID {
		return
	}
	account.CodeID = codeID
	s.accounts[address] = account
	s.markAccount(address)
}

func (s *Store) CodeID(address string) string {
	return s.account(address).CodeID
}

func (s *Store) SetDelegatedCodeID(address string, codeID string) {
	address = normalize(address)
	codeID = strings.TrimSpace(codeID)
	account := s.account(address)
	if prior, exists := s.accounts[address]; exists && prior.DelegatedCodeID == codeID {
		return
	}
	account.DelegatedCodeID = codeID
	s.accounts[address] = account
	s.markAccount(address)
}

func (s *Store) ClearDelegation(address string) {
	address = normalize(address)
	account := s.account(address)
	if _, exists := s.accounts[address]; !exists || account.DelegatedCodeID != "" {
		account.DelegatedCodeID = ""
		s.accounts[address] = account
		s.markAccount(address)
	}
	s.DeleteStorage(address, "owner")
	s.DeleteStoragePrefix(address, "session:")
	s.DeleteStoragePrefix(address, "recovery:")
}

func (s *Store) SetStorage(address string, key string, value string) error {
	if key == "" || len(key) > MaxStorageKeyBytes || len(value) > MaxStorageValueBytes {
		return ErrStorageLimit
	}
	address = normalize(address)
	account := s.account(address)
	if account.Storage == nil {
		account.Storage = make(map[string]string)
	}
	if _, exists := account.Storage[key]; !exists && len(account.Storage) >= MaxStorageEntriesPerAccount {
		return ErrStorageLimit
	}
	storageBytes, cached := s.storageBytes[address]
	if !cached && len(account.Storage) > 0 {
		return ErrStorageLimit
	}
	if previous, exists := account.Storage[key]; exists {
		previousBytes := len(key) + len(previous)
		if storageBytes < previousBytes {
			return ErrStorageLimit
		}
		storageBytes -= previousBytes
	}
	nextEntryBytes := len(key) + len(value)
	if storageBytes > MaxStorageBytesPerAccount-nextEntryBytes {
		return ErrStorageLimit
	}
	account.Storage[key] = value
	s.accounts[address] = account
	s.storageBytes[address] = storageBytes + nextEntryBytes
	s.markAccountStorage(address, key)
	return nil
}

func (s *Store) StorageEntryCount(address string) int {
	return len(s.account(address).Storage)
}

func (s *Store) GetStorageWithExists(address string, key string) (string, bool) {
	account := s.account(address)
	value, ok := account.Storage[key]
	return value, ok
}

func (s *Store) StorageSnapshot(address string) map[string]string {
	return cloneStringMap(s.account(address).Storage)
}

// SortedStorageKeys returns a detached key-only view of an account's storage.
func (s *Store) SortedStorageKeys(address string) []string {
	account := s.account(address)
	keys := make([]string, 0, len(account.Storage))
	for key := range account.Storage {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (s *Store) DeleteStorage(address string, key string) {
	address = normalize(address)
	account := s.account(address)
	_, accountExisted := s.accounts[address]
	if previous, exists := account.Storage[key]; exists {
		deletedBytes := len(key) + len(previous)
		if currentBytes, cached := s.storageBytes[address]; cached && currentBytes >= deletedBytes {
			s.storageBytes[address] = currentBytes - deletedBytes
		} else {
			s.storageBytes[address] = storageSizeAfterDeleting(account.Storage, key)
		}
		delete(account.Storage, key)
		s.markAccountStorage(address, key)
	} else if _, cached := s.storageBytes[address]; !cached && len(account.Storage) == 0 {
		s.storageBytes[address] = 0
	}
	s.accounts[address] = account
	if !accountExisted {
		s.markAccount(address)
	}
}

// DeleteStoragePrefix removes storage entries whose keys start with prefix and returns the number removed.
func (s *Store) DeleteStoragePrefix(address string, prefix string) int {
	address = normalize(address)
	account := s.account(address)
	_, accountExisted := s.accounts[address]
	keys := make([]string, 0)
	deletedBytes := 0
	for key := range account.Storage {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
			deletedBytes += len(key) + len(account.Storage[key])
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		delete(account.Storage, key)
		s.markAccountStorage(address, key)
	}
	if currentBytes, cached := s.storageBytes[address]; cached && currentBytes >= deletedBytes {
		s.storageBytes[address] = currentBytes - deletedBytes
	} else {
		s.storageBytes[address] = storageSize(account.Storage)
	}
	s.accounts[address] = account
	if !accountExisted {
		s.markAccount(address)
	}
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
	if prior, exists := s.codes[code.CodeID]; exists && prior == code {
		return
	}
	s.codes[code.CodeID] = code
	s.markContractCode(code.CodeID)
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
	_, existed := s.stakes[address]
	s.stakes[address] += amount
	if amount != 0 || !existed {
		s.markStake(address)
	}
	return nil
}

func (s *Store) SubStake(address string, amount uint64) error {
	address = normalize(address)
	if s.stakes[address] < amount {
		return errors.New("insufficient stake")
	}
	_, existed := s.stakes[address]
	s.stakes[address] -= amount
	if amount != 0 || !existed {
		s.markStake(address)
	}
	return nil
}

func (s *Store) StakeOf(address string) uint64 {
	return s.stakes[normalize(address)]
}

func (s *Store) SetValidators(validators []string) error {
	if len(validators) == 0 {
		return errors.New("validator set is empty")
	}
	if len(validators) > types.MaxValidators {
		return fmt.Errorf("validator set exceeds %d entries", types.MaxValidators)
	}
	next := &Store{}
	for _, validator := range validators {
		if err := next.addValidator(validator); err != nil {
			return err
		}
	}
	s.validators = next.validators
	s.mutations.validators = true
	return nil
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
			s.mutations.validators = true
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
	s.markProposal(proposal.ID)
}

func (s *Store) RecordVote(proposalID string, voter string, choice string, power uint64) error {
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
	if math.MaxUint64-proposal.Votes[choice] < power {
		return errors.New("proposal vote power overflow")
	}
	proposal.Votes[choice] += power
	proposal.Voters[normalize(voter)] = choice
	s.SetProposal(proposal)
	return nil
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
	if prior, exists := s.params[key]; exists && prior == value {
		return
	}
	s.params[key] = value
	s.markParam(key)
}

func (s *Store) Param(key string) string {
	return s.params[strings.TrimSpace(key)]
}

func (s *Store) Params() map[string]string {
	return cloneStringMap(s.params)
}

func (s *Store) Root() string {
	snapshot := struct {
		Accounts           map[string]types.Account      `json:"accounts"`
		Codes              map[string]types.ContractCode `json:"codes"`
		Stakes             map[string]uint64             `json:"stakes"`
		Proposals          map[string]types.Proposal     `json:"proposals"`
		Params             map[string]string             `json:"params"`
		Validators         []string                      `json:"validators"`
		ValidatorLifecycle *ValidatorLifecycle           `json:"validator_lifecycle,omitempty"`
	}{
		Accounts:   make(map[string]types.Account, len(s.accounts)),
		Codes:      cloneContractCodeMap(s.codes),
		Stakes:     cloneUint64Map(s.stakes),
		Proposals:  make(map[string]types.Proposal, len(s.proposals)),
		Params:     cloneStringMap(s.params),
		Validators: cloneStringSlice(s.validators),
	}
	if s.validatorLifecycle != nil {
		lifecycle := cloneValidatorLifecycle(*s.validatorLifecycle)
		snapshot.ValidatorLifecycle = &lifecycle
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

func (s *Store) addValidator(address string) error {
	if len(s.validators) >= types.MaxValidators {
		return fmt.Errorf("validator set exceeds %d entries", types.MaxValidators)
	}
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
	s.mutations.validators = true
	return nil
}

func (s *Store) account(address string) types.Account {
	address = normalize(address)
	account, ok := s.accounts[address]
	if !ok {
		account = types.Account{Address: address, Storage: make(map[string]string)}
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

func cloneIntMap(input map[string]int) map[string]int {
	output := make(map[string]int, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func storageSize(storage map[string]string) int {
	total := 0
	for key, value := range storage {
		total += len(key) + len(value)
	}
	return total
}

func storageSizeAfterDeleting(storage map[string]string, deletedKey string) int {
	total := 0
	for key, value := range storage {
		if key != deletedKey {
			total += len(key) + len(value)
		}
	}
	return total
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
