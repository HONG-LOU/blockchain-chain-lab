package node_test

import (
	"testing"

	"chainlab/internal/consensus"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/node"
	"chainlab/internal/types"
)

const finalityLockChainID = "chainlab-finality-lock"

type finalityLockHarness struct {
	n         *node.Node
	key       chaincrypto.PrivateKey
	validator string
	config    node.Config
}

func newFinalityLockHarness(t *testing.T, dataDir string) finalityLockHarness {
	t.Helper()

	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validator := chaincrypto.AddressFromPrivateKey(key)
	config := node.Config{
		Role:           node.RoleValidator,
		ChainID:        finalityLockChainID,
		ProposerKey:    key,
		Validators:     []string{validator},
		GenesisBalance: map[string]uint64{validator: 1_000_000},
		DataDir:        dataDir, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	}
	n, err := node.New(config)
	if err != nil {
		t.Fatal(err)
	}
	registerTestNodeClose(t, n)
	return finalityLockHarness{n: n, key: key, validator: validator, config: config}
}

func finalityLockChild(
	t *testing.T,
	key chaincrypto.PrivateKey,
	validator string,
	parent types.Block,
	timeUnix int64,
) types.Block {
	t.Helper()
	if timeUnix <= parent.Header.TimeUnix {
		t.Fatalf("child time %d must exceed parent time %d", timeUnix, parent.Header.TimeUnix)
	}

	block := types.Block{
		Header: types.BlockHeader{
			ChainID:       parent.Header.ChainID,
			Height:        parent.Header.Height + 1,
			ParentHash:    parent.Hash(),
			TimeUnix:      timeUnix,
			Proposer:      validator,
			GasLimit:      node.DefaultBlockGasLimit,
			BaseFeePerGas: node.NextBaseFee(parent, node.DefaultBlockGasLimit),
			TxRoot:        types.TransactionRoot(nil),
			ReceiptRoot:   types.ReceiptRoot(nil),
			StateRoot:     parent.Header.StateRoot,
		},
	}
	if err := consensus.SignBlock(key, &block); err != nil {
		t.Fatal(err)
	}
	return block
}

func certifyFinalityLockBlock(
	t *testing.T,
	n *node.Node,
	key chaincrypto.PrivateKey,
	block types.Block,
) types.Block {
	t.Helper()

	vote, err := consensus.SignFinalityVote(key, block)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.SubmitFinalityVote(vote); err != nil {
		t.Fatal(err)
	}
	certified, ok := n.Block(block.Header.Height)
	if !ok || certified.Hash() != block.Hash() {
		t.Fatalf("certified block lookup = %+v ok=%v", certified, ok)
	}
	if certified.FinalityCertificate == nil || len(certified.FinalityCertificate.Signatures) != 1 {
		t.Fatalf("certified block certificate = %+v", certified.FinalityCertificate)
	}
	return certified
}

func withFinalityLockCertificate(
	t *testing.T,
	key chaincrypto.PrivateKey,
	block types.Block,
) types.Block {
	t.Helper()

	vote, err := consensus.SignFinalityVote(key, block)
	if err != nil {
		t.Fatal(err)
	}
	block.FinalityCertificate = &types.FinalityCertificate{
		ChainID:    block.Header.ChainID,
		Height:     block.Header.Height,
		BlockHash:  block.Hash(),
		Signatures: []types.FinalitySignature{vote},
	}
	return block
}

func assertFinalityLockState(t *testing.T, n *node.Node, locked types.Block, head types.Block) {
	t.Helper()

	if got := n.Head(); got.Hash() != head.Hash() {
		t.Fatalf("head = %d/%s, want %d/%s", got.Header.Height, got.Hash(), head.Header.Height, head.Hash())
	}
	checkpoint := n.Finality()
	if checkpoint.HeadHeight != head.Header.Height || checkpoint.HeadHash != head.Hash() {
		t.Fatalf("checkpoint head = %+v, want %d/%s", checkpoint, head.Header.Height, head.Hash())
	}
	if checkpoint.CertifiedHeight != locked.Header.Height || checkpoint.CertifiedHash != locked.Hash() {
		t.Fatalf("certified checkpoint = %+v, want %d/%s", checkpoint, locked.Header.Height, locked.Hash())
	}
	if checkpoint.CertifiedSigners != 1 || checkpoint.CertifiedQuorum != 1 {
		t.Fatalf("certificate counts = %+v", checkpoint)
	}
	if checkpoint.SafeSource != "bft_certificate" || checkpoint.FinalizedSource != "bft_certificate" {
		t.Fatalf("certificate sources = %+v", checkpoint)
	}
}

func TestFinalityLockRejectsKnownConflictingBranchAndLongerDescendant(t *testing.T) {
	h := newFinalityLockHarness(t, "")
	genesis, ok := h.n.Block(0)
	if !ok {
		t.Fatal("genesis should exist")
	}
	blockA1, err := h.n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	blockB1 := finalityLockChild(t, h.key, h.validator, genesis, blockA1.Header.TimeUnix+10)
	blockB2 := finalityLockChild(t, h.key, h.validator, blockB1, blockB1.Header.TimeUnix+1)
	if err := h.n.ImportBlock(blockB1); err != nil {
		t.Fatalf("pre-lock side block import: %v", err)
	}
	if got := h.n.Head().Hash(); got != blockA1.Hash() {
		t.Fatalf("equal-height side block changed head to %s", got)
	}

	blockA1 = certifyFinalityLockBlock(t, h.n, h.key, blockA1)
	before := h.n.Finality()
	if err := h.n.ImportBlock(blockB1); err == nil {
		t.Error("known block conflicting with finality lock should be rejected")
	}
	if err := h.n.ImportBlock(blockB2); err == nil {
		t.Error("longer branch conflicting with finality lock should be rejected")
	}
	if after := h.n.Finality(); after != before {
		t.Fatalf("rejected branch changed finality from %+v to %+v", before, after)
	}
	assertFinalityLockState(t, h.n, blockA1, blockA1)
}

