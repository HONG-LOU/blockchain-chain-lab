package node_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"chainlab/internal/consensus"
	"chainlab/internal/contracts"
	"chainlab/internal/core"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/hash"
	"chainlab/internal/node"
	"chainlab/internal/types"
)

func TestNodeSubmitsTxAndProducesBlock(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}

	tx := types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       bob,
		Nonce:    0,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	}
	sig, err := chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = sig

	if err := n.SubmitTx(tx); err != nil {
		t.Fatal(err)
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}

	if block.Header.Height != 1 {
		t.Fatalf("height = %d", block.Header.Height)
	}
	if got := n.Account(bob).Balance; got != 100 {
		t.Fatalf("bob balance = %d", got)
	}
	if n.Head().Hash() != block.Hash() {
		t.Fatal("head should advance to produced block")
	}
}

func TestNodeProducesSmartAccountTransaction(t *testing.T) {
	ownerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	owner := chaincrypto.AddressFromPrivateKey(ownerKey)
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    ownerKey,
		GenesisBalance: map[string]uint64{owner: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}

	deploy := signedNodeTx(t, ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     owner,
		Nonce:    0,
		GasLimit: 80_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": contracts.AccountCodeID,
			"owner":   owner,
		},
	})
	if err := n.SubmitTx(deploy); err != nil {
		t.Fatal(err)
	}
	deployBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	smartAccount := deployBlock.Receipts[0].ContractAddress

	fund := signedNodeTx(t, ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     owner,
		To:       smartAccount,
		Nonce:    1,
		Value:    50_000,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if err := n.SubmitTx(fund); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	tx := signedNodeTx(t, ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     smartAccount,
		Signer:   owner,
		To:       receiver,
		Nonce:    0,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if err := n.SubmitTx(tx); err != nil {
		t.Fatal(err)
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}

	if got := n.Account(smartAccount).Nonce; got != 1 {
		t.Fatalf("smart account nonce = %d", got)
	}
	if got := n.Account(receiver).Balance; got != 100 {
		t.Fatalf("receiver balance = %d", got)
	}
	record, ok := n.Transaction(tx.Hash())
	if !ok {
		t.Fatal("smart account transaction should be indexed")
	}
	if record.BlockHash != block.Hash() || record.Transaction.Signer != owner {
		t.Fatalf("record = %+v", record)
	}
}

func TestNodeProducesMultisigAccountTransaction(t *testing.T) {
	ownerAKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerBKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerA := chaincrypto.AddressFromPrivateKey(ownerAKey)
	ownerB := chaincrypto.AddressFromPrivateKey(ownerBKey)
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    ownerAKey,
		GenesisBalance: map[string]uint64{ownerA: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}

	deploy := signedNodeTx(t, ownerAKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     ownerA,
		Nonce:    0,
		GasLimit: 80_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id":   contracts.MultisigCodeID,
			"owners":    ownerA + "," + ownerB,
			"threshold": "2",
		},
	})
	if err := n.SubmitTx(deploy); err != nil {
		t.Fatal(err)
	}
	deployBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	multisig := deployBlock.Receipts[0].ContractAddress

	fund := signedNodeTx(t, ownerAKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     ownerA,
		To:       multisig,
		Nonce:    1,
		Value:    50_000,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if err := n.SubmitTx(fund); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	tx := authorizedNodeTx(t, []chaincrypto.PrivateKey{ownerAKey, ownerBKey}, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     multisig,
		To:       receiver,
		Nonce:    0,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if err := n.SubmitTx(tx); err != nil {
		t.Fatal(err)
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}

	if got := n.Account(multisig).Nonce; got != 1 {
		t.Fatalf("multisig nonce = %d", got)
	}
	if got := n.Account(receiver).Balance; got != 100 {
		t.Fatalf("receiver balance = %d", got)
	}
	record, ok := n.Transaction(tx.Hash())
	if !ok {
		t.Fatal("multisig transaction should be indexed")
	}
	if record.BlockHash != block.Hash() || len(record.Transaction.Authorizations) != 2 {
		t.Fatalf("record = %+v", record)
	}
}

func TestNodeFeeMarketAdjustsBaseFeeAndRecordsGasUsed(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	carol := "0xcccccccccccccccccccccccccccccccccccccccc"

	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
		BlockGasLimit:  42_000,
	})
	if err != nil {
		t.Fatal(err)
	}

	first := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       bob,
		Nonce:    0,
		Value:    1,
		GasLimit: 21_000,
		GasPrice: 2,
	})
	second := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       carol,
		Nonce:    1,
		Value:    1,
		GasLimit: 21_000,
		GasPrice: 2,
	})
	if err := n.SubmitTx(first); err != nil {
		t.Fatal(err)
	}
	if err := n.SubmitTx(second); err != nil {
		t.Fatal(err)
	}
	fullBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if fullBlock.Header.BaseFeePerGas != 1 {
		t.Fatalf("full block base fee = %d", fullBlock.Header.BaseFeePerGas)
	}
	if fullBlock.Header.GasLimit != 42_000 || fullBlock.Header.GasUsed != 42_000 {
		t.Fatalf("full block gas = used %d limit %d", fullBlock.Header.GasUsed, fullBlock.Header.GasLimit)
	}

	emptyBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if emptyBlock.Header.BaseFeePerGas != 2 {
		t.Fatalf("empty block base fee = %d", emptyBlock.Header.BaseFeePerGas)
	}
	if emptyBlock.Header.GasUsed != 0 || emptyBlock.Header.GasLimit != 42_000 {
		t.Fatalf("empty block gas = used %d limit %d", emptyBlock.Header.GasUsed, emptyBlock.Header.GasLimit)
	}
}

