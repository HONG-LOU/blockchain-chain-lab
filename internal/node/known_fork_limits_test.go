package node

import (
	"fmt"
	"strings"
	"testing"

	"chainlab/internal/consensus"
	"chainlab/internal/contracts"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/types"
)

func TestImportKnownBlocksPerHeightLimitRejectsBeforeReplayAndOnRestore(t *testing.T) {
	fixture := newKnownForkLimitFixture(t, 2)
	parent := fixture.n.blocks[1]
	targetHeight := parent.Header.Height + 1

	for fork := 1; fork < MaxKnownBlocksPerHeight; fork++ {
		block := knownForkLimitEmptyChild(t, fixture.key, parent, int64(1_000+fork))
		if err := fixture.n.ImportBlock(block); err != nil {
			t.Fatalf("import block %d at height limit: %v", fork, err)
		}
	}
	if got := fixture.n.knownBlockHeights[targetHeight]; got != MaxKnownBlocksPerHeight {
		t.Fatalf("known blocks at height %d = %d", targetHeight, got)
	}

	overflow := knownForkLimitEmptyChild(t, fixture.key, parent, 10_000)
	assertKnownForkAuthenticated(t, overflow)
	knownBefore := len(fixture.n.knownBlocks)
	headBefore := fixture.n.Head().Hash()
	rootBefore := fixture.n.StateRoot()
	fixture.n.runtime = contracts.NewRuntime()
	if _, err := fixture.n.replayStateLocked(parent.Hash()); err == nil {
		t.Fatal("replay sentinel did not fail after removing the deployed contract runtime")
	}

	err := fixture.n.ImportBlock(overflow)
	want := fmt.Sprintf("known block count at height %d exceeds %d", targetHeight, MaxKnownBlocksPerHeight)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("height overflow error = %v, want %q", err, want)
	}
	assertKnownForkRejectedAtomically(t, fixture.n, overflow, knownBefore, headBefore, rootBefore)

	snapshot := knownForkLimitSnapshot(t, fixture.n, overflow)
	assertKnownForkSnapshotRejected(t, fixture, snapshot, fmt.Sprintf("persisted known block count at height %d exceeds %d", targetHeight, MaxKnownBlocksPerHeight))
}

func TestImportKnownSideBlockLimitRejectsBeforeReplayAndOnRestore(t *testing.T) {
	const sideBlocksPerFullHeight = MaxKnownBlocksPerHeight - 1
	canonicalHeight := uint64((MaxKnownSideBlocks + sideBlocksPerFullHeight - 1) / sideBlocksPerFullHeight)
	fixture := newKnownForkLimitFixture(t, canonicalHeight)

	remaining := MaxKnownSideBlocks
	for height := uint64(1); remaining > 0; height++ {
		parent := fixture.n.blocks[height-1]
		atHeight := sideBlocksPerFullHeight
		if atHeight > remaining {
			atHeight = remaining
		}
		for fork := 0; fork < atHeight; fork++ {
			block := knownForkLimitEmptyChild(t, fixture.key, parent, int64(1_000+fork))
			blockHash := block.Hash()
			if _, duplicate := fixture.n.knownBlocks[blockHash]; duplicate {
				t.Fatalf("duplicate side block %s at height %d", blockHash, height)
			}
			fixture.n.knownBlocks[blockHash] = block
			fixture.n.knownBlockHeights[height]++
		}
		remaining -= atHeight
	}
	if got := knownForkSideBlockCount(fixture.n); got != MaxKnownSideBlocks {
		t.Fatalf("known side block count = %d", got)
	}

	targetHeight := canonicalHeight
	parent := fixture.n.blocks[targetHeight-1]
	overflow := knownForkLimitEmptyChild(t, fixture.key, parent, 20_000)
	assertKnownForkAuthenticated(t, overflow)
	if got := fixture.n.knownBlockHeights[targetHeight]; got >= MaxKnownBlocksPerHeight {
		t.Fatalf("test fixture would hit per-height limit first: height %d count %d", targetHeight, got)
	}
	knownBefore := len(fixture.n.knownBlocks)
	headBefore := fixture.n.Head().Hash()
	rootBefore := fixture.n.StateRoot()
	fixture.n.runtime = contracts.NewRuntime()
	if _, err := fixture.n.replayStateLocked(parent.Hash()); err == nil {
		t.Fatal("replay sentinel did not fail after removing the deployed contract runtime")
	}

	err := fixture.n.ImportBlock(overflow)
	want := fmt.Sprintf("known side block count exceeds %d", MaxKnownSideBlocks)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("side block overflow error = %v, want %q", err, want)
	}
	assertKnownForkRejectedAtomically(t, fixture.n, overflow, knownBefore, headBefore, rootBefore)

	snapshot := knownForkLimitSnapshot(t, fixture.n, overflow)
	assertKnownForkSnapshotRejected(t, fixture, snapshot, fmt.Sprintf("persisted known side block count exceeds %d", MaxKnownSideBlocks))
}

type knownForkLimitFixture struct {
	n      *Node
	key    chaincrypto.PrivateKey
	config Config
}

