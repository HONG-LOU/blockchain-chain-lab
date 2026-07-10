package abci

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"chainlab/internal/crypto"
	"chainlab/internal/hash"
	"chainlab/internal/state"
	"chainlab/internal/types"

	"github.com/cockroachdb/pebble"
)

const (
	applicationStoreProtocol = "chainlab-app-store-v1"
	applicationStoreDirMode  = 0o700
)

var (
	applicationStoreIdentityKey = []byte("meta/identity")
	applicationStoreCurrentKey  = []byte("meta/current")
)

type persistedApplicationState struct {
	committed   committedState
	initialized bool
	proposers   map[string]string
	txs         [][]byte
	receipts    []types.Receipt
}

type applicationPersistence interface {
	LoadOrCreate(GenesisDocument, persistedApplicationState) (persistedApplicationState, error)
	Save(persistedApplicationState) error
	Restore(persistedApplicationState) error
	Close() error
}

type applicationDB struct {
	db            *pebble.DB
	genesis       GenesisDocument
	genesisHash   string
	currentHeight int64
	closed        bool
}

type applicationStoreIdentity struct {
	Protocol    string `json:"protocol"`
	GenesisHash string `json:"genesis_hash"`
}

type applicationVersionManifest struct {
	Protocol                string                `json:"protocol"`
	GenesisHash             string                `json:"genesis_hash"`
	Height                  int64                 `json:"height"`
	Initialized             bool                  `json:"initialized"`
	Commitment              applicationCommitment `json:"commitment"`
	AppHash                 string                `json:"app_hash"`
	Proposers               map[string]string     `json:"proposers,omitempty"`
	AccountCount            uint64                `json:"account_count"`
	CodeCount               uint64                `json:"code_count"`
	StakeCount              uint64                `json:"stake_count"`
	ProposalCount           uint64                `json:"proposal_count"`
	ParamCount              uint64                `json:"param_count"`
	ValidatorCount          uint64                `json:"validator_count"`
	ValidatorLifecycleCount uint64                `json:"validator_lifecycle_count,omitempty"`
	TxCount                 uint64                `json:"tx_count"`
	ReceiptCount            uint64                `json:"receipt_count"`
	Checksum                string                `json:"checksum"`
}

func openApplicationDB(path string) (*applicationDB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("application data directory is required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve application data directory: %w", err)
	}
	if err := prepareApplicationDataDirectory(absolute); err != nil {
		return nil, err
	}
	db, err := pebble.Open(absolute, &pebble.Options{})
	if err != nil {
		return nil, fmt.Errorf("open application database: %w", err)
	}
	return &applicationDB{db: db, currentHeight: -1}, nil
}

func prepareApplicationDataDirectory(path string) error {
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("application data directory must be a real directory")
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("inspect application data directory: %w", err)
	}
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("inspect application data parent: %w", err)
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return errors.New("application data parent must be a real directory")
	}
	if err := os.Mkdir(path, applicationStoreDirMode); err != nil {
		return fmt.Errorf("create application data directory: %w", err)
	}
	return nil
}

func (store *applicationDB) LoadOrCreate(
	genesis GenesisDocument,
	initial persistedApplicationState,
) (persistedApplicationState, error) {
	if store == nil || store.db == nil || store.closed {
		return persistedApplicationState{}, errors.New("application database is closed")
	}
	genesisBytes, err := genesis.CanonicalBytes()
	if err != nil {
		return persistedApplicationState{}, err
	}
	store.genesis = genesis
	store.genesisHash = hash.KeccakHex(genesisBytes)
	identityRaw, identityExists, err := pebbleValue(store.db, applicationStoreIdentityKey)
	if err != nil {
		return persistedApplicationState{}, err
	}
	currentRaw, currentExists, err := pebbleValue(store.db, applicationStoreCurrentKey)
	if err != nil {
		return persistedApplicationState{}, err
	}
	if !identityExists && !currentExists {
		empty, err := pebbleDatabaseEmpty(store.db)
		if err != nil {
			return persistedApplicationState{}, err
		}
		if !empty {
			return persistedApplicationState{}, errors.New("application database metadata is missing")
		}
		if err := store.save(initial, true, false); err != nil {
			return persistedApplicationState{}, err
		}
		return clonePersistedApplicationState(initial), nil
	}
	if !identityExists || !currentExists {
		return persistedApplicationState{}, errors.New("application database metadata is incomplete")
	}
	var identity applicationStoreIdentity
	if err := decodeCanonicalJSON(identityRaw, &identity); err != nil {
		return persistedApplicationState{}, fmt.Errorf("decode application database identity: %w", err)
	}
	if identity.Protocol != applicationStoreProtocol || identity.GenesisHash != store.genesisHash {
		return persistedApplicationState{}, errors.New("application database genesis identity does not match configured genesis")
	}
	height, err := decodeApplicationHeight(currentRaw)
	if err != nil {
		return persistedApplicationState{}, err
	}
	loaded, err := store.loadVersion(height)
	if err != nil {
		return persistedApplicationState{}, err
	}
	store.currentHeight = height
	return loaded, nil
}

