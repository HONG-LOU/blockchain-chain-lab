package node_test

import (
	"testing"

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
