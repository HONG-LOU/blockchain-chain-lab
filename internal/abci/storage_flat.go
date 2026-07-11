package abci

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"

	"chainlab/internal/hash"
	"chainlab/internal/state"
	"chainlab/internal/types"
)

const (
	flatKindAccount           = "account"
	flatKindAccountStorage    = "account_storage"
	flatKindCode              = "code"
	flatKindStake             = "stake"
	flatKindProposal          = "proposal"
	flatKindParam             = "param"
	flatKindValidators        = "validators"
	flatKindValidatorIdentity = "validator_identity"
	flatKindValidatorOffence  = "validator_offence"
	flatSingletonKey          = "state"
)

type persistedAccount struct {
	Address         string `json:"address"`
	Balance         uint64 `json:"balance"`
	Nonce           uint64 `json:"nonce"`
	CodeID          string `json:"code_id,omitempty"`
	DelegatedCodeID string `json:"delegated_code_id,omitempty"`
}

type persistedAccountStorageKey struct {
	Address string `json:"address"`
	Key     string `json:"key"`
}

type flatStateEntry struct {
	Kind  string
	Key   []byte
	Value []byte
}

type flatState map[string]flatStateEntry

type flatStateCommitmentEntry struct {
	Kind  string `json:"kind"`
	Key   string `json:"key"`
	Value string `json:"value"`
}

type flatStateCommitmentKey struct {
	Kind string `json:"kind"`
	Key  string `json:"key"`
}

type flatDeltaCommitment struct {
	Sets    []flatStateCommitmentEntry `json:"sets"`
	Deletes []flatStateCommitmentKey   `json:"deletes"`
}

func flattenStateSnapshot(snapshot state.Snapshot) (flatState, error) {
	flat := make(flatState)
	for address, account := range snapshot.Accounts {
		core := persistedAccount{
			Address: account.Address, Balance: account.Balance, Nonce: account.Nonce,
			CodeID: account.CodeID, DelegatedCodeID: account.DelegatedCodeID,
		}
		if err := putFlatStateValue(flat, flatKindAccount, []byte(address), core); err != nil {
			return nil, err
		}
		for key, value := range account.Storage {
			storageKey, err := hash.CanonicalBytes(persistedAccountStorageKey{Address: address, Key: key})
			if err != nil {
				return nil, err
			}
			if err := putFlatStateValue(flat, flatKindAccountStorage, storageKey, value); err != nil {
				return nil, err
			}
		}
	}
	for key, value := range snapshot.Codes {
		if err := putFlatStateValue(flat, flatKindCode, []byte(key), value); err != nil {
			return nil, err
		}
	}
	for key, value := range snapshot.Stakes {
		if err := putFlatStateValue(flat, flatKindStake, []byte(key), value); err != nil {
			return nil, err
		}
	}
	for key, value := range snapshot.Proposals {
		if err := putFlatStateValue(flat, flatKindProposal, []byte(key), value); err != nil {
			return nil, err
		}
	}
	for key, value := range snapshot.Params {
		if err := putFlatStateValue(flat, flatKindParam, []byte(key), value); err != nil {
			return nil, err
		}
	}
	if err := putFlatStateValue(flat, flatKindValidators, []byte(flatSingletonKey), snapshot.Validators); err != nil {
		return nil, err
	}
	if snapshot.ValidatorLifecycle != nil {
		for key, value := range snapshot.ValidatorLifecycle.Validators {
			if err := putFlatStateValue(flat, flatKindValidatorIdentity, []byte(key), value); err != nil {
				return nil, err
			}
		}
		for key, value := range snapshot.ValidatorLifecycle.Offences {
			if err := putFlatStateValue(flat, flatKindValidatorOffence, []byte(key), value); err != nil {
				return nil, err
			}
		}
	}
	return flat, nil
}

func putFlatStateValue(flat flatState, kind string, key []byte, value any) error {
	if !validFlatStateKind(kind) || len(key) == 0 {
		return errors.New("flat state kind or key is invalid")
	}
	raw, err := hash.CanonicalBytes(value)
	if err != nil {
		return err
	}
	entry := flatStateEntry{Kind: kind, Key: append([]byte(nil), key...), Value: raw}
	id := flatStateID(kind, key)
	if _, exists := flat[id]; exists {
		return fmt.Errorf("duplicate flat state entry %q", id)
	}
	flat[id] = entry
	return nil
}

func flatStateID(kind string, key []byte) string {
	return kind + ":" + base64.RawURLEncoding.EncodeToString(key)
}

