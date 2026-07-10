package abci

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"chainlab/internal/hash"
	"chainlab/internal/state"
	"chainlab/internal/types"

	"github.com/cockroachdb/pebble"
)

const applicationStoreProtocolV2 = "chainlab-app-store-v2"

var (
	applicationStoreMinimumHistoryKey = []byte("meta/min_history")
	applicationStoreMigrationV2Key    = []byte("meta/migration-v2")
	applicationV2RootPrefix           = []byte("v2/")
	applicationV2LivePrefix           = []byte("v2/live/")
	applicationV2VersionRootPrefix    = []byte("v2/version/")
	applicationBlockIndexPrefix       = []byte("index/block/")

	ErrHistoricalStatePruned      = errors.New("historical application state was pruned")
	ErrHistoricalStateUnavailable = errors.New("historical application state is unavailable")
)

type applicationStoreIdentityV2 struct {
	Protocol    string         `json:"protocol"`
	GenesisHash string         `json:"genesis_hash"`
	Storage     StorageProfile `json:"storage"`
}

type applicationVersionManifestV2 struct {
	Protocol             string                `json:"protocol"`
	GenesisHash          string                `json:"genesis_hash"`
	Height               int64                 `json:"height"`
	Initialized          bool                  `json:"initialized"`
	Commitment           applicationCommitment `json:"commitment"`
	AppHash              string                `json:"app_hash"`
	Proposers            map[string]string     `json:"proposers,omitempty"`
	StateEntryCount      uint64                `json:"state_entry_count"`
	StateFlatRoot        string                `json:"state_flat_root"`
	DeltaSetCount        uint64                `json:"delta_set_count"`
	DeltaDeleteCount     uint64                `json:"delta_delete_count"`
	DeltaRoot            string                `json:"delta_root"`
	Checkpoint           bool                  `json:"checkpoint"`
	CheckpointEntryCount uint64                `json:"checkpoint_entry_count,omitempty"`
	CheckpointRoot       string                `json:"checkpoint_root,omitempty"`
	TxCount              uint64                `json:"tx_count"`
	ReceiptCount         uint64                `json:"receipt_count"`
	Checksum             string                `json:"checksum"`
}

type applicationMigrationV2 struct {
	Protocol      string         `json:"protocol"`
	GenesisHash   string         `json:"genesis_hash"`
	Storage       StorageProfile `json:"storage"`
	StartHeight   int64          `json:"start_height"`
	CurrentHeight int64          `json:"current_height"`
	NextHeight    int64          `json:"next_height"`
}

type applicationVersionArtifactsV2 struct {
	manifest   applicationVersionManifestV2
	sets       flatState
	deletes    []flatStateEntry
	checkpoint flatState
	txs        [][]byte
	receipts   []types.Receipt
}

func (store *applicationDB) loadOrCreateV2(
	genesis GenesisDocument,
	initial persistedApplicationState,
) (persistedApplicationState, error) {
	if store == nil || store.db == nil || store.closed {
		return persistedApplicationState{}, errors.New("application database is closed")
	}
	profile, err := normalizeStorageProfile(store.profile)
	if err != nil {
		return persistedApplicationState{}, err
	}
	store.profile = profile
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
		if err := store.initializeV2(initial); err != nil {
			return persistedApplicationState{}, err
		}
		return clonePersistedApplicationState(initial), nil
	}
	if !identityExists || !currentExists {
		return persistedApplicationState{}, errors.New("application database metadata is incomplete")
	}
	var envelope struct {
		Protocol string `json:"protocol"`
	}
	if err := json.Unmarshal(identityRaw, &envelope); err != nil {
		return persistedApplicationState{}, fmt.Errorf("decode application database identity: %w", err)
	}
	height, err := decodeApplicationHeight(currentRaw)
	if err != nil {
		return persistedApplicationState{}, err
	}
	switch envelope.Protocol {
	case applicationStoreProtocolV1:
		var identity applicationStoreIdentity
		if err := decodeCanonicalJSON(identityRaw, &identity); err != nil {
			return persistedApplicationState{}, fmt.Errorf("decode legacy application database identity: %w", err)
		}
		if identity.GenesisHash != store.genesisHash {
			return persistedApplicationState{}, errors.New("application database genesis identity does not match configured genesis")
		}
		return store.migrateV1ToV2(height)
	case applicationStoreProtocolV2:
		var identity applicationStoreIdentityV2
		if err := decodeCanonicalJSON(identityRaw, &identity); err != nil {
			return persistedApplicationState{}, fmt.Errorf("decode application database identity: %w", err)
		}
		if identity.GenesisHash != store.genesisHash || identity.Storage != store.profile {
			return persistedApplicationState{}, errors.New("application database genesis identity or storage profile does not match configuration")
		}
	default:
		return persistedApplicationState{}, fmt.Errorf("application database protocol %q is unsupported", envelope.Protocol)
	}
	minimumRaw, exists, err := pebbleValue(store.db, applicationStoreMinimumHistoryKey)
	if err != nil || !exists {
		return persistedApplicationState{}, errors.New("application database minimum history metadata is missing")
	}
	minimum, err := decodeApplicationHeight(minimumRaw)
	if err != nil || minimum > height {
		return persistedApplicationState{}, errors.New("application database history range is invalid")
	}
	loaded, flat, err := store.loadCurrentV2(height)
	if err != nil {
		return persistedApplicationState{}, err
	}
	store.currentHeight = height
	store.minimumHeight = minimum
	store.currentFlat = flat
	if err := store.validateHistoryBoundaryV2(); err != nil {
		return persistedApplicationState{}, err
	}
	if err := store.pruneStaleBlockIndexesV2(); err != nil {
		return persistedApplicationState{}, err
	}
	return loaded, nil
}

