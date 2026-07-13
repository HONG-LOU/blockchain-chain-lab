package abci

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"
	"chainlab/internal/types"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmtsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

type validatorV2Fixture struct {
	app               *Application
	genesis           GenesisDocument
	genesisBytes      []byte
	keys              []chaincrypto.PrivateKey
	accounts          []string
	proposerAddresses [][]byte
	validatorUpdates  []abcitypes.ValidatorUpdate
	params            *cmtproto.ConsensusParams
}

func newValidatorV2Fixture(t *testing.T, dataDir string) validatorV2Fixture {
	t.Helper()
	fixture := validatorV2Fixture{
		keys:              make([]chaincrypto.PrivateKey, 2),
		accounts:          make([]string, 2),
		proposerAddresses: make([][]byte, 2),
		validatorUpdates:  make([]abcitypes.ValidatorUpdate, 2),
	}
	store := state.NewStore()
	for index := range fixture.keys {
		key, err := chaincrypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		account := chaincrypto.AddressFromPrivateKey(key)
		compressed := key.PubKey().SerializeCompressed()
		fixture.keys[index] = key
		fixture.accounts[index] = account
		fixture.proposerAddresses[index] = cloneBytes(cmtsecp256k1.PubKey(compressed).Address())
		fixture.validatorUpdates[index] = abcitypes.UpdateValidator(compressed, 1, cmtsecp256k1.KeyType)
		store.SetBalance(account, 1_000_000)
		if err := store.AddStake(account, 1_001); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetValidators(fixture.accounts); err != nil {
		t.Fatal(err)
	}
	policy := ValidatorPolicy{
		EpochLength: 4, DuplicateVoteSlashBasisPoints: 500,
		LightClientAttackSlashBasisPoints: 10_000,
		EvidenceMaxAgeNumBlocks:           10, EvidenceMaxAgeDurationNanos: int64(time.Hour),
	}
	genesis, err := NewGenesisDocumentV2("chainlab-validator-v2", types.DefaultBlockGasLimit, store, policy)
	if err != nil {
		t.Fatal(err)
	}
	genesisBytes, err := genesis.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	params := consensusParams(genesis.BlockGasLimit)
	params.Evidence.MaxAgeNumBlocks = policy.EvidenceMaxAgeNumBlocks
	params.Evidence.MaxAgeDuration = time.Duration(policy.EvidenceMaxAgeDurationNanos)
	params.Version = &cmtproto.VersionParams{App: AppVersionV2}
	app, err := NewApplication(Config{Genesis: genesis, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = app
	fixture.genesis = genesis
	fixture.genesisBytes = genesisBytes
	fixture.params = params
	t.Cleanup(func() { _ = app.Close() })
	return fixture
}

func (fixture validatorV2Fixture) initialize(t *testing.T) {
	t.Helper()
	response, err := fixture.app.InitChain(context.Background(), &abcitypes.RequestInitChain{
		ChainId: fixture.genesis.ChainID, ConsensusParams: fixture.params,
		Validators: fixture.validatorUpdates, AppStateBytes: fixture.genesisBytes, InitialHeight: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.AppHash) != 32 || fixture.app.committed.store.ValidatorRoot() == "" {
		t.Fatalf("v2 init response=%+v validator_root=%q", response, fixture.app.committed.store.ValidatorRoot())
	}
}

func (fixture validatorV2Fixture) finalizeAndCommit(
	t *testing.T,
	height int64,
	proposer int,
	evidence []abcitypes.Misbehavior,
) *abcitypes.ResponseFinalizeBlock {
	t.Helper()
	response, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(byte(height)), Height: height, Time: validatorV2BlockTime(height),
		ProposerAddress: fixture.proposerAddresses[proposer], Misbehavior: evidence,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	return response
}

func validatorV2BlockTime(height int64) time.Time {
	return time.Unix(1_700_000_000+height, 0).UTC()
}

func (fixture validatorV2Fixture) duplicateVoteEvidence() []abcitypes.Misbehavior {
	return []abcitypes.Misbehavior{{
		Type:      abcitypes.MisbehaviorType_DUPLICATE_VOTE,
		Validator: abcitypes.Validator{Address: fixture.proposerAddresses[1], Power: 1},
		Height:    1, Time: validatorV2BlockTime(1), TotalVotingPower: 2,
	}}
}

func TestValidatorV2EvidenceSlashingAndEpochUpdateTiming(t *testing.T) {
	fixture := newValidatorV2Fixture(t, "")
	fixture.initialize(t)
	fixture.finalizeAndCommit(t, 1, 0, nil)

	heightTwo := fixture.finalizeAndCommit(t, 2, 0, fixture.duplicateVoteEvidence())
	if len(heightTwo.ValidatorUpdates) != 0 {
		t.Fatalf("height 2 validator updates = %+v", heightTwo.ValidatorUpdates)
	}
	if got := fixture.app.committed.store.StakeOf(fixture.accounts[1]); got != 950 {
		t.Fatalf("slashed stake = %d, want 950", got)
	}
	lifecycle, exists := fixture.app.committed.store.ValidatorLifecycle()
	if !exists || len(lifecycle.Offences) != 1 {
		t.Fatalf("lifecycle after offence = %+v exists=%t", lifecycle, exists)
	}
	identity := lifecycle.Validators[bytesToHex(fixture.proposerAddresses[1])]
	if identity.InactiveHeight != 5 {
		t.Fatalf("inactive height = %d, want 5", identity.InactiveHeight)
	}

	heightThree := fixture.finalizeAndCommit(t, 3, 0, nil)
	if len(heightThree.ValidatorUpdates) != 1 || heightThree.ValidatorUpdates[0].Power != 0 ||
		!bytes.Equal(heightThree.ValidatorUpdates[0].PubKey.GetSecp256K1(), fixture.keys[1].PubKey().SerializeCompressed()) {
		t.Fatalf("height 3 validator updates = %+v", heightThree.ValidatorUpdates)
	}

	heightFour := fixture.finalizeAndCommit(t, 4, 1, fixture.duplicateVoteEvidence())
	if got := fixture.app.committed.store.StakeOf(fixture.accounts[1]); got != 950 {
		t.Fatalf("stake after repeated evidence = %d", got)
	}
	if !hasABCIAttribute(heightFour.Events, "chainlab.validator_slash", "new_offence", "false") {
		t.Fatalf("repeated evidence events = %+v", heightFour.Events)
	}

	if _, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(5), Height: 5, Time: validatorV2BlockTime(5),
		ProposerAddress: fixture.proposerAddresses[1],
	}); err == nil {
		t.Fatal("validator removed at height 5 was accepted as proposer")
	}
	heightFive := fixture.finalizeAndCommit(t, 5, 0, nil)
	if len(heightFive.ValidatorUpdates) != 0 || fixture.app.committed.commitment.Epoch != 1 {
		t.Fatalf("height 5 response=%+v commitment=%+v", heightFive, fixture.app.committed.commitment)
	}
}

func TestValidatorV2LifecycleSurvivesRestartAndSnapshot(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "source")
	fixture := newValidatorV2Fixture(t, dataDir)
	fixture.initialize(t)
	fixture.finalizeAndCommit(t, 1, 0, nil)
	fixture.finalizeAndCommit(t, 2, 0, fixture.duplicateVoteEvidence())
	wantHash := cloneBytes(fixture.app.committed.appHash)
	if err := fixture.app.Close(); err != nil {
		t.Fatal(err)
	}

	restored, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = restored
	defer fixture.app.Close()
	if !bytes.Equal(fixture.app.committed.appHash, wantHash) || fixture.app.committed.store.StakeOf(fixture.accounts[1]) != 950 {
		t.Fatal("validator lifecycle did not survive application restart")
	}
	heightThree := fixture.finalizeAndCommit(t, 3, 0, nil)
	if len(heightThree.ValidatorUpdates) != 1 {
		t.Fatalf("restored pending update = %+v", heightThree.ValidatorUpdates)
	}

	listed, err := fixture.app.ListSnapshots(context.Background(), &abcitypes.RequestListSnapshots{})
	if err != nil || len(listed.Snapshots) != 1 {
		t.Fatalf("list snapshots response=%+v err=%v", listed, err)
	}
	offered := listed.Snapshots[0]
	targetDir := filepath.Join(t.TempDir(), "target")
	targetApp, err := NewApplication(Config{Genesis: fixture.genesis, DataDir: targetDir})
	if err != nil {
		t.Fatal(err)
	}
	target := fixture
	target.app = targetApp
	defer target.app.Close()
	offer, err := target.app.OfferSnapshot(context.Background(), &abcitypes.RequestOfferSnapshot{
		Snapshot: offered, AppHash: cloneBytes(fixture.app.committed.appHash),
	})
	if err != nil || offer.Result != abcitypes.ResponseOfferSnapshot_ACCEPT {
		t.Fatalf("offer snapshot response=%+v err=%v", offer, err)
	}
	for index := uint32(0); index < offered.Chunks; index++ {
		chunk, err := fixture.app.LoadSnapshotChunk(context.Background(), &abcitypes.RequestLoadSnapshotChunk{
			Height: offered.Height, Format: offered.Format, Chunk: index,
		})
		if err != nil {
			t.Fatal(err)
		}
		applied, err := target.app.ApplySnapshotChunk(context.Background(), &abcitypes.RequestApplySnapshotChunk{
			Index: index, Chunk: chunk.Chunk, Sender: "source",
		})
		if err != nil || applied.Result != abcitypes.ResponseApplySnapshotChunk_ACCEPT {
			t.Fatalf("apply chunk %d response=%+v err=%v", index, applied, err)
		}
	}
	if !bytes.Equal(target.app.committed.appHash, fixture.app.committed.appHash) ||
		target.app.committed.store.StakeOf(fixture.accounts[1]) != 950 {
		t.Fatal("validator lifecycle did not survive state sync snapshot")
	}
	targetLifecycle, exists := target.app.committed.store.ValidatorLifecycle()
	if !exists || len(targetLifecycle.Offences) != 1 || targetLifecycle.Validators[bytesToHex(fixture.proposerAddresses[1])].InactiveHeight != 5 {
		t.Fatalf("restored snapshot lifecycle = %+v exists=%t", targetLifecycle, exists)
	}
}

func TestValidatorV2RejectsTransitionThatWouldEmptyValidatorSet(t *testing.T) {
	fixture := newValidatorV2Fixture(t, "")
	fixture.initialize(t)
	fixture.finalizeAndCommit(t, 1, 0, nil)
	evidence := make([]abcitypes.Misbehavior, 0, 2)
	for index := range fixture.accounts {
		evidence = append(evidence, abcitypes.Misbehavior{
			Type:      abcitypes.MisbehaviorType_DUPLICATE_VOTE,
			Validator: abcitypes.Validator{Address: fixture.proposerAddresses[index], Power: 1},
			Height:    1, Time: validatorV2BlockTime(1), TotalVotingPower: 2,
		})
	}
	if _, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Hash: blockHash(2), Height: 2, Time: validatorV2BlockTime(2),
		ProposerAddress: fixture.proposerAddresses[0], Misbehavior: evidence,
	}); err == nil {
		t.Fatal("transition removing the complete validator set was accepted")
	}
	for _, account := range fixture.accounts {
		if got := fixture.app.committed.store.StakeOf(account); got != 1_001 {
			t.Fatalf("committed stake changed after rejected transition: %s=%d", account, got)
		}
	}
	lifecycle, exists := fixture.app.committed.store.ValidatorLifecycle()
	if !exists || len(lifecycle.Offences) != 0 {
		t.Fatalf("committed offences changed after rejected transition: %+v", lifecycle.Offences)
	}
}