func validFlatStateKind(kind string) bool {
	switch kind {
	case flatKindAccount, flatKindAccountStorage, flatKindCode, flatKindStake,
		flatKindProposal, flatKindParam, flatKindValidators,
		flatKindValidatorIdentity, flatKindValidatorOffence:
		return true
	default:
		return false
	}
}

func unflattenStateSnapshot(flat flatState) (state.Snapshot, error) {
	snapshot := state.Snapshot{
		Accounts: make(map[string]types.Account), Codes: make(map[string]types.ContractCode),
		Stakes: make(map[string]uint64), Proposals: make(map[string]types.Proposal),
		Params: make(map[string]string),
	}
	identities := make(map[string]state.ValidatorIdentity)
	offences := make(map[string]state.ValidatorOffence)
	storageEntries := make([]struct {
		key   persistedAccountStorageKey
		value string
	}, 0)
	for id, entry := range flat {
		if id != flatStateID(entry.Kind, entry.Key) || !validFlatStateKind(entry.Kind) || len(entry.Key) == 0 {
			return state.Snapshot{}, fmt.Errorf("flat state entry %q is invalid", id)
		}
		switch entry.Kind {
		case flatKindAccount:
			address := string(entry.Key)
			var account persistedAccount
			if err := decodeCanonicalJSON(entry.Value, &account); err != nil {
				return state.Snapshot{}, fmt.Errorf("decode flat account %q: %w", address, err)
			}
			if _, exists := snapshot.Accounts[address]; exists {
				return state.Snapshot{}, fmt.Errorf("duplicate flat account %q", address)
			}
			snapshot.Accounts[address] = types.Account{
				Address: account.Address, Balance: account.Balance, Nonce: account.Nonce,
				CodeID: account.CodeID, DelegatedCodeID: account.DelegatedCodeID,
				Storage: make(map[string]string),
			}
		case flatKindAccountStorage:
			var key persistedAccountStorageKey
			if err := decodeCanonicalJSON(entry.Key, &key); err != nil {
				return state.Snapshot{}, fmt.Errorf("decode flat account storage key: %w", err)
			}
			var value string
			if err := decodeCanonicalJSON(entry.Value, &value); err != nil {
				return state.Snapshot{}, fmt.Errorf("decode flat account storage value: %w", err)
			}
			storageEntries = append(storageEntries, struct {
				key   persistedAccountStorageKey
				value string
			}{key: key, value: value})
		case flatKindCode:
			key := string(entry.Key)
			var value types.ContractCode
			if err := decodeCanonicalJSON(entry.Value, &value); err != nil {
				return state.Snapshot{}, fmt.Errorf("decode flat code %q: %w", key, err)
			}
			snapshot.Codes[key] = value
		case flatKindStake:
			key := string(entry.Key)
			var value uint64
			if err := decodeCanonicalJSON(entry.Value, &value); err != nil {
				return state.Snapshot{}, fmt.Errorf("decode flat stake %q: %w", key, err)
			}
			snapshot.Stakes[key] = value
		case flatKindProposal:
			key := string(entry.Key)
			var value types.Proposal
			if err := decodeCanonicalJSON(entry.Value, &value); err != nil {
				return state.Snapshot{}, fmt.Errorf("decode flat proposal %q: %w", key, err)
			}
			snapshot.Proposals[key] = value
		case flatKindParam:
			key := string(entry.Key)
			var value string
			if err := decodeCanonicalJSON(entry.Value, &value); err != nil {
				return state.Snapshot{}, fmt.Errorf("decode flat parameter %q: %w", key, err)
			}
			snapshot.Params[key] = value
		case flatKindValidators:
			if string(entry.Key) != flatSingletonKey || snapshot.Validators != nil {
				return state.Snapshot{}, errors.New("flat validator set key is invalid or duplicated")
			}
			if err := decodeCanonicalJSON(entry.Value, &snapshot.Validators); err != nil {
				return state.Snapshot{}, fmt.Errorf("decode flat validator set: %w", err)
			}
		case flatKindValidatorIdentity:
			key := string(entry.Key)
			var value state.ValidatorIdentity
			if err := decodeCanonicalJSON(entry.Value, &value); err != nil {
				return state.Snapshot{}, fmt.Errorf("decode flat validator identity %q: %w", key, err)
			}
			identities[key] = value
		case flatKindValidatorOffence:
			key := string(entry.Key)
			var value state.ValidatorOffence
			if err := decodeCanonicalJSON(entry.Value, &value); err != nil {
				return state.Snapshot{}, fmt.Errorf("decode flat validator offence %q: %w", key, err)
			}
			offences[key] = value
		}
	}
	for _, item := range storageEntries {
		account, exists := snapshot.Accounts[item.key.Address]
		if !exists {
			return state.Snapshot{}, fmt.Errorf("flat storage references missing account %q", item.key.Address)
		}
		if _, duplicate := account.Storage[item.key.Key]; duplicate {
			return state.Snapshot{}, fmt.Errorf("duplicate flat storage key %q for account %q", item.key.Key, item.key.Address)
		}
		account.Storage[item.key.Key] = item.value
		snapshot.Accounts[item.key.Address] = account
	}
	if len(identities) > 0 || len(offences) > 0 {
		if len(identities) == 0 {
			return state.Snapshot{}, errors.New("flat validator offences exist without identities")
		}
		snapshot.ValidatorLifecycle = &state.ValidatorLifecycle{Validators: identities, Offences: offences}
	}
	return snapshot, nil
}

