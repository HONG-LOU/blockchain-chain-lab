package abci

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"
	"chainlab/internal/types"

	"github.com/cockroachdb/pebble"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmtsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
)

const (
	storageProcessModeEnv    = "CHAINLAB_STORAGE_PROCESS_MODE"
	storageProcessDataEnv    = "CHAINLAB_STORAGE_PROCESS_DATA"
	storageProcessGenesisEnv = "CHAINLAB_STORAGE_PROCESS_GENESIS"
	storageProcessKeyEnv     = "CHAINLAB_STORAGE_PROCESS_KEY"
)

func TestPersistentApplicationSurvivesAbruptProcessExitAfterCommit(t *testing.T) {
	genesis, key, dataDir, genesisPath := storageProcessFixture(t, 0)
	command := storageHelperCommand(dataDir, genesisPath, key, "exit-after-commit")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("storage helper failed: %v\n%s", err, output)
	}
	application, err := NewApplication(Config{Genesis: genesis, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	info, err := application.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if info.LastBlockHeight != 1 || len(info.LastBlockAppHash) != 32 {
		t.Fatalf("restored abrupt-exit info = %+v", info)
	}
}

func TestPersistentApplicationRecoversWholeVersionWhenKilledDuringSync(t *testing.T) {
	genesis, key, dataDir, genesisPath := storageProcessFixture(t, 512)
	command := storageHelperCommand(dataDir, genesisPath, key, "kill-during-sync")
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = command.Stdout
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "READY" {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("storage helper did not reach commit boundary: %q err=%v", scanner.Text(), scanner.Err())
	}
	if err := command.Process.Kill(); err != nil {
		_ = command.Wait()
		t.Fatalf("kill storage helper: %v", err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed storage helper exited successfully")
	}

	application, err := NewApplication(Config{Genesis: genesis, DataDir: dataDir})
	if err != nil {
		t.Fatalf("recover killed application: %v", err)
	}
	info, err := application.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if info.LastBlockHeight != 0 && info.LastBlockHeight != 1 {
		t.Fatalf("recovered partial height %d", info.LastBlockHeight)
	}
	if len(info.LastBlockAppHash) != 32 {
		t.Fatalf("recovered app hash length = %d", len(info.LastBlockAppHash))
	}
	if err := application.Close(); err != nil {
		t.Fatal(err)
	}

	database, err := pebble.Open(dataDir, &pebble.Options{})
	if err != nil {
		t.Fatal(err)
	}
	versionOneEntries := countApplicationVersionEntries(t, database, 1)
	if info.LastBlockHeight == 0 && versionOneEntries != 0 {
		t.Fatalf("uncommitted version has %d visible entries", versionOneEntries)
	}
	if info.LastBlockHeight == 1 && versionOneEntries == 0 {
		t.Fatal("committed version has no visible entries")
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPersistentApplicationStorageProcessHelper(t *testing.T) {
	mode := os.Getenv(storageProcessModeEnv)
	if mode == "" {
		return
	}
	genesis, err := LoadGenesisDocument(os.Getenv(storageProcessGenesisEnv))
	if err != nil {
		t.Fatal(err)
	}
	key, err := chaincrypto.PrivateKeyFromHex(os.Getenv(storageProcessKeyEnv))
	if err != nil {
		t.Fatal(err)
	}
	compressed := key.PubKey().SerializeCompressed()
	validator := abcitypes.UpdateValidator(compressed, 1, cmtsecp256k1.KeyType)
	proposerAddress := cloneBytes(cmtsecp256k1.PubKey(compressed).Address())
	application, err := NewApplication(Config{Genesis: genesis, DataDir: os.Getenv(storageProcessDataEnv)})
	if err != nil {
		t.Fatal(err)
	}
	genesisBytes, err := genesis.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.InitChain(context.Background(), &abcitypes.RequestInitChain{
		ChainId: genesis.ChainID, ConsensusParams: consensusParams(genesis.BlockGasLimit),
		Validators: []abcitypes.ValidatorUpdate{validator}, AppStateBytes: genesisBytes, InitialHeight: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := application.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(1), Height: 1, ProposerAddress: proposerAddress,
	}); err != nil {
		t.Fatal(err)
	}
	switch mode {
	case "exit-after-commit":
		if _, err := application.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
			t.Fatal(err)
		}
		os.Exit(0)
	case "kill-during-sync":
		commitApplicationCandidateAtKillBoundary(t, application)
	default:
		t.Fatalf("unknown storage process mode %q", mode)
	}
}

func commitApplicationCandidateAtKillBoundary(t *testing.T, application *Application) {
	t.Helper()
	application.mu.Lock()
	defer application.mu.Unlock()
	persistence, ok := application.persistence.(*applicationDB)
	if !ok || application.candidate == nil {
		t.Fatal("persistent application candidate is unavailable")
	}
	candidateStore := application.candidate.state.store
	for _, account := range candidateStore.Snapshot().Accounts {
		for index := 0; index < candidateStore.StorageEntryCount(account.Address); index++ {
			key := fmt.Sprintf("load:%04d", index)
			if err := candidateStore.SetStorage(account.Address, key, strings.Repeat("y", 4096)); err != nil {
				t.Fatal(err)
			}
		}
	}
	application.candidate.state.commitment.StateRoot = candidateStore.Root()
	application.candidate.state.appHash = applicationHash(application.candidate.state.commitment)
	value := persistedApplicationState{
		committed: application.candidate.state, initialized: true,
		proposers: cloneStringMap(application.proposers),
		txs:       cloneTransactions(application.candidate.txs),
		receipts:  cloneReceipts(application.candidate.receipts),
	}
	next, err := flattenStateSnapshot(value.committed.store.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	sets, deletes := diffFlatState(persistence.currentFlat, next)
	manifest, err := persistence.manifestV2(value, next, sets, deletes, false)
	if err != nil {
		t.Fatal(err)
	}
	prefix := applicationVersionPrefixV2(manifest.Height)
	batch := persistence.db.NewBatch()
	defer batch.Close()
	if err := applyFlatStateToLiveBatch(batch, sets, deletes); err != nil {
		t.Fatal(err)
	}
	if err := writeApplicationVersionV2(batch, prefix, manifest, sets, deletes, nil, value.txs, value.receipts); err != nil {
		t.Fatal(err)
	}
	if err := batch.Set(applicationStoreCurrentKey, encodeApplicationHeight(manifest.Height), nil); err != nil {
		t.Fatal(err)
	}
	if err := batch.Set(applicationStoreMinimumHistoryKey, encodeApplicationHeight(0), nil); err != nil {
		t.Fatal(err)
	}
	blockIndexKey, err := applicationBlockIndexKey(manifest.Commitment.BlockHash)
	if err != nil {
		t.Fatal(err)
	}
	if err := batch.Set(blockIndexKey, encodeApplicationHeight(manifest.Height), nil); err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, "READY")
	if err := batch.Commit(pebble.Sync); err != nil {
		t.Fatal(err)
	}
	select {}
}

func storageProcessFixture(t *testing.T, storageEntries int) (GenesisDocument, chaincrypto.PrivateKey, string, string) {
	t.Helper()
	key, err := chaincrypto.PrivateKeyFromHex("0x" + strings.Repeat("0", 63) + "1")
	if err != nil {
		t.Fatal(err)
	}
	account := chaincrypto.AddressFromPrivateKey(key)
	store := state.NewStore()
	store.SetBalance(account, 1_000_000)
	if err := store.SetValidators([]string{account}); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < storageEntries; index++ {
		if err := store.SetStorage(account, fmt.Sprintf("load:%04d", index), strings.Repeat("x", 4096)); err != nil {
			t.Fatal(err)
		}
	}
	genesis, err := NewGenesisDocument("chainlab-storage-process", types.DefaultBlockGasLimit, store)
	if err != nil {
		t.Fatal(err)
	}
	genesisRaw, err := genesis.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	genesisPath := filepath.Join(root, "genesis.json")
	if err := os.WriteFile(genesisPath, genesisRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	return genesis, key, filepath.Join(root, "application"), genesisPath
}

func storageHelperCommand(dataDir string, genesisPath string, key chaincrypto.PrivateKey, mode string) *exec.Cmd {
	command := exec.Command(os.Args[0], "-test.run=^TestPersistentApplicationStorageProcessHelper$")
	command.Env = append(os.Environ(),
		storageProcessModeEnv+"="+mode,
		storageProcessDataEnv+"="+dataDir,
		storageProcessGenesisEnv+"="+genesisPath,
		storageProcessKeyEnv+"="+chaincrypto.PrivateKeyToHex(key),
	)
	return command
}

func countApplicationVersionEntries(t *testing.T, database *pebble.DB, height int64) int {
	t.Helper()
	prefix := applicationVersionPrefixV2(height)
	iterator, err := database.NewIter(&pebble.IterOptions{
		LowerBound: prefix, UpperBound: applicationPrefixUpperBound(prefix),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer iterator.Close()
	count := 0
	for iterator.First(); iterator.Valid(); iterator.Next() {
		count++
	}
	if err := iterator.Error(); err != nil {
		t.Fatal(err)
	}
	return count
}
