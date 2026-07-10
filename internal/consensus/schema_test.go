package consensus_test

import (
	"sort"
	"strings"
	"testing"

	"chainlab/internal/consensus"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/types"
)

func TestFinalityVoteEnvelopeBindsChainHeightAndBlockHash(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validator := chaincrypto.AddressFromPrivateKey(key)
	genesis := types.GenesisBlock("chainlab-local", types.TransactionRoot(nil), 1_700_000_000)
	block := signedTestBlock(t, key, genesis, validator, 1)
	vote, err := consensus.SignFinalityVote(key, block)
	if err != nil {
		t.Fatal(err)
	}
	if vote.ChainID != block.Header.ChainID || vote.Height != block.Header.Height || vote.BlockHash != block.Hash() {
		t.Fatalf("vote envelope = %+v", vote)
	}
	if !consensus.VerifyFinalityVote(block, vote) {
		t.Fatal("valid finality vote was rejected")
	}
	mutations := []func(*types.FinalitySignature){
		func(v *types.FinalitySignature) { v.ChainID = "other" },
		func(v *types.FinalitySignature) { v.Height++ },
		func(v *types.FinalitySignature) { v.BlockHash = "0x" + strings.Repeat("f", 64) },
		func(v *types.FinalitySignature) { v.Validator = strings.ToUpper(v.Validator) },
	}
	for index, mutate := range mutations {
		invalid := vote
		mutate(&invalid)
		if consensus.VerifyFinalityVote(block, invalid) {
			t.Fatalf("mutation %d was accepted: %+v", index, invalid)
		}
	}
}

func TestFinalityCertificateRequiresStrictlySortedEnvelopeSignatures(t *testing.T) {
	keys := make([]chaincrypto.PrivateKey, 3)
	validators := make([]string, 3)
	for index := range keys {
		var err error
		keys[index], err = chaincrypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		validators[index] = chaincrypto.AddressFromPrivateKey(keys[index])
	}
	engine := consensus.NewPOA(validators)
	genesis := types.GenesisBlock("chainlab-local", types.TransactionRoot(nil), 1_700_000_000)
	block := signedTestBlock(t, keys[0], genesis, validators[0], 1)
	votes := make([]types.FinalitySignature, len(keys))
	for index, key := range keys {
		var err error
		votes[index], err = consensus.SignFinalityVote(key, block)
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Slice(votes, func(i int, j int) bool { return votes[i].Validator < votes[j].Validator })
	votes[0], votes[1] = votes[1], votes[0]
	block.FinalityCertificate = &types.FinalityCertificate{
		ChainID:    block.Header.ChainID,
		Height:     block.Header.Height,
		BlockHash:  block.Hash(),
		Signatures: votes,
	}
	if err := engine.ValidateBlock(genesis, block); err == nil || !strings.Contains(err.Error(), "strictly sorted") {
		t.Fatalf("unsorted certificate error = %v", err)
	}
}

func TestInvalidBlockSignaturePrecedesBodyHashAndReceiptValidation(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := chaincrypto.AddressFromPrivateKey(key)
	genesis := types.GenesisBlock("chainlab-local", types.TransactionRoot(nil), 1_700_000_000)
	block := signedTestBlock(t, key, genesis, proposer, 1)
	block.Transactions = []types.Transaction{{Payload: map[string]string{"large": strings.Repeat("x", 1<<20)}}}
	block.Receipts = []types.Receipt{{Success: true}}
	block.Signature, err = chaincrypto.Sign(otherKey, block.Header.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	engine := consensus.NewPOA([]string{proposer})
	if err := engine.ValidateBlock(genesis, block); err == nil || !strings.Contains(err.Error(), "invalid block signature") {
		t.Fatalf("validation order error = %v", err)
	}
}
