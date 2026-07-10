package node

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"chainlab/internal/contracts"
	"chainlab/internal/core"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"
	"chainlab/internal/types"
)

type faultAfterExecutor struct {
	calls  int
	failAt int
	fault  error
}

func (e *faultAfterExecutor) ExecuteWithContext(*state.Store, types.Transaction, core.ExecutionContext) (types.Receipt, error) {
	e.calls++
	if e.calls >= e.failAt {
		if e.fault != nil {
			return types.Receipt{}, e.fault
		}
		return types.Receipt{}, fmt.Errorf("injected engine failure: %w", contracts.ErrWasmRuntimeFault)
	}
	return types.Receipt{}, nil
}

func TestSubmitNativeRuntimeFaultHaltsNode(t *testing.T) {
	n := newRuntimeFaultTestNode(t)
	fault := fmt.Errorf("injected native failure: %w", contracts.ErrNativeRuntimeFault)
	n.executor = &faultAfterExecutor{failAt: 1, fault: fault}
	err := n.SubmitTx(types.Transaction{
		From:     "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Nonce:    0,
		GasLimit: 21_000,
	})
	if !errors.Is(err, contracts.ErrNativeRuntimeFault) {
		t.Fatalf("submit error = %v", err)
	}
	if len(n.mempool) != 0 || len(n.queued) != 0 {
		t.Fatal("native runtime fault mutated txpool")
	}
	if _, err := n.ProduceBlock(); !errors.Is(err, contracts.ErrNativeRuntimeFault) {
		t.Fatalf("sticky halt error = %v", err)
	}
}

func TestRuntimeFaultHaltIsStickyAcrossConsensusEntryPoints(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	n, err := NewDevelopment(Config{
		ChainID:     "chainlab-local",
		ProposerKey: key, GenesisTimeUnix: DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}

	fault := fmt.Errorf("injected engine failure: %w", contracts.ErrWasmRuntimeFault)
	n.mu.Lock()
	if !n.recordRuntimeFaultLocked(fault) {
		n.mu.Unlock()
		t.Fatal("runtime fault was not classified as fatal")
	}
	n.mu.Unlock()

	assertRuntimeFault := func(operation string, err error) {
		t.Helper()
		if !errors.Is(err, contracts.ErrWasmRuntimeFault) {
			t.Fatalf("%s error = %v, want runtime fault", operation, err)
		}
	}
	assertRuntimeFault("submit", n.SubmitTx(types.Transaction{}))
	_, err = n.ProduceBlock()
	assertRuntimeFault("produce", err)
	head := n.Head()
	assertRuntimeFault("import", n.ImportBlock(head))
	assertRuntimeFault("finality vote", n.SubmitFinalityVote(types.FinalitySignature{}))
}

func TestImportRejectsInvalidBlockSignatureBeforeFatalExecution(t *testing.T) {
	n := newRuntimeFaultTestNode(t)
	n.executor = &faultAfterExecutor{failAt: 1, fault: fmt.Errorf("injected state fault: %w", contracts.ErrNativeRuntimeFault)}
	block := runtimeFaultEnvelopeBlock(t, n, false)

	err := n.ImportBlock(block)
	if err == nil || !strings.Contains(err.Error(), "invalid block signature") {
		t.Fatalf("import error = %v", err)
	}
	if n.haltErr != nil {
		t.Fatalf("unauthorized block halted node: %v", n.haltErr)
	}
	n.executor = &faultAfterExecutor{failAt: 100}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatalf("node did not remain live: %v", err)
	}
}

