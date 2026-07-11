package node

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"chainlab/internal/consensus"
	"chainlab/internal/core"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/hash"
	"chainlab/internal/state"
	"chainlab/internal/types"
)

const (
	legacyDiskSnapshotVersion uint64 = 2
	diskSnapshotVersion       uint64 = 3
)

// MaxDiskSnapshotBytes is a fail-closed guard for the temporary JSON harness.
// Production history must move to the versioned KV path before reaching it.
const MaxDiskSnapshotBytes int64 = 512 * 1024 * 1024

func validateDiskSnapshotEncodedSize(size int) error {
	if size < 0 || int64(size) > MaxDiskSnapshotBytes {
		return fmt.Errorf("persisted snapshot exceeds %d bytes", MaxDiskSnapshotBytes)
	}
	return nil
}

func (n *Node) restoreDiskSnapshot(snapshot *diskSnapshot, configuredGenesis *state.Store) error {
	if !supportedDiskSnapshotVersion(snapshot.Version) {
		return fmt.Errorf("unsupported persisted snapshot version %d; explicit migration is required", snapshot.Version)
	}
	if snapshot.Generation == 0 {
		return errors.New("persisted snapshot generation is required")
	}
	if snapshot.Version == legacyDiskSnapshotVersion && (len(snapshot.Pending) != 0 || len(snapshot.Queued) != 0) {
		return errors.New("persisted snapshot v2 must not contain transaction pool fields")
	}
	if err := types.ValidateCanonicalHash("persisted snapshot checksum", snapshot.Checksum); err != nil {
		return err
	}
	expectedChecksum, err := diskSnapshotChecksum(*snapshot)
	if err != nil {
		return err
	}
	if snapshot.Checksum != expectedChecksum {
		return errors.New("persisted snapshot checksum mismatch")
	}
	if snapshot.ChainID != n.chainID {
		return errors.New("persisted chain id does not match config")
	}
	if err := validatePersistedKnownBlockCardinality(snapshot.Blocks, snapshot.KnownBlocks); err != nil {
		return err
	}

	genesis, err := state.NewStoreFromSnapshot(snapshot.GenesisState)
	if err != nil {
		return fmt.Errorf("restore persisted genesis state: %w", err)
	}
	if genesis.Root() != configuredGenesis.Root() {
		return errors.New("persisted genesis state does not match config")
	}
	persistedState, err := state.NewStoreFromSnapshot(snapshot.State)
	if err != nil {
		return fmt.Errorf("restore persisted state: %w", err)
	}
	if len(snapshot.Blocks) == 0 {
		return errors.New("persisted chain has no blocks")
	}

	expectedGenesis := types.GenesisBlock(n.chainID, genesis.Root(), n.genesisTimeUnix)
	expectedGenesis.Header.GasLimit = n.blockGasLimit
	expectedGenesis.Header.BaseFeePerGas = InitialBaseFeePerGas
	if equal, err := canonicalBlocksEqual(snapshot.Blocks[0], expectedGenesis); err != nil {
		return err
	} else if !equal {
		return errors.New("persisted genesis block mismatch")
	}

	n.genesisState = snapshot.GenesisState
	canonical := make([]types.Block, len(snapshot.Blocks))
	known := make(map[string]types.Block, len(snapshot.KnownBlocks)+len(snapshot.Blocks))
	knownBlockHeights := make(map[uint64]int)
	remainingChildren := persistedBlockChildCounts(snapshot.Blocks, snapshot.KnownBlocks)
	activeStates := make(map[string]*state.Store)
	validatorsByBlock := make(map[string][]string, len(snapshot.KnownBlocks)+len(snapshot.Blocks))
	fixedValidators := genesis.Validators()
	working := genesis
	canonical[0] = cloneBlock(snapshot.Blocks[0])
	genesisHash := canonical[0].Hash()
	known[genesisHash] = canonical[0]
	validatorsByBlock[genesisHash] = fixedValidators
	knownBlockHeights[0] = 1
	if remainingChildren[genesisHash] > 0 {
		activeStates[genesisHash] = working
	}

	for index := 1; index < len(snapshot.Blocks); index++ {
		block := cloneBlock(snapshot.Blocks[index])
		if block.Header.GasLimit != n.blockGasLimit {
			return fmt.Errorf("persisted block %d gas limit does not match config", index)
		}
		validatorsByBlock[block.Hash()] = fixedValidators
		next, err := n.validateBlockOnStateLocked(canonical[index-1], working, block)
		if err != nil {
			return fmt.Errorf("validate persisted canonical block %d: %w", index, err)
		}
		canonical[index] = block
		working = next
		blockHash := block.Hash()
		known[blockHash] = block
		knownBlockHeights[block.Header.Height]++
		releasePersistedParentState(activeStates, remainingChildren, block.Header.ParentHash)
		if remainingChildren[blockHash] > 0 {
			activeStates[blockHash] = working
		}
	}

	head := canonical[len(canonical)-1]
	if replayedRoot := working.Root(); replayedRoot != head.Header.StateRoot {
		return errors.New("replayed state root does not match persisted head")
	} else if replayedRoot != persistedState.Root() {
		return errors.New("persisted state root does not match canonical replay")
	}

	knownInput := append([]types.Block(nil), snapshot.KnownBlocks...)
	sort.Slice(knownInput, func(i int, j int) bool {
		if knownInput[i].Header.Height != knownInput[j].Header.Height {
			return knownInput[i].Header.Height < knownInput[j].Header.Height
		}
		return knownInput[i].Hash() < knownInput[j].Hash()
	})
	for index, block := range knownInput {
		block = cloneBlock(block)
		blockHash := block.Hash()
		if existing, ok := known[blockHash]; ok {
			equal, err := canonicalBlocksEqual(existing, block)
			if err != nil {
				return err
			}
			if !equal {
				return fmt.Errorf("persisted known block %d conflicts with block hash %s", index, blockHash)
			}
			continue
		}
		if block.Header.Height == 0 {
			return fmt.Errorf("persisted known block %d has an alternate genesis", index)
		}
		parent, ok := known[block.Header.ParentHash]
		if !ok {
			return fmt.Errorf("persisted known block %d parent is missing", index)
		}
		parentState, ok := activeStates[parent.Hash()]
		if !ok {
			return fmt.Errorf("persisted known block %d parent state is missing", index)
		}
		next, err := n.validateBlockOnStateLocked(parent, parentState, block)
		if err != nil {
			return fmt.Errorf("validate persisted known block %d: %w", index, err)
		}
		validatorsByBlock[blockHash] = fixedValidators
		known[blockHash] = block
		knownBlockHeights[block.Header.Height]++
		releasePersistedParentState(activeStates, remainingChildren, block.Header.ParentHash)
		if remainingChildren[blockHash] > 0 {
			activeStates[blockHash] = next
		}
	}
	evidence := make(map[string]types.FinalityEquivocationEvidence, len(snapshot.FinalityEvidence))
	for index, item := range snapshot.FinalityEvidence {
		if err := n.validatePersistedFinalityEvidence(item, known, validatorsByBlock); err != nil {
			return fmt.Errorf("validate persisted finality evidence %d: %w", index, err)
		}
		key := finalityEvidenceKey(item.Height, item.Validator)
		if _, duplicate := evidence[key]; duplicate {
			return errors.New("persisted finality evidence contains duplicate identity")
		}
		evidence[key] = item
	}
	derivedLock, err := deriveFinalityLock(known, genesisHash)
	if err != nil {
		return err
	}
	if snapshot.FinalityLock != derivedLock {
		return fmt.Errorf(
			"persisted finality lock mismatch: got height %d hash %s, want height %d hash %s",
			snapshot.FinalityLock.Height,
			snapshot.FinalityLock.BlockHash,
			derivedLock.Height,
			derivedLock.BlockHash,
		)
	}
	if derivedLock.Height >= uint64(len(canonical)) || canonical[derivedLock.Height].Hash() != derivedLock.BlockHash {
		return errors.New("persisted canonical chain does not contain the finality lock")
	}
	finalityVotes, finalityVoteIndex, err := n.restorePersistedFinalityVotes(
		snapshot.FinalityVotes,
		known,
		validatorsByBlock,
		evidence,
	)
	if err != nil {
		return err
	}
	restoredMempool, restoredQueued, restoredTxPoolBytes, err := n.restorePersistedTxPool(
		snapshot.Pending,
		snapshot.Queued,
		working,
		head,
	)
	if err != nil {
		return err
	}

	n.state = working
	n.blocks = canonical
	n.knownBlocks = known
	n.knownBlockHeights = knownBlockHeights
	n.finalityLock = derivedLock
	n.finalityVotes = finalityVotes
	n.finalityVoteIndex = finalityVoteIndex
	n.finalityEvidence = evidence
	n.mempool = restoredMempool
	n.queued = restoredQueued
	n.txPoolBytes = restoredTxPoolBytes
	n.snapshotGeneration = snapshot.Generation
	n.refreshConsensusLocked()
	n.rebuildTxIndex()
	return nil
}