func (store *applicationDB) initializeV2(value persistedApplicationState) error {
	if store.currentHeight != -1 || value.committed.commitment.Height != 0 {
		return errors.New("new application database must start at height 0")
	}
	if err := validatePersistedApplicationState(store.genesis, store.genesisHash, value); err != nil {
		return err
	}
	flat, err := flattenStateSnapshot(value.committed.store.Snapshot())
	if err != nil {
		return err
	}
	empty := make(flatState)
	manifest, err := store.manifestV2(value, flat, empty, nil, true)
	if err != nil {
		return err
	}
	batch := store.db.NewBatch()
	defer batch.Close()
	identityRaw, err := hash.CanonicalBytes(applicationStoreIdentityV2{
		Protocol: applicationStoreProtocolV2, GenesisHash: store.genesisHash, Storage: store.profile,
	})
	if err != nil {
		return err
	}
	if err := batch.Set(applicationStoreIdentityKey, identityRaw, nil); err != nil {
		return err
	}
	if err := writeFlatStateNamespace(batch, applicationV2LivePrefix, flat); err != nil {
		return err
	}
	if err := writeApplicationVersionV2(batch, applicationVersionPrefixV2(0), manifest, empty, nil, flat, value.txs, value.receipts); err != nil {
		return err
	}
	if err := batch.Set(applicationStoreCurrentKey, encodeApplicationHeight(0), nil); err != nil {
		return err
	}
	if err := batch.Set(applicationStoreMinimumHistoryKey, encodeApplicationHeight(0), nil); err != nil {
		return err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("initialize application store v2: %w", err)
	}
	store.currentHeight = 0
	store.minimumHeight = 0
	store.currentFlat = cloneFlatState(flat)
	return nil
}

func (store *applicationDB) saveV2(value persistedApplicationState, forceCheckpoint bool) error {
	if store == nil || store.db == nil || store.closed {
		return errors.New("application database is closed")
	}
	height := value.committed.commitment.Height
	if height < 0 || store.currentHeight < 0 {
		return errors.New("application database is not initialized")
	}
	if height == store.currentHeight {
		if height != 0 {
			return errors.New("only initialized height 0 may be replaced in place")
		}
	} else if height != store.currentHeight+1 {
		return fmt.Errorf("application database height %d cannot advance from %d", height, store.currentHeight)
	}
	if err := validatePersistedApplicationState(store.genesis, store.genesisHash, value); err != nil {
		return err
	}
	next, err := flattenStateSnapshot(value.committed.store.Snapshot())
	if err != nil {
		return err
	}
	sets, deletes := diffFlatState(store.currentFlat, next)
	checkpoint := forceCheckpoint || height == 0 || store.shouldCheckpointV2(height)
	manifest, err := store.manifestV2(value, next, sets, deletes, checkpoint)
	if err != nil {
		return err
	}
	batch := store.db.NewBatch()
	defer batch.Close()
	if height == store.currentHeight {
		prefix := applicationVersionPrefixV2(height)
		if err := batch.DeleteRange(prefix, applicationPrefixUpperBound(prefix), nil); err != nil {
			return err
		}
	}
	if err := applyFlatStateToLiveBatch(batch, sets, deletes); err != nil {
		return err
	}
	checkpointState := flatState(nil)
	if checkpoint {
		checkpointState = next
	}
	if err := writeApplicationVersionV2(
		batch, applicationVersionPrefixV2(height), manifest, sets, deletes,
		checkpointState, value.txs, value.receipts,
	); err != nil {
		return err
	}
	if err := batch.Set(applicationStoreCurrentKey, encodeApplicationHeight(height), nil); err != nil {
		return err
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
			return err
		}
	}
	nextMinimum, err := store.pruneVersionsInBatchV2(batch, height)
	if err != nil {
		return err
	}
	if err := batch.Set(applicationStoreMinimumHistoryKey, encodeApplicationHeight(nextMinimum), nil); err != nil {
		return err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("commit incremental application version %d: %w", height, err)
	}
	store.currentHeight = height
	store.minimumHeight = nextMinimum
	store.currentFlat = cloneFlatState(next)
	return nil
}