func (store *applicationDB) Save(value persistedApplicationState) error {
	return store.save(value, false, false)
}

func (store *applicationDB) Restore(value persistedApplicationState) error {
	return store.save(value, false, true)
}

func (store *applicationDB) save(value persistedApplicationState, creating bool, restoring bool) error {
	if store == nil || store.db == nil || store.closed {
		return errors.New("application database is closed")
	}
	if store.genesisHash == "" {
		return errors.New("application database genesis identity is not initialized")
	}
	height := value.committed.commitment.Height
	if height < 0 {
		return errors.New("application height must be non-negative")
	}
	if creating {
		if store.currentHeight != -1 || height != 0 {
			return errors.New("new application database must start at height 0")
		}
	} else if restoring {
		if store.currentHeight != 0 || height <= 0 {
			return errors.New("state sync restore requires an empty height-0 application database")
		}
	} else if height != store.currentHeight && height != store.currentHeight+1 {
		return fmt.Errorf("application database height %d cannot advance from %d", height, store.currentHeight)
	}
	manifest, snapshot, err := store.manifest(value)
	if err != nil {
		return err
	}
	prefix := applicationVersionPrefix(height)
	batch := store.db.NewBatch()
	defer batch.Close()
	if height == store.currentHeight {
		if err := batch.DeleteRange(prefix, applicationPrefixUpperBound(prefix), nil); err != nil {
			return fmt.Errorf("clear application version %d: %w", height, err)
		}
	}
	if creating {
		identityRaw, err := hash.CanonicalBytes(applicationStoreIdentity{
			Protocol: applicationStoreProtocol, GenesisHash: store.genesisHash,
		})
		if err != nil {
			return err
		}
		if err := batch.Set(applicationStoreIdentityKey, identityRaw, nil); err != nil {
			return fmt.Errorf("write application identity: %w", err)
		}
	}
	if err := writeApplicationSnapshot(batch, prefix, snapshot); err != nil {
		return err
	}
	if err := writeApplicationBlockResults(batch, prefix, value.txs, value.receipts); err != nil {
		return err
	}
	manifestRaw, err := hash.CanonicalBytes(manifest)
	if err != nil {
		return err
	}
	if err := batch.Set(applicationVersionKey(prefix, "manifest", nil), manifestRaw, nil); err != nil {
		return fmt.Errorf("write application version manifest: %w", err)
	}
	if err := batch.Set(applicationStoreCurrentKey, encodeApplicationHeight(height), nil); err != nil {
		return fmt.Errorf("write current application height: %w", err)
	}
	if height > 0 {
		indexKey, err := applicationBlockIndexKey(value.committed.commitment.BlockHash)
		if err != nil {
			return err
		}
		if existing, exists, err := pebbleValue(store.db, indexKey); err != nil {
			return err
		} else if exists {
			existingHeight, err := decodeApplicationHeight(existing)
			if err != nil || existingHeight != height {
				return errors.New("application block index conflicts with committed height")
			}
		}
		if err := batch.Set(indexKey, encodeApplicationHeight(height), nil); err != nil {
			return fmt.Errorf("write application block index: %w", err)
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("commit application version %d: %w", height, err)
	}
	store.currentHeight = height
	return nil
}

func (store *applicationDB) manifest(
	value persistedApplicationState,
) (applicationVersionManifest, state.Snapshot, error) {
	if value.committed.store == nil {
		return applicationVersionManifest{}, state.Snapshot{}, errors.New("persisted application state store is required")
	}
	snapshot := value.committed.store.Snapshot()
	manifest := applicationVersionManifest{
		Protocol:       applicationStoreProtocol,
		GenesisHash:    store.genesisHash,
		Height:         value.committed.commitment.Height,
		Initialized:    value.initialized,
		Commitment:     value.committed.commitment,
		AppHash:        hex.EncodeToString(value.committed.appHash),
		Proposers:      cloneStringMap(value.proposers),
		AccountCount:   uint64(len(snapshot.Accounts)),
		CodeCount:      uint64(len(snapshot.Codes)),
		StakeCount:     uint64(len(snapshot.Stakes)),
		ProposalCount:  uint64(len(snapshot.Proposals)),
		ParamCount:     uint64(len(snapshot.Params)),
		ValidatorCount: uint64(len(snapshot.Validators)),
		TxCount:        uint64(len(value.txs)),
		ReceiptCount:   uint64(len(value.receipts)),
	}
	if snapshot.ValidatorLifecycle != nil {
		manifest.ValidatorLifecycleCount = 1
	}
	if err := validatePersistedApplicationState(store.genesis, store.genesisHash, value); err != nil {
		return applicationVersionManifest{}, state.Snapshot{}, err
	}
	checksum, err := applicationManifestChecksum(manifest)
	if err != nil {
		return applicationVersionManifest{}, state.Snapshot{}, err
	}
	manifest.Checksum = checksum
	return manifest, snapshot, nil
}

func (store *applicationDB) loadVersion(height int64) (persistedApplicationState, error) {
	prefix := applicationVersionPrefix(height)
	manifestRaw, exists, err := pebbleValue(store.db, applicationVersionKey(prefix, "manifest", nil))
	if err != nil {
		return persistedApplicationState{}, err
	}
	if !exists {
		return persistedApplicationState{}, fmt.Errorf("application version %d manifest is missing", height)
	}
	var manifest applicationVersionManifest
	if err := decodeCanonicalJSON(manifestRaw, &manifest); err != nil {
		return persistedApplicationState{}, fmt.Errorf("decode application version %d manifest: %w", height, err)
	}
	if manifest.Protocol != applicationStoreProtocol || manifest.GenesisHash != store.genesisHash || manifest.Height != height {
		return persistedApplicationState{}, fmt.Errorf("application version %d manifest identity is invalid", height)
	}
	expectedChecksum, err := applicationManifestChecksum(manifest)
	if err != nil {
		return persistedApplicationState{}, err
	}
	if manifest.Checksum != expectedChecksum {
		return persistedApplicationState{}, fmt.Errorf("application version %d manifest checksum mismatch", height)
	}
	snapshot, txs, receipts, err := loadApplicationVersion(store.db, prefix, manifest)
	if err != nil {
		return persistedApplicationState{}, err
	}
	stateStore, err := state.NewStoreFromSnapshot(snapshot)
	if err != nil {
		return persistedApplicationState{}, fmt.Errorf("restore application version %d state: %w", height, err)
	}
	appHash, err := hex.DecodeString(manifest.AppHash)
	if err != nil {
		return persistedApplicationState{}, fmt.Errorf("decode application version %d app hash: %w", height, err)
	}
	if len(appHash) != 32 || hex.EncodeToString(appHash) != manifest.AppHash {
		return persistedApplicationState{}, fmt.Errorf("application version %d app hash is not canonical", height)
	}
	loaded := persistedApplicationState{
		committed: committedState{
			store: stateStore, commitment: manifest.Commitment, appHash: appHash,
		},
		initialized: manifest.Initialized,
		proposers:   cloneStringMap(manifest.Proposers),
		txs:         txs,
		receipts:    receipts,
	}
	if err := validatePersistedApplicationState(store.genesis, store.genesisHash, loaded); err != nil {
		return persistedApplicationState{}, fmt.Errorf("validate application version %d: %w", height, err)
	}
	if height > 0 {
		indexKey, err := applicationBlockIndexKey(manifest.Commitment.BlockHash)
		if err != nil {
			return persistedApplicationState{}, err
		}
		indexedRaw, exists, err := pebbleValue(store.db, indexKey)
		if err != nil || !exists {
			return persistedApplicationState{}, fmt.Errorf("application version %d block index is missing", height)
		}
		indexedHeight, err := decodeApplicationHeight(indexedRaw)
		if err != nil || indexedHeight != height {
			return persistedApplicationState{}, fmt.Errorf("application version %d block index is invalid", height)
		}
	}
	return loaded, nil
}

func (store *applicationDB) Close() error {
	if store == nil || store.closed {
		return nil
	}
	store.closed = true
	if store.db == nil {
		return nil
	}
	if err := store.db.Close(); err != nil {
		return fmt.Errorf("close application database: %w", err)
	}
	return nil
}

func validatePersistedApplicationState(
	genesis GenesisDocument,
	genesisHash string,
	value persistedApplicationState,
) error {
	commitment := value.committed.commitment
	if value.committed.store == nil {
		return errors.New("persisted application store is required")
	}
	if genesisHash == "" || commitment.Protocol != genesis.Protocol || commitment.ChainID != genesis.ChainID {
		return errors.New("persisted application commitment identity is invalid")
	}
	if commitment.Height < 0 || commitment.GasLimit != genesis.BlockGasLimit {
		return errors.New("persisted application commitment height or gas limit is invalid")
	}
	if commitment.StateRoot != value.committed.store.Root() {
		return errors.New("persisted application state root mismatch")
	}
	lifecycle, hasLifecycle := value.committed.store.ValidatorLifecycle()
	if genesis.Protocol == ProtocolVersion {
		if hasLifecycle || commitment.ValidatorRoot != "" || commitment.Epoch != 0 {
			return errors.New("protocol version 1 commitment contains validator lifecycle state")
		}
	} else if value.initialized {
		if !hasLifecycle || commitment.ValidatorRoot == "" || commitment.ValidatorRoot != value.committed.store.ValidatorRoot() {
			return errors.New("protocol version 2 validator root mismatch")
		}
		if err := types.ValidateCanonicalHash("persisted validator root", commitment.ValidatorRoot); err != nil {
			return err
		}
		if commitment.Epoch != validatorEpoch(commitment.Height, genesis.ValidatorPolicy.EpochLength) {
			return errors.New("protocol version 2 validator epoch mismatch")
		}
		for _, identity := range lifecycle.Validators {
			if identity.InactiveHeight != 0 &&
				(identity.InactiveHeight < 3 || (identity.InactiveHeight-1)%genesis.ValidatorPolicy.EpochLength != 0) {
				return errors.New("protocol version 2 validator removal is not on an epoch boundary")
			}
		}
		identitiesByAccount := make(map[string]state.ValidatorIdentity, len(lifecycle.Validators))
		for _, identity := range lifecycle.Validators {
			identitiesByAccount[identity.Account] = identity
		}
		for _, offence := range lifecycle.Offences {
			if offence.ObservedHeight > commitment.Height {
				return errors.New("protocol version 2 offence was observed after the committed height")
			}
			identity, exists := identitiesByAccount[offence.Validator]
			if !exists || identity.InactiveHeight != offence.RemovalHeight {
				return errors.New("protocol version 2 offence removal does not match validator lifecycle")
			}
			expectedSlash, err := evidenceSlashBasisPoints(offence.Type, *genesis.ValidatorPolicy)
			if err != nil || offence.SlashBasisPoints != expectedSlash {
				return errors.New("protocol version 2 offence slash ratio does not match validator policy")
			}
		}
	} else if hasLifecycle || commitment.ValidatorRoot != "" || commitment.Epoch != 0 {
		return errors.New("uninitialized protocol version 2 state contains a validator lifecycle")
	}
	if err := types.ValidateCanonicalHash("persisted transaction root", commitment.TxRoot); err != nil {
		return err
	}
	if err := types.ValidateCanonicalHash("persisted receipt root", commitment.ReceiptRoot); err != nil {
		return err
	}
	if err := types.ValidateCanonicalHash("persisted evidence root", commitment.EvidenceRoot); err != nil {
		return err
	}
	if err := types.ValidateCanonicalHash("persisted state root", commitment.StateRoot); err != nil {
		return err
	}
	if commitment.Height == 0 {
		if commitment.BlockHash != "" || commitment.Proposer != "genesis" {
			return errors.New("persisted genesis commitment is invalid")
		}
	} else {
		if err := types.ValidateCanonicalHash("persisted block hash", commitment.BlockHash); err != nil {
			return err
		}
		if normalized, err := crypto.NormalizeAddress(commitment.Proposer); err != nil || normalized != commitment.Proposer {
			return errors.New("persisted proposer address is not canonical")
		}
	}
	expectedAppHash := applicationHash(commitment)
	if len(value.committed.appHash) != len(expectedAppHash) || !bytes.Equal(value.committed.appHash, expectedAppHash) {
		return errors.New("persisted application hash mismatch")
	}
	if !value.initialized {
		if commitment.Height != 0 || len(value.proposers) != 0 || len(value.txs) != 0 || len(value.receipts) != 0 {
			return errors.New("uninitialized application state must be empty height 0")
		}
		return nil
	}
	if len(value.txs) != len(value.receipts) || len(value.txs) > types.MaxTransactionsPerBlock {
		return errors.New("persisted transaction and receipt counts are invalid")
	}
	decodedTransactions := make([]types.Transaction, len(value.txs))
	for index, raw := range value.txs {
		tx, err := decodeTransaction(raw, genesis.ChainID, genesis.BlockGasLimit)
		if err != nil {
			return fmt.Errorf("persisted transaction %d: %w", index, err)
		}
		decodedTransactions[index] = tx
	}
	if types.TransactionRoot(decodedTransactions) != commitment.TxRoot {
		return errors.New("persisted transaction root mismatch")
	}
	if types.ReceiptRoot(value.receipts) != commitment.ReceiptRoot {
		return errors.New("persisted receipt root mismatch")
	}
	validators := value.committed.store.Validators()
	if len(value.proposers) != len(validators) {
		return errors.New("persisted proposer bindings do not match validator count")
	}
	validatorSet := make(map[string]struct{}, len(validators))
	for _, validator := range validators {
		validatorSet[validator] = struct{}{}
	}
	seenAccounts := make(map[string]struct{}, len(value.proposers))
	for consensusAddress, account := range value.proposers {
		decoded, err := hex.DecodeString(consensusAddress)
		if err != nil || len(decoded) != 20 || hex.EncodeToString(decoded) != consensusAddress {
			return errors.New("persisted consensus address is not canonical")
		}
		normalized, err := crypto.NormalizeAddress(account)
		if err != nil || normalized != account {
			return errors.New("persisted proposer account is not canonical")
		}
		if _, exists := validatorSet[account]; !exists {
			return errors.New("persisted proposer account is not an active validator")
		}
		if _, exists := seenAccounts[account]; exists {
			return errors.New("persisted proposer account is duplicated")
		}
		if genesis.Protocol == ProtocolVersionV2 {
			identity, exists := lifecycle.Validators[consensusAddress]
			if !exists || identity.Account != account {
				return errors.New("persisted proposer binding does not match validator lifecycle identity")
			}
		}
		seenAccounts[account] = struct{}{}
	}
	return nil
}

func writeApplicationSnapshot(batch *pebble.Batch, prefix []byte, snapshot state.Snapshot) error {
	for key, value := range snapshot.Accounts {
		if err := setCanonicalApplicationValue(batch, applicationVersionKey(prefix, "account", []byte(key)), value); err != nil {
			return err
		}
	}
	for key, value := range snapshot.Codes {
		if err := setCanonicalApplicationValue(batch, applicationVersionKey(prefix, "code", []byte(key)), value); err != nil {
			return err
		}
	}
	for key, value := range snapshot.Stakes {
		if err := setCanonicalApplicationValue(batch, applicationVersionKey(prefix, "stake", []byte(key)), value); err != nil {
			return err
		}
	}
	for key, value := range snapshot.Proposals {
		if err := setCanonicalApplicationValue(batch, applicationVersionKey(prefix, "proposal", []byte(key)), value); err != nil {
			return err
		}
	}
	for key, value := range snapshot.Params {
		if err := setCanonicalApplicationValue(batch, applicationVersionKey(prefix, "param", []byte(key)), value); err != nil {
			return err
		}
	}
	for index, validator := range snapshot.Validators {
		key := []byte(fmt.Sprintf("%08d", index))
		if err := setCanonicalApplicationValue(batch, applicationVersionKey(prefix, "validator", key), validator); err != nil {
			return err
		}
	}
	if snapshot.ValidatorLifecycle != nil {
		if err := setCanonicalApplicationValue(
			batch,
			applicationVersionKey(prefix, "validator_lifecycle", []byte("state")),
			*snapshot.ValidatorLifecycle,
		); err != nil {
			return err
		}
	}
	return nil
}

func writeApplicationBlockResults(
	batch *pebble.Batch,
	prefix []byte,
	txs [][]byte,
	receipts []types.Receipt,
) error {
	if len(txs) != len(receipts) || len(txs) > types.MaxTransactionsPerBlock {
		return errors.New("application block result counts are invalid")
	}
	for index, tx := range txs {
		key := []byte(fmt.Sprintf("%08d", index))
		if err := batch.Set(applicationVersionKey(prefix, "tx", key), cloneBytes(tx), nil); err != nil {
			return fmt.Errorf("write application transaction %d: %w", index, err)
		}
		if err := setCanonicalApplicationValue(batch, applicationVersionKey(prefix, "receipt", key), receipts[index]); err != nil {
			return err
		}
	}
	return nil
}

func setCanonicalApplicationValue(batch *pebble.Batch, key []byte, value any) error {
	raw, err := hash.CanonicalBytes(value)
	if err != nil {
		return err
	}
	if err := batch.Set(key, raw, nil); err != nil {
		return fmt.Errorf("write application state entry: %w", err)
	}
	return nil
}

func loadApplicationVersion(
	db *pebble.DB,
	prefix []byte,
	manifest applicationVersionManifest,
) (state.Snapshot, [][]byte, []types.Receipt, error) {
	snapshot := state.Snapshot{
		Accounts:  make(map[string]types.Account),
		Codes:     make(map[string]types.ContractCode),
		Stakes:    make(map[string]uint64),
		Proposals: make(map[string]types.Proposal),
		Params:    make(map[string]string),
	}
	validators := make(map[int]string)
	transactions := make(map[int][]byte)
	receipts := make(map[int]types.Receipt)
	var validatorLifecycle *state.ValidatorLifecycle
	iterator, err := db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: applicationPrefixUpperBound(prefix)})
	if err != nil {
		return state.Snapshot{}, nil, nil, fmt.Errorf("iterate application version: %w", err)
	}
	defer iterator.Close()
	for iterator.First(); iterator.Valid(); iterator.Next() {
		relative := string(iterator.Key()[len(prefix):])
		if relative == "manifest" {
			continue
		}
		kind, encoded, found := strings.Cut(relative, "/")
		if !found || encoded == "" {
			return state.Snapshot{}, nil, nil, fmt.Errorf("application version contains unknown key %q", relative)
		}
		value := append([]byte(nil), iterator.Value()...)
		switch kind {
		case "account":
			key, err := decodeApplicationKey(encoded)
			if err != nil {
				return state.Snapshot{}, nil, nil, err
			}
			var account types.Account
			if err := decodeCanonicalJSON(value, &account); err != nil {
				return state.Snapshot{}, nil, nil, fmt.Errorf("decode account %q: %w", key, err)
			}
			if _, exists := snapshot.Accounts[key]; exists {
				return state.Snapshot{}, nil, nil, fmt.Errorf("duplicate persisted account %q", key)
			}
			snapshot.Accounts[key] = account
		case "code":
			key, err := decodeApplicationKey(encoded)
			if err != nil {
				return state.Snapshot{}, nil, nil, err
			}
			var code types.ContractCode
			if err := decodeCanonicalJSON(value, &code); err != nil {
				return state.Snapshot{}, nil, nil, fmt.Errorf("decode contract code %q: %w", key, err)
			}
			if _, exists := snapshot.Codes[key]; exists {
				return state.Snapshot{}, nil, nil, fmt.Errorf("duplicate persisted contract code %q", key)
			}
			snapshot.Codes[key] = code
		case "stake":
			key, err := decodeApplicationKey(encoded)
			if err != nil {
				return state.Snapshot{}, nil, nil, err
			}
			var stake uint64
			if err := decodeCanonicalJSON(value, &stake); err != nil {
				return state.Snapshot{}, nil, nil, fmt.Errorf("decode stake %q: %w", key, err)
			}
			if _, exists := snapshot.Stakes[key]; exists {
				return state.Snapshot{}, nil, nil, fmt.Errorf("duplicate persisted stake %q", key)
			}
			snapshot.Stakes[key] = stake
		case "proposal":
			key, err := decodeApplicationKey(encoded)
			if err != nil {
				return state.Snapshot{}, nil, nil, err
			}
			var proposal types.Proposal
			if err := decodeCanonicalJSON(value, &proposal); err != nil {
				return state.Snapshot{}, nil, nil, fmt.Errorf("decode proposal %q: %w", key, err)
			}
			if _, exists := snapshot.Proposals[key]; exists {
				return state.Snapshot{}, nil, nil, fmt.Errorf("duplicate persisted proposal %q", key)
			}
			snapshot.Proposals[key] = proposal
		case "param":
			key, err := decodeApplicationKey(encoded)
			if err != nil {
				return state.Snapshot{}, nil, nil, err
			}
			var parameter string
			if err := decodeCanonicalJSON(value, &parameter); err != nil {
				return state.Snapshot{}, nil, nil, fmt.Errorf("decode parameter %q: %w", key, err)
			}
			if _, exists := snapshot.Params[key]; exists {
				return state.Snapshot{}, nil, nil, fmt.Errorf("duplicate persisted parameter %q", key)
			}
			snapshot.Params[key] = parameter
		case "validator":
			indexKey, err := decodeApplicationKey(encoded)
			if err != nil {
				return state.Snapshot{}, nil, nil, err
			}
			index, err := strconv.Atoi(indexKey)
			if err != nil || index < 0 || fmt.Sprintf("%08d", index) != indexKey {
				return state.Snapshot{}, nil, nil, fmt.Errorf("persisted validator index %q is not canonical", indexKey)
			}
			var validator string
			if err := decodeCanonicalJSON(value, &validator); err != nil {
				return state.Snapshot{}, nil, nil, fmt.Errorf("decode validator %d: %w", index, err)
			}
			if _, exists := validators[index]; exists {
				return state.Snapshot{}, nil, nil, fmt.Errorf("duplicate persisted validator index %d", index)
			}
			validators[index] = validator
		case "validator_lifecycle":
			key, err := decodeApplicationKey(encoded)
			if err != nil {
				return state.Snapshot{}, nil, nil, err
			}
			if key != "state" || validatorLifecycle != nil {
				return state.Snapshot{}, nil, nil, errors.New("persisted validator lifecycle key is invalid or duplicated")
			}
			var lifecycle state.ValidatorLifecycle
			if err := decodeCanonicalJSON(value, &lifecycle); err != nil {
				return state.Snapshot{}, nil, nil, fmt.Errorf("decode validator lifecycle: %w", err)
			}
			validatorLifecycle = &lifecycle
		case "tx":
			index, err := decodeApplicationIndex(encoded)
			if err != nil {
				return state.Snapshot{}, nil, nil, err
			}
			if _, exists := transactions[index]; exists {
				return state.Snapshot{}, nil, nil, fmt.Errorf("duplicate persisted transaction index %d", index)
			}
			transactions[index] = value
		case "receipt":
			index, err := decodeApplicationIndex(encoded)
			if err != nil {
				return state.Snapshot{}, nil, nil, err
			}
			var receipt types.Receipt
			if err := decodeCanonicalJSON(value, &receipt); err != nil {
				return state.Snapshot{}, nil, nil, fmt.Errorf("decode receipt %d: %w", index, err)
			}
			if _, exists := receipts[index]; exists {
				return state.Snapshot{}, nil, nil, fmt.Errorf("duplicate persisted receipt index %d", index)
			}
			receipts[index] = receipt
		default:
			return state.Snapshot{}, nil, nil, fmt.Errorf("application version contains unknown namespace %q", kind)
		}
	}
	if err := iterator.Error(); err != nil {
		return state.Snapshot{}, nil, nil, fmt.Errorf("read application version: %w", err)
	}
	if uint64(len(snapshot.Accounts)) != manifest.AccountCount ||
		uint64(len(snapshot.Codes)) != manifest.CodeCount ||
		uint64(len(snapshot.Stakes)) != manifest.StakeCount ||
		uint64(len(snapshot.Proposals)) != manifest.ProposalCount ||
		uint64(len(snapshot.Params)) != manifest.ParamCount ||
		uint64(len(validators)) != manifest.ValidatorCount ||
		boolCount(validatorLifecycle != nil) != manifest.ValidatorLifecycleCount ||
		uint64(len(transactions)) != manifest.TxCount ||
		uint64(len(receipts)) != manifest.ReceiptCount {
		return state.Snapshot{}, nil, nil, errors.New("application version entry counts do not match manifest")
	}
	snapshot.Validators = make([]string, len(validators))
	snapshot.ValidatorLifecycle = validatorLifecycle
	for index := range snapshot.Validators {
		validator, exists := validators[index]
		if !exists {
			return state.Snapshot{}, nil, nil, fmt.Errorf("application version validator index %d is missing", index)
		}
		snapshot.Validators[index] = validator
	}
	orderedTransactions := make([][]byte, len(transactions))
	orderedReceipts := make([]types.Receipt, len(receipts))
	for index := range orderedTransactions {
		tx, txExists := transactions[index]
		receipt, receiptExists := receipts[index]
		if !txExists || !receiptExists {
			return state.Snapshot{}, nil, nil, fmt.Errorf("application version block result index %d is missing", index)
		}
		orderedTransactions[index] = tx
		orderedReceipts[index] = receipt
	}
	return snapshot, orderedTransactions, orderedReceipts, nil
}

