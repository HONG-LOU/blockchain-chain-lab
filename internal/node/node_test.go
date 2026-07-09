package node_test

import (
	"encoding/hex"
	"testing"

	"chainlab/internal/contracts"
	"chainlab/internal/core"
	chaincrypto "chainlab/internal/crypto"
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