func supportedDiskSnapshotVersion(version uint64) bool {
	return version == legacyDiskSnapshotVersion || version == diskSnapshotVersion
}

func (n *Node) restorePersistedTxPool(
	pending []types.Transaction,
	queued []types.Transaction,
	committed *state.Store,
	head types.Block,
) ([]types.Transaction, []types.Transaction, uint64, error) {
	if len(pending)+len(queued) > maxTxPoolTransactions {
		return nil, nil, 0, fmt.Errorf("persisted transaction pool exceeds %d entries", maxTxPoolTransactions)
	}
	if len(queued) > maxQueuedTransactions {
		return nil, nil, 0, fmt.Errorf("persisted queued transaction pool exceeds %d entries", maxQueuedTransactions)
	}

	if head.Header.Height == ^uint64(0) {
		return nil, nil, 0, errors.New("persisted transaction pool cannot advance beyond maximum block height")
	}
	blockHeight := head.Header.Height + 1
	baseFee := NextBaseFee(head, n.blockGasLimit)
	working := committed.Clone()
	seenHashes := make(map[string]struct{}, len(pending)+len(queued))
	type senderNonce struct {
		sender string
		nonce  uint64
	}
	seenNonces := make(map[senderNonce]struct{}, len(pending)+len(queued))
	senderCounts := make(map[string]int)
	var totalBytes uint64

	validateEnvelope := func(tx types.Transaction) error {
		txSize, err := canonicalTransactionSize(tx)
		if err != nil {
			return err
		}
		if totalBytes >= maxTxPoolBytes || txSize > maxTxPoolBytes-totalBytes {
			return fmt.Errorf("persisted transaction pool exceeds %d bytes", maxTxPoolBytes)
		}
		hash := tx.Hash()
		if _, duplicate := seenHashes[hash]; duplicate {
			return errors.New("persisted transaction pool contains a duplicate transaction hash")
		}
		sender := normalizedAddress(tx.From)
		key := senderNonce{sender: sender, nonce: tx.Nonce}
		if _, duplicate := seenNonces[key]; duplicate {
			return errors.New("persisted transaction pool contains a duplicate sender nonce")
		}
		if senderCounts[sender] >= maxTransactionsPerSender {
			return fmt.Errorf("persisted transaction pool exceeds %d entries for sender %s", maxTransactionsPerSender, sender)
		}
		seenHashes[hash] = struct{}{}
		seenNonces[key] = struct{}{}
		senderCounts[sender]++
		totalBytes += txSize
		return nil
	}

	restoredPending := cloneTransactions(pending)
	for index, tx := range restoredPending {
		if err := validateEnvelope(tx); err != nil {
			return nil, nil, 0, fmt.Errorf("validate persisted pending transaction %d: %w", index, err)
		}
		if _, err := n.executor.ExecuteWithContext(working, tx, core.ExecutionContext{BlockHeight: blockHeight, BaseFeePerGas: baseFee}); err != nil {
			return nil, nil, 0, fmt.Errorf("replay persisted pending transaction %d: %w", index, err)
		}
	}

	restoredQueued := cloneTransactions(queued)
	for index, tx := range restoredQueued {
		if err := validateEnvelope(tx); err != nil {
			return nil, nil, 0, fmt.Errorf("validate persisted queued transaction %d: %w", index, err)
		}
		account := working.GetAccount(tx.From)
		if tx.Nonce <= account.Nonce {
			return nil, nil, 0, fmt.Errorf("persisted queued transaction %d is not a future nonce", index)
		}
		if tx.Nonce-account.Nonce > maxQueuedNonceGap {
			return nil, nil, 0, fmt.Errorf("persisted queued transaction %d nonce gap exceeds %d", index, maxQueuedNonceGap)
		}
		check := working.Clone()
		check.SetNonce(tx.From, tx.Nonce)
		if _, err := n.executor.ExecuteWithContext(check, tx, core.ExecutionContext{BlockHeight: blockHeight, BaseFeePerGas: baseFee}); err != nil {
			return nil, nil, 0, fmt.Errorf("validate persisted queued transaction %d: %w", index, err)
		}
	}
	return restoredPending, restoredQueued, totalBytes, nil
}