func TestImportAuthorizedBlockFatalExecutionHaltsNode(t *testing.T) {
	n := newRuntimeFaultTestNode(t)
	contractAddress := "0xcccccccccccccccccccccccccccccccccccccccc"
	n.state.SetBalance(n.proposer, 1_000_000)
	n.state.SetCodeID(contractAddress, "counter.v1")
	if err := n.state.SetStorage(contractAddress, "count", "corrupt"); err != nil {
		t.Fatal(err)
	}
	n.genesisState = n.state.Snapshot()
	genesis := types.GenesisBlock(n.chainID, n.state.Root(), n.genesisTimeUnix)
	genesis.Header.GasLimit = n.blockGasLimit
	genesis.Header.BaseFeePerGas = InitialBaseFeePerGas
	n.blocks = []types.Block{genesis}
	n.knownBlocks = map[string]types.Block{genesis.Hash(): genesis}
	n.finalityLock = finalityLock{Height: 0, BlockHash: genesis.Hash()}
	tx := signRuntimeFaultTestTx(t, n.proposerKey, types.Transaction{
		ChainID:  n.chainID,
		Type:     types.TxCall,
		From:     n.proposer,
		To:       contractAddress,
		GasLimit: 100_000,
		GasPrice: 1,
		Payload:  map[string]string{"method": "increment"},
	})
	block := runtimeFaultEnvelopeBlockForTransaction(t, n, true, tx)

	err := n.ImportBlock(block)
	if !errors.Is(err, contracts.ErrContractStateFault) {
		t.Fatalf("import error = %v", err)
	}
	if !errors.Is(n.haltErr, contracts.ErrContractStateFault) {
		t.Fatalf("authorized consensus fault did not halt node: %v", n.haltErr)
	}
}

