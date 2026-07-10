package node_test

import (
	"strings"
	"testing"

	"chainlab/internal/consensus"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/node"
	"chainlab/internal/types"
)

func TestNodeConfigRejectsNonCanonicalConsensusIdentifiers(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := chaincrypto.AddressFromPrivateKey(key)
	tests := []struct {
		name   string
		config node.Config
	}{
		{name: "chain id whitespace", config: node.Config{ChainID: " chainlab-local ", ProposerKey: key, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix}},
		{name: "validator uppercase", config: node.Config{ChainID: "chainlab-local", ProposerKey: key, Validators: []string{strings.ToUpper(proposer)}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix}},
		{name: "duplicate validator", config: node.Config{ChainID: "chainlab-local", ProposerKey: key, Validators: []string{proposer, proposer}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix}},
		{name: "genesis address uppercase", config: node.Config{ChainID: "chainlab-local", ProposerKey: key, GenesisBalance: map[string]uint64{strings.ToUpper(proposer): 1}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix}},
		{name: "fee collector uppercase", config: node.Config{ChainID: "chainlab-local", ProposerKey: key, FeeCollector: strings.ToUpper(proposer), GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := node.NewDevelopment(test.config); err == nil {
				t.Fatal("non-canonical config was accepted")
			}
		})
	}
}

func TestDeployCodeIDWhitespaceCannotHaltProducerOrImporter(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := chaincrypto.AddressFromPrivateKey(key)
	config := node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{proposer: 1_000_000}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	}
	producer, err := node.NewDevelopment(config)
	if err != nil {
		t.Fatal(err)
	}
	importer, err := node.NewDevelopment(config)
	if err != nil {
		t.Fatal(err)
	}
	tx := signedNodeTx(t, key, types.Transaction{
		ChainID:  config.ChainID,
		Type:     types.TxDeploy,
		From:     proposer,
		GasLimit: 100_000,
		GasPrice: 1,
		Payload:  map[string]string{"code_id": " counter.v1 "},
	})
	if err := producer.SubmitTx(tx); err == nil || !strings.Contains(err.Error(), "code_id is not canonically encoded") {
		t.Fatalf("producer admission error = %v", err)
	}
	if _, err := producer.ProduceBlock(); err != nil {
		t.Fatalf("producer halted after invalid deploy: %v", err)
	}

	parent := importer.Head()
	baseFee := node.NextBaseFee(parent, parent.Header.GasLimit)
	receipt := types.Receipt{
		TxHash:            tx.Hash(),
		Success:           true,
		GasUsed:           21_000,
		BaseFeePerGas:     baseFee,
		EffectiveGasPrice: baseFee,
		BaseFeeBurned:     21_000 * baseFee,
	}
	block := types.Block{
		Header: types.BlockHeader{
			ChainID:       config.ChainID,
			Height:        parent.Header.Height + 1,
			ParentHash:    parent.Hash(),
			TimeUnix:      parent.Header.TimeUnix + 1,
			Proposer:      proposer,
			GasLimit:      parent.Header.GasLimit,
			GasUsed:       receipt.GasUsed,
			BaseFeePerGas: baseFee,
			TxRoot:        types.TransactionRoot([]types.Transaction{tx}),
			ReceiptRoot:   types.ReceiptRoot([]types.Receipt{receipt}),
			StateRoot:     importer.StateRoot(),
		},
		Transactions: []types.Transaction{tx},
		Receipts:     []types.Receipt{receipt},
	}
	if err := consensus.SignBlock(key, &block); err != nil {
		t.Fatal(err)
	}
	if err := importer.ImportBlock(block); err == nil || !strings.Contains(err.Error(), "code_id is not canonically encoded") {
		t.Fatalf("import admission error = %v", err)
	}
	if _, err := importer.ProduceBlock(); err != nil {
		t.Fatalf("importer halted after invalid deploy: %v", err)
	}
}

func TestBlockTimeIsStrictlyMonotonicAndImporterRejectsRegression(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	config := node.Config{ChainID: "chainlab-local", ProposerKey: key, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix}
	producer, err := node.NewDevelopment(config)
	if err != nil {
		t.Fatal(err)
	}
	follower, err := node.NewDevelopment(config)
	if err != nil {
		t.Fatal(err)
	}
	first, err := producer.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	second, err := producer.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if second.Header.TimeUnix <= first.Header.TimeUnix {
		t.Fatalf("block times are not monotonic: %d then %d", first.Header.TimeUnix, second.Header.TimeUnix)
	}

	regressed := first
	regressed.Header.TimeUnix = follower.Head().Header.TimeUnix
	if err := consensus.SignBlock(key, &regressed); err != nil {
		t.Fatal(err)
	}
	if err := follower.ImportBlock(regressed); err == nil || !strings.Contains(err.Error(), "block time must increase") {
		t.Fatalf("regressed time error = %v", err)
	}
}
