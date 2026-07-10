package node_test

import (
	"errors"
	"strings"
	"testing"

	"chainlab/internal/consensus"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/node"
	"chainlab/internal/types"
)

func TestObserverRoleNewHasNoLocalProposer(t *testing.T) {
	key := generateObserverRoleKey(t)
	validator := chaincrypto.AddressFromPrivateKey(key)

	n, err := node.New(node.Config{
		Role:            node.RoleObserver,
		ChainID:         "chainlab-observer",
		Validators:      []string{validator},
		GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
		GenesisBalance:  map[string]uint64{validator: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := n.Role(); got != node.RoleObserver {
		t.Fatalf("role = %q, want %q", got, node.RoleObserver)
	}
	if got := n.Proposer(); got != "" {
		t.Fatalf("observer proposer = %q, want empty", got)
	}
}

func TestObserverRoleRejectsLocalSigningWithoutMutation(t *testing.T) {
	key := generateObserverRoleKey(t)
	validator := chaincrypto.AddressFromPrivateKey(key)
	n, err := node.New(node.Config{
		Role:            node.RoleObserver,
		ChainID:         "chainlab-observer",
		Validators:      []string{validator},
		GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
		GenesisBalance:  map[string]uint64{validator: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}

	headBefore := n.Head().Hash()
	stateRootBefore := n.StateRoot()
	pendingBefore, queuedBefore := n.TxPoolCounts()

	if _, err := n.ProduceBlock(); !errors.Is(err, node.ErrLocalSigningDisabled) {
		t.Fatalf("ProduceBlock error = %v, want ErrLocalSigningDisabled", err)
	}
	if _, err := n.RequestFaucet("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 1); !errors.Is(err, node.ErrLocalSigningDisabled) {
		t.Fatalf("RequestFaucet error = %v, want ErrLocalSigningDisabled", err)
	}

	if got := n.Head().Hash(); got != headBefore {
		t.Fatalf("head changed after rejected local signing: %s != %s", got, headBefore)
	}
	if got := n.StateRoot(); got != stateRootBefore {
		t.Fatalf("state root changed after rejected local signing: %s != %s", got, stateRootBefore)
	}
	if pending, queued := n.TxPoolCounts(); pending != pendingBefore || queued != queuedBefore {
		t.Fatalf("txpool changed after rejected local signing: pending=%d queued=%d", pending, queued)
	}
}

func TestObserverRoleAcceptsExternallySignedInputs(t *testing.T) {
	key := generateObserverRoleKey(t)
	validator := chaincrypto.AddressFromPrivateKey(key)
	recipient := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	config := node.Config{
		ChainID:         "chainlab-observer-external",
		Validators:      []string{validator},
		GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
		GenesisBalance:  map[string]uint64{validator: 1_000_000},
	}

	validatorConfig := config
	validatorConfig.Role = node.RoleValidator
	validatorConfig.ProposerKey = key
	producer, err := node.New(validatorConfig)
	if err != nil {
		t.Fatal(err)
	}
	observerConfig := config
	observerConfig.Role = node.RoleObserver
	observer, err := node.New(observerConfig)
	if err != nil {
		t.Fatal(err)
	}
	registerTestNodeClose(t, observer)

	tx := signObserverRoleTx(t, key, types.Transaction{
		ChainID:  config.ChainID,
		Type:     types.TxTransfer,
		From:     validator,
		To:       recipient,
		Nonce:    0,
		Value:    7,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if err := observer.SubmitTx(tx); err != nil {
		t.Fatalf("observer SubmitTx: %v", err)
	}
	if err := producer.SubmitTx(tx); err != nil {
		t.Fatalf("producer SubmitTx: %v", err)
	}
	block, err := producer.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.ImportBlock(block); err != nil {
		t.Fatalf("observer ImportBlock: %v", err)
	}
	if got := observer.Account(recipient).Balance; got != 7 {
		t.Fatalf("recipient balance = %d, want 7", got)
	}

	vote, err := consensus.SignFinalityVote(key, block)
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.SubmitFinalityVote(vote); err != nil {
		t.Fatalf("observer SubmitFinalityVote: %v", err)
	}
	if finalized := observer.Finality(); finalized.FinalizedHash != block.Hash() {
		t.Fatalf("finalized hash = %s, want %s", finalized.FinalizedHash, block.Hash())
	}
}

func TestObserverRoleDoesNotSignOrRequeueAutomaticSlash(t *testing.T) {
	keyA := generateObserverRoleKey(t)
	keyB := generateObserverRoleKey(t)
	validatorA := chaincrypto.AddressFromPrivateKey(keyA)
	validatorB := chaincrypto.AddressFromPrivateKey(keyB)
	validators := []string{validatorA, validatorB}
	balances := map[string]uint64{validatorA: 2_000_000, validatorB: 2_000_000}
	baseConfig := node.Config{
		ChainID:         "chainlab-observer-slash",
		Validators:      validators,
		GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
		GenesisBalance:  balances,
	}

	producerAConfig := baseConfig
	producerAConfig.Role = node.RoleValidator
	producerAConfig.ProposerKey = keyA
	producerA, err := node.New(producerAConfig)
	if err != nil {
		t.Fatal(err)
	}
	observerConfig := baseConfig
	observerConfig.Role = node.RoleObserver
	observerConfig.DataDir = t.TempDir()
	observer, err := node.New(observerConfig)
	if err != nil {
		t.Fatal(err)
	}
	registerTestNodeClose(t, observer)

	stake := signObserverRoleTx(t, keyA, types.Transaction{
		ChainID:  baseConfig.ChainID,
		Type:     types.TxStake,
		From:     validatorA,
		Nonce:    0,
		Value:    500,
		GasLimit: 30_000,
		GasPrice: 1,
	})
	if err := producerA.SubmitTx(stake); err != nil {
		t.Fatal(err)
	}
	block1, err := producerA.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.ImportBlock(block1); err != nil {
		t.Fatal(err)
	}
	if got := observer.StakeOf(validatorA); got != 500 {
		t.Fatalf("validator A stake = %d, want 500", got)
	}

	producerBConfig := baseConfig
	producerBConfig.Role = node.RoleValidator
	producerBConfig.ProposerKey = keyB
	producerB, err := node.New(producerBConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := producerB.ImportBlock(block1); err != nil {
		t.Fatal(err)
	}
	block2A, err := producerB.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	block2B := emptyObserverRoleBlock(t, keyB, block1, validatorB, block2A.Header.TimeUnix+1)
	block3B := emptyObserverRoleBlock(t, keyA, block2B, validatorA, block2B.Header.TimeUnix+1)
	if err := observer.ImportBlock(block2A); err != nil {
		t.Fatal(err)
	}
	if err := observer.ImportBlock(block2B); err != nil {
		t.Fatal(err)
	}

	voteA, err := consensus.SignFinalityVote(keyA, block2A)
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.SubmitFinalityVote(voteA); err != nil {
		t.Fatalf("first finality vote: %v", err)
	}
	if err := observer.ImportBlock(block3B); err != nil {
		t.Fatalf("import longer conflicting branch: %v", err)
	}
	if got := observer.Head().Hash(); got != block3B.Hash() {
		t.Fatalf("head after longer conflicting branch = %s, want %s", got, block3B.Hash())
	}
	voteB, err := consensus.SignFinalityVote(keyA, block2B)
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.SubmitFinalityVote(voteB); !errors.Is(err, node.ErrLocalSigningDisabled) {
		t.Fatalf("conflicting vote error = %v, want ErrLocalSigningDisabled", err)
	}
	if evidence := observer.FinalityEvidence(); len(evidence) != 1 {
		t.Fatalf("finality evidence = %#v, want one item", evidence)
	}
	if pending, queued := observer.TxPoolCounts(); pending != 0 || queued != 0 {
		t.Fatalf("observer auto-enqueued slash: pending=%d queued=%d", pending, queued)
	}

	closeTestNode(t, observer)
	reloaded, err := node.New(observerConfig)
	if err != nil {
		t.Fatalf("restart observer without key: %v", err)
	}
	registerTestNodeClose(t, reloaded)
	if got := reloaded.Role(); got != node.RoleObserver {
		t.Fatalf("reloaded role = %q, want %q", got, node.RoleObserver)
	}
	if got := reloaded.Proposer(); got != "" {
		t.Fatalf("reloaded observer proposer = %q, want empty", got)
	}
	if evidence := reloaded.FinalityEvidence(); len(evidence) != 1 {
		t.Fatalf("reloaded finality evidence = %#v, want one item", evidence)
	}
	if pending, queued := reloaded.TxPoolCounts(); pending != 0 || queued != 0 {
		t.Fatalf("restart requeued observer slash: pending=%d queued=%d", pending, queued)
	}
}

func TestObserverRoleConfigurationRejections(t *testing.T) {
	key := generateObserverRoleKey(t)
	otherKey := generateObserverRoleKey(t)
	validator := chaincrypto.AddressFromPrivateKey(key)
	base := node.Config{
		ChainID:         "chainlab-observer-config",
		Validators:      []string{validator},
		GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
		GenesisBalance:  map[string]uint64{validator: 1_000_000},
	}

	tests := []struct {
		name   string
		mutate func(*node.Config)
	}{
		{name: "observer with key", mutate: func(config *node.Config) {
			config.Role = node.RoleObserver
			config.ProposerKey = key
		}},
		{name: "observer with fee collector", mutate: func(config *node.Config) {
			config.Role = node.RoleObserver
			config.FeeCollector = validator
		}},
		{name: "validator without key", mutate: func(config *node.Config) {
			config.Role = node.RoleValidator
		}},
		{name: "validator key outside set", mutate: func(config *node.Config) {
			config.Role = node.RoleValidator
			config.ProposerKey = otherKey
		}},
		{name: "empty role", mutate: func(config *node.Config) {}},
		{name: "unknown role", mutate: func(config *node.Config) {
			config.Role = node.NodeRole("archive")
		}},
		{name: "observer without validators", mutate: func(config *node.Config) {
			config.Role = node.RoleObserver
			config.Validators = nil
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := base
			test.mutate(&config)
			if n, err := node.New(config); err == nil || n != nil {
				t.Fatalf("invalid config accepted: node=%v error=%v", n, err)
			}
		})
	}
}

func TestNewDevelopmentRejectsPersistentDataDirectory(t *testing.T) {
	key := generateObserverRoleKey(t)
	_, err := node.NewDevelopment(node.Config{
		ChainID:         "chainlab-development-persistent",
		ProposerKey:     key,
		GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
		DataDir:         t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "do not support persistent data directories") {
		t.Fatalf("NewDevelopment persistent DataDir error = %v", err)
	}
}

func generateObserverRoleKey(t *testing.T) chaincrypto.PrivateKey {
	t.Helper()
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func signObserverRoleTx(t *testing.T, key chaincrypto.PrivateKey, tx types.Transaction) types.Transaction {
	t.Helper()
	signature, err := chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = signature
	return tx
}

func emptyObserverRoleBlock(
	t *testing.T,
	key chaincrypto.PrivateKey,
	parent types.Block,
	proposer string,
	timeUnix int64,
) types.Block {
	t.Helper()
	block := types.Block{Header: types.BlockHeader{
		ChainID:       parent.Header.ChainID,
		Height:        parent.Header.Height + 1,
		ParentHash:    parent.Hash(),
		TimeUnix:      timeUnix,
		Proposer:      proposer,
		GasLimit:      parent.Header.GasLimit,
		BaseFeePerGas: node.NextBaseFee(parent, parent.Header.GasLimit),
		TxRoot:        types.TransactionRoot(nil),
		ReceiptRoot:   types.ReceiptRoot(nil),
		StateRoot:     parent.Header.StateRoot,
	}}
	if err := consensus.SignBlock(key, &block); err != nil {
		t.Fatal(err)
	}
	return block
}