func TestNodeSponsoredTransferUsesPaymasterInBlock(t *testing.T) {
	userKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	paymasterKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	user := chaincrypto.AddressFromPrivateKey(userKey)
	paymaster := chaincrypto.AddressFromPrivateKey(paymasterKey)
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	feeCollector := "0xfee0000000000000000000000000000000000000"
	n, err := node.New(node.Config{
		ChainID:     "chainlab-local",
		ProposerKey: paymasterKey,
		GenesisBalance: map[string]uint64{
			user:      100,
			paymaster: 100_000,
		},
		FeeCollector: feeCollector,
	})
	if err != nil {
		t.Fatal(err)
	}

	tx := sponsoredNodeTx(t, userKey, paymasterKey, types.Transaction{
		ChainID:   "chainlab-local",
		Type:      types.TxTransfer,
		From:      user,
		To:        receiver,
		Nonce:     0,
		Value:     100,
		GasLimit:  21_000,
		GasPrice:  2,
		Paymaster: paymaster,
	})
	if err := n.SubmitTx(tx); err != nil {
		t.Fatal(err)
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}

	if block.Receipts[0].FeePayer != paymaster {
		t.Fatalf("receipt fee payer = %+v", block.Receipts[0])
	}
	if got := n.Account(user).Balance; got != 0 {
		t.Fatalf("user balance = %d", got)
	}
	if got := n.Account(receiver).Balance; got != 100 {
		t.Fatalf("receiver balance = %d", got)
	}
	if got := n.Account(paymaster).Balance; got != 58_000 {
		t.Fatalf("paymaster balance = %d", got)
	}
	if got := n.Account(feeCollector).Balance; got != 21_000 {
		t.Fatalf("fee collector balance = %d", got)
	}
}

func TestNodeBatchTransactionProducesSingleIndexedReceipt(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	carol := "0xcccccccccccccccccccccccccccccccccccccccc"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}

	tx := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxBatch,
		From:     alice,
		Nonce:    0,
		GasLimit: 84_000,
		GasPrice: 1,
		Batch: []types.BatchOperation{
			{Type: types.TxTransfer, To: bob, Value: 10},
			{Type: types.TxTransfer, To: carol, Value: 20},
		},
	})
	if err := n.SubmitTx(tx); err != nil {
		t.Fatal(err)
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}

	if len(block.Transactions) != 1 || block.Transactions[0].Type != types.TxBatch {
		t.Fatalf("block transactions = %#v", block.Transactions)
	}
	if block.Header.GasUsed != 84_000 || block.Receipts[0].GasUsed != 84_000 {
		t.Fatalf("gas used header=%d receipt=%d", block.Header.GasUsed, block.Receipts[0].GasUsed)
	}
	if got := n.Account(bob).Balance; got != 10 {
		t.Fatalf("bob balance = %d", got)
	}
	if got := n.Account(carol).Balance; got != 20 {
		t.Fatalf("carol balance = %d", got)
	}
	record, ok := n.Transaction(tx.Hash())
	if !ok {
		t.Fatal("batch transaction should be indexed")
	}
	if record.Transaction.Type != types.TxBatch || record.Receipt.GasUsed != 84_000 || record.BlockHeight != 1 {
		t.Fatalf("record = %+v", record)
	}
}

func TestNodePersistsChainStateAndTransactionIndex(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	dataDir := t.TempDir()

	first, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
		DataDir:        dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	tx := types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       bob,
		Nonce:    0,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	}
	sig, err := chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = sig
	if err := first.SubmitTx(tx); err != nil {
		t.Fatal(err)
	}
	block, err := first.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}

	reloaded, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
		DataDir:        dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	if reloaded.Head().Hash() != block.Hash() {
		t.Fatal("reloaded node should keep the committed head")
	}
	if got := reloaded.Account(bob).Balance; got != 100 {
		t.Fatalf("reloaded bob balance = %d", got)
	}
	record, ok := reloaded.Transaction(tx.Hash())
	if !ok {
		t.Fatal("reloaded node should index committed transaction")
	}
	if record.BlockHeight != 1 {
		t.Fatalf("transaction block height = %d", record.BlockHeight)
	}
	if !record.Receipt.Success {
		t.Fatalf("receipt should succeed: %+v", record.Receipt)
	}
}

func TestNodeRebuildsEventIndexFromPersistedBlocks(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	dataDir := t.TempDir()

	first, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
		DataDir:        dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	deploy := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     alice,
		Nonce:    0,
		GasLimit: 80_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": "counter.v1",
			"initial": "1",
		},
	})
	if err := first.SubmitTx(deploy); err != nil {
		t.Fatal(err)
	}
	deployBlock, err := first.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	contract := deployBlock.Receipts[0].ContractAddress

	call := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     alice,
		To:       contract,
		Nonce:    1,
		GasLimit: 80_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method": "increment",
			"amount": "2",
		},
	})
	if err := first.SubmitTx(call); err != nil {
		t.Fatal(err)
	}
	callBlock, err := first.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}

	reloaded, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
		DataDir:        dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	topic := hash.KeccakHex([]byte("counter.incremented"))
	events := reloaded.Events(node.EventFilter{
		FromBlock: 1,
		ToBlock:   callBlock.Header.Height,
		Address:   contract,
		Topic0:    topic,
	})
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	event := events[0]
	if event.Event.Type != "counter.incremented" {
		t.Fatalf("event type = %q", event.Event.Type)
	}
	if event.Address != strings.ToLower(contract) {
		t.Fatalf("event address = %q", event.Address)
	}
	if event.Topic0 != topic {
		t.Fatalf("event topic0 = %q", event.Topic0)
	}
	if event.BlockHeight != callBlock.Header.Height || event.BlockHash != callBlock.Hash() {
		t.Fatalf("event block = %+v, want height %d hash %s", event, callBlock.Header.Height, callBlock.Hash())
	}
	if event.TransactionHash != call.Hash() || event.TransactionIndex != 0 || event.EventIndex != 0 || event.LogIndex != 0 {
		t.Fatalf("event location = %+v", event)
	}
	if event.Event.Attributes["count"] != "3" {
		t.Fatalf("event attributes = %+v", event.Event.Attributes)
	}
}