func TestFinalityLockAllowsReorgOfUncertifiedSuffix(t *testing.T) {
	h := newFinalityLockHarness(t, "")
	blockA1, err := h.n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	blockA1 = certifyFinalityLockBlock(t, h.n, h.key, blockA1)
	blockA2, err := h.n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}

	blockB2 := finalityLockChild(t, h.key, h.validator, blockA1, blockA2.Header.TimeUnix+10)
	blockB3 := finalityLockChild(t, h.key, h.validator, blockB2, blockB2.Header.TimeUnix+1)
	if err := h.n.ImportBlock(blockB2); err != nil {
		t.Fatalf("compatible equal-height side block import: %v", err)
	}
	if got := h.n.Head().Hash(); got != blockA2.Hash() {
		t.Fatalf("equal-height compatible side block changed head to %s", got)
	}
	if err := h.n.ImportBlock(blockB3); err != nil {
		t.Fatalf("compatible longer branch import: %v", err)
	}
	assertFinalityLockState(t, h.n, blockA1, blockB3)
}

func TestImportBlockUpgradesKnownBlockWithFinalityCertificateAcrossRestart(t *testing.T) {
	h := newFinalityLockHarness(t, t.TempDir())
	block, err := h.n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	certified := withFinalityLockCertificate(t, h.key, block)
	if err := h.n.ImportBlock(certified); err != nil {
		t.Fatalf("known block certificate upgrade: %v", err)
	}
	stored, ok := h.n.Block(block.Header.Height)
	if !ok || stored.FinalityCertificate == nil {
		t.Fatalf("known block was not upgraded: %+v ok=%v", stored, ok)
	}
	assertFinalityLockState(t, h.n, block, block)

	closeTestNode(t, h.n)
	reloaded, err := node.New(h.config)
	if err != nil {
		t.Fatal(err)
	}
	registerTestNodeClose(t, reloaded)
	reloadedBlock, ok := reloaded.Block(block.Header.Height)
	if !ok || reloadedBlock.FinalityCertificate == nil {
		t.Fatalf("reloaded certificate = %+v ok=%v", reloadedBlock.FinalityCertificate, ok)
	}
	assertFinalityLockState(t, reloaded, block, block)
}

func TestFinalityLockRejectsValidConflictingCertificate(t *testing.T) {
	h := newFinalityLockHarness(t, "")
	genesis, ok := h.n.Block(0)
	if !ok {
		t.Fatal("genesis should exist")
	}
	blockA1, err := h.n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	blockA1 = certifyFinalityLockBlock(t, h.n, h.key, blockA1)
	before := h.n.Finality()

	blockB1 := finalityLockChild(t, h.key, h.validator, genesis, blockA1.Header.TimeUnix+10)
	blockB1 = withFinalityLockCertificate(t, h.key, blockB1)
	importErr := h.n.ImportBlock(blockB1)
	if got := h.n.Head().Hash(); got != blockA1.Hash() {
		t.Fatalf("conflicting certificate changed head to %s", got)
	}
	if after := h.n.Finality(); after != before {
		t.Fatalf("conflicting certificate changed finality from %+v to %+v", before, after)
	}
	if importErr == nil {
		if _, produceErr := h.n.ProduceBlock(); produceErr == nil {
			t.Fatal("conflicting certificate was neither rejected nor followed by a sticky halt")
		}
	}
}

func TestFinalityLockSurvivesRestartAndRejectsConflictingBranch(t *testing.T) {
	h := newFinalityLockHarness(t, t.TempDir())
	genesis, ok := h.n.Block(0)
	if !ok {
		t.Fatal("genesis should exist")
	}
	blockA1, err := h.n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	blockB1 := finalityLockChild(t, h.key, h.validator, genesis, blockA1.Header.TimeUnix+10)
	blockB2 := finalityLockChild(t, h.key, h.validator, blockB1, blockB1.Header.TimeUnix+1)
	if err := h.n.ImportBlock(blockB1); err != nil {
		t.Fatalf("pre-lock side block import: %v", err)
	}
	blockA1 = certifyFinalityLockBlock(t, h.n, h.key, blockA1)

	closeTestNode(t, h.n)
	reloaded, err := node.New(h.config)
	if err != nil {
		t.Fatal(err)
	}
	registerTestNodeClose(t, reloaded)
	before := reloaded.Finality()
	if err := reloaded.ImportBlock(blockB1); err == nil {
		t.Error("reloaded node should reject known block conflicting with finality lock")
	}
	if err := reloaded.ImportBlock(blockB2); err == nil {
		t.Error("reloaded node should reject longer branch conflicting with finality lock")
	}
	if after := reloaded.Finality(); after != before {
		t.Fatalf("rejected branch changed reloaded finality from %+v to %+v", before, after)
	}
	assertFinalityLockState(t, reloaded, blockA1, blockA1)
}
