package node_test

import (
	"math"
	"strings"
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/node"
	"chainlab/internal/types"
)

func TestNodeProducesAndImportsIncludedFailureBeforeNextNonce(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := chaincrypto.AddressFromPrivateKey(key)
	overflowed := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	receiver := "0xcccccccccccccccccccccccccccccccccccccccc"
	config := node.Config{
		ChainID:     "chainlab-local",
		ProposerKey: key,
		GenesisBalance: map[string]uint64{
			sender:     100_000,
			overflowed: math.MaxUint64,
		}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	}
	producer, err := node.NewDevelopment(config)
	if err != nil {
		t.Fatal(err)
	}
	follower, err := node.NewDevelopment(config)
	if err != nil {
		t.Fatal(err)
	}

	failed := signIncludedFailureNodeTx(t, key, types.Transaction{
		ChainID:  config.ChainID,
		Type:     types.TxTransfer,
		From:     sender,
		To:       overflowed,
		Nonce:    0,
		Value:    1,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	succeeded := signIncludedFailureNodeTx(t, key, types.Transaction{
		ChainID:  config.ChainID,
		Type:     types.TxTransfer,
		From:     sender,
		To:       receiver,
		Nonce:    1,
		Value:    1,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if err := producer.SubmitTx(failed); err != nil {
		t.Fatalf("submit included failure: %v", err)
	}
	if pending, err := producer.PendingAccount(sender); err != nil || pending.Nonce != 1 {
		t.Fatalf("pending sender after failure = %+v err=%v", pending, err)
	}
	if err := producer.SubmitTx(succeeded); err != nil {
		t.Fatalf("submit next nonce: %v", err)
	}

	block, err := producer.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 2 || len(block.Receipts) != 2 {
		t.Fatalf("block txs=%d receipts=%d", len(block.Transactions), len(block.Receipts))
	}
	failedReceipt := block.Receipts[0]
	if failedReceipt.Success || failedReceipt.FailureCode != types.ReceiptFailureExecutionReverted || failedReceipt.GasUsed != 21_000 || len(failedReceipt.Events) != 0 {
		t.Fatalf("failed receipt = %+v", failedReceipt)
	}
	if !block.Receipts[1].Success || block.Header.GasUsed != 42_000 {
		t.Fatalf("success receipt=%+v block gas=%d", block.Receipts[1], block.Header.GasUsed)
	}
	if account := producer.Account(sender); account.Nonce != 2 || account.Balance != 57_999 {
		t.Fatalf("producer sender = %+v", account)
	}
	if got := producer.Account(overflowed).Balance; got != math.MaxUint64 {
		t.Fatalf("overflow recipient balance = %d", got)
	}
	if got := producer.Account(receiver).Balance; got != 1 {
		t.Fatalf("success recipient balance = %d", got)
	}

	if err := follower.ImportBlock(block); err != nil {
		t.Fatal(err)
	}
	if follower.StateRoot() != producer.StateRoot() || follower.Head().Header.ReceiptRoot != block.Header.ReceiptRoot {
		t.Fatalf("producer/follower mismatch: producer=%s follower=%s", producer.StateRoot(), follower.StateRoot())
	}
	if account := follower.Account(sender); account.Nonce != 2 || account.Balance != 57_999 {
		t.Fatalf("follower sender = %+v", account)
	}
}

func TestUnstakeSettlementOverflowCannotHaltProducerOrImporter(t *testing.T) {
	proposerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	funderKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := chaincrypto.AddressFromPrivateKey(proposerKey)
	funder := chaincrypto.AddressFromPrivateKey(funderKey)
	config := node.Config{
		ChainID:     "chainlab-local",
		ProposerKey: proposerKey,
		GenesisBalance: map[string]uint64{
			proposer: math.MaxUint64,
			funder:   200_000,
		}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	}
	producer, err := node.NewDevelopment(config)
	if err != nil {
		t.Fatal(err)
	}
	importer, err := node.NewDevelopment(config)
	if err != nil {
		t.Fatal(err)
	}

	const stakeValue = math.MaxUint64 - 100_000
	stake := signIncludedFailureNodeTx(t, proposerKey, types.Transaction{
		ChainID:  config.ChainID,
		Type:     types.TxStake,
		From:     proposer,
		Nonce:    0,
		Value:    stakeValue,
		GasLimit: 30_000,
		GasPrice: 1,
	})
	if err := producer.SubmitTx(stake); err != nil {
		t.Fatal(err)
	}
	blockOne, err := producer.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := importer.ImportBlock(blockOne); err != nil {
		t.Fatal(err)
	}

	fund := signIncludedFailureNodeTx(t, funderKey, types.Transaction{
		ChainID:  config.ChainID,
		Type:     types.TxTransfer,
		From:     funder,
		To:       proposer,
		Nonce:    0,
		Value:    61_000,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if err := producer.SubmitTx(fund); err != nil {
		t.Fatal(err)
	}
	blockTwo, err := producer.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := importer.ImportBlock(blockTwo); err != nil {
		t.Fatal(err)
	}

	overflow := signIncludedFailureNodeTx(t, proposerKey, types.Transaction{
		ChainID:  config.ChainID,
		Type:     types.TxUnstake,
		From:     proposer,
		Nonce:    1,
		Value:    stakeValue,
		GasLimit: 31_000,
		GasPrice: 1,
	})
	if err := producer.SubmitTx(overflow); err == nil || err.Error() != "recipient balance overflow" {
		t.Fatalf("producer overflow admission error = %v", err)
	}

	parent := importer.Head()
	baseFee := node.NextBaseFee(parent, parent.Header.GasLimit)
	maliciousReceipt := types.Receipt{
		TxHash:            overflow.Hash(),
		Success:           true,
		GasUsed:           overflow.GasLimit,
		BaseFeePerGas:     baseFee,
		EffectiveGasPrice: baseFee,
		BaseFeeBurned:     overflow.GasLimit * baseFee,
	}
	malicious := types.Block{
		Header: types.BlockHeader{
			ChainID:       config.ChainID,
			Height:        parent.Header.Height + 1,
			ParentHash:    parent.Hash(),
			TimeUnix:      parent.Header.TimeUnix + 1,
			Proposer:      proposer,
			TxRoot:        types.TransactionRoot([]types.Transaction{overflow}),
			ReceiptRoot:   types.ReceiptRoot([]types.Receipt{maliciousReceipt}),
			StateRoot:     importer.StateRoot(),
			GasLimit:      parent.Header.GasLimit,
			GasUsed:       maliciousReceipt.GasUsed,
			BaseFeePerGas: baseFee,
		},
		Transactions: []types.Transaction{overflow},
		Receipts:     []types.Receipt{maliciousReceipt},
	}
	malicious.Signature, err = chaincrypto.Sign(proposerKey, malicious.Header.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := importer.ImportBlock(malicious); err == nil || !strings.Contains(err.Error(), "recipient balance overflow") {
		t.Fatalf("importer overflow admission error = %v", err)
	}

	valid := signIncludedFailureNodeTx(t, funderKey, types.Transaction{
		ChainID:  config.ChainID,
		Type:     types.TxTransfer,
		From:     funder,
		To:       "0xcccccccccccccccccccccccccccccccccccccccc",
		Nonce:    1,
		Value:    1,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if err := producer.SubmitTx(valid); err != nil {
		t.Fatalf("producer remained halted after rejected overflow: %v", err)
	}
	blockThree, err := producer.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := importer.ImportBlock(blockThree); err != nil {
		t.Fatalf("importer remained halted after rejected overflow: %v", err)
	}
	if producer.StateRoot() != importer.StateRoot() {
		t.Fatalf("state roots diverged: producer=%s importer=%s", producer.StateRoot(), importer.StateRoot())
	}
}

func signIncludedFailureNodeTx(t *testing.T, key chaincrypto.PrivateKey, tx types.Transaction) types.Transaction {
	t.Helper()
	signature, err := chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = signature
	return tx
}