func (store *applicationDB) restoreV2(value persistedApplicationState) error {
	if store == nil || store.db == nil || store.closed {
		return errors.New("application database is closed")
	}
	height := value.committed.commitment.Height
	if store.currentHeight != 0 || height <= 0 {
		return errors.New("state sync restore requires an empty height-0 application database")
	}
	if err := validatePersistedApplicationState(store.genesis, store.genesisHash, value); err != nil {
		return err
	}
	flat, err := flattenStateSnapshot(value.committed.store.Snapshot())
	if err != nil {
		return err
	}
	empty := make(flatState)
	manifest, err := store.manifestV2(value, flat, empty, nil, true)
	if err != nil {
		return err
	}
	batch := store.db.NewBatch()
	defer batch.Close()
	if err := batch.DeleteRange(applicationV2LivePrefix, applicationPrefixUpperBound(applicationV2LivePrefix), nil); err != nil {
		return err
	}
	if err := batch.DeleteRange(applicationV2VersionRootPrefix, applicationPrefixUpperBound(applicationV2VersionRootPrefix), nil); err != nil {
		return err
	}
	if err := batch.DeleteRange(applicationBlockIndexPrefix, applicationPrefixUpperBound(applicationBlockIndexPrefix), nil); err != nil {
		return err
	}
	if err := writeFlatStateNamespace(batch, applicationV2LivePrefix, flat); err != nil {
		return err
	}
	if err := writeApplicationVersionV2(
		batch, applicationVersionPrefixV2(height), manifest, empty, nil, flat, value.txs, value.receipts,
	); err != nil {
		return err
	}
	if err := batch.Set(applicationStoreCurrentKey, encodeApplicationHeight(height), nil); err != nil {
		return err
	}
	if err := batch.Set(applicationStoreMinimumHistoryKey, encodeApplicationHeight(height), nil); err != nil {
		return err
	}
	indexKey, err := applicationBlockIndexKey(value.committed.commitment.BlockHash)
	if err != nil {
		return err
	}
	if err := batch.Set(indexKey, encodeApplicationHeight(height), nil); err != nil {
		return err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return fmt.Errorf("restore incremental application version %d: %w", height, err)
	}
	store.currentHeight = height
	store.minimumHeight = height
	store.currentFlat = cloneFlatState(flat)
	return nil
}

func (store *applicationDB) manifestV2(
	value persistedApplicationState,
	flat flatState,
	sets flatState,
	deletes []flatStateEntry,
	checkpoint bool,
) (applicationVersionManifestV2, error) {
	stateRoot, err := flatStateRoot(flat)
	if err != nil {
		return applicationVersionManifestV2{}, err
	}
	deltaRoot, err := flatDeltaRoot(sets, deletes)
	if err != nil {
		return applicationVersionManifestV2{}, err
	}
	manifest := applicationVersionManifestV2{
		Protocol: applicationStoreProtocolV2, GenesisHash: store.genesisHash,
		Height: value.committed.commitment.Height, Initialized: value.initialized,
		Commitment: value.committed.commitment, AppHash: hex.EncodeToString(value.committed.appHash),
		Proposers: cloneStringMap(value.proposers), StateEntryCount: uint64(len(flat)),
		StateFlatRoot: stateRoot, DeltaSetCount: uint64(len(sets)),
		DeltaDeleteCount: uint64(len(deletes)), DeltaRoot: deltaRoot,
		Checkpoint: checkpoint, TxCount: uint64(len(value.txs)), ReceiptCount: uint64(len(value.receipts)),
	}
	if checkpoint {
		manifest.CheckpointEntryCount = uint64(len(flat))
		manifest.CheckpointRoot = stateRoot
	}
	checksum, err := applicationManifestChecksumV2(manifest)
	if err != nil {
		return applicationVersionManifestV2{}, err
	}
	manifest.Checksum = checksum
	return manifest, nil
}

func (store *applicationDB) shouldCheckpointV2(height int64) bool {
	if store.profile.Mode == StorageModePruned || height <= 0 {
		return false
	}
	return uint64(height)%store.profile.CheckpointInterval == 0
}