func TestNodePersistsGovernanceProposalAndParam(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	dataDir := t.TempDir()

	first, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
		DataDir:        dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	stake := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxStake,
		From:     alice,
		Nonce:    0,
		Value:    500,
		GasLimit: 30_000,
		GasPrice: 1,
	})
	if err := first.SubmitTx(stake); err != nil {
		t.Fatal(err)
	}
	if _, err := first.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	submit := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxProposalSubmit,
		From:     alice,
		Nonce:    1,
		GasLimit: 35_000,
		GasPrice: 1,
		Payload: map[string]string{
			"title":         "Enable majority quorum",
			"kind":          "param.change",
			"param":         "governance.quorum",
			"value":         "majority",
			"voting_period": "2",
		},
	})
	if err := first.SubmitTx(submit); err != nil {
		t.Fatal(err)
	}
	submitBlock, err := first.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	proposalID := submitBlock.Receipts[0].ProposalID
	if proposalID == "" {
		t.Fatalf("submit receipt = %+v", submitBlock.Receipts[0])
	}

	vote := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxVote,
		From:     alice,
		Nonce:    2,
		GasLimit: 25_000,
		GasPrice: 1,
		Payload: map[string]string{
			"proposal": proposalID,
			"choice":   "yes",
		},
	})
	if err := first.SubmitTx(vote); err != nil {
		t.Fatal(err)
	}
	if _, err := first.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	execute := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxProposalExecute,
		From:     alice,
		Nonce:    3,
		GasLimit: 35_000,
		GasPrice: 1,
		Payload: map[string]string{
			"proposal": proposalID,
		},
	})
	if err := first.SubmitTx(execute); err != nil {
		t.Fatal(err)
	}
	if _, err := first.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
		DataDir:        dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	proposal := reloaded.Proposal(proposalID)
	if proposal.Status != types.ProposalStatusExecuted {
		t.Fatalf("reloaded proposal = %+v", proposal)
	}
	if got := reloaded.Param("governance.quorum"); got != "majority" {
		t.Fatalf("reloaded param = %q", got)
	}
}

func TestNodePersistsUploadedWASMCode(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	dataDir := t.TempDir()

	first, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
		DataDir:        dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	bytecode := contracts.WasmEchoCode()
	upload := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxWASMUpload,
		From:     alice,
		Nonce:    0,
		GasLimit: core.EstimateWASMUploadGas(bytecode),
		GasPrice: 1,
		Payload: map[string]string{
			"bytecode": "0x" + hex.EncodeToString(bytecode),
		},
	})
	if err := first.SubmitTx(upload); err != nil {
		t.Fatal(err)
	}
	uploadBlock, err := first.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	codeID := uploadBlock.Receipts[0].CodeID
	if codeID == "" {
		t.Fatalf("upload receipt = %+v", uploadBlock.Receipts[0])
	}

	deploy := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     alice,
		Nonce:    1,
		GasLimit: 90_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": codeID,
			"message": "persisted",
		},
	})
	if err := first.SubmitTx(deploy); err != nil {
		t.Fatal(err)
	}
	deployBlock, err := first.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	contract := deployBlock.Receipts[0].ContractAddress

	reloaded, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
		DataDir:        dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	value, err := reloaded.ReadContract(alice, contract, "get", nil)
	if err != nil {
		t.Fatal(err)
	}
	if value != "persisted" {
		t.Fatalf("reloaded wasm read = %q", value)
	}
}

func TestNodeImportsValidatedBlockFromPeer(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	genesisBalances := map[string]uint64{alice: 1_000_000}

	producer, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: genesisBalances,
	})
	if err != nil {
		t.Fatal(err)
	}
	follower, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: genesisBalances,
		Validators:     []string{alice},
	})
	if err != nil {
		t.Fatal(err)
	}

	tx := types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       bob,
		Nonce:    0,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	}
	sig, err := chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = sig
	if err := producer.SubmitTx(tx); err != nil {
		t.Fatal(err)
	}
	block, err := producer.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}

	if err := follower.ImportBlock(block); err != nil {
		t.Fatal(err)
	}
	if follower.Head().Hash() != block.Hash() {
		t.Fatal("follower head should match imported block")
	}
	if got := follower.Account(bob).Balance; got != 100 {
		t.Fatalf("follower bob balance = %d", got)
	}
}

func TestNodeReorgsToLongerImportedBranch(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	carol := "0xcccccccccccccccccccccccccccccccccccccccc"
	genesisBalances := map[string]uint64{alice: 1_000_000}

	branchA, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: genesisBalances,
		Validators:     []string{alice},
	})
	if err != nil {
		t.Fatal(err)
	}
	branchB, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: genesisBalances,
		Validators:     []string{alice},
	})
	if err != nil {
		t.Fatal(err)
	}
	follower, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: genesisBalances,
		Validators:     []string{alice},
	})
	if err != nil {
		t.Fatal(err)
	}

	txA := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       bob,
		Nonce:    0,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if err := branchA.SubmitTx(txA); err != nil {
		t.Fatal(err)
	}
	blockA1, err := branchA.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}

	txB := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       carol,
		Nonce:    0,
		Value:    200,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if err := branchB.SubmitTx(txB); err != nil {
		t.Fatal(err)
	}
	blockB1, err := branchB.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	blockB2, err := branchB.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}

	if err := follower.ImportBlock(blockA1); err != nil {
		t.Fatal(err)
	}
	if follower.Head().Hash() != blockA1.Hash() {
		t.Fatal("follower should adopt first imported branch")
	}
	if got := follower.Account(bob).Balance; got != 100 {
		t.Fatalf("bob balance before reorg = %d", got)
	}
	if _, ok := follower.Transaction(txA.Hash()); !ok {
		t.Fatal("canonical branch A transaction should be indexed before reorg")
	}

	if err := follower.ImportBlock(blockB1); err != nil {
		t.Fatal(err)
	}
	if follower.Head().Hash() != blockA1.Hash() {
		t.Fatal("equal-height side branch should not replace canonical head")
	}
	if got := follower.Account(carol).Balance; got != 0 {
		t.Fatalf("carol balance before longer branch = %d", got)
	}

	if err := follower.ImportBlock(blockB2); err != nil {
		t.Fatal(err)
	}
	if follower.Head().Hash() != blockB2.Hash() {
		t.Fatal("longer side branch should become canonical head")
	}
	if got := follower.Account(bob).Balance; got != 0 {
		t.Fatalf("bob balance after reorg = %d", got)
	}
	if got := follower.Account(carol).Balance; got != 200 {
		t.Fatalf("carol balance after reorg = %d", got)
	}
	if _, ok := follower.Transaction(txA.Hash()); ok {
		t.Fatal("old branch transaction should leave canonical tx index after reorg")
	}
	if record, ok := follower.Transaction(txB.Hash()); !ok || record.BlockHash != blockB1.Hash() || record.BlockHeight != 1 {
		t.Fatalf("new canonical transaction record = %+v ok=%v", record, ok)
	}
}