func boolCount(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}

func decodeApplicationIndex(encoded string) (int, error) {
	indexKey, err := decodeApplicationKey(encoded)
	if err != nil {
		return 0, err
	}
	index, err := strconv.Atoi(indexKey)
	if err != nil || index < 0 || fmt.Sprintf("%08d", index) != indexKey {
		return 0, fmt.Errorf("persisted application index %q is not canonical", indexKey)
	}
	return index, nil
}

func applicationBlockIndexKey(blockHash string) ([]byte, error) {
	if err := types.ValidateCanonicalHash("application block index hash", blockHash); err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(blockHash, "0x"))
	if err != nil {
		return nil, err
	}
	return append([]byte("index/block/"), raw...), nil
}

func applicationManifestChecksum(manifest applicationVersionManifest) (string, error) {
	manifest.Checksum = ""
	return hash.Hex(manifest)
}

func applicationVersionPrefix(height int64) []byte {
	prefix := make([]byte, len("version/")+8+1)
	copy(prefix, "version/")
	binary.BigEndian.PutUint64(prefix[len("version/"):], uint64(height))
	prefix[len(prefix)-1] = '/'
	return prefix
}

func applicationVersionKey(prefix []byte, namespace string, key []byte) []byte {
	result := append([]byte(nil), prefix...)
	result = append(result, namespace...)
	if key != nil {
		result = append(result, '/')
		result = append(result, base64.RawURLEncoding.EncodeToString(key)...)
	}
	return result
}

