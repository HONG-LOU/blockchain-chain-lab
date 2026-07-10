package node

import (
	"strings"
	"testing"

	"chainlab/internal/consensus"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/types"
)

type finalityAtomicHarness struct {
	n          *Node
	config     Config
	keys       []chaincrypto.PrivateKey
	validators []string
}

func newFinalityAtomicHarness(t *testing.T, validatorCount int, dataDir string) finalityAtomicHarness {
	t.Helper()

	keys := make([]chaincrypto.PrivateKey, validatorCount)
	validators := make([]string, validatorCount)
	for index := range keys {
		key, err := chaincrypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		keys[index] = key
		validators[index] = chaincrypto.AddressFromPrivateKey(key)
	}
	config := Config{
		Role:           RoleValidator,
		ChainID:        "chainlab-finality-atomic",
		ProposerKey:    keys[0],
		Validators:     validators,
		GenesisBalance: map[string]uint64{validators[0]: 10_000_000},
		DataDir:        dataDir, GenesisTimeUnix: DeterministicDevGenesisTimeUnix,
	}
	n, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	registerTestNodeClose(t, n)
	return finalityAtomicHarness{
		n:          n,
		config:     config,
		keys:       keys,
		validators: validators,
	}
}

func signFinalityAtomicVote(t *testing.T, key chaincrypto.PrivateKey, block types.Block) types.FinalitySignature {
	t.Helper()
	vote, err := consensus.SignFinalityVote(key, block)
	if err != nil {
		t.Fatal(err)
	}
	return vote
}

func TestFinalityVoteJournalSurvivesRestartAndCompletesCertificate(t *testing.T) {
	h := newFinalityAtomicHarness(t, 3, t.TempDir())
	block, err := h.n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	first := signFinalityAtomicVote(t, h.keys[0], block)
	if err := h.n.SubmitFinalityVote(first); err != nil {
		t.Fatal(err)
	}
	if stored, ok := h.n.Block(block.Header.Height); !ok || stored.FinalityCertificate != nil {
		t.Fatalf("below-quorum block unexpectedly certified: %+v ok=%v", stored.FinalityCertificate, ok)
	}

	closeTestNode(t, h.n)
	reloaded, err := New(h.config)
	if err != nil {
		t.Fatal(err)
	}
	registerTestNodeClose(t, reloaded)
	if votes := reloaded.finalityVotes[block.Hash()]; len(votes) != 1 || votes[first.Validator].Signature != first.Signature {
		t.Fatalf("restored vote journal = %+v, want first vote", votes)
	}
	for _, key := range h.keys[1:] {
		if err := reloaded.SubmitFinalityVote(signFinalityAtomicVote(t, key, block)); err != nil {
			t.Fatal(err)
		}
	}

	certified, ok := reloaded.Block(block.Header.Height)
	if !ok || certified.FinalityCertificate == nil {
		t.Fatalf("reloaded node did not complete certificate: %+v ok=%v", certified.FinalityCertificate, ok)
	}
	if got := len(certified.FinalityCertificate.Signatures); got != consensus.FinalityQuorumSize(len(h.validators)) {
		t.Fatalf("certificate signatures = %d, want %d", got, consensus.FinalityQuorumSize(len(h.validators)))
	}
	checkpoint := reloaded.Finality()
	if checkpoint.CertifiedHeight != block.Header.Height || checkpoint.CertifiedHash != block.Hash() {
		t.Fatalf("completed finality checkpoint = %+v", checkpoint)
	}
}