func (n *Node) requeuePersistedFinalitySlashesLocked() error {
	for _, evidence := range n.finalityEvidenceListLocked() {
		offenceID := types.FinalityEquivocationOffenceID(
			n.chainID,
			evidence.Validator,
			evidence.Height,
		)
		if n.state.Param("slash:offence:"+offenceID) != "" || n.state.StakeOf(evidence.Validator) == 0 {
			continue
		}
		if err := n.enqueueFinalityEvidenceSlashLocked(evidence); err != nil && core.IsFatalExecutionError(err) {
			return err
		}
	}
	return nil
}

func validatePersistedKnownBlockCardinality(canonical []types.Block, known []types.Block) error {
	canonicalHashes := make(map[string]struct{}, len(canonical))
	allHashes := make(map[string]struct{}, len(canonical)+len(known))
	countsByHeight := make(map[uint64]int)
	for _, block := range canonical {
		blockHash := block.Hash()
		if _, duplicate := allHashes[blockHash]; !duplicate {
			allHashes[blockHash] = struct{}{}
			countsByHeight[block.Header.Height]++
		}
		canonicalHashes[blockHash] = struct{}{}
	}
	for _, block := range known {
		blockHash := block.Hash()
		if _, duplicate := allHashes[blockHash]; duplicate {
			continue
		}
		allHashes[blockHash] = struct{}{}
		countsByHeight[block.Header.Height]++
	}
	for height, count := range countsByHeight {
		if count > MaxKnownBlocksPerHeight {
			return fmt.Errorf("persisted known block count at height %d exceeds %d", height, MaxKnownBlocksPerHeight)
		}
	}
	if len(allHashes)-len(canonicalHashes) > MaxKnownSideBlocks {
		return fmt.Errorf("persisted known side block count exceeds %d", MaxKnownSideBlocks)
	}
	return nil
}