func decodeApplicationKey(encoded string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != encoded || len(raw) == 0 {
		return "", fmt.Errorf("persisted application key %q is not canonical", encoded)
	}
	return string(raw), nil
}

func applicationPrefixUpperBound(prefix []byte) []byte {
	return append(append([]byte(nil), prefix...), 0xff)
}

func encodeApplicationHeight(height int64) []byte {
	raw := make([]byte, 8)
	binary.BigEndian.PutUint64(raw, uint64(height))
	return raw
}

func decodeApplicationHeight(raw []byte) (int64, error) {
	if len(raw) != 8 {
		return 0, errors.New("current application height encoding is invalid")
	}
	height := binary.BigEndian.Uint64(raw)
	if height > uint64(^uint64(0)>>1) {
		return 0, errors.New("current application height exceeds int64")
	}
	return int64(height), nil
}

func decodeCanonicalJSON(raw []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("persisted value must contain exactly one JSON value")
	}
	canonical, err := hash.CanonicalBytes(destination)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, canonical) {
		return errors.New("persisted value is not canonically encoded")
	}
	return nil
}

func pebbleValue(db *pebble.DB, key []byte) ([]byte, bool, error) {
	value, closer, err := db.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer closer.Close()
	return append([]byte(nil), value...), true, nil
}

func pebbleDatabaseEmpty(db *pebble.DB) (bool, error) {
	iterator, err := db.NewIter(&pebble.IterOptions{})
	if err != nil {
		return false, err
	}
	defer iterator.Close()
	empty := !iterator.First()
	if err := iterator.Error(); err != nil {
		return false, err
	}
	return empty, nil
}

func clonePersistedApplicationState(value persistedApplicationState) persistedApplicationState {
	return persistedApplicationState{
		committed: committedState{
			store:      value.committed.store.Clone(),
			commitment: value.committed.commitment,
			appHash:    cloneBytes(value.committed.appHash),
		},
		initialized: value.initialized,
		proposers:   cloneStringMap(value.proposers),
		txs:         cloneTransactions(value.txs),
		receipts:    cloneReceipts(value.receipts),
	}
}

func cloneStringMap(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
