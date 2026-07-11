package abci

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"chainlab/internal/state"
	"chainlab/internal/types"
	chainproof "chainlab/pkg/proof"

	abcitypes "github.com/cometbft/cometbft/abci/types"
)

func TestProtocolV4UpgradeUsesIncrementalSparseStateRootsAndProofs(t *testing.T) {
	fixture := newProtocolV4Fixture(t, filepath.Join(t.TempDir(), "upgrade"), AppVersionV4)
	fixture.initialize(t)

	for height := int64(1); height <= 3; height++ {
		response, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
			Hash: blockHash(byte(height)), Height: height, Time: upgradeBlockTime(height),
			ProposerAddress: fixture.proposerAddresses[0],
		})
		if err != nil {
			t.Fatal(err)
		}
		if height == 1 {
			assertConsensusAppVersion(t, response, AppVersionV3)
		}
		if height == 3 {
			assertConsensusAppVersion(t, response, AppVersionV4)
		}
		if _, err := fixture.app.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
			t.Fatal(err)
		}
	}

	tx := signFixtureTransaction(t, fixture.keys[0], types.Transaction{
		ChainID: fixture.genesis.ChainID, Type: types.TxTransfer,
		From: fixture.accounts[0], To: fixture.accounts[1], Nonce: 0, Value: 11,
		GasLimit: 21_000, GasPrice: 1,
	})
	response, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(4), Height: 4, Time: upgradeBlockTime(4),
		ProposerAddress: fixture.proposerAddresses[0], Txs: [][]byte{rawFixtureTransaction(t, tx)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.ConsensusParamUpdates != nil {
		t.Fatalf("height-4 repeated consensus update = %+v", response.ConsensusParamUpdates)
	}
	commitment := fixture.app.candidate.state.commitment
	if commitment.Protocol != ProtocolVersionV4 || commitment.StateRoot != fixture.app.candidate.state.flatTree.Root() {
		t.Fatalf("v4 commitment = %+v", commitment)
	}
	rebuilt, err := buildStoreSparseTree(fixture.app.candidate.state.store)
	if err != nil {
		t.Fatal(err)
	}
	if commitment.StateRoot != rebuilt.Root() {
		t.Fatalf("incremental root=%s rebuilt=%s", commitment.StateRoot, rebuilt.Root())
	}
	v3StateRoot, err := stateMerkleRootForTest(fixture.app.candidate.state.store)
	if err != nil {
		t.Fatal(err)
	}
	if commitment.StateRoot == v3StateRoot {
		t.Fatal("v4 reused the v3 exact-total state root")
	}
	if _, err := fixture.app.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}

	assertSparseProofQuery(t, fixture.app, "/proof/account", fixture.accounts[1], true)
	missing := "0x1111111111111111111111111111111111111111"
	assertSparseProofQuery(t, fixture.app, "/proof/account", missing, false)
	stakeKey := flatStateID(flatKindStake, []byte(fixture.accounts[0]))
	assertSparseProofQuery(t, fixture.app, "/proof/state", stakeKey, true)
	missingCodeKey := flatStateID(flatKindCode, []byte("missing-code"))
	assertSparseProofQuery(t, fixture.app, "/proof/state", missingCodeKey, false)

	legacy, err := fixture.app.Query(context.Background(), &abcitypes.RequestQuery{
		Path: "/proof/account", Data: []byte(fixture.accounts[0]), Height: 3,
	})
	if err != nil || legacy.Code != CodeOK {
		t.Fatalf("historical v3 proof=%+v err=%v", legacy, err)
	}
	var legacyEnvelope chainproof.Envelope
	if err := json.Unmarshal(legacy.Value, &legacyEnvelope); err != nil {
		t.Fatal(err)
	}
	if legacyEnvelope.Protocol != chainproof.EnvelopeProtocol {
		t.Fatalf("historical envelope protocol = %q", legacyEnvelope.Protocol)
	}

	database := fixture.app.persistence.(*applicationDB)
	v3Artifacts, err := database.loadVersionArtifactsV2(3)
	if err != nil {
		t.Fatal(err)
	}
	v4Artifacts, err := database.loadVersionArtifactsV2(4)
	if err != nil {
		t.Fatal(err)
	}
	if v3Artifacts.manifest.StateFlatRootProtocol != flatRootProtocolV1 ||
		v4Artifacts.manifest.StateFlatRootProtocol != sparseFlatRootProtocolV1 {
		t.Fatalf(
			"manifest root protocols v3=%q v4=%q",
			v3Artifacts.manifest.StateFlatRootProtocol, v4Artifacts.manifest.StateFlatRootProtocol,
		)
	}

	dataDir := database.path
	if err := fixture.app.Close(); err != nil {
		t.Fatal(err)
	}
	differentSchedule := fixture.genesis
	differentSchedule.Upgrades = append([]ProtocolUpgrade(nil), fixture.genesis.Upgrades...)
	differentSchedule.Upgrades[1].Height++
	if _, err := NewApplication(Config{
		Genesis: differentSchedule, DataDir: dataDir, MaxAppVersion: AppVersionV4,
	}); err == nil || !strings.Contains(err.Error(), "genesis identity") {
		t.Fatalf("modified v4 schedule restart error = %v", err)
	}
	restarted, err := NewApplication(Config{
		Genesis: fixture.genesis, DataDir: dataDir, MaxAppVersion: AppVersionV4,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restarted.committed.flatTree.Root() != commitment.StateRoot {
		t.Fatalf("restarted sparse root=%s want=%s", restarted.committed.flatTree.Root(), commitment.StateRoot)
	}
	assertSparseProofQuery(t, restarted, "/proof/account", missing, false)
}

func TestProtocolV4SnapshotRestoreRebuildsSparseAccumulator(t *testing.T) {
	source := newProtocolV4Fixture(t, filepath.Join(t.TempDir(), "source"), AppVersionV4)
	source.initialize(t)
	for height := int64(1); height <= 4; height++ {
		if _, err := source.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
			Hash: blockHash(byte(height)), Height: height, Time: upgradeBlockTime(height),
			ProposerAddress: source.proposerAddresses[0],
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := source.app.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
			t.Fatal(err)
		}
	}
	info, err := source.app.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, chunks := exportedApplicationSnapshot(t, source.app)
	target, err := NewApplication(Config{
		Genesis: source.genesis, DataDir: filepath.Join(t.TempDir(), "target"), MaxAppVersion: AppVersionV4,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	offer, err := target.OfferSnapshot(context.Background(), &abcitypes.RequestOfferSnapshot{
		Snapshot: snapshot, AppHash: info.LastBlockAppHash,
	})
	if err != nil || offer.Result != abcitypes.ResponseOfferSnapshot_ACCEPT {
		t.Fatalf("v4 snapshot offer=%+v err=%v", offer, err)
	}
	for index, chunk := range chunks {
		applied, err := target.ApplySnapshotChunk(context.Background(), &abcitypes.RequestApplySnapshotChunk{
			Index: uint32(index), Chunk: chunk, Sender: "v4-source",
		})
		if err != nil || applied.Result != abcitypes.ResponseApplySnapshotChunk_ACCEPT {
			t.Fatalf("v4 snapshot chunk %d response=%+v err=%v", index, applied, err)
		}
	}
	if target.committed.commitment.StateRoot != source.app.committed.commitment.StateRoot ||
		target.committed.flatTree.Root() != source.app.committed.flatTree.Root() {
		t.Fatalf(
			"restored root=%s tree=%s want=%s",
			target.committed.commitment.StateRoot, target.committed.flatTree.Root(),
			source.app.committed.commitment.StateRoot,
		)
	}
	assertSparseProofQuery(
		t, target, "/proof/account", "0x1111111111111111111111111111111111111111", false,
	)
}

func TestProtocolV4ArchiveCheckpointsVersionFlatRoots(t *testing.T) {
	fixture := newProtocolV4FixtureWithStorage(
		t, filepath.Join(t.TempDir(), "archive"), AppVersionV4, ArchiveStorageProfile(2),
	)
	fixture.initialize(t)
	for height := int64(1); height <= 5; height++ {
		if _, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
			Hash: blockHash(byte(height)), Height: height, Time: upgradeBlockTime(height),
			ProposerAddress: fixture.proposerAddresses[0],
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.app.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
			t.Fatal(err)
		}
	}
	database := fixture.app.persistence.(*applicationDB)
	for _, test := range []struct {
		height       int64
		rootProtocol string
		protocol     string
	}{
		{height: 2, rootProtocol: flatRootProtocolV1, protocol: ProtocolVersionV3},
		{height: 4, rootProtocol: sparseFlatRootProtocolV1, protocol: ProtocolVersionV4},
	} {
		artifacts, err := database.loadVersionArtifactsV2(test.height)
		if err != nil {
			t.Fatal(err)
		}
		if !artifacts.manifest.Checkpoint ||
			artifacts.manifest.StateFlatRootProtocol != test.rootProtocol ||
			artifacts.manifest.Commitment.Protocol != test.protocol ||
			artifacts.manifest.CheckpointRoot != artifacts.manifest.StateFlatRoot {
			t.Fatalf("checkpoint %d manifest=%+v", test.height, artifacts.manifest)
		}
		loaded, err := database.LoadHeight(test.height)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.committed.flatTree == nil {
			t.Fatalf("checkpoint %d did not rebuild sparse accumulator", test.height)
		}
	}
	v3Proof, err := fixture.app.Query(context.Background(), &abcitypes.RequestQuery{
		Path: "/proof/account", Data: []byte(fixture.accounts[0]), Height: 2,
	})
	if err != nil || v3Proof.Code != CodeOK {
		t.Fatalf("archive v3 proof=%+v err=%v", v3Proof, err)
	}
	var v3Envelope chainproof.Envelope
	if err := json.Unmarshal(v3Proof.Value, &v3Envelope); err != nil {
		t.Fatal(err)
	}
	if _, err := chainproof.VerifyEnvelope(
		v3Envelope.Root, 2, chainproof.KindState,
		flatStateID(flatKindAccount, []byte(fixture.accounts[0])), v3Envelope,
	); err != nil {
		t.Fatal(err)
	}
	v4Proof, err := fixture.app.Query(context.Background(), &abcitypes.RequestQuery{
		Path: "/proof/account", Data: []byte(fixture.accounts[0]), Height: 4,
	})
	if err != nil || v4Proof.Code != CodeOK {
		t.Fatalf("archive v4 proof=%+v err=%v", v4Proof, err)
	}
	var v4Envelope chainproof.SparseEnvelope
	if err := json.Unmarshal(v4Proof.Value, &v4Envelope); err != nil {
		t.Fatal(err)
	}
	if _, exists, err := chainproof.VerifySparseEnvelope(
		v4Envelope.Root, 4, chainproof.KindState, v4Envelope.Key, v4Envelope,
	); err != nil || !exists {
		t.Fatalf("archive v4 proof exists=%t err=%v", exists, err)
	}
}

func TestProtocolV4UpgradeRejectsOldBinaryBeforeActivation(t *testing.T) {
	fixture := newProtocolV4Fixture(t, "", AppVersionV3)
	fixture.initialize(t)
	for height := int64(1); height <= 2; height++ {
		if _, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
			Hash: blockHash(byte(height)), Height: height, Time: upgradeBlockTime(height),
			ProposerAddress: fixture.proposerAddresses[0],
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.app.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(3), Height: 3, Time: upgradeBlockTime(3),
		ProposerAddress: fixture.proposerAddresses[0],
	}); err == nil || !strings.Contains(err.Error(), "requires version 4") {
		t.Fatalf("old binary finalize error = %v", err)
	}
	if fixture.app.candidate != nil || fixture.app.committed.commitment.Height != 2 {
		t.Fatal("old binary published pre-v4 candidate state")
	}
}

