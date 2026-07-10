package node

import (
	"strings"
	"testing"

	"chainlab/internal/core"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/hash"
	"chainlab/internal/state"
	"chainlab/internal/types"
)

type sizeLimitExecutor struct{}

func TestDiskSnapshotWriteSizeMatchesRestartLimit(t *testing.T) {
	if err := validateDiskSnapshotEncodedSize(int(MaxDiskSnapshotBytes)); err != nil {
		t.Fatalf("maximum snapshot size: %v", err)
	}
	if err := validateDiskSnapshotEncodedSize(int(MaxDiskSnapshotBytes) + 1); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized snapshot error = %v", err)
	}
}

func TestTxPoolBoundedRejectsBeforeCloningOversizedSource(t *testing.T) {
	n := &Node{mempool: []types.Transaction{{Payload: map[string]string{"padding": strings.Repeat("x", 1024)}}}}
	if _, err := n.TxPoolBounded(1); err == nil || !strings.Contains(err.Error(), "query exceeds") {
		t.Fatalf("bounded txpool error = %v", err)
	}
}

func (sizeLimitExecutor) ExecuteWithContext(_ *state.Store, tx types.Transaction, context core.ExecutionContext) (types.Receipt, error) {
	return types.Receipt{
		TxHash:            tx.Hash(),
		Success:           true,
		GasUsed:           1,
		BaseFeePerGas:     context.BaseFeePerGas,
		EffectiveGasPrice: context.BaseFeePerGas,
		BaseFeeBurned:     context.BaseFeePerGas,
	}, nil
}

func TestCanonicalBlockSizeMatchesCanonicalEncoding(t *testing.T) {
	block := types.Block{
		Header: types.BlockHeader{
			ChainID:       "chainlab-local",
			Height:        7,
			ParentHash:    "0xparent",
			TimeUnix:      123,
			Proposer:      "0xproposer",
			GasLimit:      30_000_000,
			GasUsed:       21_000,
			BaseFeePerGas: 1,
			TxRoot:        "0xtx",
			ReceiptRoot:   "0xreceipt",
			StateRoot:     "0xstate",
		},
		Transactions: []types.Transaction{{
			ChainID:  "chainlab-local",
			Type:     types.TxTransfer,
			From:     "0xfrom",
			To:       "0xto",
			GasLimit: 21_000,
			Payload:  map[string]string{"escaped": "<value>\n"},
		}},
		Receipts: []types.Receipt{{
			TxHash:  "0xtx",
			Success: true,
			GasUsed: 21_000,
			Events: []types.Event{{
				Type:       "transfer",
				Attributes: map[string]string{"to": "0xto"},
			}},
		}},
		Signature: "0xsignature",
		FinalityCertificate: &types.FinalityCertificate{
			ChainID:   "chainlab-local",
			Height:    7,
			BlockHash: "0xblock",
			Signatures: []types.FinalitySignature{{
				Validator: "0xvalidator",
				Signature: "0xfinality",
			}},
		},
	}

	got, err := canonicalBlockSize(block)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hash.CanonicalBytes(block)
	if err != nil {
		t.Fatal(err)
	}
	if got != uint64(len(raw)) {
		t.Fatalf("canonical block size = %d, encoded size = %d", got, len(raw))
	}
}

func TestCanonicalFinalityCertificateSizeMatchesCanonicalEncoding(t *testing.T) {
	variants := []*types.FinalityCertificate{
		{ChainID: "chainlab-local", Height: 1, BlockHash: "0xblock"},
		{ChainID: "chainlab-local", Height: 1, BlockHash: "0xblock", Signatures: []types.FinalitySignature{}},
		{
			ChainID:   "chainlab-local",
			Height:    1,
			BlockHash: "0xblock",
			Signatures: []types.FinalitySignature{{
				Validator: "0xvalidator",
				Signature: "0xsignature",
			}},
		},
	}
	for index, certificate := range variants {
		got, err := canonicalFinalityCertificateSize(certificate)
		if err != nil {
			t.Fatalf("variant %d: %v", index, err)
		}
		raw, err := hash.CanonicalBytes(certificate)
		if err != nil {
			t.Fatalf("variant %d: %v", index, err)
		}
		if got != uint64(len(raw)) {
			t.Fatalf("variant %d size = %d, encoded size = %d", index, got, len(raw))
		}
	}
}

func TestBlockByteLimitAccommodatesMaximumStandardTransactionCount(t *testing.T) {
	tx := types.Transaction{
		ChainID:   "chainlab-local",
		Type:      types.TxTransfer,
		From:      "0x1111111111111111111111111111111111111111",
		To:        "0x2222222222222222222222222222222222222222",
		GasLimit:  21_000,
		GasPrice:  1,
		Signature: "0x" + strings.Repeat("1", 130),
	}
	receipt := types.Receipt{
		TxHash:  "0x" + strings.Repeat("2", 64),
		Success: true,
		GasUsed: 21_000,
	}
	block := types.Block{
		Header: types.BlockHeader{
			ChainID:       "chainlab-local",
			ParentHash:    "0x" + strings.Repeat("3", 64),
			Proposer:      tx.From,
			GasLimit:      100_000_000,
			GasUsed:       86_016_000,
			BaseFeePerGas: 1,
			TxRoot:        "0x" + strings.Repeat("4", 64),
			ReceiptRoot:   "0x" + strings.Repeat("5", 64),
			StateRoot:     "0x" + strings.Repeat("6", 64),
		},
		Transactions: make([]types.Transaction, MaxTransactionsPerBlock),
		Receipts:     make([]types.Receipt, MaxTransactionsPerBlock),
		Signature:    "0x" + strings.Repeat("7", 130),
	}
	for index := range block.Transactions {
		block.Transactions[index] = tx
		block.Transactions[index].Nonce = uint64(index)
		block.Receipts[index] = receipt
	}

	size, err := canonicalBlockSize(block)
	if err != nil {
		t.Fatal(err)
	}
	if size > MaxBlockBytes {
		t.Fatalf("standard maximum-count block size = %d, limit = %d", size, MaxBlockBytes)
	}
}