func diffFlatState(previous flatState, next flatState) (flatState, []flatStateEntry) {
	sets := make(flatState)
	deletes := make([]flatStateEntry, 0)
	for id, entry := range next {
		prior, exists := previous[id]
		if !exists || !bytes.Equal(prior.Value, entry.Value) {
			sets[id] = cloneFlatStateEntry(entry)
		}
	}
	for id, entry := range previous {
		if _, exists := next[id]; !exists {
			deletes = append(deletes, cloneFlatStateEntry(entry))
		}
	}
	sort.Slice(deletes, func(left int, right int) bool {
		return flatStateID(deletes[left].Kind, deletes[left].Key) < flatStateID(deletes[right].Kind, deletes[right].Key)
	})
	return sets, deletes
}

func flatDeltaFromStoreMutations(previous flatState, next *state.Store) (flatState, []flatStateEntry, error) {
	if next == nil {
		return nil, nil, errors.New("next state store is required")
	}
	sets := make(flatState)
	deletes := make(flatState)
	mutations := next.Mutations()
	for _, address := range mutations.Accounts {
		account, exists := next.AccountMetadata(address)
		core := persistedAccount{}
		if exists {
			core = persistedAccount{
				Address: account.Address, Balance: account.Balance, Nonce: account.Nonce,
				CodeID: account.CodeID, DelegatedCodeID: account.DelegatedCodeID,
			}
		}
		if err := addFlatMutation(previous, sets, deletes, flatKindAccount, []byte(address), core, exists); err != nil {
			return nil, nil, err
		}
	}
	for _, mutation := range mutations.AccountStorage {
		key, err := hash.CanonicalBytes(persistedAccountStorageKey{Address: mutation.Address, Key: mutation.Key})
		if err != nil {
			return nil, nil, err
		}
		value, exists := next.GetStorageWithExists(mutation.Address, mutation.Key)
		if err := addFlatMutation(previous, sets, deletes, flatKindAccountStorage, key, value, exists); err != nil {
			return nil, nil, err
		}
	}
	for _, codeID := range mutations.ContractCodes {
		value, exists := next.ContractCode(codeID)
		if err := addFlatMutation(previous, sets, deletes, flatKindCode, []byte(codeID), value, exists); err != nil {
			return nil, nil, err
		}
	}
	for _, address := range mutations.Stakes {
		value, exists := next.StakeWithExists(address)
		if err := addFlatMutation(previous, sets, deletes, flatKindStake, []byte(address), value, exists); err != nil {
			return nil, nil, err
		}
	}
	for _, proposalID := range mutations.Proposals {
		value, exists := next.ProposalWithExists(proposalID)
		if err := addFlatMutation(previous, sets, deletes, flatKindProposal, []byte(proposalID), value, exists); err != nil {
			return nil, nil, err
		}
	}
	for _, key := range mutations.Params {
		value, exists := next.ParamWithExists(key)
		if err := addFlatMutation(previous, sets, deletes, flatKindParam, []byte(key), value, exists); err != nil {
			return nil, nil, err
		}
	}
	if mutations.Validators {
		if err := addFlatMutation(
			previous, sets, deletes, flatKindValidators, []byte(flatSingletonKey), next.Validators(), true,
		); err != nil {
			return nil, nil, err
		}
	}
	for _, consensusAddress := range mutations.ValidatorIdentities {
		value, exists := next.ValidatorIdentityByConsensusAddress(consensusAddress)
		if err := addFlatMutation(
			previous, sets, deletes, flatKindValidatorIdentity, []byte(consensusAddress), value, exists,
		); err != nil {
			return nil, nil, err
		}
	}
	for _, key := range mutations.ValidatorOffences {
		value, exists := next.ValidatorOffence(key)
		if err := addFlatMutation(
			previous, sets, deletes, flatKindValidatorOffence, []byte(key), value, exists,
		); err != nil {
			return nil, nil, err
		}
	}
	orderedDeletes := flatEntriesSorted(deletes)
	return sets, orderedDeletes, nil
}

