package abci

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"
	"chainlab/internal/types"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmtsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
)

func TestPersistentApplicationSnapshotRoundTripAcrossChunksAndRestart(t *testing.T) {
	fixture := newSnapshotApplicationFixture(t, 320)
	source := newPersistentSnapshotApplication(t, fixture, filepath.Join(t.TempDir(), "source"))
	defer source.Close()
	commitEmptySnapshotBlock(t, source, fixture, 1)
	sourceInfo, err := source.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, chunks := exportedApplicationSnapshot(t, source)
	if snapshot.Chunks < 2 || len(chunks) != int(snapshot.Chunks) {
		t.Fatalf("snapshot chunks = %d loaded=%d", snapshot.Chunks, len(chunks))
	}

	targetDir := filepath.Join(t.TempDir(), "target")
	target, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: targetDir})
	if err != nil {
		t.Fatal(err)
	}
	offer, err := target.OfferSnapshot(context.Background(), &abcitypes.RequestOfferSnapshot{
		Snapshot: snapshot, AppHash: cloneBytes(sourceInfo.LastBlockAppHash),
	})
	if err != nil || offer.Result != abcitypes.ResponseOfferSnapshot_ACCEPT {
		t.Fatalf("offer = %+v err=%v", offer, err)
	}
	for index := len(chunks) - 1; index >= 0; index-- {
		response, err := target.ApplySnapshotChunk(context.Background(), &abcitypes.RequestApplySnapshotChunk{
			Index: uint32(index), Chunk: chunks[index], Sender: fmt.Sprintf("peer-%d", index),
		})
		if err != nil || response.Result != abcitypes.ResponseApplySnapshotChunk_ACCEPT {
			t.Fatalf("apply chunk %d = %+v err=%v", index, response, err)
		}
	}
	targetInfo, err := target.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if targetInfo.LastBlockHeight != sourceInfo.LastBlockHeight || !bytes.Equal(targetInfo.LastBlockAppHash, sourceInfo.LastBlockAppHash) {
		t.Fatalf("target info = %+v, source = %+v", targetInfo, sourceInfo)
	}
	prepared, err := target.PrepareProposal(context.Background(), &abcitypes.RequestPrepareProposal{
		Height: 2, MaxTxBytes: types.MaxProposalTxBytes, ProposerAddress: fixture.proposerAddress,
	})
	if err != nil || len(prepared.Txs) != 0 {
		t.Fatalf("restored proposer binding response=%+v err=%v", prepared, err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: targetDir})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restartedInfo, err := restarted.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if restartedInfo.LastBlockHeight != sourceInfo.LastBlockHeight || !bytes.Equal(restartedInfo.LastBlockAppHash, sourceInfo.LastBlockAppHash) {
		t.Fatalf("restarted info = %+v", restartedInfo)
	}
	relisted, err := restarted.ListSnapshots(context.Background(), &abcitypes.RequestListSnapshots{})
	if err != nil || len(relisted.Snapshots) != 1 || !bytes.Equal(relisted.Snapshots[0].Hash, snapshot.Hash) {
		t.Fatalf("relisted snapshots = %+v err=%v", relisted, err)
	}
}

func TestSnapshotRejectsUntrustedOrCorruptContentWithoutPublishing(t *testing.T) {
	fixture := newSnapshotApplicationFixture(t, 0)
	source := newPersistentSnapshotApplication(t, fixture, filepath.Join(t.TempDir(), "source"))
	defer source.Close()
	commitEmptySnapshotBlock(t, source, fixture, 1)
	sourceInfo, err := source.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, chunks := exportedApplicationSnapshot(t, source)
	if len(chunks) != 1 {
		t.Fatalf("small snapshot chunks = %d", len(chunks))
	}
	target, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: filepath.Join(t.TempDir(), "target")})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()

	badTrustedHash := cloneBytes(sourceInfo.LastBlockAppHash)
	badTrustedHash[0] ^= 0xff
	offer, err := target.OfferSnapshot(context.Background(), &abcitypes.RequestOfferSnapshot{
		Snapshot: snapshot, AppHash: badTrustedHash,
	})
	if err != nil || offer.Result != abcitypes.ResponseOfferSnapshot_REJECT {
		t.Fatalf("untrusted offer = %+v err=%v", offer, err)
	}
	offer, err = target.OfferSnapshot(context.Background(), &abcitypes.RequestOfferSnapshot{
		Snapshot: snapshot, AppHash: sourceInfo.LastBlockAppHash,
	})
	if err != nil || offer.Result != abcitypes.ResponseOfferSnapshot_ACCEPT {
		t.Fatalf("valid offer = %+v err=%v", offer, err)
	}
	corrupt := cloneBytes(chunks[0])
	corrupt[len(corrupt)-1] ^= 1
	apply, err := target.ApplySnapshotChunk(context.Background(), &abcitypes.RequestApplySnapshotChunk{
		Index: 0, Chunk: corrupt, Sender: "bad-peer",
	})
	if err != nil || apply.Result != abcitypes.ResponseApplySnapshotChunk_REJECT_SNAPSHOT || len(apply.RejectSenders) != 1 {
		t.Fatalf("corrupt apply = %+v err=%v", apply, err)
	}
	info, err := target.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil || info.LastBlockHeight != 0 {
		t.Fatalf("corrupt snapshot published info=%+v err=%v", info, err)
	}
}