func (store *applicationDB) pruneVersionsInBatchV2(batch *pebble.Batch, height int64) (int64, error) {
	minimum := store.minimumHeight
	if minimum < 0 {
		minimum = 0
	}
	nextMinimum := minimum
	switch store.profile.Mode {
	case StorageModeArchive:
		return minimum, nil
	case StorageModePruned:
		nextMinimum = height
	case StorageModeFull:
		if height < 0 || uint64(height)+1 <= store.profile.RetainHeights {
			return minimum, nil
		}
		desired := height - int64(store.profile.RetainHeights) + 1
		candidate := int64((uint64(desired) / store.profile.CheckpointInterval) * store.profile.CheckpointInterval)
		if candidate > nextMinimum {
			nextMinimum = candidate
		}
	}
	if nextMinimum <= minimum {
		return minimum, nil
	}
	for prunedHeight := minimum; prunedHeight < nextMinimum; prunedHeight++ {
		artifacts, err := store.loadVersionArtifactsV2(prunedHeight)
		if err != nil {
			return 0, fmt.Errorf("load application version %d for pruning: %w", prunedHeight, err)
		}
		prefix := applicationVersionPrefixV2(prunedHeight)
		if err := batch.DeleteRange(prefix, applicationPrefixUpperBound(prefix), nil); err != nil {
			return 0, err
		}
		if prunedHeight > 0 {
			indexKey, err := applicationBlockIndexKey(artifacts.manifest.Commitment.BlockHash)
			if err != nil {
				return 0, err
			}
			if err := batch.Delete(indexKey, nil); err != nil {
				return 0, err
			}
		}
	}
	return nextMinimum, nil
}

func (store *applicationDB) loadCurrentV2(height int64) (persistedApplicationState, flatState, error) {
	flat, err := loadFlatStateNamespace(store.db, applicationV2LivePrefix)
	if err != nil {
		return persistedApplicationState{}, nil, err
	}
	artifacts, err := store.loadVersionArtifactsV2(height)
	if err != nil {
		return persistedApplicationState{}, nil, err
	}
	value, err := store.persistedStateFromFlatV2(flat, artifacts)
	if err != nil {
		return persistedApplicationState{}, nil, err
	}
	return value, flat, nil
}

func (store *applicationDB) LoadHeight(height int64) (persistedApplicationState, error) {
	if store == nil || store.db == nil || store.closed {
		return persistedApplicationState{}, errors.New("application database is closed")
	}
	if height < store.minimumHeight {
		return persistedApplicationState{}, ErrHistoricalStatePruned
	}
	if height > store.currentHeight || height < 0 {
		return persistedApplicationState{}, ErrHistoricalStateUnavailable
	}
	if height == store.currentHeight {
		value, _, err := store.loadCurrentV2(height)
		return value, err
	}
	checkpointHeight := int64(-1)
	var checkpointArtifacts applicationVersionArtifactsV2
	for candidate := height; candidate >= store.minimumHeight; candidate-- {
		artifacts, err := store.loadVersionArtifactsV2(candidate)
		if err != nil {
			return persistedApplicationState{}, err
		}
		if artifacts.manifest.Checkpoint {
			checkpointHeight = candidate
			checkpointArtifacts = artifacts
			break
		}
	}
	if checkpointHeight < 0 {
		return persistedApplicationState{}, errors.New("historical checkpoint is missing")
	}
	flat := cloneFlatState(checkpointArtifacts.checkpoint)
	var targetArtifacts applicationVersionArtifactsV2
	if checkpointHeight == height {
		targetArtifacts = checkpointArtifacts
	}
	for candidate := checkpointHeight + 1; candidate <= height; candidate++ {
		artifacts, err := store.loadVersionArtifactsV2(candidate)
		if err != nil {
			return persistedApplicationState{}, err
		}
		if err := applyFlatDelta(flat, artifacts.sets, artifacts.deletes); err != nil {
			return persistedApplicationState{}, fmt.Errorf("apply application delta %d: %w", candidate, err)
		}
		targetArtifacts = artifacts
	}
	return store.persistedStateFromFlatV2(flat, targetArtifacts)
}

func (store *applicationDB) validateHistoryBoundaryV2() error {
	if store.minimumHeight == store.currentHeight {
		return nil
	}
	artifacts, err := store.loadVersionArtifactsV2(store.minimumHeight)
	if err != nil {
		return fmt.Errorf("load application history boundary %d: %w", store.minimumHeight, err)
	}
	if !artifacts.manifest.Checkpoint {
		return errors.New("application history boundary is not a checkpoint")
	}
	if _, err := store.persistedStateFromFlatV2(artifacts.checkpoint, artifacts); err != nil {
		return fmt.Errorf("validate application history boundary %d: %w", store.minimumHeight, err)
	}
	return nil
}

func (store *applicationDB) HistoryRange() StorageInfo {
	return StorageInfo{
		Protocol: applicationStoreProtocolV2, Mode: store.profile.Mode,
		RetainHeights: store.profile.RetainHeights, CheckpointInterval: store.profile.CheckpointInterval,
		MinimumHeight: store.minimumHeight, CurrentHeight: store.currentHeight,
	}
}