func TestNodeProducesOnlyWhenLocalValidatorIsScheduled(t *testing.T) {
	keyA, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorA := chaincrypto.AddressFromPrivateKey(keyA)
	validatorB := chaincrypto.AddressFromPrivateKey(keyB)
	genesisBalances := map[string]uint64{validatorA: 1_000_000}
	validators := []string{validatorA, validatorB}

	nodeA, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyA,
		GenesisBalance: genesisBalances,
		Validators:     validators,
	})
	if err != nil {
		t.Fatal(err)
	}
	nodeB, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyB,
		GenesisBalance: genesisBalances,
		Validators:     validators,
	})
	if err != nil {
		t.Fatal(err)
	}

	block1, err := nodeA.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if block1.Header.Proposer != validatorA {
		t.Fatalf("height 1 proposer = %q", block1.Header.Proposer)
	}
	if _, err := nodeA.ProduceBlock(); err == nil {
		t.Fatal("validator A should not produce height 2")
	}

	if err := nodeB.ImportBlock(block1); err != nil {
		t.Fatal(err)
	}
	block2, err := nodeB.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if block2.Header.Height != 2 {
		t.Fatalf("height = %d", block2.Header.Height)
	}
	if block2.Header.Proposer != validatorB {
		t.Fatalf("height 2 proposer = %q", block2.Header.Proposer)
	}
}

func TestValidatorJoinUpdatesNextBlockScheduleAndPersists(t *testing.T) {
	keyA, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorA := chaincrypto.AddressFromPrivateKey(keyA)
	validatorB := chaincrypto.AddressFromPrivateKey(keyB)
	genesisBalances := map[string]uint64{
		validatorA: 1_000_000,
		validatorB: 1_000_000,
	}
	dataDirA := t.TempDir()
	dataDirB := t.TempDir()

	nodeA, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyA,
		GenesisBalance: genesisBalances,
		Validators:     []string{validatorA},
		DataDir:        dataDirA,
	})
	if err != nil {
		t.Fatal(err)
	}
	nodeB, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyB,
		GenesisBalance: genesisBalances,
		Validators:     []string{validatorA},
		DataDir:        dataDirB,
	})
	if err != nil {
		t.Fatal(err)
	}

	stake := signedNodeTx(t, keyB, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxStake,
		From:     validatorB,
		Nonce:    0,
		Value:    500,
		GasLimit: 30_000,
		GasPrice: 1,
	})
	join := signedNodeTx(t, keyB, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxValidatorJoin,
		From:     validatorB,
		Nonce:    1,
		GasLimit: 40_000,
		GasPrice: 1,
	})
	if err := nodeA.SubmitTx(stake); err != nil {
		t.Fatal(err)
	}
	if err := nodeA.SubmitTx(join); err != nil {
		t.Fatal(err)
	}
	block1, err := nodeA.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if block1.Header.Proposer != validatorA {
		t.Fatalf("height 1 proposer = %q", block1.Header.Proposer)
	}
	if _, err := nodeA.ProduceBlock(); err == nil {
		t.Fatal("validator A should not produce height 2 after validator B joins")
	}

	if err := nodeB.ImportBlock(block1); err != nil {
		t.Fatal(err)
	}
	reloadedB, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyB,
		GenesisBalance: genesisBalances,
		Validators:     []string{validatorA},
		DataDir:        dataDirB,
	})
	if err != nil {
		t.Fatal(err)
	}
	block2, err := reloadedB.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if block2.Header.Height != 2 || block2.Header.Proposer != validatorB {
		t.Fatalf("height 2 block = %+v", block2.Header)
	}
}

func TestValidatorLeaveUpdatesNextBlockScheduleAndPersists(t *testing.T) {
	keyA, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorA := chaincrypto.AddressFromPrivateKey(keyA)
	validatorB := chaincrypto.AddressFromPrivateKey(keyB)
	genesisBalances := map[string]uint64{
		validatorA: 1_000_000,
		validatorB: 1_000_000,
	}
	dataDirA := t.TempDir()
	dataDirB := t.TempDir()

	nodeA, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyA,
		GenesisBalance: genesisBalances,
		Validators:     []string{validatorA, validatorB},
		DataDir:        dataDirA,
	})
	if err != nil {
		t.Fatal(err)
	}
	nodeB, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyB,
		GenesisBalance: genesisBalances,
		Validators:     []string{validatorA, validatorB},
		DataDir:        dataDirB,
	})
	if err != nil {
		t.Fatal(err)
	}

	leave := signedNodeTx(t, keyB, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxValidatorLeave,
		From:     validatorB,
		Nonce:    0,
		GasLimit: 40_000,
		GasPrice: 1,
	})
	if err := nodeA.SubmitTx(leave); err != nil {
		t.Fatal(err)
	}
	block1, err := nodeA.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if block1.Header.Proposer != validatorA {
		t.Fatalf("height 1 proposer = %q", block1.Header.Proposer)
	}
	if err := nodeB.ImportBlock(block1); err != nil {
		t.Fatal(err)
	}

	reloadedA, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyA,
		GenesisBalance: genesisBalances,
		Validators:     []string{validatorA, validatorB},
		DataDir:        dataDirA,
	})
	if err != nil {
		t.Fatal(err)
	}
	block2, err := reloadedA.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if block2.Header.Height != 2 || block2.Header.Proposer != validatorA {
		t.Fatalf("height 2 block = %+v", block2.Header)
	}
	if _, err := nodeB.ProduceBlock(); err == nil {
		t.Fatal("validator B should not produce after leaving")
	}
}

