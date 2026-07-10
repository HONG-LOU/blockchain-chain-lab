package abci

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"
	"chainlab/internal/types"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmtsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
)

type applicationFixture struct {
	app             *Application
	key             chaincrypto.PrivateKey
	account         string
	proposerAddress []byte
	genesis         GenesisDocument
	genesisBytes    []byte
	validator       abcitypes.ValidatorUpdate
	params          *cmtproto.ConsensusParams
}

func newApplicationFixture(t *testing.T, balance uint64) applicationFixture {
	t.Helper()
	return newApplicationFixtureWithGas(t, balance, types.DefaultBlockGasLimit)
}

func newApplicationFixtureWithGas(t *testing.T, balance uint64, blockGasLimit uint64) applicationFixture {
	t.Helper()
	return newApplicationFixtureWithBalances(t, balance, blockGasLimit, nil)
}

func newApplicationFixtureWithBalances(t *testing.T, balance uint64, blockGasLimit uint64, balances map[string]uint64) applicationFixture {
	t.Helper()
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	account := chaincrypto.AddressFromPrivateKey(key)
	store := state.NewStore()
	store.SetBalance(account, balance)
	for address, amount := range balances {
		store.SetBalance(address, amount)
	}
	if err := store.SetValidators([]string{account}); err != nil {
		t.Fatal(err)
	}
	genesis, err := NewGenesisDocument("chainlab-abci-test", blockGasLimit, store)
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApplication(Config{Genesis: genesis})
	if err != nil {
		t.Fatal(err)
	}
	compressed := key.PubKey().SerializeCompressed()
	validator := abcitypes.UpdateValidator(compressed, 1, cmtsecp256k1.KeyType)
	params := consensusParams(blockGasLimit)
	genesisBytes, err := genesis.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	return applicationFixture{
		app:             app,
		key:             key,
		account:         account,
		proposerAddress: cloneBytes(cmtsecp256k1.PubKey(compressed).Address()),
		genesis:         genesis,
		genesisBytes:    genesisBytes,
		validator:       validator,
		params:          params,
	}
}

func consensusParams(blockGasLimit uint64) *cmtproto.ConsensusParams {
	return &cmtproto.ConsensusParams{
		Block: &cmtproto.BlockParams{
			MaxBytes: types.MaxBlockBytes,
			MaxGas:   int64(blockGasLimit),
		},
		Evidence: &cmtproto.EvidenceParams{
			MaxAgeNumBlocks: 100_000,
			MaxAgeDuration:  7 * 24 * time.Hour,
			MaxBytes:        maxEvidenceBytes,
		},
		Validator: &cmtproto.ValidatorParams{PubKeyTypes: []string{cmtsecp256k1.KeyType}},
		Abci:      &cmtproto.ABCIParams{VoteExtensionsEnableHeight: 0},
	}
}

func (fixture applicationFixture) initRequest() *abcitypes.RequestInitChain {
	return &abcitypes.RequestInitChain{
		ChainId:         fixture.genesis.ChainID,
		ConsensusParams: fixture.params,
		Validators:      []abcitypes.ValidatorUpdate{fixture.validator},
		AppStateBytes:   cloneBytes(fixture.genesisBytes),
		InitialHeight:   1,
	}
}

func (fixture applicationFixture) initialize(t *testing.T) {
	t.Helper()
	response, err := fixture.app.InitChain(context.Background(), fixture.initRequest())
	if err != nil {
		t.Fatal(err)
	}
	if len(response.AppHash) != 32 {
		t.Fatalf("genesis app hash length = %d", len(response.AppHash))
	}
}

func signFixtureTransaction(t *testing.T, key chaincrypto.PrivateKey, tx types.Transaction) types.Transaction {
	t.Helper()
	signature, err := chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = signature
	return tx
}