func (store *applicationDB) Backup(path string) error {
	if store == nil || store.db == nil || store.closed {
		return errors.New("application database is closed")
	}
	if strings.TrimSpace(path) == "" {
		return errors.New("application backup directory is required")
	}
	absolute, err := filepathAbs(path)
	if err != nil {
		return err
	}
	inside, err := pathWithinDirectory(store.path, absolute)
	if err != nil {
		return err
	}
	if inside {
		return errors.New("application backup directory must be outside the application data directory")
	}
	if _, err := os.Lstat(absolute); err == nil {
		return errors.New("application backup directory already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := store.db.Checkpoint(absolute, pebble.WithFlushedWAL()); err != nil {
		return fmt.Errorf("checkpoint application database: %w", err)
	}
	return nil
}

func pathWithinDirectory(directory string, path string) (bool, error) {
	relative, err := filepath.Rel(directory, path)
	if err != nil {
		return false, fmt.Errorf("compare application storage paths: %w", err)
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}

func filepathAbs(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve application backup directory: %w", err)
	}
	return absolute, nil
}

func (store *applicationDB) Compact() error {
	if store == nil || store.db == nil || store.closed {
		return errors.New("application database is closed")
	}
	return store.db.Compact([]byte{0x00}, []byte{0xff}, true)
}

func (store *applicationDB) persistedStateFromFlatV2(
	flat flatState,
	artifacts applicationVersionArtifactsV2,
) (persistedApplicationState, error) {
	manifest := artifacts.manifest
	if uint64(len(flat)) != manifest.StateEntryCount {
		return persistedApplicationState{}, errors.New("application flat state entry count mismatch")
	}
	root, err := flatStateRoot(flat)
	if err != nil || root != manifest.StateFlatRoot {
		return persistedApplicationState{}, errors.New("application flat state root mismatch")
	}
	snapshot, err := unflattenStateSnapshot(flat)
	if err != nil {
		return persistedApplicationState{}, err
	}
	stateStore, err := state.NewStoreFromSnapshot(snapshot)
	if err != nil {
		return persistedApplicationState{}, err
	}
	appHash, err := hex.DecodeString(manifest.AppHash)
	if err != nil || len(appHash) != 32 || hex.EncodeToString(appHash) != manifest.AppHash {
		return persistedApplicationState{}, errors.New("application version app hash is not canonical")
	}
	value := persistedApplicationState{
		committed:   committedState{store: stateStore, commitment: manifest.Commitment, appHash: appHash},
		initialized: manifest.Initialized, proposers: cloneStringMap(manifest.Proposers),
		txs: cloneTransactions(artifacts.txs), receipts: cloneReceipts(artifacts.receipts),
	}
	if err := validatePersistedApplicationState(store.genesis, store.genesisHash, value); err != nil {
		return persistedApplicationState{}, err
	}
	if manifest.Height > 0 {
		indexKey, err := applicationBlockIndexKey(manifest.Commitment.BlockHash)
		if err != nil {
			return persistedApplicationState{}, err
		}
		indexed, exists, err := pebbleValue(store.db, indexKey)
		if err != nil || !exists {
			return persistedApplicationState{}, errors.New("application block index is missing")
		}
		indexedHeight, err := decodeApplicationHeight(indexed)
		if err != nil || indexedHeight != manifest.Height {
			return persistedApplicationState{}, errors.New("application block index is invalid")
		}
	}
	return value, nil
}

func (store *applicationDB) loadVersionArtifactsV2(height int64) (applicationVersionArtifactsV2, error) {
	prefix := applicationVersionPrefixV2(height)
	manifestRaw, exists, err := pebbleValue(store.db, applicationVersionKey(prefix, "manifest", nil))
	if err != nil || !exists {
		return applicationVersionArtifactsV2{}, fmt.Errorf("application version %d manifest is missing", height)
	}
	var manifest applicationVersionManifestV2
	if err := decodeCanonicalJSON(manifestRaw, &manifest); err != nil {
		return applicationVersionArtifactsV2{}, fmt.Errorf("decode application version %d manifest: %w", height, err)
	}
	if manifest.Protocol != applicationStoreProtocolV2 || manifest.GenesisHash != store.genesisHash ||
		manifest.Height != height || manifest.Commitment.Height != height {
		return applicationVersionArtifactsV2{}, fmt.Errorf("application version %d identity is invalid", height)
	}
	checksum, err := applicationManifestChecksumV2(manifest)
	if err != nil || checksum != manifest.Checksum {
		return applicationVersionArtifactsV2{}, fmt.Errorf("application version %d manifest checksum mismatch", height)
	}
	artifacts := applicationVersionArtifactsV2{
		manifest: manifest, sets: make(flatState), checkpoint: make(flatState),
	}
	transactions := make(map[int][]byte)
	receipts := make(map[int]types.Receipt)
	iterator, err := store.db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: applicationPrefixUpperBound(prefix)})
	if err != nil {
		return applicationVersionArtifactsV2{}, err
	}
	defer iterator.Close()
	for iterator.First(); iterator.Valid(); iterator.Next() {
		relative := string(iterator.Key()[len(prefix):])
		value := append([]byte(nil), iterator.Value()...)
		if relative == "manifest" {
			continue
		}
		if strings.HasPrefix(relative, "delta_set/") {
			entry, err := decodeFlatStateDiskEntry(strings.TrimPrefix(relative, "delta_set/"), value)
			if err != nil {
				return applicationVersionArtifactsV2{}, err
			}
			artifacts.sets[flatStateID(entry.Kind, entry.Key)] = entry
			continue
		}
		if strings.HasPrefix(relative, "delta_delete/") {
			if len(value) != 0 {
				return applicationVersionArtifactsV2{}, errors.New("application delta deletion marker is not empty")
			}
			entry, err := decodeFlatStateDiskEntry(strings.TrimPrefix(relative, "delta_delete/"), nil)
			if err != nil {
				return applicationVersionArtifactsV2{}, err
			}
			artifacts.deletes = append(artifacts.deletes, entry)
			continue
		}
		if strings.HasPrefix(relative, "checkpoint/") {
			entry, err := decodeFlatStateDiskEntry(strings.TrimPrefix(relative, "checkpoint/"), value)
			if err != nil {
				return applicationVersionArtifactsV2{}, err
			}
			artifacts.checkpoint[flatStateID(entry.Kind, entry.Key)] = entry
			continue
		}
		kind, encoded, found := strings.Cut(relative, "/")
		if !found || encoded == "" {
			return applicationVersionArtifactsV2{}, fmt.Errorf("application version contains unknown key %q", relative)
		}
		switch kind {
		case "tx":
			index, err := decodeApplicationIndex(encoded)
			if err != nil {
				return applicationVersionArtifactsV2{}, err
			}
			transactions[index] = value
		case "receipt":
			index, err := decodeApplicationIndex(encoded)
			if err != nil {
				return applicationVersionArtifactsV2{}, err
			}
			var receipt types.Receipt
			if err := decodeCanonicalJSON(value, &receipt); err != nil {
				return applicationVersionArtifactsV2{}, err
			}
			receipts[index] = receipt
		default:
			return applicationVersionArtifactsV2{}, fmt.Errorf("application version contains unknown namespace %q", kind)
		}
	}
	if err := iterator.Error(); err != nil {
		return applicationVersionArtifactsV2{}, err
	}
	sort.Slice(artifacts.deletes, func(left int, right int) bool {
		return flatStateID(artifacts.deletes[left].Kind, artifacts.deletes[left].Key) <
			flatStateID(artifacts.deletes[right].Kind, artifacts.deletes[right].Key)
	})
	if uint64(len(artifacts.sets)) != manifest.DeltaSetCount ||
		uint64(len(artifacts.deletes)) != manifest.DeltaDeleteCount ||
		uint64(len(transactions)) != manifest.TxCount || uint64(len(receipts)) != manifest.ReceiptCount {
		return applicationVersionArtifactsV2{}, errors.New("application version entry counts do not match manifest")
	}
	for _, deleted := range artifacts.deletes {
		if _, exists := artifacts.sets[flatStateID(deleted.Kind, deleted.Key)]; exists {
			return applicationVersionArtifactsV2{}, errors.New("application delta sets and deletes the same entry")
		}
	}
	deltaRoot, err := flatDeltaRoot(artifacts.sets, artifacts.deletes)
	if err != nil || deltaRoot != manifest.DeltaRoot {
		return applicationVersionArtifactsV2{}, errors.New("application version delta root mismatch")
	}
	if manifest.Checkpoint {
		if manifest.CheckpointEntryCount != manifest.StateEntryCount || manifest.CheckpointRoot != manifest.StateFlatRoot {
			return applicationVersionArtifactsV2{}, errors.New("application checkpoint does not match version state")
		}
		if uint64(len(artifacts.checkpoint)) != manifest.CheckpointEntryCount {
			return applicationVersionArtifactsV2{}, errors.New("application checkpoint entry count mismatch")
		}
		checkpointRoot, err := flatStateRoot(artifacts.checkpoint)
		if err != nil || checkpointRoot != manifest.CheckpointRoot {
			return applicationVersionArtifactsV2{}, errors.New("application checkpoint root mismatch")
		}
	} else if len(artifacts.checkpoint) != 0 || manifest.CheckpointEntryCount != 0 || manifest.CheckpointRoot != "" {
		return applicationVersionArtifactsV2{}, errors.New("non-checkpoint application version contains checkpoint state")
	}
	artifacts.txs = make([][]byte, len(transactions))
	artifacts.receipts = make([]types.Receipt, len(receipts))
	for index := range artifacts.txs {
		tx, txExists := transactions[index]
		receipt, receiptExists := receipts[index]
		if !txExists || !receiptExists {
			return applicationVersionArtifactsV2{}, fmt.Errorf("application block result index %d is missing", index)
		}
		artifacts.txs[index] = tx
		artifacts.receipts[index] = receipt
	}
	return artifacts, nil
}

