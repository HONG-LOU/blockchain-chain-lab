package consensus_test

import (
	"testing"
	"time"

	"chainlab/internal/consensus"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/types"
)

func TestValidateSignedPoABlock(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := chaincrypto.AddressFromPrivateKey(key)
	engine := consensus.NewPOA([]string{proposer})
	genesis := types.GenesisBlock("chainlab-local", "0xstate")
	block := types.Block{
		Header: types.BlockHeader{
			ChainID:     "chainlab-local",
			Height:      1,
			ParentHash:  genesis.Hash(),
			TimeUnix:    time.Unix(1, 0).Unix(),
			Proposer:    proposer,
			TxRoot:      types.TransactionRoot(nil),
			ReceiptRoot: types.ReceiptRoot(nil),
			StateRoot:   "0xstate2",
		},
	}
	if err := consensus.SignBlock(key, &block); err != nil {
		t.Fatal(err)
	}

	if err := engine.ValidateBlock(genesis, block); err != nil {
		t.Fatal(err)
	}

	block.Header.Proposer = "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := engine.ValidateBlock(genesis, block); err == nil {
		t.Fatal("unauthorized proposer should fail")
	}
}

func TestValidateBlockRequiresScheduledProposer(t *testing.T) {
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
	engine := consensus.NewPOA([]string{validatorA, validatorB})
	genesis := types.GenesisBlock("chainlab-local", "0xstate")

	block1 := signedTestBlock(t, keyA, genesis, validatorA, 1)
	if err := engine.ValidateBlock(genesis, block1); err != nil {
		t.Fatal(err)
	}

	wrongBlock1 := signedTestBlock(t, keyB, genesis, validatorB, 1)
	if err := engine.ValidateBlock(genesis, wrongBlock1); err == nil {
		t.Fatal("height 1 should reject authorized but unscheduled proposer")
	}

	block2 := signedTestBlock(t, keyB, block1, validatorB, 2)
	if err := engine.ValidateBlock(block1, block2); err != nil {
		t.Fatal(err)
	}

	wrongBlock2 := signedTestBlock(t, keyA, block1, validatorA, 2)
	if err := engine.ValidateBlock(block1, wrongBlock2); err == nil {
		t.Fatal("height 2 should reject authorized but unscheduled proposer")
	}
}

func TestValidateBlockRejectsBadRootsAndParent(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := chaincrypto.AddressFromPrivateKey(key)
	engine := consensus.NewPOA([]string{proposer})
	genesis := types.GenesisBlock("chainlab-local", "0xstate")
	block := types.Block{
		Header: types.BlockHeader{
			ChainID:     "chainlab-local",
			Height:      1,
			ParentHash:  genesis.Hash(),
			TimeUnix:    time.Unix(1, 0).Unix(),
			Proposer:    proposer,
			TxRoot:      "0xwrong",
			ReceiptRoot: types.ReceiptRoot(nil),
			StateRoot:   "0xstate2",
		},
	}
	if err := consensus.SignBlock(key, &block); err != nil {
		t.Fatal(err)
	}
	if err := engine.ValidateBlock(genesis, block); err == nil {
		t.Fatal("bad transaction root should fail")
	}

	block.Header.TxRoot = types.TransactionRoot(nil)
	block.Header.ParentHash = "0xwrong"
	if err := consensus.SignBlock(key, &block); err != nil {
		t.Fatal(err)
	}
	if err := engine.ValidateBlock(genesis, block); err == nil {
		t.Fatal("bad parent hash should fail")
	}
}

func signedTestBlock(t *testing.T, key chaincrypto.PrivateKey, parent types.Block, proposer string, height uint64) types.Block {
	t.Helper()
	block := types.Block{
		Header: types.BlockHeader{
			ChainID:     "chainlab-local",
			Height:      height,
			ParentHash:  parent.Hash(),
			TimeUnix:    time.Unix(int64(height), 0).Unix(),
			Proposer:    proposer,
			TxRoot:      types.TransactionRoot(nil),
			ReceiptRoot: types.ReceiptRoot(nil),
			StateRoot:   "0xstate-next",
		},
	}
	if err := consensus.SignBlock(key, &block); err != nil {
		t.Fatal(err)
	}
	return block
}
