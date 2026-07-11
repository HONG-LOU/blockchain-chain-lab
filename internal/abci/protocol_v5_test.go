package abci

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"chainlab/internal/state"

	abcitypes "github.com/cometbft/cometbft/abci/types"
)

func TestProtocolV5RecordsAndCompactsOnlyTimestampedOffences(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "v5")
	fixture := newProtocolV5Fixture(t, dataDir, AppVersionV5)
	fixture.initialize(t)

	first := fixture.finalizeAndCommit(t, 1, 0, nil)
	assertConsensusAppVersion(t, first, AppVersionV3)
	legacyEvidence := protocolV5Evidence(fixture, 1)
	second := fixture.finalizeAndCommit(t, 2, 0, legacyEvidence)
	assertConsensusAppVersion(t, second, AppVersionV4)
	legacyKey := state.ValidatorOffenceKey(fixture.accounts[1], 1)
	legacy, exists := fixture.app.committed.store.ValidatorOffence(legacyKey)
	if !exists || legacy.EvidenceTimePresent || legacy.EvidenceTimeUnix != 0 || legacy.EvidenceTimeNanosecond != 0 {
		t.Fatalf("legacy offence = %+v exists=%t", legacy, exists)
	}

	third := fixture.finalizeAndCommit(t, 3, 0, nil)
	assertConsensusAppVersion(t, third, AppVersionV5)
	if fixture.app.committed.commitment.Protocol != ProtocolVersionV4 {
		t.Fatalf("height-3 protocol = %q", fixture.app.committed.commitment.Protocol)
	}
	info, err := fixture.app.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil || info.AppVersion != AppVersionV5 {
		t.Fatalf("activation-ready info=%+v err=%v", info, err)
	}

	v5Evidence := protocolV5Evidence(fixture, 3)
	fourth := fixture.finalizeAndCommit(t, 4, 0, v5Evidence)
	if fourth.ConsensusParamUpdates != nil {
		t.Fatalf("height-4 repeated consensus update = %+v", fourth.ConsensusParamUpdates)
	}
	if fixture.app.committed.commitment.Protocol != ProtocolVersionV5 {
		t.Fatalf("height-4 protocol = %q", fixture.app.committed.commitment.Protocol)
	}
	v5Key := state.ValidatorOffenceKey(fixture.accounts[1], 3)
	v5Offence, exists := fixture.app.committed.store.ValidatorOffence(v5Key)
	if !exists || !v5Offence.EvidenceTimePresent ||
		v5Offence.EvidenceTimeUnix != validatorV2BlockTime(3).Unix() ||
		v5Offence.EvidenceTimeNanosecond != int32(validatorV2BlockTime(3).Nanosecond()) {
		t.Fatalf("v5 offence = %+v exists=%t", v5Offence, exists)
	}
	v5ProofKey := flatStateID(flatKindValidatorOffence, []byte(v5Key))
	assertSparseProofQuery(t, fixture.app, "/proof/state", v5ProofKey, true)
	rootBeforeCompaction := fixture.app.committed.commitment.StateRoot
	for _, test := range []struct {
		name      string
		height    int64
		blockTime time.Time
	}{
		{name: "block age only", height: 5, blockTime: validatorV2BlockTime(4)},
		{name: "duration age only", height: 4, blockTime: validatorV2BlockTime(6)},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := fixture.app.committed.store.Clone()
			pruned, err := compactValidatorOffencesV5(
				store, test.height, test.blockTime, *fixture.genesis.ValidatorPolicy,
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(pruned) != 0 {
				t.Fatalf("single-dimension expiry pruned offences = %+v", pruned)
			}
			if _, exists := store.ValidatorOffence(v5Key); !exists {
				t.Fatal("single-dimension expiry removed the v5 offence")
			}
		})
	}

	expired, err := fixture.app.ProcessProposal(context.Background(), &abcitypes.RequestProcessProposal{
		Hash: blockHash(5), Height: 5, Time: validatorV2BlockTime(5),
		ProposerAddress: fixture.proposerAddresses[0], Misbehavior: v5Evidence,
	})
	if err != nil || expired.Status != abcitypes.ResponseProcessProposal_REJECT {
		t.Fatalf("expired v5 evidence response=%+v err=%v", expired, err)
	}
	fifth := fixture.finalizeAndCommit(t, 5, 0, nil)
	if !hasABCIAttribute(fifth.Events, "chainlab.validator_offence_pruned", "height", "3") {
		t.Fatalf("height-5 prune events = %+v", fifth.Events)
	}
	if fixture.app.committed.commitment.StateRoot == rootBeforeCompaction {
		t.Fatal("v5 compaction did not change the sparse state root")
	}
	lifecycle, exists := fixture.app.committed.store.ValidatorLifecycle()
	if !exists || len(lifecycle.Offences) != 1 {
		t.Fatalf("post-compaction lifecycle = %+v exists=%t", lifecycle, exists)
	}
	if _, exists := lifecycle.Offences[legacyKey]; !exists {
		t.Fatal("legacy offence without an evidence timestamp was compacted")
	}
	if _, exists := lifecycle.Offences[v5Key]; exists {
		t.Fatal("expired v5 offence survived compaction")
	}
	assertSparseProofQuery(t, fixture.app, "/proof/state", v5ProofKey, false)

	if err := fixture.app.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := NewApplication(Config{
		Genesis: fixture.genesis, DataDir: dataDir, MaxAppVersion: AppVersionV5,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restoredLifecycle, exists := restored.committed.store.ValidatorLifecycle()
	if !exists || len(restoredLifecycle.Offences) != 1 {
		t.Fatalf("restored lifecycle = %+v exists=%t", restoredLifecycle, exists)
	}
	if _, exists := restoredLifecycle.Offences[legacyKey]; !exists {
		t.Fatal("legacy offence did not survive restart")
	}
}

func TestProtocolV5SnapshotRestorePreservesTimestampedOffence(t *testing.T) {
	source := newProtocolV5Fixture(t, filepath.Join(t.TempDir(), "source"), AppVersionV5)
	source.initialize(t)
	source.finalizeAndCommit(t, 1, 0, nil)
	source.finalizeAndCommit(t, 2, 0, protocolV5Evidence(source, 1))
	source.finalizeAndCommit(t, 3, 0, nil)
	source.finalizeAndCommit(t, 4, 0, protocolV5Evidence(source, 3))
	info, err := source.app.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, chunks := exportedApplicationSnapshot(t, source.app)
	target, err := NewApplication(Config{
		Genesis: source.genesis, DataDir: filepath.Join(t.TempDir(), "target"), MaxAppVersion: AppVersionV5,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	offer, err := target.OfferSnapshot(context.Background(), &abcitypes.RequestOfferSnapshot{
		Snapshot: snapshot, AppHash: info.LastBlockAppHash,
	})
	if err != nil || offer.Result != abcitypes.ResponseOfferSnapshot_ACCEPT {
		t.Fatalf("v5 snapshot offer=%+v err=%v", offer, err)
	}
	for index, chunk := range chunks {
		applied, err := target.ApplySnapshotChunk(context.Background(), &abcitypes.RequestApplySnapshotChunk{
			Index: uint32(index), Chunk: chunk, Sender: "v5-source",
		})
		if err != nil || applied.Result != abcitypes.ResponseApplySnapshotChunk_ACCEPT {
			t.Fatalf("v5 snapshot chunk %d response=%+v err=%v", index, applied, err)
		}
	}
	key := state.ValidatorOffenceKey(source.accounts[1], 3)
	offence, exists := target.committed.store.ValidatorOffence(key)
	if !exists || !offence.EvidenceTimePresent ||
		target.committed.commitment.StateRoot != source.app.committed.commitment.StateRoot ||
		target.committed.flatTree.Root() != source.app.committed.flatTree.Root() {
		t.Fatalf("restored v5 offence=%+v exists=%t", offence, exists)
	}
	response, err := target.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(5), Height: 5, Time: validatorV2BlockTime(5),
		ProposerAddress: source.proposerAddresses[0],
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hasABCIAttribute(response.Events, "chainlab.validator_offence_pruned", "height", "3") {
		t.Fatalf("restored v5 prune events = %+v", response.Events)
	}
}

func TestProtocolV5OldBinaryRejectsBeforeActivation(t *testing.T) {
	fixture := newProtocolV5Fixture(t, "", AppVersionV4)
	fixture.initialize(t)
	fixture.finalizeAndCommit(t, 1, 0, nil)
	fixture.finalizeAndCommit(t, 2, 0, nil)
	_, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(3), Height: 3, Time: validatorV2BlockTime(3),
		ProposerAddress: fixture.proposerAddresses[0],
	})
	if err == nil || !strings.Contains(err.Error(), "requires version 5") {
		t.Fatalf("old binary finalize error = %v", err)
	}
	if fixture.app.candidate != nil || fixture.app.committed.commitment.Height != 2 {
		t.Fatal("old binary published a pre-v5 candidate")
	}
}

func TestProtocolV5ScheduleValidation(t *testing.T) {
	fixture := newValidatorV2Fixture(t, "")
	valid := fixture.genesis
	valid.Upgrades = []ProtocolUpgrade{
		{Height: 2, Protocol: ProtocolVersionV3},
		{Height: 3, Protocol: ProtocolVersionV4},
		{Height: 4, Protocol: ProtocolVersionV5},
	}
	if _, err := valid.CanonicalBytes(); err != nil {
		t.Fatalf("valid v5 schedule: %v", err)
	}
	invalid := [][]ProtocolUpgrade{
		{{Height: 2, Protocol: ProtocolVersionV5}},
		{{Height: 2, Protocol: ProtocolVersionV3}, {Height: 3, Protocol: ProtocolVersionV5}},
		{
			{Height: 2, Protocol: ProtocolVersionV3},
			{Height: 4, Protocol: ProtocolVersionV4},
			{Height: 3, Protocol: ProtocolVersionV5},
		},
	}
	for index, upgrades := range invalid {
		genesis := fixture.genesis
		genesis.Upgrades = upgrades
		if _, err := genesis.CanonicalBytes(); err == nil {
			t.Fatalf("invalid v5 schedule %d was canonicalized", index)
		}
	}
}

func TestValidatorOffenceRejectsUnmarkedEvidenceTime(t *testing.T) {
	fixture := newProtocolV5Fixture(t, "", AppVersionV5)
	fixture.initialize(t)
	fixture.finalizeAndCommit(t, 1, 0, nil)
	fixture.finalizeAndCommit(t, 2, 0, fixture.duplicateVoteEvidence())
	lifecycle, exists := fixture.app.committed.store.ValidatorLifecycle()
	if !exists {
		t.Fatal("validator lifecycle is missing")
	}
	key := state.ValidatorOffenceKey(fixture.accounts[1], 1)
	offence := lifecycle.Offences[key]
	offence.EvidenceTimeUnix = 1
	lifecycle.Offences[key] = offence
	if err := fixture.app.committed.store.SetValidatorLifecycle(lifecycle); err == nil ||
		!strings.Contains(err.Error(), "unmarked evidence time") {
		t.Fatalf("unmarked evidence time error = %v", err)
	}
}

func newProtocolV5Fixture(t *testing.T, dataDir string, maxAppVersion uint64) validatorV2Fixture {
	t.Helper()
	fixture := newValidatorV2Fixture(t, "")
	if err := fixture.app.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.genesis.ValidatorPolicy.EpochLength = 100
	fixture.genesis.ValidatorPolicy.EvidenceMaxAgeNumBlocks = 1
	fixture.genesis.ValidatorPolicy.EvidenceMaxAgeDurationNanos = int64(time.Second)
	fixture.genesis.Upgrades = []ProtocolUpgrade{
		{Height: 2, Protocol: ProtocolVersionV3},
		{Height: 3, Protocol: ProtocolVersionV4},
		{Height: 4, Protocol: ProtocolVersionV5},
	}
	genesisBytes, err := fixture.genesis.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	fixture.params.Evidence.MaxAgeNumBlocks = 1
	fixture.params.Evidence.MaxAgeDuration = time.Second
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

func protocolV5Evidence(fixture validatorV2Fixture, height int64) []abcitypes.Misbehavior {
	return []abcitypes.Misbehavior{{
		Type: abcitypes.MisbehaviorType_DUPLICATE_VOTE,
		Validator: abcitypes.Validator{
			Address: fixture.proposerAddresses[1], Power: 1,
		},
		Height: height, Time: validatorV2BlockTime(height), TotalVotingPower: 2,
	}}
}