func addFlatMutation(
	previous flatState,
	sets flatState,
	deletes flatState,
	kind string,
	key []byte,
	value any,
	exists bool,
) error {
	if !validFlatStateKind(kind) || len(key) == 0 {
		return errors.New("flat state mutation kind or key is invalid")
	}
	id := flatStateID(kind, key)
	prior, existed := previous[id]
	if !exists {
		if existed {
			deletes[id] = cloneFlatStateEntry(prior)
		}
		return nil
	}
	raw, err := hash.CanonicalBytes(value)
	if err != nil {
		return err
	}
	if existed && bytes.Equal(prior.Value, raw) {
		return nil
	}
	sets[id] = flatStateEntry{Kind: kind, Key: append([]byte(nil), key...), Value: raw}
	return nil
}

func validateFlatMutationDelta(previous flatState, next flatState, sets flatState, deletes []flatStateEntry) error {
	wantSets, wantDeletes := diffFlatState(previous, next)
	if len(sets) != len(wantSets) || len(deletes) != len(wantDeletes) {
		return errors.New("state mutation journal does not match the complete state delta")
	}
	for id, want := range wantSets {
		got, exists := sets[id]
		if !exists || got.Kind != want.Kind || !bytes.Equal(got.Key, want.Key) || !bytes.Equal(got.Value, want.Value) {
			return errors.New("state mutation journal does not match the complete state delta")
		}
	}
	for index, want := range wantDeletes {
		got := deletes[index]
		if got.Kind != want.Kind || !bytes.Equal(got.Key, want.Key) {
			return errors.New("state mutation journal does not match the complete state delta")
		}
	}
	return nil
}

func applyFlatDelta(target flatState, sets flatState, deletes []flatStateEntry) error {
	for _, entry := range deletes {
		id := flatStateID(entry.Kind, entry.Key)
		if _, exists := target[id]; !exists {
			return fmt.Errorf("flat delta deletes missing entry %q", id)
		}
		delete(target, id)
	}
	for id, entry := range sets {
		if id != flatStateID(entry.Kind, entry.Key) {
			return fmt.Errorf("flat delta set id %q is invalid", id)
		}
		target[id] = cloneFlatStateEntry(entry)
	}
	return nil
}

func flatStateRoot(flat flatState) (string, error) {
	entries := make([]flatStateCommitmentEntry, 0, len(flat))
	for _, entry := range flatEntriesSorted(flat) {
		entries = append(entries, flatCommitmentEntry(entry))
	}
	return hash.Hex(entries)
}

func flatDeltaRoot(sets flatState, deletes []flatStateEntry) (string, error) {
	commitment := flatDeltaCommitment{
		Sets:    make([]flatStateCommitmentEntry, 0, len(sets)),
		Deletes: make([]flatStateCommitmentKey, 0, len(deletes)),
	}
	for _, entry := range flatEntriesSorted(sets) {
		commitment.Sets = append(commitment.Sets, flatCommitmentEntry(entry))
	}
	for _, entry := range deletes {
		commitment.Deletes = append(commitment.Deletes, flatStateCommitmentKey{
			Kind: entry.Kind, Key: base64.RawURLEncoding.EncodeToString(entry.Key),
		})
	}
	return hash.Hex(commitment)
}

func flatEntriesSorted(flat flatState) []flatStateEntry {
	ids := make([]string, 0, len(flat))
	for id := range flat {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	entries := make([]flatStateEntry, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, flat[id])
	}
	return entries
}

func flatCommitmentEntry(entry flatStateEntry) flatStateCommitmentEntry {
	return flatStateCommitmentEntry{
		Kind:  entry.Kind,
		Key:   base64.RawURLEncoding.EncodeToString(entry.Key),
		Value: base64.RawURLEncoding.EncodeToString(entry.Value),
	}
}

func cloneFlatState(input flatState) flatState {
	cloned := make(flatState, len(input))
	for id, entry := range input {
		cloned[id] = cloneFlatStateEntry(entry)
	}
	return cloned
}

func projectFlatState(input flatState, sets flatState, deletes []flatStateEntry) (flatState, error) {
	projected := make(flatState, len(input)+len(sets))
	for id, entry := range input {
		projected[id] = entry
	}
	if err := applyFlatDelta(projected, sets, deletes); err != nil {
		return nil, err
	}
	return projected, nil
}

func cloneFlatStateEntry(entry flatStateEntry) flatStateEntry {
	return flatStateEntry{
		Kind: entry.Kind, Key: append([]byte(nil), entry.Key...), Value: append([]byte(nil), entry.Value...),
	}
}
