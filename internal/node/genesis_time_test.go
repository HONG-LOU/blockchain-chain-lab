package node_test

import (
	"math"
	"strings"
	"testing"
	"time"

	"chainlab/internal/consensus"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/node"
)

func TestNodeRejectsInvalidGenesisTimeDomain(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		value   int64
		wantErr string
	}{
		{name: "missing", value: 0, wantErr: "must be positive"},
		{name: "negative", value: -1, wantErr: "must be positive"},
		{name: "exhausted", value: math.MaxInt64 - consensus.MaxBlockTimeStepSeconds + 1, wantErr: "time domain"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := node.NewDevelopment(node.Config{
				ChainID:         "chainlab-local",
				ProposerKey:     key,
				GenesisTimeUnix: test.value,
			})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("genesis time error = %v", err)
			}
		})
	}
}

func TestProductionNodeRequiresExplicitGenesisValidators(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	config := node.Config{
		Role:            node.RoleValidator,
		ChainID:         "chainlab-local",
		ProposerKey:     key,
		GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	}
	if _, err := node.New(config); err == nil || !strings.Contains(err.Error(), "explicit genesis validator set") {
		t.Fatalf("missing validator set error = %v", err)
	}
	development, err := node.NewDevelopment(config)
	if err != nil {
		t.Fatal(err)
	}
	validators := development.Validators()
	if len(validators) != 1 || validators[0] != chaincrypto.AddressFromPrivateKey(key) {
		t.Fatalf("development validators = %#v", validators)
	}
}

func TestSharedGenesisTimeProducesSameGenesisAcrossValidators(t *testing.T) {
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
	balances := map[string]uint64{validatorA: 100, validatorB: 200}
	genesisTime := time.Now().Unix()

	nodeA, err := node.NewDevelopment(node.Config{
		ChainID:         "chainlab-local",
		ProposerKey:     keyA,
		Validators:      validators,
		GenesisBalance:  balances,
		GenesisTimeUnix: genesisTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	nodeB, err := node.NewDevelopment(node.Config{
		ChainID:         "chainlab-local",
		ProposerKey:     keyB,
		Validators:      validators,
		GenesisBalance:  balances,
		GenesisTimeUnix: genesisTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	genesisA, ok := nodeA.Block(0)
	if !ok {
		t.Fatal("node A genesis is missing")
	}
	genesisB, ok := nodeB.Block(0)
	if !ok {
		t.Fatal("node B genesis is missing")
	}
	if genesisA.Hash() != genesisB.Hash() {
		t.Fatalf("shared genesis hashes differ: %s != %s", genesisA.Hash(), genesisB.Hash())
	}

	differentTime, err := node.NewDevelopment(node.Config{
		ChainID:         "chainlab-local",
		ProposerKey:     keyA,
		Validators:      validators,
		GenesisBalance:  balances,
		GenesisTimeUnix: genesisTime + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if differentTime.Head().Hash() == genesisA.Hash() {
		t.Fatal("different genesis times produced the same genesis hash")
	}
}

func TestFirstBlockUsesBoundedTimeAfterExplicitGenesis(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	genesisTime := time.Now().Unix() - 1
	n, err := node.NewDevelopment(node.Config{
		ChainID:         "chainlab-local",
		ProposerKey:     key,
		GenesisTimeUnix: genesisTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	genesis := n.Head()
	if genesis.Header.TimeUnix != genesisTime {
		t.Fatalf("genesis time = %d, want %d", genesis.Header.TimeUnix, genesisTime)
	}
	first, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if first.Header.TimeUnix <= genesisTime {
		t.Fatalf("first block time = %d, genesis = %d", first.Header.TimeUnix, genesisTime)
	}
	if first.Header.TimeUnix > genesisTime+consensus.MaxBlockTimeStepSeconds/2 {
		t.Fatalf("first block time step = %d", first.Header.TimeUnix-genesisTime)
	}
	if first.Header.TimeUnix < time.Now().Unix()-5 {
		t.Fatalf("first block time is unexpectedly stale: %d", first.Header.TimeUnix)
	}
}

func TestPersistedGenesisTimeMustMatchConfig(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	config := node.Config{
		Role:            node.RoleValidator,
		ChainID:         "chainlab-local",
		ProposerKey:     key,
		Validators:      []string{chaincrypto.AddressFromPrivateKey(key)},
		GenesisTimeUnix: time.Now().Unix(),
		DataDir:         t.TempDir(),
	}
	first, err := node.New(config)
	if err != nil {
		t.Fatal(err)
	}
	registerTestNodeClose(t, first)
	genesisHash := first.Head().Hash()
	closeTestNode(t, first)
	reloaded, err := node.New(config)
	if err != nil {
		t.Fatal(err)
	}
	registerTestNodeClose(t, reloaded)
	if reloaded.Head().Header.Height != 0 || reloaded.Head().Hash() != genesisHash {
		t.Fatal("matching genesis time did not restore the same chain")
	}

	closeTestNode(t, reloaded)
	config.GenesisTimeUnix++
	if _, err := node.New(config); err == nil || !strings.Contains(err.Error(), "genesis") {
		t.Fatalf("mismatched genesis time error = %v", err)
	}
}