func TestFinalityVoteIndexSurvivesRestartAndDetectsEquivocation(t *testing.T) {
	h := newFinalityAtomicHarness(t, 3, t.TempDir())
	blockA1, err := h.n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	voteA1 := signFinalityAtomicVote(t, h.keys[0], blockA1)
	if err := h.n.SubmitFinalityVote(voteA1); err != nil {
		t.Fatal(err)
	}

	closeTestNode(t, h.n)
	reloaded, err := New(h.config)
	if err != nil {
		t.Fatal(err)
	}
	registerTestNodeClose(t, reloaded)
	genesis, ok := reloaded.Block(0)
	if !ok {
		t.Fatal("genesis should exist")
	}
	blockB1 := signedRestartSecurityEmptyBlock(t, h.keys[0], genesis, blockA1.Header.TimeUnix+10)
	blockB2 := signedRestartSecurityEmptyBlock(t, h.keys[1], blockB1, blockB1.Header.TimeUnix+1)
	if err := reloaded.ImportBlock(blockB1); err != nil {
		t.Fatalf("import conflicting height-one block: %v", err)
	}
	if err := reloaded.ImportBlock(blockB2); err != nil {
		t.Fatalf("import longer conflicting branch: %v", err)
	}
	if got := reloaded.Head().Hash(); got != blockB2.Hash() {
		t.Fatalf("reloaded head = %s, want %s", got, blockB2.Hash())
	}

	voteB1 := signFinalityAtomicVote(t, h.keys[0], blockB1)
	err = reloaded.SubmitFinalityVote(voteB1)
	if err == nil || !strings.Contains(err.Error(), "equivocation") {
		t.Fatalf("conflicting vote error = %v, want equivocation", err)
	}
	evidence := reloaded.FinalityEvidence()
	if len(evidence) != 1 {
		t.Fatalf("equivocation evidence = %+v", evidence)
	}
	got := evidence[0]
	if got.Validator != h.validators[0] || got.Height != blockA1.Header.Height ||
		got.FirstBlockHash != blockA1.Hash() || got.FirstSignature != voteA1.Signature ||
		got.SecondBlockHash != blockB1.Hash() || got.SecondSignature != voteB1.Signature {
		t.Fatalf("equivocation evidence = %+v", got)
	}
}

func TestSubmitFinalityVotePersistenceFailureIsAtomic(t *testing.T) {
	t.Run("below quorum vote", func(t *testing.T) {
		h := newFinalityAtomicHarness(t, 3, "")
		block, err := h.n.ProduceBlock()
		if err != nil {
			t.Fatal(err)
		}
		beforeLock := h.n.finalityLock
		h.n.dataDir = fileInsteadOfDirectory(t)
		vote := signFinalityAtomicVote(t, h.keys[0], block)
		if err := h.n.SubmitFinalityVote(vote); err == nil {
			t.Fatal("below-quorum vote unexpectedly survived persistence failure")
		}
		if votes := h.n.finalityVotes[block.Hash()]; len(votes) != 0 {
			t.Fatalf("failed vote remained in block journal: %+v", votes)
		}
		if heightVotes := h.n.finalityVoteIndex[block.Header.Height]; len(heightVotes) != 0 {
			t.Fatalf("failed vote remained in equivocation index: %+v", heightVotes)
		}
		assertFinalityAtomicUncertified(t, h.n, block, beforeLock)
	})

	t.Run("quorum vote", func(t *testing.T) {
		h := newFinalityAtomicHarness(t, 3, "")
		block, err := h.n.ProduceBlock()
		if err != nil {
			t.Fatal(err)
		}
		for _, key := range h.keys[:2] {
			if err := h.n.SubmitFinalityVote(signFinalityAtomicVote(t, key, block)); err != nil {
				t.Fatal(err)
			}
		}
		beforeLock := h.n.finalityLock
		h.n.dataDir = fileInsteadOfDirectory(t)
		last := signFinalityAtomicVote(t, h.keys[2], block)
		if err := h.n.SubmitFinalityVote(last); err == nil {
			t.Fatal("quorum vote unexpectedly survived persistence failure")
		}
		votes := h.n.finalityVotes[block.Hash()]
		if len(votes) != 2 {
			t.Fatalf("vote journal size = %d, want the two previously committed votes", len(votes))
		}
		if _, exists := votes[last.Validator]; exists {
			t.Fatal("failed quorum vote remained in block journal")
		}
		if indexed, exists := h.n.finalityVoteIndex[block.Header.Height][last.Validator]; exists {
			t.Fatalf("failed quorum vote remained in equivocation index: %+v", indexed)
		}
		assertFinalityAtomicUncertified(t, h.n, block, beforeLock)
	})
}

