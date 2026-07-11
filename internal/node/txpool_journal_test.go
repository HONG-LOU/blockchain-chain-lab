package node

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/types"
)

func TestPersistentTxPoolSurvivesRestartAndCommitsAtomicallyWithBlock(t *testing.T) {
	config, key := newTxPoolJournalConfig(t)
	n, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	sender := chaincrypto.AddressFromPrivateKey(key)
	pending := txPoolJournalSignedTransfer(t, key, sender, 0, 1)
	queued := txPoolJournalSignedTransfer(t, key, sender, 2, 3)
	if err := n.SubmitTx(pending); err != nil {
		t.Fatal(err)
	}
	if err := n.SubmitTx(queued); err != nil {
		t.Fatal(err)
	}
	assertTxPoolByteAccounting(t, n)
	closeTestNode(t, n)

	restored, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	pool := restored.TxPool()
	if len(pool.Pending) != 1 || pool.Pending[0].Hash() != pending.Hash() ||
		len(pool.Queued) != 1 || pool.Queued[0].Hash() != queued.Hash() {
		t.Fatalf("restored transaction pool = %+v", pool)
	}
	assertTxPoolByteAccounting(t, restored)
	replacement := signRuntimeFaultTestTx(t, key, types.Transaction{
		ChainID: "chainlab-txpool-journal", Type: types.TxTransfer, From: sender,
		To: "0xcccccccccccccccccccccccccccccccccccccccc", Nonce: 2,
		Value: 4, GasLimit: 21_000, GasPrice: 2,
	})
	if err := restored.SubmitTx(replacement); err != nil {
		t.Fatal(err)
	}
	closeTestNode(t, restored)

	restored, err = New(config)
	if err != nil {
		t.Fatal(err)
	}
	pool = restored.TxPool()
	if len(pool.Queued) != 1 || pool.Queued[0].Hash() != replacement.Hash() {
		t.Fatalf("restored queued replacement = %+v", pool.Queued)
	}

	gap := txPoolJournalSignedTransfer(t, key, sender, 1, 2)
	if err := restored.SubmitTx(gap); err != nil {
		t.Fatal(err)
	}
	pool = restored.TxPool()
	if len(pool.Pending) != 3 || len(pool.Queued) != 0 {
		t.Fatalf("promoted transaction pool = %+v", pool)
	}
	if _, err := restored.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	if pool = restored.TxPool(); len(pool.Pending) != 0 || len(pool.Queued) != 0 {
		t.Fatalf("post-block transaction pool = %+v", pool)
	}
	closeTestNode(t, restored)

	afterBlock, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestNode(t, afterBlock)
	if afterBlock.Head().Header.Height != 1 {
		t.Fatalf("restored head height = %d, want 1", afterBlock.Head().Header.Height)
	}
	if pool = afterBlock.TxPool(); len(pool.Pending) != 0 || len(pool.Queued) != 0 {
		t.Fatalf("restored post-block transaction pool = %+v", pool)
	}
}

func TestPersistentTxPoolRejectsChecksumValidInvalidTransaction(t *testing.T) {
	config, key := newTxPoolJournalConfig(t)
	n, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	sender := chaincrypto.AddressFromPrivateKey(key)
	if err := n.SubmitTx(txPoolJournalSignedTransfer(t, key, sender, 0, 1)); err != nil {
		t.Fatal(err)
	}
	closeTestNode(t, n)

	snapshot, err := loadDiskSnapshot(config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Pending[0].Value++
	writeTxPoolJournalSnapshot(t, config.DataDir, *snapshot)
	if _, err := New(config); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("invalid persisted transaction error = %v", err)
	}
}

