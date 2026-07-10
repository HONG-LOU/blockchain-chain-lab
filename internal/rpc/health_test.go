package rpc

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"chainlab/internal/consensus"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/node"
	"chainlab/internal/types"
)

func TestReadinessReportsPersistentConsensusHalt(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validator := chaincrypto.AddressFromPrivateKey(key)
	n, err := node.NewDevelopment(node.Config{
		ChainID:        "chainlab-health",
		ProposerKey:    key,
		Validators:     []string{validator},
		GenesisBalance: map[string]uint64{validator: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	vote, err := consensus.SignFinalityVote(key, canonical)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.SubmitFinalityVote(vote); err != nil {
		t.Fatal(err)
	}
	genesis, ok := n.Block(0)
	if !ok {
		t.Fatal("genesis is missing")
	}
	conflicting := types.Block{Header: types.BlockHeader{
		ChainID:       genesis.Header.ChainID,
		Height:        1,
		ParentHash:    genesis.Hash(),
		TimeUnix:      canonical.Header.TimeUnix + 1,
		Proposer:      validator,
		GasLimit:      node.DefaultBlockGasLimit,
		BaseFeePerGas: node.NextBaseFee(genesis, node.DefaultBlockGasLimit),
		TxRoot:        types.TransactionRoot(nil),
		ReceiptRoot:   types.ReceiptRoot(nil),
		StateRoot:     genesis.Header.StateRoot,
	}}
	if err := consensus.SignBlock(key, &conflicting); err != nil {
		t.Fatal(err)
	}
	conflictingVote, err := consensus.SignFinalityVote(key, conflicting)
	if err != nil {
		t.Fatal(err)
	}
	conflicting.FinalityCertificate = &types.FinalityCertificate{
		ChainID: conflicting.Header.ChainID, Height: 1, BlockHash: conflicting.Hash(),
		Signatures: []types.FinalitySignature{conflictingVote},
	}
	if err := n.ImportBlock(conflicting); err == nil {
		t.Fatal("conflicting certificate did not halt the node")
	}

	handler := NewServer(n)
	for _, path := range []string{"/health", "/health/ready"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s status = %d", path, response.Code)
		}
	}
	live := httptest.NewRecorder()
	handler.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/health/live", nil))
	if live.Code != http.StatusOK {
		t.Fatalf("liveness status = %d", live.Code)
	}
}
