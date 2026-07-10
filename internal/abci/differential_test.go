package abci

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/node"
	"chainlab/internal/state"
	"chainlab/internal/types"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmtsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
)

func TestABCIExecutionMatchesLocalHarnessStateAndReceipts(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := chaincrypto.AddressFromPrivateKey(key)
	overflowed := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	receiver := "0xcccccccccccccccccccccccccccccccccccccccc"
	balances := map[string]uint64{
		sender:     100_000,
		overflowed: math.MaxUint64,
	}

	store := state.NewStore()
	for address, balance := range balances {
		store.SetBalance(address, balance)
	}
	if err := store.SetValidators([]string{sender}); err != nil {
		t.Fatal(err)
	}
	genesis, err := NewGenesisDocument("chainlab-differential", types.DefaultBlockGasLimit, store)
	if err != nil {
		t.Fatal(err)
	}
	application, err := NewApplication(Config{Genesis: genesis})
	if err != nil {
		t.Fatal(err)
	}
	compressed := key.PubKey().SerializeCompressed()
	genesisBytes, err := genesis.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	proposerAddress := cloneBytes(cmtsecp256k1.PubKey(compressed).Address())
	if _, err := application.InitChain(context.Background(), &abcitypes.RequestInitChain{
		ChainId:         genesis.ChainID,
		ConsensusParams: consensusParams(genesis.BlockGasLimit),
		Validators:      []abcitypes.ValidatorUpdate{abcitypes.UpdateValidator(compressed, 1, cmtsecp256k1.KeyType)},
		AppStateBytes:   genesisBytes,
		InitialHeight:   1,
	}); err != nil {
		t.Fatal(err)
	}

	harness, err := node.NewDevelopment(node.Config{
		ChainID:         genesis.ChainID,
		ProposerKey:     key,
		GenesisBalance:  balances,
		GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
		BlockGasLimit:   genesis.BlockGasLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := harness.Close(); err != nil {
			t.Errorf("close harness: %v", err)
		}
	}()

	failed := signFixtureTransaction(t, key, types.Transaction{
		ChainID: genesis.ChainID, Type: types.TxTransfer, From: sender, To: overflowed,
		Nonce: 0, Value: 1, GasLimit: 21_000, GasPrice: 1,
	})
	succeeded := signFixtureTransaction(t, key, types.Transaction{
		ChainID: genesis.ChainID, Type: types.TxTransfer, From: sender, To: receiver,
		Nonce: 1, Value: 1, GasLimit: 21_000, GasPrice: 1,
	})
	for _, tx := range []types.Transaction{failed, succeeded} {
		if err := harness.SubmitTx(tx); err != nil {
			t.Fatal(err)
		}
	}
	harnessBlock, err := harness.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	rawTxs := [][]byte{rawFixtureTransaction(t, failed), rawFixtureTransaction(t, succeeded)}
	processed, err := application.ProcessProposal(context.Background(), &abcitypes.RequestProcessProposal{
		Txs: rawTxs, Hash: blockHash(1), Height: 1, ProposerAddress: proposerAddress,
	})
	if err != nil || processed.Status != abcitypes.ResponseProcessProposal_ACCEPT {
		t.Fatalf("process proposal = %+v err=%v", processed, err)
	}
	finalized, err := application.FinalizeBlock(context.Background(), &abcitypes.RequestFinalizeBlock{
		Txs: rawTxs, Hash: blockHash(1), Height: 1, ProposerAddress: proposerAddress,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(finalized.TxResults) != len(harnessBlock.Receipts) {
		t.Fatalf("ABCI results=%d harness receipts=%d", len(finalized.TxResults), len(harnessBlock.Receipts))
	}
	for index, result := range finalized.TxResults {
		var receipt types.Receipt
		if err := json.Unmarshal(result.Data, &receipt); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(receipt, harnessBlock.Receipts[index]) {
			t.Fatalf("receipt %d mismatch\nABCI:    %+v\nharness: %+v", index, receipt, harnessBlock.Receipts[index])
		}
	}
	candidate := application.candidate.state.commitment
	if candidate.StateRoot != harnessBlock.Header.StateRoot {
		t.Fatalf("state root ABCI=%s harness=%s", candidate.StateRoot, harnessBlock.Header.StateRoot)
	}
	if candidate.TxRoot != harnessBlock.Header.TxRoot {
		t.Fatalf("tx root ABCI=%s harness=%s", candidate.TxRoot, harnessBlock.Header.TxRoot)
	}
	if candidate.ReceiptRoot != harnessBlock.Header.ReceiptRoot {
		t.Fatalf("receipt root ABCI=%s harness=%s", candidate.ReceiptRoot, harnessBlock.Header.ReceiptRoot)
	}
	if candidate.GasUsed != harnessBlock.Header.GasUsed {
		t.Fatalf("gas used ABCI=%d harness=%d", candidate.GasUsed, harnessBlock.Header.GasUsed)
	}
	if candidate.NextBaseFeePerGas != node.NextBaseFee(harnessBlock, harnessBlock.Header.GasLimit) {
		t.Fatalf("next base fee ABCI=%d harness=%d", candidate.NextBaseFeePerGas, node.NextBaseFee(harnessBlock, harnessBlock.Header.GasLimit))
	}
	if _, err := application.Commit(context.Background(), &abcitypes.RequestCommit{}); err != nil {
		t.Fatal(err)
	}
	if application.committed.store.Root() != harness.StateRoot() {
		t.Fatalf("committed state ABCI=%s harness=%s", application.committed.store.Root(), harness.StateRoot())
	}
	if account := application.committed.store.GetAccount(sender); account.Nonce != 2 || account.Balance != 57_999 {
		t.Fatalf("ABCI sender = %+v", account)
	}
}

func TestIndependentApplicationsFinalizeIdentically(t *testing.T) {
	fixture := newApplicationFixture(t, 1_000_000)
	fixture.initialize(t)
	second, err := NewApplication(Config{Genesis: fixture.genesis})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.InitChain(context.Background(), fixture.initRequest()); err != nil {
		t.Fatal(err)
	}
	tx := signFixtureTransaction(t, fixture.key, types.Transaction{
		ChainID: fixture.genesis.ChainID, Type: types.TxTransfer, From: fixture.account,
		To: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Nonce: 0, Value: 9, GasLimit: 21_000, GasPrice: 1,
	})
	request := &abcitypes.RequestFinalizeBlock{
		Txs: [][]byte{rawFixtureTransaction(t, tx)}, Hash: blockHash(1), Height: 1, ProposerAddress: fixture.proposerAddress,
	}
	firstResponse, err := fixture.app.FinalizeBlock(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	secondResponse, err := second.FinalizeBlock(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(firstResponse, secondResponse) {
		t.Fatalf("independent finalize responses differ\nfirst:  %+v\nsecond: %+v", firstResponse, secondResponse)
	}
}