func persistedBlockChildCounts(canonical []types.Block, known []types.Block) map[string]int {
	counts := make(map[string]int)
	seen := make(map[string]struct{}, len(canonical)+len(known))
	for _, blocks := range [][]types.Block{canonical, known} {
		for _, block := range blocks {
			blockHash := block.Hash()
			if _, duplicate := seen[blockHash]; duplicate {
				continue
			}
			seen[blockHash] = struct{}{}
			if block.Header.Height > 0 {
				counts[block.Header.ParentHash]++
			}
		}
	}
	return counts
}

func releasePersistedParentState(states map[string]*state.Store, remainingChildren map[string]int, parentHash string) {
	remainingChildren[parentHash]--
	if remainingChildren[parentHash] <= 0 {
		delete(states, parentHash)
		delete(remainingChildren, parentHash)
	}
}

func deriveFinalityLock(known map[string]types.Block, genesisHash string) (finalityLock, error) {
	if err := types.ValidateCanonicalHash("persisted genesis hash", genesisHash); err != nil {
		return finalityLock{}, err
	}
	certified := make([]types.Block, 0)
	for _, block := range known {
		if block.FinalityCertificate != nil {
			certified = append(certified, block)
		}
	}
	sort.Slice(certified, func(i int, j int) bool {
		if certified[i].Header.Height != certified[j].Header.Height {
			return certified[i].Header.Height < certified[j].Header.Height
		}
		return certified[i].Hash() < certified[j].Hash()
	})
	lock := finalityLock{Height: 0, BlockHash: genesisHash}
	for _, block := range certified {
		if block.Header.Height == 0 {
			return finalityLock{}, errors.New("persisted genesis block must not carry a finality certificate")
		}
		blockHash := block.Hash()
		if block.Header.Height == lock.Height {
			if blockHash != lock.BlockHash {
				return finalityLock{}, errors.New("persisted snapshot contains conflicting finality certificates")
			}
			continue
		}
		if block.Header.Height < lock.Height || !knownBlockDescendsFrom(known, blockHash, lock) {
			return finalityLock{}, errors.New("persisted snapshot contains incompatible finality certificates")
		}
		lock = finalityLock{Height: block.Header.Height, BlockHash: blockHash}
	}
	return lock, nil
}

