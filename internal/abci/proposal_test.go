package abci

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"

	"chainlab/internal/types"

	abcitypes "github.com/cometbft/cometbft/abci/types"
)

func TestProposalLifecyclePublishesOnlyOnCommit(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	fixture.initialize(t)
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tx := signFixtureTransaction(t, fixture.key, types.Transaction{
		ChainID:  fixture.genesis.ChainID,
		Type:     types.TxTransfer,
		From:     fixture.account,
		To:       receiver,
		Nonce:    0,
		Value:    7,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	raw := rawFixtureTransaction(t, tx)

	prepared, err := fixture.app.PrepareProposal(context.Background(), &abcitypes.RequestPrepareProposal{
		MaxTxBytes:      types.MaxProposalTxBytes,
		Txs:             [][]byte{{0xff}, raw},
		Height:          1,
		ProposerAddress: fixture.proposerAddress,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Txs) != 1 || string(prepared.Txs[0]) != string(raw) {
		t.Fatalf("prepared txs = %x", prepared.Txs)
	}
	processed, err := fixture.app.ProcessProposal(context.Background(), &abcitypes.RequestProcessProposal{
		Txs:             prepared.Txs,
		Hash:            blockHash(1),
		Height:          1,
		ProposerAddress: fixture.proposerAddress,
	})
	if err != nil || processed.Status != abcitypes.ResponseProcessProposal_ACCEPT {
		t.Fatalf("process proposal = %+v err=%v", processed, err)
	}

	beforeHash := cloneBytes(fixture.app.committed.appHash)
	finalized, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Txs:             prepared.Txs,
		Hash:            blockHash(1),
		Height:          1,
		ProposerAddress: fixture.proposerAddress,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(finalized.TxResults) != 1 || finalized.TxResults[0].Code != CodeOK || len(finalized.AppHash) != 32 {
		t.Fatalf("finalize response = %+v", finalized)
	}
	var receipt types.Receipt
	if err := json.Unmarshal(finalized.TxResults[0].Data, &receipt); err != nil {
		t.Fatal(err)
	}
	if !receipt.Success || receipt.TxHash != tx.Hash() {
		t.Fatalf("receipt = %+v", receipt)
	}
	if got := fixture.app.committed.store.GetAccount(receiver).Balance; got != 0 {
		t.Fatalf("uncommitted receiver balance = %d", got)
	}
	info, err := fixture.app.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if info.LastBlockHeight != 0 || string(info.LastBlockAppHash) != string(beforeHash) {
		t.Fatalf("info changed before commit = %+v", info)
	}
	if _, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{Height: 1}); err == nil {
		t.Fatal("second finalize before commit should fail")
	}

	expectedAppHash := cloneBytes(finalized.AppHash)
	finalized.AppHash[0] ^= 0xff
	finalized.TxResults[0].Data[0] ^= 0xff
	if _, err := fixture.app.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	if got := fixture.app.committed.store.GetAccount(receiver).Balance; got != 7 {
		t.Fatalf("committed receiver balance = %d", got)
	}
	info, err = fixture.app.Info(context.Background(), &abcitypes.RequestInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if info.LastBlockHeight != 1 || string(info.LastBlockAppHash) != string(expectedAppHash) {
		t.Fatalf("committed info = %+v", info)
	}
	if _, err := fixture.app.Commit(context.Background(), &abcitypes.RequestCommit{}); err == nil || !strings.Contains(err.Error(), "finalized block") {
		t.Fatalf("duplicate commit error = %v", err)
	}
}

func TestProcessProposalRejectsInvalidAndOverBudgetBlocks(t *testing.T) {
	fixture := newApplicationFixtureWithGas(t, 1_000_000, 30_000)
	fixture.initialize(t)
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	first := signFixtureTransaction(t, fixture.key, types.Transaction{
		ChainID: fixture.genesis.ChainID, Type: types.TxTransfer, From: fixture.account, To: receiver,
		Nonce: 0, Value: 1, GasLimit: 21_000, GasPrice: 1,
	})
	second := signFixtureTransaction(t, fixture.key, types.Transaction{
		ChainID: fixture.genesis.ChainID, Type: types.TxTransfer, From: fixture.account, To: receiver,
		Nonce: 1, Value: 1, GasLimit: 21_000, GasPrice: 1,
	})
	raws := [][]byte{rawFixtureTransaction(t, first), rawFixtureTransaction(t, second)}
	request := &abcitypes.RequestProcessProposal{
		Txs: raws, Hash: blockHash(1), Height: 1, ProposerAddress: fixture.proposerAddress,
	}
	response, err := fixture.app.ProcessProposal(context.Background(), request)
	if err != nil || response.Status != abcitypes.ResponseProcessProposal_REJECT {
		t.Fatalf("over-gas process response = %+v err=%v", response, err)
	}
	prepared, err := fixture.app.PrepareProposal(context.Background(), &abcitypes.RequestPrepareProposal{
		MaxTxBytes: types.MaxProposalTxBytes, Txs: raws, Height: 1, ProposerAddress: fixture.proposerAddress,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Txs) != 1 {
		t.Fatalf("prepared over-gas tx count = %d", len(prepared.Txs))
	}

	wrongProposer := *request
	wrongProposer.Txs = prepared.Txs
	wrongProposer.ProposerAddress = make([]byte, 20)
	response, err = fixture.app.ProcessProposal(context.Background(), &wrongProposer)
	if err != nil || response.Status != abcitypes.ResponseProcessProposal_REJECT {
		t.Fatalf("wrong proposer response = %+v err=%v", response, err)
	}

	tooMany := *request
	tooMany.Txs = make([][]byte, types.MaxTransactionsPerBlock+1)
	response, err = fixture.app.ProcessProposal(context.Background(), &tooMany)
	if err != nil || response.Status != abcitypes.ResponseProcessProposal_REJECT {
		t.Fatalf("too-many response = %+v err=%v", response, err)
	}
	if _, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Txs: raws, Hash: blockHash(1), Height: 1, ProposerAddress: fixture.proposerAddress,
	}); err == nil || !strings.Contains(err.Error(), "block gas limit") {
		t.Fatalf("over-gas finalize error = %v", err)
	}
}

func TestIncludedFailureAdvancesNonceAndReturnsFailedResult(t *testing.T) {
	overflowed := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	fixture := newApplicationFixtureWithBalances(t, 100_000, types.DefaultBlockGasLimit, map[string]uint64{overflowed: math.MaxUint64})
	fixture.initialize(t)

	tx := signFixtureTransaction(t, fixture.key, types.Transaction{
		ChainID: fixture.genesis.ChainID, Type: types.TxTransfer, From: fixture.account, To: overflowed,
		Nonce: 0, Value: 1, GasLimit: 21_000, GasPrice: 1,
	})
	raw := rawFixtureTransaction(t, tx)
	finalized, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Txs: [][]byte{raw}, Hash: blockHash(1), Height: 1, ProposerAddress: fixture.proposerAddress,
	})
	if err != nil {
		t.Fatal(err)
	}
	if finalized.TxResults[0].Code != CodeExecutionFailed || finalized.TxResults[0].Log != types.ReceiptFailureExecutionReverted {
		t.Fatalf("failed tx result = %+v", finalized.TxResults[0])
	}
	if _, err := fixture.app.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	account := fixture.app.committed.store.GetAccount(fixture.account)
	if account.Nonce != 1 || account.Balance != 79_000 {
		t.Fatalf("sender after included failure = %+v", account)
	}
	if got := fixture.app.committed.store.GetAccount(overflowed).Balance; got != math.MaxUint64 {
		t.Fatalf("overflowed recipient balance = %d", got)
	}
}

func TestCheckTxRejectsUnknownRequestType(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	fixture.initialize(t)
	response, err := fixture.app.CheckTx(context.Background(), &abcitypes.RequestCheckTx{Type: abcitypes.CheckTxType(99)})
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != CodeInvalidTx || !strings.Contains(response.Log, "type is unsupported") {
		t.Fatalf("unknown CheckTx type response = %+v", response)
	}
}