func TestValidatorSlashUpdatesNextBlockScheduleAndPersists(t *testing.T) {
	keyA, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorA := chaincrypto.AddressFromPrivateKey(keyA)
	validatorB := chaincrypto.AddressFromPrivateKey(keyB)
	genesisBalances := map[string]uint64{
		validatorA: 1_000_000,
		validatorB: 1_000_000,
	}
	dataDirA := t.TempDir()
	dataDirB := t.TempDir()

	nodeA, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyA,
		GenesisBalance: genesisBalances,
		Validators:     []string{validatorA, validatorB},
		DataDir:        dataDirA,
	})
	if err != nil {
		t.Fatal(err)
	}
	nodeB, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyB,
		GenesisBalance: genesisBalances,
		Validators:     []string{validatorA, validatorB},
		DataDir:        dataDirB,
	})
	if err != nil {
		t.Fatal(err)
	}

	stakeB := signedNodeTx(t, keyB, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxStake,
		From:     validatorB,
		Nonce:    0,
		Value:    500,
		GasLimit: 30_000,
		GasPrice: 1,
	})
	slashB := signedNodeTx(t, keyA, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxValidatorSlash,
		From:     validatorA,
		Nonce:    0,
		GasLimit: 45_000,
		GasPrice: 1,
		Payload: map[string]string{
			"target":   validatorB,
			"amount":   "500",
			"evidence": "double-sign-height-4",
		},
	})
	if err := nodeA.SubmitTx(stakeB); err != nil {
		t.Fatal(err)
	}
	if err := nodeA.SubmitTx(slashB); err != nil {
		t.Fatal(err)
	}
	block1, err := nodeA.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if block1.Header.Proposer != validatorA {
		t.Fatalf("height 1 proposer = %q", block1.Header.Proposer)
	}
	if err := nodeB.ImportBlock(block1); err != nil {
		t.Fatal(err)
	}

	reloadedA, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyA,
		GenesisBalance: genesisBalances,
		Validators:     []string{validatorA, validatorB},
		DataDir:        dataDirA,
	})
	if err != nil {
		t.Fatal(err)
	}
	if validators := reloadedA.Validators(); len(validators) != 1 || validators[0] != validatorA {
		t.Fatalf("validators = %#v", validators)
	}
	block2, err := reloadedA.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if block2.Header.Height != 2 || block2.Header.Proposer != validatorA {
		t.Fatalf("height 2 block = %+v", block2.Header)
	}
	if _, err := nodeB.ProduceBlock(); err == nil {
		t.Fatal("validator B should not produce after slashing removed it")
	}
}

func TestNodeImportKnownCanonicalBlockIsIdempotent(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validator := chaincrypto.AddressFromPrivateKey(key)
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{validator: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}

	block1, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	if err := n.ImportBlock(block1); err != nil {
		t.Fatal(err)
	}
	if n.Head().Header.Height != 2 {
		t.Fatalf("head height = %d", n.Head().Header.Height)
	}
}