func (n *Node) restorePersistedFinalityVotes(
	persisted []types.FinalitySignature,
	known map[string]types.Block,
	validatorsByBlock map[string][]string,
	evidence map[string]types.FinalityEquivocationEvidence,
) (map[string]map[string]types.FinalitySignature, map[uint64]map[string]finalityVoteRecord, error) {
	votesByBlock := make(map[string]map[string]types.FinalitySignature)
	index := make(map[uint64]map[string]finalityVoteRecord)

	blocks := make([]types.Block, 0, len(known))
	for _, block := range known {
		blocks = append(blocks, block)
	}
	sort.Slice(blocks, func(i int, j int) bool {
		if blocks[i].Header.Height != blocks[j].Header.Height {
			return blocks[i].Header.Height < blocks[j].Header.Height
		}
		return blocks[i].Hash() < blocks[j].Hash()
	})
	for _, block := range blocks {
		if block.FinalityCertificate == nil {
			continue
		}
		for _, vote := range block.FinalityCertificate.Signatures {
			if err := addRestoredFinalityVote(votesByBlock, index, evidence, vote); err != nil {
				return nil, nil, fmt.Errorf("restore certified finality vote: %w", err)
			}
		}
	}

	seenPersisted := make(map[string]struct{}, len(persisted))
	for position, vote := range persisted {
		block, err := n.validatePersistedFinalityVote(vote, known, validatorsByBlock)
		if err != nil {
			return nil, nil, fmt.Errorf("validate persisted finality vote %d: %w", position, err)
		}
		identity := fmt.Sprintf("%d:%s:%s", vote.Height, vote.Validator, vote.BlockHash)
		if _, duplicate := seenPersisted[identity]; duplicate {
			return nil, nil, errors.New("persisted finality votes contain a duplicate identity")
		}
		seenPersisted[identity] = struct{}{}
		if block.Hash() != vote.BlockHash {
			return nil, nil, errors.New("persisted finality vote block hash changed during validation")
		}
		if err := addRestoredFinalityVote(votesByBlock, index, evidence, vote); err != nil {
			return nil, nil, fmt.Errorf("restore persisted finality vote %d: %w", position, err)
		}
	}

	for _, item := range evidence {
		heightVotes := index[item.Height]
		if heightVotes == nil {
			heightVotes = make(map[string]finalityVoteRecord)
			index[item.Height] = heightVotes
		}
		if _, ok := heightVotes[item.Validator]; !ok {
			heightVotes[item.Validator] = finalityVoteRecord{
				BlockHash: item.FirstBlockHash,
				Signature: item.FirstSignature,
			}
		}
	}
	return votesByBlock, index, nil
}