func TestValidatorV2BindsEvidenceRetentionAndRejectsExpiredEvidence(t *testing.T) {
	fixture := newValidatorV2Fixture(t, "")
	badRequest := &abcitypes.RequestInitChain{
		ChainId: fixture.genesis.ChainID, ConsensusParams: consensusParams(fixture.genesis.BlockGasLimit),
		Validators: fixture.validatorUpdates, AppStateBytes: fixture.genesisBytes, InitialHeight: 1,
	}
	badRequest.ConsensusParams.Version = &cmtproto.VersionParams{App: AppVersionV2}
	if _, err := fixture.app.InitChain(context.Background(), badRequest); err == nil {
		t.Fatal("mismatched evidence retention was accepted")
	}
	fixture.initialize(t)
	for height := int64(1); height <= 12; height++ {
		fixture.finalizeAndCommit(t, height, 0, nil)
	}
	evidence := fixture.duplicateVoteEvidence()
	withinDuration, err := fixture.app.ProcessProposal(context.Background(), &abcitypes.RequestProcessProposal{
		Hash: blockHash(13), Height: 13, Time: validatorV2BlockTime(13),
		ProposerAddress: fixture.proposerAddresses[0], Misbehavior: evidence,
	})
	if err != nil || withinDuration.Status != abcitypes.ResponseProcessProposal_ACCEPT {
		t.Fatalf("evidence exceeding only the block-age limit was rejected: response=%+v err=%v", withinDuration, err)
	}
	expired, err := fixture.app.ProcessProposal(context.Background(), &abcitypes.RequestProcessProposal{
		Hash: blockHash(13), Height: 13, Time: validatorV2BlockTime(1).Add(2 * time.Hour),
		ProposerAddress: fixture.proposerAddresses[0], Misbehavior: evidence,
	})
	if err != nil || expired.Status != abcitypes.ResponseProcessProposal_REJECT {
		t.Fatalf("evidence older than both retention dimensions was accepted: response=%+v err=%v", expired, err)
	}
}