func newKnownForkLimitFixture(t *testing.T, canonicalHeight uint64) knownForkLimitFixture {
	t.Helper()
	if canonicalHeight < 1 {
		t.Fatal("known fork fixture requires a non-genesis canonical block")
	}
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := chaincrypto.AddressFromPrivateKey(key)
	config := Config{
		ChainID:        "chainlab-known-fork-limits",
		ProposerKey:    key,
		Validators:     []string{proposer},
		GenesisBalance: map[string]uint64{proposer: 10_000_000}, GenesisTimeUnix: DeterministicDevGenesisTimeUnix,
	}
	n, err := NewDevelopment(config)
	if err != nil {
		t.Fatal(err)
	}
	deploy := types.Transaction{
		ChainID:  config.ChainID,
		Type:     types.TxDeploy,
		From:     proposer,
		GasLimit: 100_000,
		GasPrice: 1,
		Payload:  map[string]string{"code_id": "counter.v1"},
	}
	deploy.Signature, err = chaincrypto.Sign(key, deploy.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := n.SubmitTx(deploy); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	for n.blocks[len(n.blocks)-1].Header.Height < canonicalHeight {
		parent := n.blocks[len(n.blocks)-1]
		block := knownForkLimitEmptyChild(t, key, parent, 1)
		n.blocks = append(n.blocks, block)
		n.knownBlocks[block.Hash()] = block
		n.knownBlockHeights[block.Header.Height]++
	}
	return knownForkLimitFixture{n: n, key: key, config: config}
}

func knownForkLimitEmptyChild(t *testing.T, key chaincrypto.PrivateKey, parent types.Block, timeOffset int64) types.Block {
	t.Helper()
	if timeOffset <= 0 {
		t.Fatal("fork block time offset must be positive")
	}
	block := types.Block{Header: types.BlockHeader{
		ChainID:       parent.Header.ChainID,
		Height:        parent.Header.Height + 1,
		ParentHash:    parent.Hash(),
		TimeUnix:      parent.Header.TimeUnix + timeOffset,
		Proposer:      chaincrypto.AddressFromPrivateKey(key),
		GasLimit:      parent.Header.GasLimit,
		BaseFeePerGas: NextBaseFee(parent, parent.Header.GasLimit),
		TxRoot:        types.TransactionRoot(nil),
		ReceiptRoot:   types.ReceiptRoot(nil),
		StateRoot:     parent.Header.StateRoot,
	}}
	if err := consensus.SignBlock(key, &block); err != nil {
		t.Fatal(err)
	}
	return block
}

func knownForkLimitSnapshot(t *testing.T, n *Node, overflow types.Block) diskSnapshot {
	t.Helper()
	blocks := make([]types.Block, len(n.blocks))
	for index, block := range n.blocks {
		blocks[index] = cloneBlock(block)
	}
	snapshot := diskSnapshot{
		Version:          diskSnapshotVersion,
		Generation:       1,
		ChainID:          n.chainID,
		GenesisState:     n.genesisState,
		State:            n.state.Snapshot(),
		Blocks:           blocks,
		KnownBlocks:      append(n.knownBlockListLocked(), cloneBlock(overflow)),
		FinalityLock:     n.finalityLock,
		FinalityVotes:    n.finalityVoteListLocked(),
		FinalityEvidence: n.finalityEvidenceListLocked(),
	}
	checksum, err := diskSnapshotChecksum(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Checksum = checksum
	return snapshot
}

func assertKnownForkSnapshotRejected(t *testing.T, fixture knownForkLimitFixture, snapshot diskSnapshot, want string) {
	t.Helper()
	target, err := NewDevelopment(fixture.config)
	if err != nil {
		t.Fatal(err)
	}
	headBefore := target.Head().Hash()
	rootBefore := target.StateRoot()
	knownBefore := len(target.knownBlocks)
	configuredGenesis := newGenesisStore(fixture.config.GenesisBalance, fixture.config.Validators)
	err = target.restoreDiskSnapshot(&snapshot, configuredGenesis)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("snapshot restore error = %v, want %q", err, want)
	}
	if target.Head().Hash() != headBefore || target.StateRoot() != rootBefore || len(target.knownBlocks) != knownBefore {
		t.Fatal("rejected snapshot mutated the live node")
	}
}

func assertKnownForkAuthenticated(t *testing.T, block types.Block) {
	t.Helper()
	if err := consensus.ValidateBlockSignature(block); err != nil {
		t.Fatalf("fork block is not authenticated: %v", err)
	}
}

func assertKnownForkRejectedAtomically(t *testing.T, n *Node, block types.Block, knownCount int, headHash string, stateRoot string) {
	t.Helper()
	if len(n.knownBlocks) != knownCount {
		t.Fatalf("rejected block changed known count: got %d want %d", len(n.knownBlocks), knownCount)
	}
	if _, exists := n.knownBlocks[block.Hash()]; exists {
		t.Fatal("rejected block remained in known block index")
	}
	if n.Head().Hash() != headHash || n.StateRoot() != stateRoot {
		t.Fatal("rejected block changed canonical head or state")
	}
	if n.haltErr != nil {
		t.Fatalf("pre-replay capacity rejection halted the node: %v", n.haltErr)
	}
}

func knownForkSideBlockCount(n *Node) int {
	return len(n.knownBlocks) - len(n.blocks)
}
