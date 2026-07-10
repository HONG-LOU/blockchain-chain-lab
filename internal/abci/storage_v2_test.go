package abci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"chainlab/internal/hash"
	"chainlab/internal/state"
	"chainlab/internal/types"

	"github.com/cockroachdb/pebble"
	abcitypes "github.com/cometbft/cometbft/abci/types"
)

func TestFlatStateDeltaChangesOnlyTouchedStorageEntry(t *testing.T) {
	store := state.NewStore()
	account := "0x1111111111111111111111111111111111111111"
	store.SetBalance(account, 10)
	if err := store.SetValidators([]string{account}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 1_000; index++ {
		if err := store.SetStorage(account, storageTestKey(index), strings.Repeat("x", 128)); err != nil {
			t.Fatal(err)
		}
	}
	before, err := flattenStateSnapshot(store.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetStorage(account, storageTestKey(500), "changed"); err != nil {
		t.Fatal(err)
	}
	after, err := flattenStateSnapshot(store.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	sets, deletes := diffFlatState(before, after)
	if len(sets) != 1 || len(deletes) != 0 {
		t.Fatalf("delta sets=%d deletes=%d", len(sets), len(deletes))
	}
	for _, entry := range sets {
		if entry.Kind != flatKindAccountStorage {
			t.Fatalf("changed entry kind = %q", entry.Kind)
		}
	}
	restoredSnapshot, err := unflattenStateSnapshot(after)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := state.NewStoreFromSnapshot(restoredSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Root() != store.Root() {
		t.Fatalf("flat round trip root=%s want=%s", restored.Root(), store.Root())
	}
}

func TestArchiveStorageHistoricalQueriesRestartAndBackup(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	profile := ArchiveStorageProfile(2)
	dataDir := filepath.Join(t.TempDir(), "archive")
	application := newPersistentApplicationWithProfile(t, fixture, dataDir, profile)
	for height := int64(1); height <= 3; height++ {
		commitStorageTransfer(t, application, fixture, height, uint64(height-1), uint64(height))
	}
	receiver := "0x2222222222222222222222222222222222222222"
	for height, want := range map[int64]uint64{1: 1, 2: 3, 3: 6} {
		account := queryAccountAtHeight(t, application, receiver, height)
		if account.Balance != want {
			t.Fatalf("receiver balance at height %d = %d, want %d", height, account.Balance, want)
		}
	}
	storage := queryStorageInfo(t, application)
	if storage.Mode != StorageModeArchive || storage.MinimumHeight != 0 || storage.CurrentHeight != 3 {
		t.Fatalf("archive storage info = %+v", storage)
	}
	historicalStorage, err := application.Query(context.Background(), &abcitypes.RequestQuery{
		Path: "/storage", Height: 2,
	})
	if err != nil || historicalStorage.Code != CodeUnsupported || !strings.Contains(historicalStorage.Log, "latest") {
		t.Fatalf("historical storage response=%+v err=%v", historicalStorage, err)
	}
	backupDir := filepath.Join(t.TempDir(), "backup")
	if err := application.Backup(filepath.Join(dataDir, "nested-backup")); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("nested backup error = %v", err)
	}
	if err := application.Backup(backupDir); err != nil {
		t.Fatal(err)
	}
	if err := application.Backup(backupDir); err == nil {
		t.Fatal("backup overwrote an existing directory")
	}
	backup, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: backupDir, Storage: profile})
	if err != nil {
		t.Fatal(err)
	}
	if account := queryAccountAtHeight(t, backup, receiver, 2); account.Balance != 3 {
		t.Fatalf("backup historical balance = %d", account.Balance)
	}
	if err := backup.Close(); err != nil {
		t.Fatal(err)
	}
	if err := application.CompactStorage(); err != nil {
		t.Fatal(err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: dataDir, Storage: profile})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if account := queryAccountAtHeight(t, restarted, receiver, 1); account.Balance != 1 {
		t.Fatalf("restarted historical balance = %d", account.Balance)
	}
}

func TestFullStorageRetentionKeepsCheckpointWindow(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	profile := StorageProfile{Mode: StorageModeFull, RetainHeights: 4, CheckpointInterval: 2}
	dataDir := filepath.Join(t.TempDir(), "full")
	application := newPersistentApplicationWithProfile(t, fixture, dataDir, profile)
	defer application.Close()
	for height := int64(1); height <= 8; height++ {
		commitStorageEmptyBlock(t, application, fixture, height)
	}
	storage := queryStorageInfo(t, application)
	if storage.MinimumHeight != 4 || storage.CurrentHeight != 8 {
		t.Fatalf("full storage info = %+v", storage)
	}
	pruned, err := application.Query(context.Background(), &abcitypes.RequestQuery{Path: "/app", Height: 3})
	if err != nil || pruned.Code != CodeUnsupported || !strings.Contains(pruned.Log, "pruned") {
		t.Fatalf("pruned query response=%+v err=%v", pruned, err)
	}
	retained, err := application.Query(context.Background(), &abcitypes.RequestQuery{Path: "/app", Height: 4})
	if err != nil || retained.Code != CodeOK || retained.Height != 4 {
		t.Fatalf("retained query response=%+v err=%v", retained, err)
	}
	if _, exists, err := pebbleValue(application.persistence.(*applicationDB).db, applicationVersionKey(applicationVersionPrefixV2(3), "manifest", nil)); err != nil || exists {
		t.Fatalf("pruned manifest exists=%t err=%v", exists, err)
	}
	if _, exists, err := pebbleValue(application.persistence.(*applicationDB).db, applicationVersionKey(applicationVersionPrefixV2(4), "manifest", nil)); err != nil || !exists {
		t.Fatalf("retained checkpoint exists=%t err=%v", exists, err)
	}
}

func TestPrunedStorageRetainsOnlyLatestHeight(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	dataDir := filepath.Join(t.TempDir(), "pruned")
	application := newPersistentApplicationWithProfile(t, fixture, dataDir, PrunedStorageProfile())
	defer application.Close()
	for height := int64(1); height <= 3; height++ {
		commitStorageEmptyBlock(t, application, fixture, height)
	}
	storage := queryStorageInfo(t, application)
	if storage.MinimumHeight != 3 || storage.CurrentHeight != 3 {
		t.Fatalf("pruned storage info = %+v", storage)
	}
	for height := int64(0); height < 3; height++ {
		if _, exists, err := pebbleValue(application.persistence.(*applicationDB).db, applicationVersionKey(applicationVersionPrefixV2(height), "manifest", nil)); err != nil || exists {
			t.Fatalf("version %d exists=%t err=%v", height, exists, err)
		}
	}
	latest, err := application.Query(context.Background(), &abcitypes.RequestQuery{Path: "/app", Height: 3})
	if err != nil || latest.Code != CodeOK {
		t.Fatalf("latest query response=%+v err=%v", latest, err)
	}
	old, err := application.Query(context.Background(), &abcitypes.RequestQuery{Path: "/app", Height: 2})
	if err != nil || old.Code != CodeUnsupported || !strings.Contains(old.Log, "pruned") {
		t.Fatalf("old query response=%+v err=%v", old, err)
	}
}

func TestLegacyV1StorageMigratesDeterministicallyAndDiscardsInterruptedShadow(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	dataDir := filepath.Join(t.TempDir(), "legacy")
	createLegacyV1ApplicationDB(t, fixture, dataDir)
	database, err := pebble.Open(dataDir, &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	bogus := []byte("interrupted")
	if err := database.Set(append(applicationV2LivePrefix, []byte("bogus/key")...), bogus, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	marker, err := hash.CanonicalBytes(applicationMigrationV2{
		Protocol: applicationStoreProtocolV2, GenesisHash: "stale",
		Storage: ArchiveStorageProfile(2), StartHeight: 0, CurrentHeight: 1, NextHeight: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Set(applicationStoreMigrationV2Key, marker, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	profile := ArchiveStorageProfile(2)
	migrated, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: dataDir, Storage: profile})
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	info, err := migrated.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil || info.LastBlockHeight != 1 {
		t.Fatalf("migrated info=%+v err=%v", info, err)
	}
	storage := queryStorageInfo(t, migrated)
	if storage.Protocol != applicationStoreProtocolV2 || storage.MinimumHeight != 0 {
		t.Fatalf("migrated storage info = %+v", storage)
	}
	persistence := migrated.persistence.(*applicationDB)
	if _, exists, err := pebbleValue(persistence.db, applicationStoreMigrationV2Key); err != nil || exists {
		t.Fatalf("migration marker exists=%t err=%v", exists, err)
	}
	legacyPrefix := applicationVersionPrefix(0)
	iterator, err := persistence.db.NewIter(&pebble.IterOptions{LowerBound: legacyPrefix, UpperBound: applicationPrefixUpperBound(legacyPrefix)})
	if err != nil {
		t.Fatal(err)
	}
	if iterator.First() {
		t.Fatal("legacy version entries remain after migration")
	}
	if err := iterator.Close(); err != nil {
		t.Fatal(err)
	}
	historical, err := persistence.LoadHeight(0)
	if err != nil || historical.committed.commitment.Height != 0 {
		t.Fatalf("migrated genesis history height=%d err=%v", historical.committed.commitment.Height, err)
	}
	if err := migrated.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: dataDir, Storage: PrunedStorageProfile()}); err == nil || !strings.Contains(err.Error(), "storage profile") {
		t.Fatalf("profile mismatch error = %v", err)
	}
}

func TestLegacyV1StorageMigrationHonorsPrunedProfile(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	dataDir := filepath.Join(t.TempDir(), "legacy-pruned")
	createLegacyV1ApplicationDB(t, fixture, dataDir)
	migrated, err := NewApplication(Config{
		Genesis: fixture.genesis, DataDir: dataDir, Storage: PrunedStorageProfile(),
	})
	if err != nil {
		t.Fatal(err)
	}
	storage := queryStorageInfo(t, migrated)
	if storage.MinimumHeight != 1 || storage.CurrentHeight != 1 {
		t.Fatalf("pruned migration storage info = %+v", storage)
	}
	if _, err := migrated.persistence.(*applicationDB).LoadHeight(0); !errors.Is(err, ErrHistoricalStatePruned) {
		t.Fatalf("pruned migrated genesis error = %v", err)
	}
	if _, exists, err := pebbleValue(
		migrated.persistence.(*applicationDB).db,
		applicationVersionKey(applicationVersionPrefixV2(0), "manifest", nil),
	); err != nil || exists {
		t.Fatalf("pruned migrated height-0 manifest exists=%t err=%v", exists, err)
	}
	if err := migrated.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewApplication(Config{
		Genesis: fixture.genesis, DataDir: dataDir, Storage: PrunedStorageProfile(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if info := queryStorageInfo(t, restarted); info.MinimumHeight != 1 || info.CurrentHeight != 1 {
		t.Fatalf("restarted pruned migration storage info = %+v", info)
	}
}

func TestIncrementalVersionRejectsCorruptDelta(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	profile := ArchiveStorageProfile(10)
	dataDir := filepath.Join(t.TempDir(), "corrupt")
	application := newPersistentApplicationWithProfile(t, fixture, dataDir, profile)
	commitStorageTransfer(t, application, fixture, 1, 0, 1)
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := pebble.Open(dataDir, &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	prefix := append(applicationVersionPrefixV2(1), []byte("delta_set/")...)
	iterator, err := database.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: applicationPrefixUpperBound(prefix)})
	if err != nil {
		t.Fatal(err)
	}
	if !iterator.First() {
		t.Fatal("version 1 contains no delta set")
	}
	key := append([]byte(nil), iterator.Key()...)
	value := append([]byte(nil), iterator.Value()...)
	if err := iterator.Close(); err != nil {
		t.Fatal(err)
	}
	value[len(value)-1] ^= 1
	if err := database.Set(key, value, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: dataDir, Storage: profile}); err == nil || !strings.Contains(err.Error(), "delta root mismatch") {
		t.Fatalf("corrupt delta error = %v", err)
	}
}

func TestArchiveStorageRejectsHistoryBoundaryWithoutCheckpoint(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	profile := ArchiveStorageProfile(2)
	dataDir := filepath.Join(t.TempDir(), "boundary")
	application := newPersistentApplicationWithProfile(t, fixture, dataDir, profile)
	for height := int64(1); height <= 3; height++ {
		commitStorageEmptyBlock(t, application, fixture, height)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := pebble.Open(dataDir, &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	prefix := applicationVersionPrefixV2(0)
	manifestKey := applicationVersionKey(prefix, "manifest", nil)
	raw, exists, err := pebbleValue(database, manifestKey)
	if err != nil || !exists {
		t.Fatalf("height-0 manifest exists=%t err=%v", exists, err)
	}
	var manifest applicationVersionManifestV2
	if err := decodeCanonicalJSON(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.Checkpoint = false
	manifest.CheckpointEntryCount = 0
	manifest.CheckpointRoot = ""
	manifest.Checksum, err = applicationManifestChecksumV2(manifest)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = hash.CanonicalBytes(manifest)
	if err != nil {
		t.Fatal(err)
	}
	batch := database.NewBatch()
	checkpointPrefix := append(applicationVersionPrefixV2(0), []byte("checkpoint/")...)
	if err := batch.DeleteRange(checkpointPrefix, applicationPrefixUpperBound(checkpointPrefix), nil); err != nil {
		t.Fatal(err)
	}
	if err := batch.Set(manifestKey, raw, nil); err != nil {
		t.Fatal(err)
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := batch.Close(); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: dataDir, Storage: profile}); err == nil ||
		!strings.Contains(err.Error(), "history boundary is not a checkpoint") {
		t.Fatalf("history boundary error = %v", err)
	}
}

func storageTestKey(index int) string {
	return fmt.Sprintf("key:%04d", index)
}

func newPersistentApplicationWithProfile(
	t *testing.T,
	fixture applicationFixture,
	dataDir string,
	profile StorageProfile,
) *Application {
	t.Helper()
	application, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: dataDir, Storage: profile})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.InitChain(context.Background(), fixture.initRequest()); err != nil {
		_ = application.Close()
		t.Fatal(err)
	}
	return application
}

func commitStorageTransfer(
	t *testing.T,
	application *Application,
	fixture applicationFixture,
	height int64,
	nonce uint64,
	value uint64,
) {
	t.Helper()
	tx := signFixtureTransaction(t, fixture.key, types.Transaction{
		ChainID: fixture.genesis.ChainID, Type: types.TxTransfer,
		From: fixture.account, To: "0x2222222222222222222222222222222222222222",
		Nonce: nonce, Value: value, GasLimit: 21_000, GasPrice: 1,
	})
	if _, err := application.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Txs: [][]byte{rawFixtureTransaction(t, tx)}, Hash: blockHash(byte(height)), Height: height,
		ProposerAddress: fixture.proposerAddress,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
}

func commitStorageEmptyBlock(t *testing.T, application *Application, fixture applicationFixture, height int64) {
	t.Helper()
	if _, err := application.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(byte(height)), Height: height, ProposerAddress: fixture.proposerAddress,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
}

func queryAccountAtHeight(t *testing.T, application *Application, address string, height int64) types.Account {
	t.Helper()
	response, err := application.Query(context.Background(), &abcitypes.RequestQuery{
		Path: "/account", Data: []byte(address), Height: height,
	})
	if err != nil || response.Code != CodeOK {
		t.Fatalf("account query height %d response=%+v err=%v", height, response, err)
	}
	var account types.Account
	if err := json.Unmarshal(response.Value, &account); err != nil {
		t.Fatal(err)
	}
	return account
}

func queryStorageInfo(t *testing.T, application *Application) StorageInfo {
	t.Helper()
	response, err := application.Query(context.Background(), &abcitypes.RequestQuery{Path: "/storage"})
	if err != nil || response.Code != CodeOK {
		t.Fatalf("storage query response=%+v err=%v", response, err)
	}
	var info StorageInfo
	if err := json.Unmarshal(response.Value, &info); err != nil {
		t.Fatal(err)
	}
	return info
}

func createLegacyV1ApplicationDB(t *testing.T, fixture applicationFixture, dataDir string) {
	t.Helper()
	if err := prepareApplicationDataDirectory(dataDir); err != nil {
		t.Fatal(err)
	}
	database, err := pebble.Open(dataDir, &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	genesisRaw, err := fixture.genesis.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	legacy := &applicationDB{
		db: database, genesis: fixture.genesis, genesisHash: hash.KeccakHex(genesisRaw), currentHeight: -1,
	}
	initial := persistedApplicationState{
		committed: fixture.app.committed, initialized: false, proposers: map[string]string{},
	}
	if err := legacy.saveV1(initial, true, false); err != nil {
		t.Fatal(err)
	}
	fixture.initialize(t)
	heightZero := persistedApplicationState{
		committed: fixture.app.committed, initialized: true,
		proposers: cloneStringMap(fixture.app.proposers),
	}
	if err := legacy.saveV1(heightZero, false, false); err != nil {
		t.Fatal(err)
	}
	commitStorageTransfer(t, fixture.app, fixture, 1, 0, 1)
	heightOne := persistedApplicationState{
		committed: fixture.app.committed, initialized: true,
		proposers: cloneStringMap(fixture.app.proposers),
		txs:       cloneTransactions(fixture.app.committedTxs), receipts: cloneReceipts(fixture.app.committedReceipts),
	}
	if err := legacy.saveV1(heightOne, false, false); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}