func bytesToHex(value []byte) string {
	const digits = "0123456789abcdef"
	encoded := make([]byte, len(value)*2)
	for index, item := range value {
		encoded[index*2] = digits[item>>4]
		encoded[index*2+1] = digits[item&0x0f]
	}
	return string(encoded)
}

func TestValidatorUpdatesAtHeightActivatesAndRemovesIdentities(t *testing.T) {
	fixture := newValidatorV2Fixture(t, "")
	fixture.initialize(t)
	lifecycle, exists := fixture.app.committed.store.ValidatorLifecycle()
	if !exists {
		t.Fatal("validator lifecycle is missing")
	}
	candidateKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	candidatePublicKey := candidateKey.PubKey().SerializeCompressed()
	candidateAddress := bytesToHex(cmtsecp256k1.PubKey(candidatePublicKey).Address())
	lifecycle.Validators[candidateAddress] = state.ValidatorIdentity{
		Account: chaincrypto.AddressFromPrivateKey(candidateKey), ConsensusAddress: candidateAddress,
		PublicKey: bytesToHex(candidatePublicKey), Power: 3, ActiveHeight: 5,
	}
	removed := lifecycle.Validators[bytesToHex(fixture.proposerAddresses[1])]
	removed.InactiveHeight = 5
	lifecycle.Validators[removed.ConsensusAddress] = removed

	updates, err := validatorUpdatesAtHeight(lifecycle, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 2 {
		t.Fatalf("validator updates = %+v", updates)
	}
	powers := map[string]int64{}
	for _, update := range updates {
		address := bytesToHex(cmtsecp256k1.PubKey(update.PubKey.GetSecp256K1()).Address())
		powers[address] = update.Power
	}
	if powers[candidateAddress] != 3 || powers[removed.ConsensusAddress] != 0 {
		t.Fatalf("validator update powers = %+v", powers)
	}
	updates, err = validatorUpdatesAtHeight(lifecycle, 4)
	if err != nil || len(updates) != 0 {
		t.Fatalf("repeated validator updates = %+v err=%v", updates, err)
	}
}

func TestLifecycleValidatorIdentityAuthorizesOnlyItsActiveInterval(t *testing.T) {
	fixture := newValidatorV2Fixture(t, "")
	fixture.initialize(t)
	candidateKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	candidatePublicKey := candidateKey.PubKey().SerializeCompressed()
	candidateAddress := cmtsecp256k1.PubKey(candidatePublicKey).Address()
	lifecycle, _ := fixture.app.committed.store.ValidatorLifecycle()
	lifecycle.Validators[bytesToHex(candidateAddress)] = state.ValidatorIdentity{
		Account:          chaincrypto.AddressFromPrivateKey(candidateKey),
		ConsensusAddress: bytesToHex(candidateAddress), PublicKey: bytesToHex(candidatePublicKey),
		Power: 2, ActiveHeight: 3, InactiveHeight: 5,
	}
	if err := fixture.app.committed.store.SetValidatorLifecycle(lifecycle); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		height int64
		valid  bool
	}{
		{height: 2, valid: false},
		{height: 3, valid: true},
		{height: 4, valid: true},
		{height: 5, valid: false},
	} {
		account, err := fixture.app.validatorAccountLocked(candidateAddress, test.height)
		if test.valid && (err != nil || account != chaincrypto.AddressFromPrivateKey(candidateKey)) {
			t.Fatalf("height %d account=%q err=%v", test.height, account, err)
		}
		if !test.valid && err == nil {
			t.Fatalf("height %d unexpectedly authorized account %q", test.height, account)
		}
	}
	unknown := make([]byte, 20)
	unknown[0] = 1
	if _, err := fixture.app.validatorAccountLocked(unknown, 3); err == nil {
		t.Fatal("unknown validator identity was authorized")
	}
}

func hasABCIAttribute(events []abcitypes.Event, eventType string, key string, value string) bool {
	for _, event := range events {
		if event.Type != eventType {
			continue
		}
		for _, attribute := range event.Attributes {
			if attribute.Key == key && attribute.Value == value {
				return true
			}
		}
	}
	return false
}