func TestNodeFinalityUsesConservativeBlockDepths(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validator := chaincrypto.AddressFromPrivateKey(key)
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{validator: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	genesis := n.Head()

	initial := n.Finality()
	if initial.HeadHeight != 0 || initial.FinalizedHeight != 0 || initial.SafeHeight != 0 {
		t.Fatalf("initial finality = %+v", initial)
	}
	if initial.FinalizedHash != genesis.Hash() || initial.SafeHash != genesis.Hash() {
		t.Fatalf("initial hashes = %+v, genesis hash %s", initial, genesis.Hash())
	}

	var blocks []types.Block
	for i := 0; i < 4; i++ {
		block, err := n.ProduceBlock()
		if err != nil {
			t.Fatal(err)
		}
		blocks = append(blocks, block)
	}

	finality := n.Finality()
	if finality.HeadHeight != 4 || finality.HeadHash != blocks[3].Hash() {
		t.Fatalf("head finality = %+v", finality)
	}
	if finality.SafeHeight != 3 || finality.SafeHash != blocks[2].Hash() {
		t.Fatalf("safe finality = %+v, want height 3 hash %s", finality, blocks[2].Hash())
	}
	if finality.FinalizedHeight != 2 || finality.FinalizedHash != blocks[1].Hash() {
		t.Fatalf("finalized finality = %+v, want height 2 hash %s", finality, blocks[1].Hash())
	}
	if finality.SafeDepth != 1 || finality.FinalizedDepth != 2 {
		t.Fatalf("depths = safe %d finalized %d", finality.SafeDepth, finality.FinalizedDepth)
	}
}

func TestNodeFinalityUsesBFTCertificateWhenQuorumVotesCommitBlock(t *testing.T) {
	keyA, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyC, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorA := chaincrypto.AddressFromPrivateKey(keyA)
	validatorB := chaincrypto.AddressFromPrivateKey(keyB)
	validatorC := chaincrypto.AddressFromPrivateKey(keyC)
	validators := []string{validatorA, validatorB, validatorC}
	dataDir := t.TempDir()
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyA,
		GenesisBalance: map[string]uint64{validatorA: 1_000_000},
		Validators:     validators,
		DataDir:        dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	before := n.Finality()
	if before.CertifiedHeight != 0 || before.FinalizedSource != "depth_fallback" {
		t.Fatalf("finality before votes = %+v", before)
	}

	for _, key := range []chaincrypto.PrivateKey{keyA, keyB, keyC} {
		vote, err := consensus.SignFinalityVote(key, block)
		if err != nil {
			t.Fatal(err)
		}
		if err := n.SubmitFinalityVote(vote); err != nil {
			t.Fatal(err)
		}
	}

	certifiedBlock, ok := n.Block(block.Header.Height)
	if !ok {
		t.Fatal("certified block should exist")
	}
	if certifiedBlock.FinalityCertificate == nil {
		t.Fatal("block should have a finality certificate after quorum votes")
	}
	if len(certifiedBlock.FinalityCertificate.Signatures) != 3 {
		t.Fatalf("certificate signatures = %+v", certifiedBlock.FinalityCertificate.Signatures)
	}
	finality := n.Finality()
	if finality.SafeHeight != block.Header.Height || finality.FinalizedHeight != block.Header.Height {
		t.Fatalf("certified finality = %+v", finality)
	}
	if finality.SafeSource != "bft_certificate" || finality.FinalizedSource != "bft_certificate" {
		t.Fatalf("finality sources = %+v", finality)
	}
	if finality.CertifiedHash != block.Hash() || finality.CertifiedSigners != 3 || finality.CertifiedQuorum != 3 {
		t.Fatalf("certificate summary = %+v", finality)
	}

	reloaded, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyA,
		GenesisBalance: map[string]uint64{validatorA: 1_000_000},
		Validators:     validators,
		DataDir:        dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	reloadedFinality := reloaded.Finality()
	if reloadedFinality.FinalizedHeight != block.Header.Height || reloadedFinality.FinalizedSource != "bft_certificate" {
		t.Fatalf("reloaded finality = %+v", reloadedFinality)
	}
}

func TestNodeRecordsFinalityEquivocationEvidence(t *testing.T) {
	keyA, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyC, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorA := chaincrypto.AddressFromPrivateKey(keyA)
	validatorB := chaincrypto.AddressFromPrivateKey(keyB)
	validatorC := chaincrypto.AddressFromPrivateKey(keyC)
	validators := []string{validatorA, validatorB, validatorC}
	dataDir := t.TempDir()
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyA,
		GenesisBalance: map[string]uint64{validatorA: 1_000_000},
		Validators:     validators,
		DataDir:        dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	blockA1, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	voteA1, err := consensus.SignFinalityVote(keyA, blockA1)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.SubmitFinalityVote(voteA1); err != nil {
		t.Fatal(err)
	}

	genesis, ok := n.Block(0)
	if !ok {
		t.Fatal("genesis should exist")
	}
	blockB1 := signedEmptyNodeBlock(t, keyA, genesis, validatorA, 1, blockA1.Header.TimeUnix+10)
	blockB2 := signedEmptyNodeBlock(t, keyB, blockB1, validatorB, 2, blockA1.Header.TimeUnix+20)
	if err := n.ImportBlock(blockB1); err != nil {
		t.Fatal(err)
	}
	if err := n.ImportBlock(blockB2); err != nil {
		t.Fatal(err)
	}
	if n.Head().Hash() != blockB2.Hash() {
		t.Fatal("longer branch should become canonical before conflicting vote")
	}

	voteB1, err := consensus.SignFinalityVote(keyA, blockB1)
	if err != nil {
		t.Fatal(err)
	}
	err = n.SubmitFinalityVote(voteB1)
	if err == nil || !strings.Contains(err.Error(), "equivocation") {
		t.Fatalf("conflicting finality vote error = %v", err)
	}
	evidence := n.FinalityEvidence()
	if len(evidence) != 1 {
		t.Fatalf("evidence = %+v", evidence)
	}
	got := evidence[0]
	if got.Validator != validatorA || got.Height != 1 {
		t.Fatalf("evidence identity = %+v", got)
	}
	if got.FirstBlockHash != blockA1.Hash() || got.SecondBlockHash != blockB1.Hash() {
		t.Fatalf("evidence hashes = %+v, want %s then %s", got, blockA1.Hash(), blockB1.Hash())
	}
	if got.FirstSignature != voteA1.Signature || got.SecondSignature != voteB1.Signature {
		t.Fatalf("evidence signatures = %+v", got)
	}

	reloaded, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyA,
		GenesisBalance: map[string]uint64{validatorA: 1_000_000},
		Validators:     validators,
		DataDir:        dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if reloadedEvidence := reloaded.FinalityEvidence(); len(reloadedEvidence) != 1 || reloadedEvidence[0].SecondBlockHash != blockB1.Hash() {
		t.Fatalf("reloaded evidence = %+v", reloadedEvidence)
	}
	if err := reloaded.SubmitFinalityVote(voteB1); err == nil || !strings.Contains(err.Error(), "equivocation") {
		t.Fatalf("reloaded conflicting finality vote error = %v", err)
	}
}

func TestNodeAutoSlashesFinalityEquivocationEvidence(t *testing.T) {
	keyA, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorA := chaincrypto.AddressFromPrivateKey(keyA)
	validatorB := chaincrypto.AddressFromPrivateKey(keyB)
	validators := []string{validatorA, validatorB}
	genesisBalances := map[string]uint64{
		validatorA: 1_000_000,
		validatorB: 1_000_000,
	}

	producerA, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyA,
		GenesisBalance: genesisBalances,
		Validators:     validators,
	})
	if err != nil {
		t.Fatal(err)
	}
	observerB, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyB,
		GenesisBalance: genesisBalances,
		Validators:     validators,
	})
	if err != nil {
		t.Fatal(err)
	}

	stakeA := signedNodeTx(t, keyA, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxStake,
		From:     validatorA,
		Nonce:    0,
		Value:    500,
		GasLimit: 30_000,
		GasPrice: 1,
	})
	if err := producerA.SubmitTx(stakeA); err != nil {
		t.Fatal(err)
	}
	commonBlock1, err := producerA.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := observerB.ImportBlock(commonBlock1); err != nil {
		t.Fatal(err)
	}
	if got := observerB.StakeOf(validatorA); got != 500 {
		t.Fatalf("validator A stake = %d", got)
	}

	blockA2, err := observerB.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	voteA2, err := consensus.SignFinalityVote(keyA, blockA2)
	if err != nil {
		t.Fatal(err)
	}
	if err := observerB.SubmitFinalityVote(voteA2); err != nil {
		t.Fatal(err)
	}

	blockB2 := signedEmptyNodeBlock(t, keyB, commonBlock1, validatorB, 2, blockA2.Header.TimeUnix+10)
	blockB3 := signedEmptyNodeBlock(t, keyA, blockB2, validatorA, 3, blockA2.Header.TimeUnix+20)
	if err := observerB.ImportBlock(blockB2); err != nil {
		t.Fatal(err)
	}
	if err := observerB.ImportBlock(blockB3); err != nil {
		t.Fatal(err)
	}
	if observerB.Head().Hash() != blockB3.Hash() {
		t.Fatal("longer branch should become canonical before conflicting vote")
	}

	voteB2, err := consensus.SignFinalityVote(keyA, blockB2)
	if err != nil {
		t.Fatal(err)
	}
	err = observerB.SubmitFinalityVote(voteB2)
	if err == nil || !strings.Contains(err.Error(), "equivocation") {
		t.Fatalf("conflicting finality vote error = %v", err)
	}
	pool := observerB.Mempool()
	if len(pool) != 1 {
		t.Fatalf("mempool = %#v", pool)
	}
	slashTx := pool[0]
	if slashTx.Type != types.TxValidatorSlash || slashTx.From != validatorB {
		t.Fatalf("auto slash tx = %+v", slashTx)
	}
	if slashTx.Payload["target"] != validatorA || slashTx.Payload["amount"] != "500" || !strings.Contains(slashTx.Payload["evidence"], blockB2.Hash()) {
		t.Fatalf("auto slash payload = %+v", slashTx.Payload)
	}
	if !chaincrypto.Verify(validatorB, slashTx.SigningBytes(), slashTx.Signature) {
		t.Fatal("auto slash tx should be signed by local validator")
	}

	slashBlock, err := observerB.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(slashBlock.Transactions) != 1 || slashBlock.Transactions[0].Type != types.TxValidatorSlash {
		t.Fatalf("slash block transactions = %+v", slashBlock.Transactions)
	}
	if got := observerB.StakeOf(validatorA); got != 0 {
		t.Fatalf("validator A stake after auto slash = %d", got)
	}
	if validators := observerB.Validators(); len(validators) != 1 || validators[0] != validatorB {
		t.Fatalf("validators after auto slash = %#v", validators)
	}
}