func TestImportBlockRejectsOversizedKnownBlockPayloadBeforeHashBypass(t *testing.T) {
	n := newSizeLimitTestNode(t)
	known := n.Head()
	known.Transactions = []types.Transaction{{
		ChainID: "chainlab-local",
		Type:    types.TxTransfer,
		Payload: map[string]string{"padding": strings.Repeat("x", MaxTransactionBytes)},
	}}
	known.Receipts = []types.Receipt{{}}

	err := n.ImportBlock(known)
	if err == nil || !strings.Contains(err.Error(), "transaction exceeds") {
		t.Fatalf("oversized known block error = %v", err)
	}
	if n.Head().Header.Height != 0 {
		t.Fatal("oversized known block advanced the chain")
	}
}

func TestImportBlockRejectsTransactionCountBeforeDeepCopy(t *testing.T) {
	n := newSizeLimitTestNode(t)
	known := n.Head()
	known.Transactions = make([]types.Transaction, MaxTransactionsPerBlock+1)

	err := n.ImportBlock(known)
	if err == nil || !strings.Contains(err.Error(), "transaction count exceeds") {
		t.Fatalf("transaction count error = %v", err)
	}
}

func TestImportBlockRejectsReceiptCountBeforeDeepCopy(t *testing.T) {
	n := newSizeLimitTestNode(t)
	known := n.Head()
	known.Receipts = make([]types.Receipt, MaxTransactionsPerBlock+1)

	err := n.ImportBlock(known)
	if err == nil || !strings.Contains(err.Error(), "receipt count exceeds") {
		t.Fatalf("receipt count error = %v", err)
	}
}

func TestImportBlockRejectsNonEmptyReceiptCountMismatch(t *testing.T) {
	n := newSizeLimitTestNode(t)
	known := n.Head()
	known.Receipts = []types.Receipt{{Success: true}}

	err := n.ImportBlock(known)
	if err == nil || !strings.Contains(err.Error(), "receipts must match transaction count") {
		t.Fatalf("receipt count mismatch error = %v", err)
	}
}

func TestImportKnownNonGenesisBlockRejectsMissingReceiptBody(t *testing.T) {
	n := newSizeLimitTestNode(t)
	n.executor = sizeLimitExecutor{}
	n.mempool = []types.Transaction{{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     "0x1111111111111111111111111111111111111111",
		To:       "0x2222222222222222222222222222222222222222",
		GasLimit: 1,
	}}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	block.Receipts = nil

	err = n.ImportBlock(block)
	if err == nil || !strings.Contains(err.Error(), "receipts must match transaction count") {
		t.Fatalf("missing receipt body error = %v", err)
	}
}

func TestImportBlockRejectsFinalitySignatureCountBeforeDeepCopy(t *testing.T) {
	n := newSizeLimitTestNode(t)
	known := n.Head()
	known.FinalityCertificate = &types.FinalityCertificate{
		ChainID:    "chainlab-local",
		Signatures: make([]types.FinalitySignature, MaxFinalitySignaturesPerBlock+1),
	}

	err := n.ImportBlock(known)
	if err == nil || !strings.Contains(err.Error(), "finality signature count exceeds") {
		t.Fatalf("finality signature count error = %v", err)
	}
}

func TestImportBlockRejectsCumulativeCanonicalTransactionBytes(t *testing.T) {
	n := newSizeLimitTestNode(t)
	known := n.Head()
	tx := types.Transaction{
		ChainID: "chainlab-local",
		Type:    types.TxTransfer,
		Payload: map[string]string{"padding": strings.Repeat("x", 1_900_000)},
	}
	known.Transactions = make([]types.Transaction, 9)
	known.Receipts = make([]types.Receipt, 9)
	for index := range known.Transactions {
		known.Transactions[index] = tx
		known.Transactions[index].Nonce = uint64(index)
	}

	err := n.ImportBlock(known)
	if err == nil || !strings.Contains(err.Error(), "block exceeds") {
		t.Fatalf("cumulative block byte error = %v", err)
	}
}

func TestProduceBlockStopsBeforeCanonicalBlockByteLimit(t *testing.T) {
	n := newSizeLimitTestNode(t)
	n.executor = sizeLimitExecutor{}
	padding := strings.Repeat("x", 1_900_000)
	n.mempool = make([]types.Transaction, 9)
	for index := range n.mempool {
		n.mempool[index] = types.Transaction{
			ChainID:  "chainlab-local",
			Type:     types.TxTransfer,
			From:     "0xsender",
			To:       "0xreceiver",
			Nonce:    uint64(index),
			GasLimit: 1,
			Payload:  map[string]string{"padding": padding},
		}
	}

	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) == 0 || len(block.Transactions) >= 9 {
		t.Fatalf("produced transaction count = %d", len(block.Transactions))
	}
	raw, err := hash.CanonicalBytes(block)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > MaxBlockBytes {
		t.Fatalf("produced block size = %d, limit = %d", len(raw), MaxBlockBytes)
	}
	if got := len(n.mempool) + len(n.queued); got != 9-len(block.Transactions) {
		t.Fatalf("remaining transaction pool count = %d", got)
	}
}

func newSizeLimitTestNode(t *testing.T) *Node {
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