func assertFinalityAtomicUncertified(t *testing.T, n *Node, block types.Block, beforeLock finalityLock) {
	t.Helper()
	if n.finalityLock != beforeLock {
		t.Fatalf("finality lock changed from %+v to %+v", beforeLock, n.finalityLock)
	}
	canonical, ok := n.Block(block.Header.Height)
	if !ok || canonical.FinalityCertificate != nil {
		t.Fatalf("canonical block retained failed certificate: %+v ok=%v", canonical.FinalityCertificate, ok)
	}
	known, ok := n.knownBlocks[block.Hash()]
	if !ok || known.FinalityCertificate != nil {
		t.Fatalf("known block retained failed certificate: %+v ok=%v", known.FinalityCertificate, ok)
	}
}

func TestRestartRejectsTamperedFinalityLockAndMissingCertificate(t *testing.T) {
	t.Run("height", func(t *testing.T) {
		h, snapshot := newFinalityAtomicCertifiedSnapshot(t, 1)
		snapshot.FinalityLock.Height++
		writeRestartSecuritySnapshot(t, h.config.DataDir, snapshot)
		requireRestartSecurityFailure(t, h.config, "persisted finality lock mismatch")
	})

	t.Run("hash", func(t *testing.T) {
		h, snapshot := newFinalityAtomicCertifiedSnapshot(t, 1)
		snapshot.FinalityLock.BlockHash = snapshot.Blocks[0].Hash()
		writeRestartSecuritySnapshot(t, h.config.DataDir, snapshot)
		requireRestartSecurityFailure(t, h.config, "persisted finality lock mismatch")
	})

	t.Run("certificate deleted", func(t *testing.T) {
		h, snapshot := newFinalityAtomicCertifiedSnapshot(t, 1)
		certifiedHash := snapshot.FinalityLock.BlockHash
		for index := range snapshot.Blocks {
			if snapshot.Blocks[index].Hash() == certifiedHash {
				snapshot.Blocks[index].FinalityCertificate = nil
			}
		}
		for index := range snapshot.KnownBlocks {
			if snapshot.KnownBlocks[index].Hash() == certifiedHash {
				snapshot.KnownBlocks[index].FinalityCertificate = nil
			}
		}
		writeRestartSecuritySnapshot(t, h.config.DataDir, snapshot)
		requireRestartSecurityFailure(t, h.config, "persisted finality lock mismatch")
	})
}

func TestRestartRejectsValidConflictingFinalityCertificates(t *testing.T) {
	h, snapshot := newFinalityAtomicCertifiedSnapshot(t, 3)
	genesis := snapshot.Blocks[0]
	canonical := snapshot.Blocks[1]
	conflicting := signedRestartSecurityEmptyBlock(t, h.keys[0], genesis, canonical.Header.TimeUnix+10)
	votes := make(map[string]types.FinalitySignature, len(h.keys))
	for _, key := range h.keys {
		vote := signFinalityAtomicVote(t, key, conflicting)
		votes[vote.Validator] = vote
	}
	conflicting.FinalityCertificate = &types.FinalityCertificate{
		ChainID:    conflicting.Header.ChainID,
		Height:     conflicting.Header.Height,
		BlockHash:  conflicting.Hash(),
		Signatures: sortedFinalitySignatures(votes),
	}
	snapshot.KnownBlocks = append(snapshot.KnownBlocks, conflicting)
	writeRestartSecuritySnapshot(t, h.config.DataDir, snapshot)

	requireRestartSecurityFailure(t, h.config, "persisted snapshot contains conflicting finality certificates")
}

func newFinalityAtomicCertifiedSnapshot(t *testing.T, validatorCount int) (finalityAtomicHarness, diskSnapshot) {
	t.Helper()
	h := newFinalityAtomicHarness(t, validatorCount, t.TempDir())
	block, err := h.n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range h.keys {
		if err := h.n.SubmitFinalityVote(signFinalityAtomicVote(t, key, block)); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := loadDiskSnapshot(h.config.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || len(snapshot.Blocks) != 2 || snapshot.Blocks[1].FinalityCertificate == nil {
		t.Fatalf("certified snapshot = %+v", snapshot)
	}
	closeTestNode(t, h.n)
	return h, *snapshot
}