func (n *Node) validatePersistedFinalityVote(
	vote types.FinalitySignature,
	known map[string]types.Block,
	validatorsByBlock map[string][]string,
) (types.Block, error) {
	validator, err := chaincrypto.NormalizeAddress(vote.Validator)
	if err != nil || validator != vote.Validator {
		return types.Block{}, errors.New("finality vote validator is not canonically encoded")
	}
	if vote.ChainID != n.chainID || vote.Height == 0 {
		return types.Block{}, errors.New("finality vote envelope is not for this non-genesis chain height")
	}
	if err := types.ValidateCanonicalHash("persisted finality vote block hash", vote.BlockHash); err != nil {
		return types.Block{}, err
	}
	if err := types.ValidateCanonicalSignature("persisted finality vote signature", vote.Signature); err != nil {
		return types.Block{}, err
	}
	block, ok := known[vote.BlockHash]
	if !ok || block.Header.Height != vote.Height {
		return types.Block{}, errors.New("persisted finality vote block is missing or has the wrong height")
	}
	validators, ok := validatorsByBlock[vote.BlockHash]
	if !ok || !validatorSetContains(validators, validator) {
		return types.Block{}, errors.New("persisted finality vote signer is not a validator for the block")
	}
	if !consensus.VerifyFinalityVote(block, vote) {
		return types.Block{}, errors.New("persisted finality vote signature is invalid")
	}
	return block, nil
}

func addRestoredFinalityVote(
	votesByBlock map[string]map[string]types.FinalitySignature,
	index map[uint64]map[string]finalityVoteRecord,
	evidence map[string]types.FinalityEquivocationEvidence,
	vote types.FinalitySignature,
) error {
	heightVotes := index[vote.Height]
	if heightVotes == nil {
		heightVotes = make(map[string]finalityVoteRecord)
		index[vote.Height] = heightVotes
	}
	if existing, ok := heightVotes[vote.Validator]; ok {
		if existing.BlockHash != vote.BlockHash {
			if _, proven := evidence[finalityEvidenceKey(vote.Height, vote.Validator)]; !proven {
				return errors.New("finality vote journal contains an unproven equivocation")
			}
		}
	} else {
		heightVotes[vote.Validator] = finalityVoteRecord{BlockHash: vote.BlockHash, Signature: vote.Signature}
	}
	blockVotes := votesByBlock[vote.BlockHash]
	if blockVotes == nil {
		blockVotes = make(map[string]types.FinalitySignature)
		votesByBlock[vote.BlockHash] = blockVotes
	}
	if _, ok := blockVotes[vote.Validator]; !ok {
		blockVotes[vote.Validator] = vote
	}
	return nil
}

func knownBlockDescendsFrom(known map[string]types.Block, blockHash string, checkpoint finalityLock) bool {
	block, ok := known[blockHash]
	if !ok || block.Header.Height < checkpoint.Height {
		return false
	}
	for block.Header.Height > checkpoint.Height {
		parent, exists := known[block.Header.ParentHash]
		if !exists || parent.Header.Height+1 != block.Header.Height {
			return false
		}
		block = parent
	}
	return block.Hash() == checkpoint.BlockHash
}

