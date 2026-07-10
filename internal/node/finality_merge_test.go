package node_test

import (
	"sort"
	"testing"

	"chainlab/internal/consensus"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/node"
	"chainlab/internal/types"
)

func TestImportMergesAlternateQuorumCertificatesForKnownBlock(t *testing.T) {
	keys := make([]chaincrypto.PrivateKey, 4)
	validators := make([]string, 4)
	for index := range keys {
		key, err := chaincrypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		keys[index] = key
		validators[index] = chaincrypto.AddressFromPrivateKey(key)
	}
	dataDir := t.TempDir()
	config := node.Config{
		Role:           node.RoleValidator,
		ChainID:        "chainlab-qc-merge",
		ProposerKey:    keys[0],
		Validators:     validators,
		GenesisBalance: map[string]uint64{validators[0]: 1_000_000},
		DataDir:        dataDir, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	}
	n, err := node.New(config)
	if err != nil {
		t.Fatal(err)
	}
	registerTestNodeClose(t, n)
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}

	first := block
	first.FinalityCertificate = finalityMergeCertificate(t, block, keys[:3])
	if err := n.ImportBlock(first); err != nil {
		t.Fatal(err)
	}
	second := block
	second.FinalityCertificate = finalityMergeCertificate(t, block, keys[1:])
	if err := n.ImportBlock(second); err != nil {
		t.Fatal(err)
	}

	merged, ok := n.Block(1)
	if !ok || merged.FinalityCertificate == nil || len(merged.FinalityCertificate.Signatures) != 4 {
		t.Fatalf("merged certificate = %+v ok=%v", merged.FinalityCertificate, ok)
	}
	closeTestNode(t, n)
	reloaded, err := node.New(config)
	if err != nil {
		t.Fatal(err)
	}
	registerTestNodeClose(t, reloaded)
	persisted, ok := reloaded.Block(1)
	if !ok || persisted.FinalityCertificate == nil || len(persisted.FinalityCertificate.Signatures) != 4 {
		t.Fatalf("persisted merged certificate = %+v ok=%v", persisted.FinalityCertificate, ok)
	}
}

func finalityMergeCertificate(t *testing.T, block types.Block, keys []chaincrypto.PrivateKey) *types.FinalityCertificate {
	t.Helper()
	votes := make([]types.FinalitySignature, len(keys))
	for index, key := range keys {
		vote, err := consensus.SignFinalityVote(key, block)
		if err != nil {
			t.Fatal(err)
		}
		votes[index] = vote
	}
	sort.Slice(votes, func(i int, j int) bool {
		return votes[i].Validator < votes[j].Validator
	})
	return &types.FinalityCertificate{
		ChainID:    block.Header.ChainID,
		Height:     block.Header.Height,
		BlockHash:  block.Hash(),
		Signatures: votes,
	}
}