func runtimeFaultEnvelopeBlock(t *testing.T, n *Node, authorized bool) types.Block {
	tx := types.Transaction{ChainID: n.chainID, Type: types.TxTransfer, From: n.proposer, To: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", GasLimit: 1}
	return runtimeFaultEnvelopeBlockForTransaction(t, n, authorized, tx)
}

func runtimeFaultEnvelopeBlockForTransaction(t *testing.T, n *Node, authorized bool, tx types.Transaction) types.Block {
	t.Helper()
	parent := n.Head()
	baseFee := NextBaseFee(parent, n.blockGasLimit)
	receipt := types.Receipt{
		TxHash:            tx.Hash(),
		Success:           true,
		GasUsed:           1,
		BaseFeePerGas:     baseFee,
		EffectiveGasPrice: baseFee,
		BaseFeeBurned:     baseFee,
	}
	block := types.Block{
		Header: types.BlockHeader{
			ChainID:       n.chainID,
			Height:        parent.Header.Height + 1,
			ParentHash:    parent.Hash(),
			TimeUnix:      parent.Header.TimeUnix + 1,
			Proposer:      n.proposer,
			GasLimit:      parent.Header.GasLimit,
			GasUsed:       receipt.GasUsed,
			BaseFeePerGas: baseFee,
			TxRoot:        types.TransactionRoot([]types.Transaction{tx}),
			ReceiptRoot:   types.ReceiptRoot([]types.Receipt{receipt}),
			StateRoot:     n.state.Root(),
		},
		Transactions: []types.Transaction{tx},
		Receipts:     []types.Receipt{receipt},
	}
	key := n.proposerKey
	if !authorized {
		var err error
		key, err = chaincrypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
	}
	var err error
	block.Signature, err = chaincrypto.Sign(key, block.Header.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	return block
}

func TestProducePersistenceFailureDoesNotAdvanceCanonicalState(t *testing.T) {
	n := newRuntimeFaultTestNode(t)
	n.dataDir = fileInsteadOfDirectory(t)
	beforeHead := n.Head().Hash()
	beforeRoot := n.state.Root()
	beforeKnown := len(n.knownBlocks)

	if _, err := n.ProduceBlock(); err == nil {
		t.Fatal("produce unexpectedly succeeded with invalid data directory")
	}
	if got := n.Head().Hash(); got != beforeHead {
		t.Fatalf("head advanced after persistence failure: got %s want %s", got, beforeHead)
	}
	if got := n.state.Root(); got != beforeRoot {
		t.Fatalf("state changed after persistence failure: got %s want %s", got, beforeRoot)
	}
	if len(n.knownBlocks) != beforeKnown {
		t.Fatalf("known block count changed after persistence failure: got %d want %d", len(n.knownBlocks), beforeKnown)
	}
}

func TestImportPersistenceFailureDoesNotAdvanceCanonicalState(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	config := Config{ChainID: "chainlab-local", ProposerKey: key, GenesisTimeUnix: DeterministicDevGenesisTimeUnix}
	producer, err := NewDevelopment(config)
	if err != nil {
		t.Fatal(err)
	}
	follower, err := NewDevelopment(config)
	if err != nil {
		t.Fatal(err)
	}
	block, err := producer.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	follower.dataDir = fileInsteadOfDirectory(t)
	beforeHead := follower.Head().Hash()
	beforeRoot := follower.state.Root()

	if err := follower.ImportBlock(block); err == nil {
		t.Fatal("import unexpectedly succeeded with invalid data directory")
	}
	if got := follower.Head().Hash(); got != beforeHead {
		t.Fatalf("head advanced after persistence failure: got %s want %s", got, beforeHead)
	}
	if got := follower.state.Root(); got != beforeRoot {
		t.Fatalf("state changed after persistence failure: got %s want %s", got, beforeRoot)
	}
	if _, ok := follower.knownBlocks[block.Hash()]; ok {
		t.Fatal("failed imported block remained in known block index")
	}
}

func TestQueuedRevalidationFiltersBeforeCapacityLimit(t *testing.T) {
	n := newRuntimeFaultTestNode(t)
	staleSender := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	validSender := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n.state.SetNonce(staleSender, 1)
	n.executor = &faultAfterExecutor{failAt: maxQueuedTransactions + 2}
	queued := make([]types.Transaction, maxQueuedTransactions)
	for index := range queued {
		queued[index] = types.Transaction{From: staleSender, Nonce: 0}
	}
	queued = append(queued, types.Transaction{From: validSender, Nonce: 0})

	validated, err := n.revalidateQueuedForState(n.state, nil, queued, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(validated) != 1 || validated[0].From != validSender {
		t.Fatalf("validated queue = %+v, want only the valid post-limit transaction", validated)
	}
}

func TestQueuedRevalidationDropsNonceBeyondGap(t *testing.T) {
	n := newRuntimeFaultTestNode(t)
	n.executor = &faultAfterExecutor{failAt: 1}
	queued := []types.Transaction{{
		From:  "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Nonce: maxQueuedNonceGap + 1,
	}}

	validated, err := n.revalidateQueuedForState(n.state, nil, queued, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(validated) != 0 {
		t.Fatalf("out-of-gap transaction survived revalidation: %+v", validated)
	}
}

func TestSubmitRuntimeFaultDoesNotMutateTxPool(t *testing.T) {
	n := newRuntimeFaultTestNode(t)
	n.executor = &faultAfterExecutor{failAt: 2}

	err := n.SubmitTx(types.Transaction{
		From:     "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Nonce:    0,
		GasLimit: 21_000,
	})
	if !errors.Is(err, contracts.ErrWasmRuntimeFault) {
		t.Fatalf("submit error = %v, want runtime fault", err)
	}
	if len(n.mempool) != 0 || len(n.queued) != 0 {
		t.Fatalf("txpool mutated after runtime fault: pending=%d queued=%d", len(n.mempool), len(n.queued))
	}
}

func TestProduceRuntimeFaultDoesNotAdvanceCanonicalState(t *testing.T) {
	n := newRuntimeFaultTestNode(t)
	n.queued = []types.Transaction{{
		From:  "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Nonce: 0,
	}}
	n.executor = &faultAfterExecutor{failAt: 1}
	beforeHead := n.Head().Hash()
	beforeRoot := n.state.Root()
	beforeKnown := len(n.knownBlocks)

	_, err := n.ProduceBlock()
	if !errors.Is(err, contracts.ErrWasmRuntimeFault) {
		t.Fatalf("produce error = %v, want runtime fault", err)
	}
	if got := n.Head().Hash(); got != beforeHead {
		t.Fatalf("head advanced after runtime fault: got %s want %s", got, beforeHead)
	}
	if got := n.state.Root(); got != beforeRoot {
		t.Fatalf("state root changed after runtime fault: got %s want %s", got, beforeRoot)
	}
	if len(n.knownBlocks) != beforeKnown {
		t.Fatalf("known block count changed after runtime fault: got %d want %d", len(n.knownBlocks), beforeKnown)
	}
	if len(n.queued) != 1 {
		t.Fatalf("queued pool changed after runtime fault: got %d want 1", len(n.queued))
	}
}

func TestImportPromotionRuntimeFaultDoesNotAdvanceCanonicalState(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	config := Config{ChainID: "chainlab-local", ProposerKey: key, GenesisTimeUnix: DeterministicDevGenesisTimeUnix}
	producer, err := NewDevelopment(config)
	if err != nil {
		t.Fatal(err)
	}
	follower, err := NewDevelopment(config)
	if err != nil {
		t.Fatal(err)
	}
	block, err := producer.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	follower.queued = []types.Transaction{{
		From:  "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Nonce: 0,
	}}
	follower.executor = &faultAfterExecutor{failAt: 1}
	beforeHead := follower.Head().Hash()
	beforeRoot := follower.state.Root()

	err = follower.ImportBlock(block)
	if !errors.Is(err, contracts.ErrWasmRuntimeFault) {
		t.Fatalf("import error = %v, want runtime fault", err)
	}
	if got := follower.Head().Hash(); got != beforeHead {
		t.Fatalf("head advanced after runtime fault: got %s want %s", got, beforeHead)
	}
	if got := follower.state.Root(); got != beforeRoot {
		t.Fatalf("state root changed after runtime fault: got %s want %s", got, beforeRoot)
	}
	if _, ok := follower.knownBlocks[block.Hash()]; ok {
		t.Fatal("failed imported block remained in known block index")
	}
	if len(follower.queued) != 1 {
		t.Fatalf("queued pool changed after runtime fault: got %d want 1", len(follower.queued))
	}
}

func TestImportDropsQueuedTransactionBelowCanonicalNonce(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := chaincrypto.AddressFromPrivateKey(key)
	config := Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{sender: 1_000_000}, GenesisTimeUnix: DeterministicDevGenesisTimeUnix,
	}
	producer, err := NewDevelopment(config)
	if err != nil {
		t.Fatal(err)
	}
	follower, err := NewDevelopment(config)
	if err != nil {
		t.Fatal(err)
	}
	canonical := signRuntimeFaultTestTx(t, key, types.Transaction{
		ChainID:  config.ChainID,
		Type:     types.TxTransfer,
		From:     sender,
		To:       "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Nonce:    0,
		Value:    1,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if err := producer.SubmitTx(canonical); err != nil {
		t.Fatal(err)
	}
	block, err := producer.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	stale := signRuntimeFaultTestTx(t, key, types.Transaction{
		ChainID:  config.ChainID,
		Type:     types.TxTransfer,
		From:     sender,
		To:       "0xcccccccccccccccccccccccccccccccccccccccc",
		Nonce:    0,
		Value:    1,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	follower.queued = []types.Transaction{stale}

	if err := follower.ImportBlock(block); err != nil {
		t.Fatal(err)
	}
	if len(follower.queued) != 0 {
		t.Fatalf("stale queued transaction survived import: %d", len(follower.queued))
	}
}

func TestPendingReplacementPromotesNewlyAffordableQueuedTransaction(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := chaincrypto.AddressFromPrivateKey(key)
	n, err := NewDevelopment(Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{sender: 150_000}, GenesisTimeUnix: DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	queued := signRuntimeFaultTestTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     sender,
		To:       "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Nonce:    1,
		Value:    70_000,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if err := n.SubmitTx(queued); err != nil {
		t.Fatal(err)
	}
	original := signRuntimeFaultTestTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     sender,
		To:       "0xcccccccccccccccccccccccccccccccccccccccc",
		Nonce:    0,
		Value:    50_000,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if err := n.SubmitTx(original); err != nil {
		t.Fatal(err)
	}
	if len(n.mempool) != 1 || len(n.queued) != 1 {
		t.Fatalf("pre-replacement txpool = pending %d queued %d, want 1/1", len(n.mempool), len(n.queued))
	}
	replacement := signRuntimeFaultTestTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     sender,
		To:       "0xcccccccccccccccccccccccccccccccccccccccc",
		Nonce:    0,
		GasLimit: 21_000,
		GasPrice: 2,
	})
	if err := n.SubmitTx(replacement); err != nil {
		t.Fatal(err)
	}
	if len(n.mempool) != 2 || len(n.queued) != 0 {
		t.Fatalf("post-replacement txpool = pending %d queued %d, want 2/0", len(n.mempool), len(n.queued))
	}
	if n.mempool[0].Hash() != replacement.Hash() || n.mempool[1].Hash() != queued.Hash() {
		t.Fatal("replacement or promoted transaction order is incorrect")
	}
}

func newRuntimeFaultTestNode(t *testing.T) *Node {
	t.Helper()
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	n, err := NewDevelopment(Config{ChainID: "chainlab-local", ProposerKey: key, GenesisTimeUnix: DeterministicDevGenesisTimeUnix})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func signRuntimeFaultTestTx(t *testing.T, key chaincrypto.PrivateKey, tx types.Transaction) types.Transaction {
	t.Helper()
	signature, err := chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = signature
	return tx
}

func fileInsteadOfDirectory(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