func TestSnapshotRestoreFailureAbortsAndHaltsWithoutPublishing(t *testing.T) {
	fixture := newSnapshotApplicationFixture(t, 0)
	source := newPersistentSnapshotApplication(t, fixture, filepath.Join(t.TempDir(), "source"))
	defer source.Close()
	commitEmptySnapshotBlock(t, source, fixture, 1)
	sourceInfo, err := source.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, chunks := exportedApplicationSnapshot(t, source)
	persistence := &failingApplicationPersistence{failAtSave: 1}
	target, err := NewApplication(Config{Genesis: fixture.genesis, persistence: persistence})
	if err != nil {
		t.Fatal(err)
	}
	offer, err := target.OfferSnapshot(context.Background(), &abcitypes.RequestOfferSnapshot{
		Snapshot: snapshot, AppHash: sourceInfo.LastBlockAppHash,
	})
	if err != nil || offer.Result != abcitypes.ResponseOfferSnapshot_ACCEPT {
		t.Fatalf("offer = %+v err=%v", offer, err)
	}
	apply, err := target.ApplySnapshotChunk(context.Background(), &abcitypes.RequestApplySnapshotChunk{
		Index: 0, Chunk: chunks[0], Sender: "peer",
	})
	if err != nil || apply.Result != abcitypes.ResponseApplySnapshotChunk_ABORT {
		t.Fatalf("failed restore apply = %+v err=%v", apply, err)
	}
	info, err := target.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil || info.LastBlockHeight != 0 || !bytes.Contains([]byte(info.Data), []byte(`"halted":true`)) {
		t.Fatalf("failed restore info = %+v err=%v", info, err)
	}
}

func TestAdvertisedSnapshotRemainsLoadableAcrossRotation(t *testing.T) {
	fixture := newSnapshotApplicationFixture(t, 0)
	persistence := &failingApplicationPersistence{}
	application, err := NewApplication(Config{Genesis: fixture.genesis, persistence: persistence})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.InitChain(context.Background(), fixture.initRequest()); err != nil {
		t.Fatal(err)
	}
	commitEmptySnapshotBlock(t, application, fixture, 1)
	first, firstChunks := exportedApplicationSnapshot(t, application)
	for height := int64(2); height <= int64(applicationSnapshotInterval)+1; height++ {
		commitEmptySnapshotBlock(t, application, fixture, height)
	}
	listed, err := application.ListSnapshots(context.Background(), &abcitypes.RequestListSnapshots{})
	if err != nil || len(listed.Snapshots) != 1 {
		t.Fatalf("rotated list = %+v err=%v", listed, err)
	}
	if listed.Snapshots[0].Height != applicationSnapshotInterval+1 || listed.Snapshots[0].Height == first.Height {
		t.Fatalf("rotated snapshot height = %d, first=%d", listed.Snapshots[0].Height, first.Height)
	}
	oldChunk, err := application.LoadSnapshotChunk(context.Background(), &abcitypes.RequestLoadSnapshotChunk{
		Height: first.Height, Format: first.Format, Chunk: 0,
	})
	if err != nil || !bytes.Equal(oldChunk.Chunk, firstChunks[0]) {
		t.Fatalf("old advertised chunk changed: bytes=%d err=%v", len(oldChunk.Chunk), err)
	}
}

func newSnapshotApplicationFixture(t *testing.T, storageEntries int) applicationFixture {
	t.Helper()
	key, err := chaincrypto.PrivateKeyFromHex("0x" + fmt.Sprintf("%064x", 1))
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
		valueByte := byte('a' + index%26)
		if err := store.SetStorage(account, fmt.Sprintf("snapshot:%04d", index), string(bytes.Repeat([]byte{valueByte}, 4096))); err != nil {
			t.Fatal(err)
		}
	}
	genesis, err := NewGenesisDocument("chainlab-snapshot-test", types.DefaultBlockGasLimit, store)
	if err != nil {
		t.Fatal(err)
	}
	genesisBytes, err := genesis.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	compressed := key.PubKey().SerializeCompressed()
	return applicationFixture{
		key: key, account: account, genesis: genesis, genesisBytes: genesisBytes,
		validator:       abcitypes.UpdateValidator(compressed, 1, cmtsecp256k1.KeyType),
		proposerAddress: cloneBytes(cmtsecp256k1.PubKey(compressed).Address()),
		params:          consensusParams(types.DefaultBlockGasLimit),
	}
}

func newPersistentSnapshotApplication(t *testing.T, fixture applicationFixture, dataDir string) *Application {
	t.Helper()
	application, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.InitChain(context.Background(), fixture.initRequest()); err != nil {
		_ = application.Close()
		t.Fatal(err)
	}
	return application
}

func commitEmptySnapshotBlock(t *testing.T, application *Application, fixture applicationFixture, height int64) {
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

func exportedApplicationSnapshot(t *testing.T, application *Application) (*abcitypes.Snapshot, [][]byte) {
	t.Helper()
	listed, err := application.ListSnapshots(context.Background(), &abcitypes.RequestListSnapshots{})
	if err != nil || len(listed.Snapshots) != 1 {
		t.Fatalf("list snapshots = %+v err=%v", listed, err)
	}
	snapshot := listed.Snapshots[0]
	chunks := make([][]byte, snapshot.Chunks)
	for index := uint32(0); index < snapshot.Chunks; index++ {
		response, err := application.LoadSnapshotChunk(context.Background(), &abcitypes.RequestLoadSnapshotChunk{
			Height: snapshot.Height, Format: snapshot.Format, Chunk: index,
		})
		if err != nil || len(response.Chunk) == 0 || len(response.Chunk) > applicationSnapshotChunkBytes {
			t.Fatalf("load chunk %d = %d bytes err=%v", index, len(response.Chunk), err)
		}
		chunks[index] = response.Chunk
	}
	return snapshot, chunks
}
