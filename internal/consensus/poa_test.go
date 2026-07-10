package consensus_test

import (
	"math"
	"sort"
	"strings"
	"testing"

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
	genesis := types.GenesisBlock("chainlab-local", types.TransactionRoot(nil), 1_700_000_000)
	block := types.Block{
		Header: types.BlockHeader{
			ChainID:       "chainlab-local",
			Height:        1,
			ParentHash:    genesis.Hash(),
			TimeUnix:      genesis.Header.TimeUnix + 1,
			Proposer:      proposer,
			GasLimit:      types.DefaultBlockGasLimit,
			BaseFeePerGas: types.InitialBaseFeePerGas,
			TxRoot:        types.TransactionRoot(nil),
			ReceiptRoot:   types.ReceiptRoot(nil),
			StateRoot:     types.TransactionRoot(nil),
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

func TestValidateBlockRejectsTimestampDomainExhaustion(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := chaincrypto.AddressFromPrivateKey(key)
	engine := consensus.NewPOA([]string{proposer})
	genesis := types.GenesisBlock("chainlab-local", types.TransactionRoot(nil), 1_700_000_000)
	block := signedTestBlock(t, key, genesis, proposer, 1)
	block.Header.TimeUnix = math.MaxInt64
	if err := consensus.SignBlock(key, &block); err != nil {
		t.Fatal(err)
	}
	if err := engine.ValidateBlock(genesis, block); err == nil || !strings.Contains(err.Error(), "time step exceeds") {
		t.Fatalf("timestamp exhaustion error = %v", err)
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
	genesis := types.GenesisBlock("chainlab-local", types.TransactionRoot(nil), 1_700_000_000)

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
	genesis := types.GenesisBlock("chainlab-local", types.TransactionRoot(nil), 1_700_000_000)
	block := types.Block{
		Header: types.BlockHeader{
			ChainID:       "chainlab-local",
			Height:        1,
			ParentHash:    genesis.Hash(),
			TimeUnix:      genesis.Header.TimeUnix + 1,
			Proposer:      proposer,
			GasLimit:      types.DefaultBlockGasLimit,
			BaseFeePerGas: types.InitialBaseFeePerGas,
			TxRoot:        "0xwrong",
			ReceiptRoot:   types.ReceiptRoot(nil),
			StateRoot:     types.TransactionRoot(nil),
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

func TestValidateFinalityCertificateRequiresTwoThirdsValidatorCommits(t *testing.T) {
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
	engine := consensus.NewPOA([]string{validatorA, validatorB, validatorC})
	genesis := types.GenesisBlock("chainlab-local", types.TransactionRoot(nil), 1_700_000_000)
	block := signedTestBlock(t, keyA, genesis, validatorA, 1)

	voteA, err := consensus.SignFinalityVote(keyA, block)
	if err != nil {
		t.Fatal(err)
	}
	voteB, err := consensus.SignFinalityVote(keyB, block)
	if err != nil {
		t.Fatal(err)
	}
	voteC, err := consensus.SignFinalityVote(keyC, block)
	if err != nil {
		t.Fatal(err)
	}

	validVotes := []types.FinalitySignature{voteA, voteB, voteC}
	sort.Slice(validVotes, func(i int, j int) bool { return validVotes[i].Validator < validVotes[j].Validator })
	block.FinalityCertificate = &types.FinalityCertificate{
		ChainID:    block.Header.ChainID,
		Height:     block.Header.Height,
		BlockHash:  block.Hash(),
		Signatures: validVotes,
	}
	if err := engine.ValidateBlock(genesis, block); err != nil {
		t.Fatal(err)
	}

	insufficient := block
	insufficient.FinalityCertificate = &types.FinalityCertificate{
		ChainID:    block.Header.ChainID,
		Height:     block.Header.Height,
		BlockHash:  block.Hash(),
		Signatures: []types.FinalitySignature{voteA, voteB},
	}
	if err := engine.ValidateBlock(genesis, insufficient); err == nil {
		t.Fatal("certificate below two-thirds quorum should fail")
	}

	duplicate := block
	duplicate.FinalityCertificate = &types.FinalityCertificate{
		ChainID:    block.Header.ChainID,
		Height:     block.Header.Height,
		BlockHash:  block.Hash(),
		Signatures: []types.FinalitySignature{voteA, voteA, voteC},
	}
	if err := engine.ValidateBlock(genesis, duplicate); err == nil {
		t.Fatal("duplicate validator commits should fail")
	}

	tampered := block
	tamperedVote := voteC
	tamperedVote.Signature = voteA.Signature
	tampered.FinalityCertificate = &types.FinalityCertificate{
		ChainID:    block.Header.ChainID,
		Height:     block.Header.Height,
		BlockHash:  block.Hash(),
		Signatures: []types.FinalitySignature{voteA, voteB, tamperedVote},
	}
	if err := engine.ValidateBlock(genesis, tampered); err == nil {
		t.Fatal("invalid finality signature should fail")
	}
}

func signedTestBlock(t *testing.T, key chaincrypto.PrivateKey, parent types.Block, proposer string, height uint64) types.Block {
	t.Helper()
	block := types.Block{
		Header: types.BlockHeader{
			ChainID:       "chainlab-local",
			Height:        height,
			ParentHash:    parent.Hash(),
			TimeUnix:      parent.Header.TimeUnix + 1,
			Proposer:      proposer,
			GasLimit:      types.DefaultBlockGasLimit,
			BaseFeePerGas: types.InitialBaseFeePerGas,
			TxRoot:        types.TransactionRoot(nil),
			ReceiptRoot:   types.ReceiptRoot(nil),
			StateRoot:     types.TransactionRoot(nil),
		},
	}
	if err := consensus.SignBlock(key, &block); err != nil {
		t.Fatal(err)
	}
	return block
}
