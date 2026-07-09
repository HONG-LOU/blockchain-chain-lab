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