func writeApplicationVersionV2(
	batch *pebble.Batch,
	prefix []byte,
	manifest applicationVersionManifestV2,
	sets flatState,
	deletes []flatStateEntry,
	checkpoint flatState,
	txs [][]byte,
	receipts []types.Receipt,
) error {
	if err := writeFlatStateNamespace(batch, append(prefix, []byte("delta_set/")...), sets); err != nil {
		return err
	}
	for _, entry := range deletes {
		if err := batch.Set(flatStateDiskKey(append(prefix, []byte("delta_delete/")...), entry), nil, nil); err != nil {
			return err
		}
	}
	if manifest.Checkpoint {
		if err := writeFlatStateNamespace(batch, append(prefix, []byte("checkpoint/")...), checkpoint); err != nil {
			return err
		}
	}
	if err := writeApplicationBlockResults(batch, prefix, txs, receipts); err != nil {
		return err
	}
	manifestRaw, err := hash.CanonicalBytes(manifest)
	if err != nil {
		return err
	}
	return batch.Set(applicationVersionKey(prefix, "manifest", nil), manifestRaw, nil)
}

func writeFlatStateNamespace(batch *pebble.Batch, prefix []byte, flat flatState) error {
	for _, entry := range flatEntriesSorted(flat) {
		if err := batch.Set(flatStateDiskKey(prefix, entry), entry.Value, nil); err != nil {
			return err
		}
	}
	return nil
}

