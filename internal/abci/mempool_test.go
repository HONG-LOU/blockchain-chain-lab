package abci

import (
	"context"
	"testing"

	"chainlab/internal/types"

	abcitypes "github.com/cometbft/cometbft/abci/types"
)

func TestAppMempoolSupportsSequentialNonceAndRebuildsAfterCommit(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	fixture.initialize(t)
	first := signFixtureTransaction(t, fixture.key, types.Transaction{
		ChainID: fixture.genesis.ChainID, Type: types.TxTransfer, From: fixture.account,
		To: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Nonce: 0, Value: 1, GasLimit: 21_000, GasPrice: 1,
	})
	second := signFixtureTransaction(t, fixture.key, types.Transaction{
		ChainID: fixture.genesis.ChainID, Type: types.TxTransfer, From: fixture.account,
		To: "0xcccccccccccccccccccccccccccccccccccccccc", Nonce: 1, Value: 1, GasLimit: 21_000, GasPrice: 1,
	})
	firstRaw := rawFixtureTransaction(t, first)
	secondRaw := rawFixtureTransaction(t, second)

	checked, err := fixture.app.CheckTx(context.Background(), &abcitypes.RequestCheckTx{Tx: secondRaw})
	if err != nil || checked.Code != CodeInvalidTx {
		t.Fatalf("future nonce CheckTx = %+v err=%v", checked, err)
	}
	for _, raw := range [][]byte{firstRaw, secondRaw, firstRaw} {
		inserted, err := fixture.app.InsertTx(context.Background(), &abcitypes.RequestInsertTx{Tx: raw})
		if err != nil || inserted.Code != CodeOK {
			t.Fatalf("insert tx = %+v err=%v", inserted, err)
		}
	}
	if len(fixture.app.mempool.entries) != 2 {
		t.Fatalf("mempool entries = %d", len(fixture.app.mempool.entries))
	}
	one, err := fixture.app.ReapTxs(context.Background(), &abcitypes.RequestReapTxs{MaxGas: 21_000})
	if err != nil || len(one.Txs) != 1 || string(one.Txs[0]) != string(firstRaw) {
		t.Fatalf("gas-bounded reap = %+v err=%v", one, err)
	}
	byBytes, err := fixture.app.ReapTxs(context.Background(), &abcitypes.RequestReapTxs{MaxBytes: uint64(proposalTxSize(firstRaw))})
	if err != nil || len(byBytes.Txs) != 1 {
		t.Fatalf("byte-bounded reap = %+v err=%v", byBytes, err)
	}

	if _, err := fixture.app.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Txs: [][]byte{firstRaw}, Hash: blockHash(1), Height: 1, ProposerAddress: fixture.proposerAddress,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	remaining, err := fixture.app.ReapTxs(context.Background(), &abcitypes.RequestReapTxs{})
	if err != nil || len(remaining.Txs) != 1 || string(remaining.Txs[0]) != string(secondRaw) {
		t.Fatalf("remaining mempool = %+v err=%v", remaining, err)
	}
}