func TestNodePendingAccountIncludesMempoolTransactions(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}

	tx := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       bob,
		Nonce:    0,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if err := n.SubmitTx(tx); err != nil {
		t.Fatal(err)
	}

	latest := n.Account(alice)
	if latest.Nonce != 0 {
		t.Fatalf("latest nonce = %d", latest.Nonce)
	}
	pending, err := n.PendingAccount(alice)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Nonce != 1 {
		t.Fatalf("pending nonce = %d", pending.Nonce)
	}
	pool := n.Mempool()
	if len(pool) != 1 || pool[0].Hash() != tx.Hash() {
		t.Fatalf("mempool = %#v", pool)
	}
}

func TestNodeReplacesPendingTransactionWithHigherFeeSameNonce(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	carol := "0xcccccccccccccccccccccccccccccccccccccccc"
	dave := "0xdddddddddddddddddddddddddddddddddddddddd"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}

	original := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       bob,
		Nonce:    0,
		Value:    10,
		GasLimit: 21_000,
		GasPrice: 10,
	})
	next := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       dave,
		Nonce:    1,
		Value:    5,
		GasLimit: 21_000,
		GasPrice: 10,
	})
	replacement := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       carol,
		Nonce:    0,
		Value:    20,
		GasLimit: 21_000,
		GasPrice: 11,
	})

	if err := n.SubmitTx(original); err != nil {
		t.Fatal(err)
	}
	if err := n.SubmitTx(next); err != nil {
		t.Fatal(err)
	}
	if err := n.SubmitTx(replacement); err != nil {
		t.Fatal(err)
	}

	pool := n.Mempool()
	if len(pool) != 2 {
		t.Fatalf("mempool = %#v", pool)
	}
	if pool[0].Hash() != replacement.Hash() || pool[1].Hash() != next.Hash() {
		t.Fatalf("mempool order = %#v", pool)
	}
	pending, err := n.PendingAccount(alice)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Nonce != 2 {
		t.Fatalf("pending nonce = %d", pending.Nonce)
	}

	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 2 || block.Transactions[0].Hash() != replacement.Hash() || block.Transactions[1].Hash() != next.Hash() {
		t.Fatalf("block transactions = %#v", block.Transactions)
	}
	if got := n.Account(bob).Balance; got != 0 {
		t.Fatalf("bob balance = %d", got)
	}
	if got := n.Account(carol).Balance; got != 20 {
		t.Fatalf("carol balance = %d", got)
	}
	if got := n.Account(dave).Balance; got != 5 {
		t.Fatalf("dave balance = %d", got)
	}
}

func TestNodeRejectsUnderpricedPendingReplacement(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	carol := "0xcccccccccccccccccccccccccccccccccccccccc"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}

	original := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       bob,
		Nonce:    0,
		Value:    10,
		GasLimit: 21_000,
		GasPrice: 10,
	})
	underpriced := signedNodeTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       carol,
		Nonce:    0,
		Value:    20,
		GasLimit: 21_000,
		GasPrice: 10,
	})

	if err := n.SubmitTx(original); err != nil {
		t.Fatal(err)
	}
	err = n.SubmitTx(underpriced)
	if err == nil || !strings.Contains(err.Error(), "replacement transaction underpriced") {
		t.Fatalf("underpriced replacement error = %v", err)
	}
	pool := n.Mempool()
	if len(pool) != 1 || pool[0].Hash() != original.Hash() {
		t.Fatalf("mempool = %#v", pool)
	}
}

