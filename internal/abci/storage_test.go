package abci

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"chainlab/internal/hash"
	"chainlab/internal/types"

	"github.com/cockroachdb/pebble"
	abcitypes "github.com/cometbft/cometbft/abci/types"
)

func TestPersistentApplicationRestoresAtomicCommittedVersion(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	dataDir := filepath.Join(t.TempDir(), "application")
	application, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.InitChain(context.Background(), fixture.initRequest()); err != nil {
		t.Fatal(err)
	}
	receiver := "0x1111111111111111111111111111111111111111"
	tx := signFixtureTransaction(t, fixture.key, types.Transaction{
		ChainID: fixture.genesis.ChainID, Type: types.TxTransfer,
		From: fixture.account, To: receiver, Nonce: 0, Value: 7,
		GasLimit: 21_000, GasPrice: 1,
	})
	finalized, err := application.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Txs: [][]byte{rawFixtureTransaction(t, tx)}, Hash: blockHash(1), Height: 1,
		ProposerAddress: fixture.proposerAddress,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	before, err := application.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if before.LastBlockHeight != 1 || !bytes.Equal(before.LastBlockAppHash, finalized.AppHash) {
		t.Fatalf("committed info = %+v", before)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}

	restored, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	after, err := restored.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if after.LastBlockHeight != before.LastBlockHeight || !bytes.Equal(after.LastBlockAppHash, before.LastBlockAppHash) {
		t.Fatalf("restored info = %+v, want %+v", after, before)
	}
	if _, err := restored.InitChain(context.Background(), fixture.initRequest()); err == nil || !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("restored duplicate init error = %v", err)
	}
	account := queryPersistentAccount(t, restored, fixture.account)
	if account.Nonce != 1 || account.Balance != 1_000_000-21_000-7 {
		t.Fatalf("restored sender = %+v", account)
	}
	prepared, err := restored.PrepareProposal(context.Background(), &abcitypes.RequestPrepareProposal{
		Height: 2, MaxTxBytes: types.MaxProposalTxBytes, ProposerAddress: fixture.proposerAddress,
	})
	if err != nil || len(prepared.Txs) != 0 {
		t.Fatalf("restored proposer binding failed: response=%+v err=%v", prepared, err)
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := pebble.Open(dataDir, &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	databaseClosed := false
	t.Cleanup(func() {
		if !databaseClosed {
			_ = database.Close()
		}
	})
	current, exists, err := pebbleValue(database, applicationStoreCurrentKey)
	if err != nil || !exists {
		t.Fatalf("current version exists=%t err=%v", exists, err)
	}
	height, err := decodeApplicationHeight(current)
	if err != nil || height != 1 {
		t.Fatalf("current height = %d err=%v", height, err)
	}
	for _, version := range []int64{0, 1} {
		_, exists, err := pebbleValue(database, applicationVersionKey(applicationVersionPrefixV2(version), "manifest", nil))
		if err != nil || !exists {
			t.Fatalf("version %d manifest exists=%t err=%v", version, exists, err)
		}
	}
	for _, namespace := range []string{"tx", "receipt"} {
		key := applicationVersionKey(applicationVersionPrefixV2(1), namespace, []byte("00000000"))
		if _, exists, err := pebbleValue(database, key); err != nil || !exists {
			t.Fatalf("version 1 %s exists=%t err=%v", namespace, exists, err)
		}
	}
	blockIndexKey, err := applicationBlockIndexKey("0x" + strings.Repeat("01", 32))
	if err != nil {
		t.Fatal(err)
	}
	indexedRaw, exists, err := pebbleValue(database, blockIndexKey)
	if err != nil || !exists {
		t.Fatalf("block index exists=%t err=%v", exists, err)
	}
	indexedHeight, err := decodeApplicationHeight(indexedRaw)
	if err != nil || indexedHeight != 1 {
		t.Fatalf("indexed height=%d err=%v", indexedHeight, err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	databaseClosed = true

	other := newApplicationFixture(t, 1_000_000)
	if _, err := NewApplication(Config{Genesis: other.genesis, DataDir: dataDir}); err == nil || !strings.Contains(err.Error(), "genesis identity") {
		t.Fatalf("mismatched genesis error = %v", err)
	}
}

func TestPersistentApplicationRejectsReceiptRootCorruption(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	dataDir := filepath.Join(t.TempDir(), "application")
	application, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.InitChain(context.Background(), fixture.initRequest()); err != nil {
		t.Fatal(err)
	}
	tx := signFixtureTransaction(t, fixture.key, types.Transaction{
		ChainID: fixture.genesis.ChainID, Type: types.TxTransfer,
		From: fixture.account, To: "0x2222222222222222222222222222222222222222",
		Nonce: 0, Value: 1, GasLimit: 21_000, GasPrice: 1,
	})
	if _, err := application.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Txs: [][]byte{rawFixtureTransaction(t, tx)}, Hash: blockHash(1), Height: 1,
		ProposerAddress: fixture.proposerAddress,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := pebble.Open(dataDir, &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	databaseClosed := false
	t.Cleanup(func() {
		if !databaseClosed {
			_ = database.Close()
		}
	})
	receiptKey := applicationVersionKey(applicationVersionPrefixV2(1), "receipt", []byte("00000000"))
	receiptRaw, exists, err := pebbleValue(database, receiptKey)
	if err != nil || !exists {
		t.Fatalf("receipt exists=%t err=%v", exists, err)
	}
	var receipt types.Receipt
	if err := decodeCanonicalJSON(receiptRaw, &receipt); err != nil {
		t.Fatal(err)
	}
	receipt.GasUsed++
	corruptRaw, err := hash.CanonicalBytes(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Set(receiptKey, corruptRaw, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	databaseClosed = true
	if _, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: dataDir}); err == nil || !strings.Contains(err.Error(), "receipt root mismatch") {
		t.Fatalf("corrupt receipt error = %v", err)
	}
}

func TestPersistentApplicationRejectsMissingVersionEntry(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	dataDir := filepath.Join(t.TempDir(), "application")
	application, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.InitChain(context.Background(), fixture.initRequest()); err != nil {
		t.Fatal(err)
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := pebble.Open(dataDir, &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	databaseClosed := false
	t.Cleanup(func() {
		if !databaseClosed {
			_ = database.Close()
		}
	})
	accountKey := flatStateDiskKey(applicationV2LivePrefix, flatStateEntry{Kind: flatKindAccount, Key: []byte(fixture.account)})
	if err := database.Delete(accountKey, pebble.Sync); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	databaseClosed = true
	if _, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: dataDir}); err == nil || !strings.Contains(err.Error(), "entry count") {
		t.Fatalf("missing entry error = %v", err)
	}
}

func TestPersistenceFailureHaltsWithoutPublishingCandidate(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	persistence := &failingApplicationPersistence{failAtSave: 2}
	application, err := NewApplication(Config{Genesis: fixture.genesis, persistence: persistence})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.InitChain(context.Background(), fixture.initRequest()); err != nil {
		t.Fatal(err)
	}
	committedSparseRoot := application.committed.flatTree.Root()
	receiver := "0x1111111111111111111111111111111111111111"
	tx := signFixtureTransaction(t, fixture.key, types.Transaction{
		ChainID: fixture.genesis.ChainID, Type: types.TxTransfer,
		From: fixture.account, To: receiver, Nonce: 0, Value: 1,
		GasLimit: 21_000, GasPrice: 1,
	})
	finalized, err := application.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(1), Height: 1, ProposerAddress: fixture.proposerAddress,
		Txs: [][]byte{rawFixtureTransaction(t, tx)},
	})
	if err != nil || len(finalized.AppHash) != 32 {
		t.Fatalf("finalize = %+v err=%v", finalized, err)
	}
	if _, err := application.Commit(context.Background(), &abcitypes.RequestCommit{}); err == nil || !strings.Contains(err.Error(), "injected persistence failure") {
		t.Fatalf("commit error = %v", err)
	}
	if application.committed.flatTree.Root() != committedSparseRoot ||
		application.candidate.state.flatTree.Root() == committedSparseRoot {
		t.Fatal("failed persistence published or failed to isolate the sparse candidate overlay")
	}
	info, err := application.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if info.LastBlockHeight != 0 || bytes.Equal(info.LastBlockAppHash, finalized.AppHash) || !strings.Contains(info.Data, `"halted":true`) {
		t.Fatalf("failed persistence published candidate: %+v", info)
	}
	if _, err := application.CheckTx(context.Background(), &abcitypes.RequestCheckTx{}); err == nil || !strings.Contains(err.Error(), "application is halted") {
		t.Fatalf("halted application check error = %v", err)
	}
}

func queryPersistentAccount(t *testing.T, application *Application, address string) types.Account {
	t.Helper()
	response, err := application.Query(context.Background(), &abcitypes.RequestQuery{Path: "/account", Data: []byte(address)})
	if err != nil || response.Code != CodeOK {
		t.Fatalf("account query response=%+v err=%v", response, err)
	}
	var account types.Account
	if err := decodeCanonicalJSON(response.Value, &account); err != nil {
		t.Fatal(err)
	}
	return account
}

type failingApplicationPersistence struct {
	value      persistedApplicationState
	saveCalls  int
	failAtSave int
}

func (persistence *failingApplicationPersistence) LoadOrCreate(
	_ GenesisDocument,
	initial persistedApplicationState,
) (persistedApplicationState, error) {
	persistence.value = clonePersistedApplicationState(initial)
	return clonePersistedApplicationState(initial), nil
}

func (persistence *failingApplicationPersistence) Save(value persistedApplicationState) error {
	persistence.saveCalls++
	if persistence.saveCalls == persistence.failAtSave {
		return errors.New("injected persistence failure")
	}
	persistence.value = clonePersistedApplicationState(value)
	return nil
}

func (*failingApplicationPersistence) Close() error {
	return nil
}

func (persistence *failingApplicationPersistence) Restore(value persistedApplicationState) error {
	return persistence.Save(value)
}