func TestProtocolV4ScheduleValidation(t *testing.T) {
	fixture := newValidatorV2Fixture(t, "")
	valid := fixture.genesis
	valid.Upgrades = []ProtocolUpgrade{
		{Height: 2, Protocol: ProtocolVersionV3},
		{Height: 4, Protocol: ProtocolVersionV4},
	}
	if _, err := valid.CanonicalBytes(); err != nil {
		t.Fatalf("valid v4 schedule: %v", err)
	}
	invalid := []struct {
		name     string
		upgrades []ProtocolUpgrade
	}{
		{name: "v4 without v3", upgrades: []ProtocolUpgrade{{Height: 2, Protocol: ProtocolVersionV4}}},
		{name: "same height", upgrades: []ProtocolUpgrade{
			{Height: 2, Protocol: ProtocolVersionV3}, {Height: 2, Protocol: ProtocolVersionV4},
		}},
		{name: "reversed", upgrades: []ProtocolUpgrade{
			{Height: 4, Protocol: ProtocolVersionV3}, {Height: 3, Protocol: ProtocolVersionV4},
		}},
		{name: "third", upgrades: []ProtocolUpgrade{
			{Height: 2, Protocol: ProtocolVersionV3}, {Height: 3, Protocol: ProtocolVersionV4},
			{Height: 4, Protocol: "chainlab-v5"},
		}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			genesis := fixture.genesis
			genesis.Upgrades = test.upgrades
			if _, err := genesis.CanonicalBytes(); err == nil {
				t.Fatal("invalid v4 schedule was canonicalized")
			}
		})
	}
}