func TestNodeEIP1559ReplacementRequiresBothFeeCapsBumped(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	carol := "0xcccccccccccccccccccccccccccccccccccccccc"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}

	original := signedNodeTx(t, key, types.Transaction{
		ChainID:              "chainlab-local",
		Type:                 types.TxTransfer,
		From:                 alice,
		To:                   bob,
		Nonce:                0,
		Value:                10,
		GasLimit:             21_000,
		MaxFeePerGas:         20,
		MaxPriorityFeePerGas: 10,
	})
	lowTip := signedNodeTx(t, key, types.Transaction{
		ChainID:              "chainlab-local",
		Type:                 types.TxTransfer,
		From:                 alice,
		To:                   carol,
		Nonce:                0,
		Value:                20,
		GasLimit:             21_000,
		MaxFeePerGas:         22,
		MaxPriorityFeePerGas: 10,
	})
	bumped := signedNodeTx(t, key, types.Transaction{
		ChainID:              "chainlab-local",
		Type:                 types.TxTransfer,
		From:                 alice,
		To:                   carol,
		Nonce:                0,
		Value:                20,
		GasLimit:             21_000,
		MaxFeePerGas:         22,
		MaxPriorityFeePerGas: 11,
	})

	if err := n.SubmitTx(original); err != nil {
		t.Fatal(err)
	}
	err = n.SubmitTx(lowTip)
	if err == nil || !strings.Contains(err.Error(), "replacement transaction underpriced") {
		t.Fatalf("low tip replacement error = %v", err)
	}
	if err := n.SubmitTx(bumped); err != nil {
		t.Fatal(err)
	}
	pool := n.Mempool()
	if len(pool) != 1 || pool[0].Hash() != bumped.Hash() {
		t.Fatalf("mempool = %#v", pool)
	}
}

func TestNodeFaucetRequestsSignedTransferThroughMempool(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := chaincrypto.AddressFromPrivateKey(key)
	recipient := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{proposer: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}

	tx, err := n.RequestFaucet(recipient, 250)
	if err != nil {
		t.Fatal(err)
	}
	if tx.Type != types.TxTransfer || tx.From != proposer || tx.To != recipient || tx.Value != 250 {
		t.Fatalf("faucet tx = %+v", tx)
	}
	if !chaincrypto.Verify(proposer, tx.SigningBytes(), tx.Signature) {
		t.Fatal("faucet tx signature should verify")
	}
	pool := n.Mempool()
	if len(pool) != 1 || pool[0].Hash() != tx.Hash() {
		t.Fatalf("mempool = %#v", pool)
	}
	pending, err := n.PendingAccount(proposer)
	if err != nil {
		t.Fatal(err)
	}
	if pending.Nonce != 1 {
		t.Fatalf("pending proposer nonce = %d", pending.Nonce)
	}

	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	if got := n.Account(recipient).Balance; got != 250 {
		t.Fatalf("recipient balance = %d", got)
	}
}

func signedNodeTx(t *testing.T, key chaincrypto.PrivateKey, tx types.Transaction) types.Transaction {
	t.Helper()
	sig, err := chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = sig
	return tx
}

func sponsoredNodeTx(t *testing.T, userKey chaincrypto.PrivateKey, paymasterKey chaincrypto.PrivateKey, tx types.Transaction) types.Transaction {
	t.Helper()
	tx = signedNodeTx(t, userKey, tx)
	signature, err := chaincrypto.Sign(paymasterKey, tx.PaymasterSigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.PaymasterSignature = signature
	return tx
}

func authorizedNodeTx(t *testing.T, keys []chaincrypto.PrivateKey, tx types.Transaction) types.Transaction {
	t.Helper()
	tx.Authorizations = make([]types.Authorization, len(keys))
	for i, key := range keys {
		tx.Authorizations[i].Signer = chaincrypto.AddressFromPrivateKey(key)
	}
	for i, key := range keys {
		signature, err := chaincrypto.Sign(key, tx.SigningBytes())
		if err != nil {
			t.Fatal(err)
		}
		tx.Authorizations[i].Signature = signature
	}
	return tx
}

func signedEmptyNodeBlock(t *testing.T, key chaincrypto.PrivateKey, parent types.Block, proposer string, height uint64, timeUnix int64) types.Block {
	t.Helper()
	block := types.Block{
		Header: types.BlockHeader{
			ChainID:       parent.Header.ChainID,
			Height:        height,
			ParentHash:    parent.Hash(),
			TimeUnix:      timeUnix,
			Proposer:      proposer,
			GasLimit:      node.DefaultBlockGasLimit,
			GasUsed:       0,
			BaseFeePerGas: node.NextBaseFee(parent, node.DefaultBlockGasLimit),
			TxRoot:        types.TransactionRoot(nil),
			ReceiptRoot:   types.ReceiptRoot(nil),
			StateRoot:     parent.Header.StateRoot,
		},
	}
	if err := consensus.SignBlock(key, &block); err != nil {
		t.Fatal(err)
	}
	return block
}

func TestNodeRejectsImportedBlockWithBadStateRoot(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	genesisBalances := map[string]uint64{alice: 1_000_000}

	producer, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: genesisBalances,
	})
	if err != nil {
		t.Fatal(err)
	}
	follower, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: genesisBalances,
		Validators:     []string{alice},
	})
	if err != nil {
		t.Fatal(err)
	}

	tx := types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       bob,
		Nonce:    0,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	}
	sig, err := chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = sig
	if err := producer.SubmitTx(tx); err != nil {
		t.Fatal(err)
	}
	block, err := producer.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	block.Header.StateRoot = "0xdeadbeef"

	if err := follower.ImportBlock(block); err == nil {
		t.Fatal("bad imported state root should fail")
	}
	if got := follower.Account(bob).Balance; got != 0 {
		t.Fatalf("follower state should not mutate on rejected block, bob balance = %d", got)
	}
}