func (n *Node) validatePersistedFinalityEvidence(
	evidence types.FinalityEquivocationEvidence,
	known map[string]types.Block,
	validatorsByBlock map[string][]string,
) error {
	validator, err := chaincrypto.NormalizeAddress(evidence.Validator)
	if err != nil || validator != evidence.Validator {
		return errors.New("finality evidence validator is not canonically encoded")
	}
	if evidence.Height == 0 || evidence.FirstBlockHash == evidence.SecondBlockHash {
		return errors.New("finality evidence must identify two distinct non-genesis blocks")
	}
	if err := types.ValidateCanonicalHash("first finality evidence block hash", evidence.FirstBlockHash); err != nil {
		return err
	}
	if err := types.ValidateCanonicalHash("second finality evidence block hash", evidence.SecondBlockHash); err != nil {
		return err
	}
	if err := types.ValidateCanonicalSignature("first finality evidence signature", evidence.FirstSignature); err != nil {
		return err
	}
	if err := types.ValidateCanonicalSignature("second finality evidence signature", evidence.SecondSignature); err != nil {
		return err
	}
	first, firstOK := known[evidence.FirstBlockHash]
	second, secondOK := known[evidence.SecondBlockHash]
	if !firstOK || !secondOK || first.Header.Height != evidence.Height || second.Header.Height != evidence.Height {
		return errors.New("finality evidence blocks are missing or have the wrong height")
	}
	firstValidators, firstOK := validatorsByBlock[evidence.FirstBlockHash]
	secondValidators, secondOK := validatorsByBlock[evidence.SecondBlockHash]
	if !firstOK || !secondOK ||
		!validatorSetContains(firstValidators, validator) ||
		!validatorSetContains(secondValidators, validator) {
		return errors.New("finality evidence signer is not a validator for both blocks")
	}
	firstVote := types.FinalitySignature{
		ChainID: n.chainID, Height: evidence.Height, BlockHash: evidence.FirstBlockHash,
		Validator: validator, Signature: evidence.FirstSignature,
	}
	secondVote := types.FinalitySignature{
		ChainID: n.chainID, Height: evidence.Height, BlockHash: evidence.SecondBlockHash,
		Validator: validator, Signature: evidence.SecondSignature,
	}
	if !consensus.VerifyFinalityVote(first, firstVote) || !consensus.VerifyFinalityVote(second, secondVote) {
		return errors.New("finality evidence signature is invalid")
	}
	return nil
}

func canonicalBlocksEqual(left types.Block, right types.Block) (bool, error) {
	leftBytes, err := hash.CanonicalBytes(left)
	if err != nil {
		return false, fmt.Errorf("encode persisted block: %w", err)
	}
	rightBytes, err := hash.CanonicalBytes(right)
	if err != nil {
		return false, fmt.Errorf("encode expected block: %w", err)
	}
	return bytes.Equal(leftBytes, rightBytes), nil
}

func diskSnapshotChecksum(snapshot diskSnapshot) (string, error) {
	snapshot.Checksum = ""
	checksum, err := hash.Hex(snapshot)
	if err != nil {
		return "", fmt.Errorf("checksum persisted snapshot: %w", err)
	}
	return checksum, nil
}

func loadDiskSnapshot(dataDir string) (*diskSnapshot, error) {
	path := chainPath(dataDir)
	raw, err := readBoundedRegularFile(path, "persisted snapshot", MaxDiskSnapshotBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var snapshot diskSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("decode persisted snapshot: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("persisted snapshot must contain exactly one JSON value")
		}
		return nil, fmt.Errorf("decode trailing persisted snapshot data: %w", err)
	}
	if !supportedDiskSnapshotVersion(snapshot.Version) {
		return nil, fmt.Errorf("unsupported persisted snapshot version %d; explicit migration is required", snapshot.Version)
	}
	if snapshot.Generation == 0 {
		return nil, errors.New("persisted snapshot generation is required")
	}
	if err := types.ValidateCanonicalHash("persisted snapshot checksum", snapshot.Checksum); err != nil {
		return nil, err
	}
	expectedChecksum, err := diskSnapshotChecksum(snapshot)
	if err != nil {
		return nil, err
	}
	if snapshot.Checksum != expectedChecksum {
		return nil, errors.New("persisted snapshot checksum mismatch")
	}
	return &snapshot, nil
}