func applyFlatStateToLiveBatch(batch *pebble.Batch, sets flatState, deletes []flatStateEntry) error {
	for _, entry := range flatEntriesSorted(sets) {
		if err := batch.Set(flatStateDiskKey(applicationV2LivePrefix, entry), entry.Value, nil); err != nil {
			return err
		}
	}
	for _, entry := range deletes {
		if err := batch.Delete(flatStateDiskKey(applicationV2LivePrefix, entry), nil); err != nil {
			return err
		}
	}
	return nil
}

func flatStateDiskKey(prefix []byte, entry flatStateEntry) []byte {
	encoded := base64.RawURLEncoding.EncodeToString(entry.Key)
	key := make([]byte, 0, len(prefix)+len(entry.Kind)+1+len(encoded))
	key = append(key, prefix...)
	key = append(key, entry.Kind...)
	key = append(key, '/')
	key = append(key, encoded...)
	return key
}

func decodeFlatStateDiskEntry(relative string, value []byte) (flatStateEntry, error) {
	kind, encoded, found := strings.Cut(relative, "/")
	if !found || !validFlatStateKind(kind) || encoded == "" {
		return flatStateEntry{}, fmt.Errorf("flat state key %q is invalid", relative)
	}
	key, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(key) == 0 || base64.RawURLEncoding.EncodeToString(key) != encoded {
		return flatStateEntry{}, fmt.Errorf("flat state key %q is not canonical", relative)
	}
	return flatStateEntry{Kind: kind, Key: key, Value: append([]byte(nil), value...)}, nil
}

func loadFlatStateNamespace(db *pebble.DB, prefix []byte) (flatState, error) {
	flat := make(flatState)
	iterator, err := db.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: applicationPrefixUpperBound(prefix)})
	if err != nil {
		return nil, err
	}
	defer iterator.Close()
	for iterator.First(); iterator.Valid(); iterator.Next() {
		relative := string(iterator.Key()[len(prefix):])
		entry, err := decodeFlatStateDiskEntry(relative, append([]byte(nil), iterator.Value()...))
		if err != nil {
			return nil, err
		}
		id := flatStateID(entry.Kind, entry.Key)
		if _, exists := flat[id]; exists {
			return nil, fmt.Errorf("duplicate flat state entry %q", id)
		}
		flat[id] = entry
	}
	if err := iterator.Error(); err != nil {
		return nil, err
	}
	return flat, nil
}

func applicationManifestChecksumV2(manifest applicationVersionManifestV2) (string, error) {
	manifest.Checksum = ""
	return hash.Hex(manifest)
}

func applicationVersionPrefixV2(height int64) []byte {
	prefix := make([]byte, len(applicationV2VersionRootPrefix)+8+1)
	copy(prefix, applicationV2VersionRootPrefix)
	binary.BigEndian.PutUint64(prefix[len(applicationV2VersionRootPrefix):], uint64(height))
	prefix[len(prefix)-1] = '/'
	return prefix
}

