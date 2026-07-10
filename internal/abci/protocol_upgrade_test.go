package abci

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"chainlab/internal/types"
	chainproof "chainlab/pkg/proof"

	abcitypes "github.com/cometbft/cometbft/abci/types"
)

func TestProtocolV3UpgradePublishesVersionBeforeMerkleRootActivation(t *testing.T) {
	fixture := newProtocolUpgradeFixture(t, filepath.Join(t.TempDir(), "upgrade"), AppVersionV3)
	fixture.initialize(t)
	info, err := fixture.app.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil || info.AppVersion != AppVersionV2 {
		t.Fatalf("pre-upgrade info=%+v err=%v", info, err)
	}

	first, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(1), Height: 1, Time: upgradeBlockTime(1), ProposerAddress: fixture.proposerAddresses[0],
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ConsensusParamUpdates == nil || first.ConsensusParamUpdates.Version == nil ||
		first.ConsensusParamUpdates.Version.App != AppVersionV3 {
		t.Fatalf("height-1 consensus updates = %+v", first.ConsensusParamUpdates)
	}
	if fixture.app.candidate.state.commitment.Protocol != ProtocolVersionV2 {
		t.Fatalf("height-1 protocol = %q", fixture.app.candidate.state.commitment.Protocol)
	}
	if _, err := fixture.app.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	info, err = fixture.app.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil || info.AppVersion != AppVersionV3 {
		t.Fatalf("activation-ready info=%+v err=%v", info, err)
	}

	tx := signFixtureTransaction(t, fixture.keys[0], types.Transaction{
		ChainID: fixture.genesis.ChainID, Type: types.TxTransfer,
		From: fixture.accounts[0], To: fixture.accounts[1], Nonce: 0, Value: 7,
		GasLimit: 21_000, GasPrice: 1,
	})
	second, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(2), Height: 2, Time: upgradeBlockTime(2),
		ProposerAddress: fixture.proposerAddresses[0], Txs: [][]byte{rawFixtureTransaction(t, tx)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ConsensusParamUpdates != nil {
		t.Fatalf("height-2 repeated consensus updates = %+v", second.ConsensusParamUpdates)
	}
	commitment := fixture.app.candidate.state.commitment
	if commitment.Protocol != ProtocolVersionV3 {
		t.Fatalf("height-2 protocol = %q", commitment.Protocol)
	}
	wantTxRoot, err := types.TransactionMerkleRoot([]types.Transaction{tx})
	if err != nil {
		t.Fatal(err)
	}
	wantReceiptRoot, err := types.ReceiptMerkleRoot(fixture.app.candidate.receipts)
	if err != nil {
		t.Fatal(err)
	}
	wantStateRoot, err := stateRootForProtocol(ProtocolVersionV3, fixture.app.candidate.state.store)
	if err != nil {
		t.Fatal(err)
	}
	if commitment.TxRoot != wantTxRoot || commitment.ReceiptRoot != wantReceiptRoot || commitment.StateRoot != wantStateRoot {
		t.Fatalf("v3 roots = %+v", commitment)
	}
	if commitment.TxRoot == types.TransactionRoot([]types.Transaction{tx}) ||
		commitment.ReceiptRoot == types.ReceiptRoot(fixture.app.candidate.receipts) ||
		commitment.StateRoot == fixture.app.candidate.state.store.Root() {
		t.Fatal("v3 commitment reused a legacy root")
	}
	if _, err := fixture.app.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	info, err = fixture.app.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil || !strings.Contains(info.Data, `"protocol":"chainlab-v3"`) {
		t.Fatalf("active v3 info=%+v err=%v", info, err)
	}
	assertApplicationProofQuery(t, fixture.app, "/proof/transaction", "0", commitment.TxRoot, chainproof.KindTransaction)
	assertApplicationProofQuery(t, fixture.app, "/proof/receipt", "0", commitment.ReceiptRoot, chainproof.KindReceipt)
	assertApplicationProofQuery(t, fixture.app, "/proof/account", fixture.accounts[1], commitment.StateRoot, chainproof.KindState)
	legacyProof, err := fixture.app.Query(context.Background(), &abcitypes.RequestQuery{
		Path: "/proof/account", Data: []byte(fixture.accounts[1]), Height: 1,
	})
	if err != nil || legacyProof.Code != CodeUnsupported || !strings.Contains(legacyProof.Log, "chainlab-v3") {
		t.Fatalf("legacy proof response=%+v err=%v", legacyProof, err)
	}
	oversized, err := fixture.app.Query(context.Background(), &abcitypes.RequestQuery{
		Path: "/proof/transaction", Data: make([]byte, maxQueryDataBytes+1),
	})
	if err != nil || oversized.Code != CodeInvalidRequest || !strings.Contains(oversized.Log, "limit") {
		t.Fatalf("oversized proof response=%+v err=%v", oversized, err)
	}
	if err := fixture.app.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewApplication(Config{
		Genesis: fixture.genesis, DataDir: fixture.app.persistence.(*applicationDB).path,
		MaxAppVersion: AppVersionV3,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restarted.committed.commitment.Protocol != ProtocolVersionV3 || restarted.committed.commitment.StateRoot != wantStateRoot {
		t.Fatalf("restarted v3 commitment = %+v", restarted.committed.commitment)
	}
	if _, err := restarted.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(3), Height: 3, Time: upgradeBlockTime(3), ProposerAddress: fixture.proposerAddresses[0],
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	historical, err := restarted.Query(context.Background(), &abcitypes.RequestQuery{
		Path: "/proof/transaction", Data: []byte("0"), Height: 2,
	})
	if err != nil || historical.Code != CodeOK || historical.Height != 2 {
		t.Fatalf("historical v3 proof response=%+v err=%v", historical, err)
	}
	var historicalEnvelope chainproof.Envelope
	if err := json.Unmarshal(historical.Value, &historicalEnvelope); err != nil {
		t.Fatal(err)
	}
	if _, err := chainproof.VerifyEnvelope(
		wantTxRoot, 2, chainproof.KindTransaction, "0", historicalEnvelope,
	); err != nil {
		t.Fatal(err)
	}
}

func TestApplicationClonesProtocolUpgradeConfiguration(t *testing.T) {
	fixture := newProtocolUpgradeFixture(t, "", AppVersionV3)
	fixture.genesis.Upgrades[0].Height = 100
	fixture.genesis.ValidatorPolicy.EpochLength = 200
	if got := fixture.app.genesis.Upgrades[0].Height; got != 2 {
		t.Fatalf("application upgrade height mutated to %d", got)
	}
	if got := fixture.app.genesis.ValidatorPolicy.EpochLength; got != 4 {
		t.Fatalf("application validator policy mutated to %d", got)
	}
}

func TestProtocolVersionReportingSaturatesAtMaximumHeight(t *testing.T) {
	fixture := newProtocolUpgradeFixture(t, "", AppVersionV3)
	fixture.app.committed.commitment.Height = math.MaxInt64
	fixture.app.committed.commitment.Protocol = ProtocolVersionV3
	info, err := fixture.app.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil || info.AppVersion != AppVersionV3 {
		t.Fatalf("maximum-height info=%+v err=%v", info, err)
	}
}

func TestProtocolV3SnapshotRestorePreservesProofRoots(t *testing.T) {
	source := newProtocolUpgradeFixture(t, filepath.Join(t.TempDir(), "source"), AppVersionV3)
	source.initialize(t)
	for height := int64(1); height <= 2; height++ {
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
	sourceInfo, err := source.app.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, chunks := exportedApplicationSnapshot(t, source.app)
	target, err := NewApplication(Config{
		Genesis: source.genesis, DataDir: filepath.Join(t.TempDir(), "target"), MaxAppVersion: AppVersionV3,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	offer, err := target.OfferSnapshot(context.Background(), &abcitypes.RequestOfferSnapshot{
		Snapshot: snapshot, AppHash: sourceInfo.LastBlockAppHash,
	})
	if err != nil || offer.Result != abcitypes.ResponseOfferSnapshot_ACCEPT {
		t.Fatalf("v3 snapshot offer=%+v err=%v", offer, err)
	}
	for index, chunk := range chunks {
		applied, err := target.ApplySnapshotChunk(context.Background(), &abcitypes.RequestApplySnapshotChunk{
			Index: uint32(index), Chunk: chunk, Sender: "v3-source",
		})
		if err != nil || applied.Result != abcitypes.ResponseApplySnapshotChunk_ACCEPT {
			t.Fatalf("v3 snapshot chunk %d response=%+v err=%v", index, applied, err)
		}
	}
	targetInfo, err := target.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil || targetInfo.AppVersion != AppVersionV3 || targetInfo.LastBlockHeight != 2 {
		t.Fatalf("v3 restored info=%+v err=%v", targetInfo, err)
	}
	assertApplicationProofQuery(
		t, target, "/proof/account", source.accounts[0], target.committed.commitment.StateRoot, chainproof.KindState,
	)
}

func TestProtocolUpgradeRejectsIncompatibleApplicationBinary(t *testing.T) {
	fixture := newProtocolUpgradeFixture(t, "", AppVersionV2)
	fixture.initialize(t)
	if _, err := fixture.app.PrepareProposal(context.Background(), &abcitypes.RequestPrepareProposal{
		Height: 1, Time: upgradeBlockTime(1), MaxTxBytes: types.MaxProposalTxBytes, ProposerAddress: fixture.proposerAddresses[0],
	}); err == nil || !strings.Contains(err.Error(), "requires version 3") {
		t.Fatalf("incompatible prepare error = %v", err)
	}
	processed, err := fixture.app.ProcessProposal(context.Background(), &abcitypes.RequestProcessProposal{
		Hash: blockHash(1), Height: 1, Time: upgradeBlockTime(1), ProposerAddress: fixture.proposerAddresses[0],
	})
	if err != nil || processed.Status != abcitypes.ResponseProcessProposal_REJECT {
		t.Fatalf("incompatible process response=%+v err=%v", processed, err)
	}
	if _, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(1), Height: 1, Time: upgradeBlockTime(1), ProposerAddress: fixture.proposerAddresses[0],
	}); err == nil || !strings.Contains(err.Error(), "requires version 3") {
		t.Fatalf("incompatible finalize error = %v", err)
	}
	if fixture.app.candidate != nil || fixture.app.committed.commitment.Height != 0 {
		t.Fatal("incompatible application published upgrade state")
	}

	dataDir := filepath.Join(t.TempDir(), "restart")
	compatible := newProtocolUpgradeFixture(t, dataDir, AppVersionV3)
	compatible.initialize(t)
	if _, err := compatible.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(1), Height: 1, Time: upgradeBlockTime(1), ProposerAddress: compatible.proposerAddresses[0],
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := compatible.app.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	if err := compatible.app.Close(); err != nil {
		t.Fatal(err)
	}
	differentSchedule := compatible.genesis
	differentSchedule.Upgrades = append([]ProtocolUpgrade(nil), compatible.genesis.Upgrades...)
	differentSchedule.Upgrades[0].Height++
	if _, err := NewApplication(Config{
		Genesis: differentSchedule, DataDir: dataDir, MaxAppVersion: AppVersionV3,
	}); err == nil || !strings.Contains(err.Error(), "genesis identity") {
		t.Fatalf("modified schedule restart error = %v", err)
	}
	if _, err := NewApplication(Config{
		Genesis: compatible.genesis, DataDir: dataDir, MaxAppVersion: AppVersionV2,
	}); err == nil || !strings.Contains(err.Error(), "next height requires version 3") {
		t.Fatalf("incompatible restart error = %v", err)
	}
}

func TestProtocolUpgradeScheduleValidation(t *testing.T) {
	fixture := newValidatorV2Fixture(t, "")
	tests := []struct {
		name     string
		protocol string
		upgrades []ProtocolUpgrade
	}{
		{name: "height one", protocol: ProtocolVersionV2, upgrades: []ProtocolUpgrade{{Height: 1, Protocol: ProtocolVersionV3}}},
		{name: "same version", protocol: ProtocolVersionV2, upgrades: []ProtocolUpgrade{{Height: 2, Protocol: ProtocolVersionV2}}},
		{name: "multiple", protocol: ProtocolVersionV2, upgrades: []ProtocolUpgrade{{Height: 2, Protocol: ProtocolVersionV3}, {Height: 3, Protocol: ProtocolVersionV3}}},
		{name: "v1 source", protocol: ProtocolVersion, upgrades: []ProtocolUpgrade{{Height: 2, Protocol: ProtocolVersionV3}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			genesis := fixture.genesis
			genesis.Protocol = test.protocol
			genesis.Upgrades = test.upgrades
			if _, err := genesis.CanonicalBytes(); err == nil {
				t.Fatal("invalid upgrade schedule was canonicalized")
			}
		})
	}
}

func newProtocolUpgradeFixture(t *testing.T, dataDir string, maxAppVersion uint64) validatorV2Fixture {
	t.Helper()
	fixture := newValidatorV2Fixture(t, "")
	if err := fixture.app.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.genesis.Upgrades = []ProtocolUpgrade{{Height: 2, Protocol: ProtocolVersionV3}}
	genesisBytes, err := fixture.genesis.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewApplication(Config{
		Genesis: fixture.genesis, DataDir: dataDir, MaxAppVersion: maxAppVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = application
	fixture.genesisBytes = genesisBytes
	t.Cleanup(func() { _ = application.Close() })
	return fixture
}

func upgradeBlockTime(height int64) time.Time {
	return time.Unix(1_800_000_000+height, 0).UTC()
}

func assertApplicationProofQuery(
	t *testing.T,
	application *Application,
	path string,
	data string,
	trustedRoot string,
	kind string,
) {
	t.Helper()
	response, err := application.Query(context.Background(), &abcitypes.RequestQuery{
		Path: path, Data: []byte(data),
	})
	if err != nil || response.Code != CodeOK {
		t.Fatalf("proof query %s response=%+v err=%v", path, response, err)
	}
	var envelope chainproof.Envelope
	if err := json.Unmarshal(response.Value, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Kind != kind || envelope.Root != trustedRoot || envelope.Height != response.Height {
		t.Fatalf("proof envelope = %+v", envelope)
	}
	expectedKey := data
	if kind == chainproof.KindState {
		expectedKey = flatStateID(flatKindAccount, []byte(data))
	}
	if _, err := chainproof.VerifyEnvelope(
		trustedRoot, response.Height, kind, expectedKey, envelope,
	); err != nil {
		t.Fatalf("verify proof query %s: %v", path, err)
	}
}