func rawFixtureTransaction(t *testing.T, tx types.Transaction) []byte {
	t.Helper()
	encoded, err := types.EncodeRawTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(encoded, "0x"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func blockHash(height byte) []byte {
	return bytes.Repeat([]byte{height}, 32)
}

func TestInitChainBindsCanonicalGenesisAndConsensusParameters(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	info, err := fixture.app.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if info.LastBlockHeight != 0 || len(info.LastBlockAppHash) != 32 || info.AppVersion != AppVersion {
		t.Fatalf("pre-init info = %+v", info)
	}

	badGenesis := fixture.initRequest()
	badGenesis.AppStateBytes = append(cloneBytes(fixture.genesisBytes[:len(fixture.genesisBytes)-1]), []byte(`,"unknown":true}`)...)
	if _, err := fixture.app.InitChain(context.Background(), badGenesis); err == nil {
		t.Fatal("unknown genesis field should fail")
	}

	badPower := fixture.initRequest()
	badPower.Validators[0].Power = 2
	if _, err := fixture.app.InitChain(context.Background(), badPower); err == nil || !strings.Contains(err.Error(), "voting power 1") {
		t.Fatalf("bad validator power error = %v", err)
	}

	badBlockBytes := fixture.initRequest()
	badBlockBytes.ConsensusParams = consensusParams(fixture.genesis.BlockGasLimit)
	badBlockBytes.ConsensusParams.Block.MaxBytes--
	if _, err := fixture.app.InitChain(context.Background(), badBlockBytes); err == nil || !strings.Contains(err.Error(), "max block bytes") {
		t.Fatalf("bad block bytes error = %v", err)
	}

	fixture.initialize(t)
	if _, err := fixture.app.InitChain(context.Background(), fixture.initRequest()); err == nil || !strings.Contains(err.Error(), "already initialized") {
		t.Fatalf("duplicate init error = %v", err)
	}
}

func TestApplicationDetachesConfiguredGenesisState(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	expectedRoot := fixture.app.committed.store.Root()
	account := fixture.genesis.State.Accounts[fixture.account]
	account.Balance = 1
	fixture.genesis.State.Accounts[fixture.account] = account
	fixture.genesis.State.Validators[0] = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if fixture.app.committed.store.Root() != expectedRoot {
		t.Fatal("configured genesis mutation changed application state")
	}
	fixture.initialize(t)
	if fixture.app.committed.store.Root() != expectedRoot {
		t.Fatal("configured genesis mutation changed initialized application state")
	}
}

func TestFinalHeightCommitsWithoutOverflowingMempoolHeight(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	fixture.initialize(t)
	fixture.app.committed.commitment.Height = math.MaxInt64 - 1
	commitment := fixture.app.committed.commitment
	commitment.Height = math.MaxInt64
	fixture.app.candidate = &blockCandidate{state: committedState{
		store:      fixture.app.committed.store.Clone(),
		commitment: commitment,
		appHash:    applicationHash(commitment),
	}}
	if _, err := fixture.app.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	if fixture.app.committed.commitment.Height != math.MaxInt64 {
		t.Fatalf("committed height = %d", fixture.app.committed.commitment.Height)
	}
	if _, err := fixture.app.PrepareProposal(context.Background(), &abcitypes.RequestPrepareProposal{}); err == nil || !strings.Contains(err.Error(), "height domain") {
		t.Fatalf("exhausted height error = %v", err)
	}
}

func TestQueryVoteAndSnapshotBoundaries(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	fixture.initialize(t)

	root, err := fixture.app.Query(context.Background(), &abcitypes.RequestQuery{Path: "/state/root"})
	if err != nil {
		t.Fatal(err)
	}
	if root.Code != CodeOK || string(root.Value) != fixture.app.committed.store.Root() {
		t.Fatalf("root query = %+v", root)
	}
	account, err := fixture.app.Query(context.Background(), &abcitypes.RequestQuery{Path: "/account", Data: []byte(fixture.account)})
	if err != nil {
		t.Fatal(err)
	}
	var decoded types.Account
	if err := json.Unmarshal(account.Value, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Address != fixture.account || decoded.Balance != 1_000_000 {
		t.Fatalf("account query = %+v", decoded)
	}
	proof, err := fixture.app.Query(context.Background(), &abcitypes.RequestQuery{Path: "/state/root", Prove: true})
	if err != nil {
		t.Fatal(err)
	}
	if proof.Code != CodeUnsupported {
		t.Fatalf("proof query code = %d", proof.Code)
	}

	extended, err := fixture.app.ExtendVote(context.Background(), &abcitypes.RequestExtendVote{
		Hash: blockHash(1), Height: 1, ProposerAddress: fixture.proposerAddress,
	})
	if err != nil || len(extended.VoteExtension) != 0 {
		t.Fatalf("extend vote = %+v err=%v", extended, err)
	}
	verified, err := fixture.app.VerifyVoteExtension(context.Background(), &abcitypes.RequestVerifyVoteExtension{
		Hash: blockHash(1), ValidatorAddress: fixture.proposerAddress, Height: 1,
	})
	if err != nil || verified.Status != abcitypes.ResponseVerifyVoteExtension_ACCEPT {
		t.Fatalf("verify empty extension = %+v err=%v", verified, err)
	}
	rejected, err := fixture.app.VerifyVoteExtension(context.Background(), &abcitypes.RequestVerifyVoteExtension{
		Hash: blockHash(1), ValidatorAddress: fixture.proposerAddress, Height: 1, VoteExtension: []byte{1},
	})
	if err != nil || rejected.Status != abcitypes.ResponseVerifyVoteExtension_REJECT {
		t.Fatalf("verify non-empty extension = %+v err=%v", rejected, err)
	}

	snapshots, err := fixture.app.ListSnapshots(context.Background(), &abcitypes.RequestListSnapshots{})
	if err != nil || len(snapshots.Snapshots) != 0 {
		t.Fatalf("list snapshots = %+v err=%v", snapshots, err)
	}
	offer, err := fixture.app.OfferSnapshot(context.Background(), &abcitypes.RequestOfferSnapshot{})
	if err != nil || offer.Result != abcitypes.ResponseOfferSnapshot_ABORT {
		t.Fatalf("offer snapshot = %+v err=%v", offer, err)
	}
	apply, err := fixture.app.ApplySnapshotChunk(context.Background(), &abcitypes.RequestApplySnapshotChunk{})
	if err != nil || apply.Result != abcitypes.ResponseApplySnapshotChunk_ABORT {
		t.Fatalf("apply snapshot = %+v err=%v", apply, err)
	}
}

func TestProposalTxSizeMatchesCometProtoEncoding(t *testing.T) {
	for _, size := range []int{0, 1, 127, 128, 16_383, 16_384, types.MaxTransactionBytes} {
		raw := make([]byte, size)
		data := &cmtproto.Data{Txs: [][]byte{raw}}
		if got, want := proposalTxSize(raw), int64(data.Size()); got != want {
			t.Fatalf("size %d: proposal size=%d proto size=%d", size, got, want)
		}
	}
}