func newProtocolV4Fixture(t *testing.T, dataDir string, maxAppVersion uint64) validatorV2Fixture {
	return newProtocolV4FixtureWithStorage(t, dataDir, maxAppVersion, StorageProfile{})
}

func newProtocolV4FixtureWithStorage(
	t *testing.T,
	dataDir string,
	maxAppVersion uint64,
	storage StorageProfile,
) validatorV2Fixture {
	t.Helper()
	fixture := newValidatorV2Fixture(t, "")
	if err := fixture.app.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.genesis.Upgrades = []ProtocolUpgrade{
		{Height: 2, Protocol: ProtocolVersionV3},
		{Height: 4, Protocol: ProtocolVersionV4},
	}
	genesisBytes, err := fixture.genesis.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewApplication(Config{
		Genesis: fixture.genesis, DataDir: dataDir, MaxAppVersion: maxAppVersion, Storage: storage,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = application
	fixture.genesisBytes = genesisBytes
	t.Cleanup(func() { _ = application.Close() })
	return fixture
}

func assertConsensusAppVersion(t *testing.T, response *abcitypes.ResponseFinalizeBlock, want uint64) {
	t.Helper()
	if response.ConsensusParamUpdates == nil || response.ConsensusParamUpdates.Version == nil ||
		response.ConsensusParamUpdates.Version.App != want {
		t.Fatalf("consensus app version update=%+v want=%d", response.ConsensusParamUpdates, want)
	}
}

func assertSparseProofQuery(
	t *testing.T,
	application *Application,
	path string,
	data string,
	wantExists bool,
) {
	t.Helper()
	response, err := application.Query(context.Background(), &abcitypes.RequestQuery{
		Path: path, Data: []byte(data),
	})
	if err != nil || response.Code != CodeOK {
		t.Fatalf("sparse proof query %s response=%+v err=%v", path, response, err)
	}
	var envelope chainproof.SparseEnvelope
	if err := json.Unmarshal(response.Value, &envelope); err != nil {
		t.Fatal(err)
	}
	expectedKey := data
	if path == "/proof/account" {
		expectedKey = flatStateID(flatKindAccount, []byte(data))
	}
	_, exists, err := chainproof.VerifySparseEnvelope(
		envelope.Root, response.Height, chainproof.KindState, expectedKey, envelope,
	)
	if err != nil || exists != wantExists {
		t.Fatalf("verify sparse proof exists=%t want=%t err=%v", exists, wantExists, err)
	}
}

func stateMerkleRootForTest(store *state.Store) (string, error) {
	_, leaves, err := stateMerkleLeaves(store)
	if err != nil {
		return "", err
	}
	return chainproof.Root(chainproof.DomainState, leaves)
}