func (store *applicationDB) migrateV1ToV2(currentHeight int64) (persistedApplicationState, error) {
	startHeight := int64(0)
	switch store.profile.Mode {
	case StorageModePruned:
		startHeight = currentHeight
	case StorageModeFull:
		if currentHeight >= int64(store.profile.RetainHeights) {
			startHeight = currentHeight - int64(store.profile.RetainHeights) + 1
		}
	}
	cleanup := store.db.NewBatch()
	if err := cleanup.DeleteRange(applicationV2RootPrefix, applicationPrefixUpperBound(applicationV2RootPrefix), nil); err != nil {
		cleanup.Close()
		return persistedApplicationState{}, err
	}
	if err := cleanup.Delete(applicationStoreMigrationV2Key, nil); err != nil {
		cleanup.Close()
		return persistedApplicationState{}, err
	}
	if err := cleanup.Commit(pebble.Sync); err != nil {
		cleanup.Close()
		return persistedApplicationState{}, err
	}
	cleanup.Close()

	store.currentFlat = make(flatState)
	store.currentHeight = startHeight - 1
	store.minimumHeight = startHeight
	for height := startHeight; height <= currentHeight; height++ {
		value, err := store.loadVersionV1(height)
		if err != nil {
			return persistedApplicationState{}, fmt.Errorf("load legacy application version %d: %w", height, err)
		}
		next, err := flattenStateSnapshot(value.committed.store.Snapshot())
		if err != nil {
			return persistedApplicationState{}, err
		}
		sets, deletes := diffFlatState(store.currentFlat, next)
		checkpoint := height == startHeight || store.shouldCheckpointV2(height)
		manifest, err := store.manifestV2(value, next, sets, deletes, checkpoint)
		if err != nil {
			return persistedApplicationState{}, err
		}
		batch := store.db.NewBatch()
		if err := applyFlatStateToLiveBatch(batch, sets, deletes); err != nil {
			batch.Close()
			return persistedApplicationState{}, err
		}
		var checkpointState flatState
		if checkpoint {
			checkpointState = next
		}
		if err := writeApplicationVersionV2(
			batch, applicationVersionPrefixV2(height), manifest, sets, deletes,
			checkpointState, value.txs, value.receipts,
		); err != nil {
			batch.Close()
			return persistedApplicationState{}, err
		}
		marker, err := hash.CanonicalBytes(applicationMigrationV2{
			Protocol: applicationStoreProtocolV2, GenesisHash: store.genesisHash, Storage: store.profile,
			StartHeight: startHeight, CurrentHeight: currentHeight, NextHeight: height + 1,
		})
		if err != nil {
			batch.Close()
			return persistedApplicationState{}, err
		}
		if err := batch.Set(applicationStoreMigrationV2Key, marker, nil); err != nil {
			batch.Close()
			return persistedApplicationState{}, err
		}
		if err := batch.Commit(pebble.Sync); err != nil {
			batch.Close()
			return persistedApplicationState{}, fmt.Errorf("migrate legacy application version %d: %w", height, err)
		}
		batch.Close()
		store.currentFlat = cloneFlatState(next)
		store.currentHeight = height
	}
	identityRaw, err := hash.CanonicalBytes(applicationStoreIdentityV2{
		Protocol: applicationStoreProtocolV2, GenesisHash: store.genesisHash, Storage: store.profile,
	})
	if err != nil {
		return persistedApplicationState{}, err
	}
	finalBatch := store.db.NewBatch()
	if err := finalBatch.Set(applicationStoreIdentityKey, identityRaw, nil); err != nil {
		finalBatch.Close()
		return persistedApplicationState{}, err
	}
	if err := finalBatch.Set(applicationStoreMinimumHistoryKey, encodeApplicationHeight(startHeight), nil); err != nil {
		finalBatch.Close()
		return persistedApplicationState{}, err
	}
	if err := finalBatch.Delete(applicationStoreMigrationV2Key, nil); err != nil {
		finalBatch.Close()
		return persistedApplicationState{}, err
	}
	legacyRoot := []byte("version/")
	if err := finalBatch.DeleteRange(legacyRoot, applicationPrefixUpperBound(legacyRoot), nil); err != nil {
		finalBatch.Close()
		return persistedApplicationState{}, err
	}
	if err := finalBatch.Commit(pebble.Sync); err != nil {
		finalBatch.Close()
		return persistedApplicationState{}, fmt.Errorf("activate application store v2 migration: %w", err)
	}
	finalBatch.Close()
	store.minimumHeight = startHeight
	if err := store.pruneStaleBlockIndexesV2(); err != nil {
		return persistedApplicationState{}, err
	}
	loaded, flat, err := store.loadCurrentV2(currentHeight)
	if err != nil {
		return persistedApplicationState{}, err
	}
	store.currentFlat = flat
	return loaded, nil
}

func (store *applicationDB) pruneStaleBlockIndexesV2() error {
	iterator, err := store.db.NewIter(&pebble.IterOptions{
		LowerBound: applicationBlockIndexPrefix,
		UpperBound: applicationPrefixUpperBound(applicationBlockIndexPrefix),
	})
	if err != nil {
		return err
	}
	batch := store.db.NewBatch()
	defer batch.Close()
	defer iterator.Close()
	changed := false
	for iterator.First(); iterator.Valid(); iterator.Next() {
		height, err := decodeApplicationHeight(append([]byte(nil), iterator.Value()...))
		if err != nil {
			return err
		}
		if height < store.minimumHeight {
			if err := batch.Delete(append([]byte(nil), iterator.Key()...), nil); err != nil {
				return err
			}
			changed = true
		}
	}
	if err := iterator.Error(); err != nil {
		return err
	}
	if !changed {
		return nil
	}
	return batch.Commit(pebble.Sync)
}