func TestPersistentTxPoolRejectsQueuedCapacityOverflow(t *testing.T) {
	config, _ := newTxPoolJournalConfig(t)
	n, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	closeTestNode(t, n)

	snapshot, err := loadDiskSnapshot(config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Queued = make([]types.Transaction, maxQueuedTransactions+1)
	writeTxPoolJournalSnapshot(t, config.DataDir, *snapshot)
	if _, err := New(config); err == nil || !strings.Contains(err.Error(), "queued transaction pool exceeds") {
		t.Fatalf("persisted queued capacity error = %v", err)
	}
}

func TestDiskSnapshotV2MigratesToV3(t *testing.T) {
	config, _ := newTxPoolJournalConfig(t)
	n, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	closeTestNode(t, n)

	snapshot, err := loadDiskSnapshot(config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	previousGeneration := snapshot.Generation
	snapshot.Version = legacyDiskSnapshotVersion
	snapshot.Pending = nil
	snapshot.Queued = nil
	writeTxPoolJournalSnapshot(t, config.DataDir, *snapshot)
	manifest, err := loadDataManifest(config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SnapshotVersion = legacyDiskSnapshotVersion
	if err := writeDataManifest(config.DataDir, *manifest); err != nil {
		t.Fatal(err)
	}

	migrated, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	closeTestNode(t, migrated)
	snapshot, err = loadDiskSnapshot(config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err = loadDataManifest(config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Version != diskSnapshotVersion || manifest.SnapshotVersion != diskSnapshotVersion {
		t.Fatalf("migrated versions = snapshot %d manifest %d", snapshot.Version, manifest.SnapshotVersion)
	}
	if snapshot.Generation != previousGeneration+1 {
		t.Fatalf("migrated generation = %d, want %d", snapshot.Generation, previousGeneration+1)
	}
}

func TestDiskSnapshotV3MigrationResumesWithLegacyManifest(t *testing.T) {
	config, _ := newTxPoolJournalConfig(t)
	n, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	closeTestNode(t, n)
	before, err := loadDiskSnapshot(config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := loadDataManifest(config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SnapshotVersion = legacyDiskSnapshotVersion
	if err := writeDataManifest(config.DataDir, *manifest); err != nil {
		t.Fatal(err)
	}

	resumed, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	closeTestNode(t, resumed)
	after, err := loadDiskSnapshot(config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err = loadDataManifest(config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if after.Version != diskSnapshotVersion || manifest.SnapshotVersion != diskSnapshotVersion {
		t.Fatalf("resumed versions = snapshot %d manifest %d", after.Version, manifest.SnapshotVersion)
	}
	if after.Generation != before.Generation+1 {
		t.Fatalf("resumed generation = %d, want %d", after.Generation, before.Generation+1)
	}
}

func TestDataManifestV3RejectsSnapshotV2Rollback(t *testing.T) {
	config, _ := newTxPoolJournalConfig(t)
	n, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	closeTestNode(t, n)
	snapshot, err := loadDiskSnapshot(config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Version = legacyDiskSnapshotVersion
	snapshot.Pending = nil
	snapshot.Queued = nil
	writeTxPoolJournalSnapshot(t, config.DataDir, *snapshot)
	if _, err := New(config); err == nil || !strings.Contains(err.Error(), "rollback is not allowed") {
		t.Fatalf("snapshot rollback error = %v", err)
	}
}

func TestSubmitTxRollsBackPoolWhenSnapshotWriteFails(t *testing.T) {
	config, key := newTxPoolJournalConfig(t)
	n, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestNode(t, n)
	if err := os.Remove(chainPath(config.DataDir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(chainPath(config.DataDir), 0o700); err != nil {
		t.Fatal(err)
	}
	sender := chaincrypto.AddressFromPrivateKey(key)
	err = n.SubmitTx(txPoolJournalSignedTransfer(t, key, sender, 0, 1))
	if err == nil {
		t.Fatal("transaction submission succeeded after snapshot path became a directory")
	}
	if len(n.mempool) != 0 || len(n.queued) != 0 || n.txPoolBytes != 0 || n.txPoolRevision != 0 {
		t.Fatalf(
			"transaction pool changed after persistence failure: pending=%d queued=%d bytes=%d revision=%d",
			len(n.mempool), len(n.queued), n.txPoolBytes, n.txPoolRevision,
		)
	}
}

func newTxPoolJournalConfig(t *testing.T) (Config, chaincrypto.PrivateKey) {
	t.Helper()
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := chaincrypto.AddressFromPrivateKey(key)
	return Config{
		Role: RoleValidator, ChainID: "chainlab-txpool-journal", ProposerKey: key,
		Validators: []string{proposer}, GenesisBalance: map[string]uint64{proposer: 1_000_000},
		DataDir: t.TempDir(), GenesisTimeUnix: DeterministicDevGenesisTimeUnix,
	}, key
}

func txPoolJournalSignedTransfer(
	t *testing.T,
	key chaincrypto.PrivateKey,
	from string,
	nonce uint64,
	value uint64,
) types.Transaction {
	t.Helper()
	return signRuntimeFaultTestTx(t, key, types.Transaction{
		ChainID: "chainlab-txpool-journal", Type: types.TxTransfer, From: from,
		To: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Nonce: nonce,
		Value: value, GasLimit: 21_000, GasPrice: 1,
	})
}

func writeTxPoolJournalSnapshot(t *testing.T, dataDir string, snapshot diskSnapshot) {
	t.Helper()
	snapshot.Checksum = ""
	checksum, err := diskSnapshotChecksum(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Checksum = checksum
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chainPath(dataDir), append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}
