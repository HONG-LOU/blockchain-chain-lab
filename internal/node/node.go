package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"chainlab/internal/consensus"
	"chainlab/internal/contracts"
	"chainlab/internal/core"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/hash"
	"chainlab/internal/state"
	"chainlab/internal/types"
)

type Config struct {
	ChainID        string
	ProposerKey    chaincrypto.PrivateKey
	Validators     []string
	GenesisBalance map[string]uint64
	FeeCollector   string
	DataDir        string
	BlockGasLimit  uint64
}

type Node struct {
	mu                sync.Mutex
	chainID           string
	proposerKey       chaincrypto.PrivateKey
	proposer          string
	state             *state.Store
	executor          *core.Executor
	consensus         *consensus.POA
	blocks            []types.Block
	genesisState      state.Snapshot
	knownBlocks       map[string]types.Block
	finalityVotes     map[string]map[string]types.FinalitySignature
	finalityVoteIndex map[uint64]map[string]finalityVoteRecord
	finalityEvidence  map[string]types.FinalityEquivocationEvidence
	mempool           []types.Transaction
	txIndex           map[string]types.TransactionRecord
	eventIndex        []types.EventRecord
	dataDir           string
	blockGasLimit     uint64
}

type finalityVoteRecord struct {
	BlockHash string
	Signature string
}

const (
	SafeBlockDepth              uint64 = 1
	FinalizedBlockDepth         uint64 = 2
	DefaultBlockGasLimit        uint64 = 30_000_000
	InitialBaseFeePerGas        uint64 = 1
	DefaultMaxPriorityFeePerGas uint64 = 1
	baseFeeChangeDenominator    uint64 = 8
	txpoolReplacementPriceBump  uint64 = 10
)

var errReplacementTransactionUnderpriced = errors.New("replacement transaction underpriced")

type FinalityCheckpoint struct {
	HeadHeight       uint64 `json:"head_height"`
	HeadHash         string `json:"head_hash"`
	SafeHeight       uint64 `json:"safe_height"`
	SafeHash         string `json:"safe_hash"`
	SafeDepth        uint64 `json:"safe_depth"`
	SafeSource       string `json:"safe_source"`
	FinalizedHeight  uint64 `json:"finalized_height"`
	FinalizedHash    string `json:"finalized_hash"`
	FinalizedDepth   uint64 `json:"finalized_depth"`
	FinalizedSource  string `json:"finalized_source"`
	CertifiedHeight  uint64 `json:"certified_height,omitempty"`
	CertifiedHash    string `json:"certified_hash,omitempty"`
	CertifiedSigners int    `json:"certified_signers,omitempty"`
	CertifiedQuorum  int    `json:"certified_quorum,omitempty"`
}

type MempoolSnapshot struct {
	Pending      []types.Transaction `json:"pending"`
	PendingCount int                 `json:"pending_count"`
	QueuedCount  int                 `json:"queued_count"`
}

type FeeMarketSnapshot struct {
	BaseFeePerGas        uint64 `json:"base_fee_per_gas"`
	NextBaseFeePerGas    uint64 `json:"next_base_fee_per_gas"`
	MaxPriorityFeePerGas uint64 `json:"max_priority_fee_per_gas"`
	GasPrice             uint64 `json:"gas_price"`
	BlockGasLimit        uint64 `json:"block_gas_limit"`
	LastBlockGasUsed     uint64 `json:"last_block_gas_used"`
}

type EventFilter struct {
	FromBlock  uint64
	ToBlock    uint64
	HasToBlock bool
	Address    string
	Topic0     string
	Limit      int
	Descending bool
}

type diskSnapshot struct {
	ChainID          string                               `json:"chain_id"`
	GenesisState     state.Snapshot                       `json:"genesis_state,omitempty"`
	State            state.Snapshot                       `json:"state"`
	Blocks           []types.Block                        `json:"blocks"`
	KnownBlocks      []types.Block                        `json:"known_blocks,omitempty"`
	FinalityEvidence []types.FinalityEquivocationEvidence `json:"finality_evidence,omitempty"`
}

func New(config Config) (*Node, error) {
	if config.ChainID == "" {
		return nil, errors.New("chain id is required")
	}
	if config.ProposerKey == nil {
		return nil, errors.New("proposer key is required")
	}
	proposer := chaincrypto.AddressFromPrivateKey(config.ProposerKey)
	validators := config.Validators
	if len(validators) == 0 {
		validators = []string{proposer}
	}
	blockGasLimit := config.BlockGasLimit
	if blockGasLimit == 0 {
		blockGasLimit = DefaultBlockGasLimit
	}
	feeCollector := config.FeeCollector
	if feeCollector == "" {
		feeCollector = proposer
	}

	n := &Node{
		chainID:           config.ChainID,
		proposerKey:       config.ProposerKey,
		proposer:          proposer,
		executor:          core.NewExecutor(config.ChainID, feeCollector, contracts.NewRuntimeWithDefaults()),
		consensus:         consensus.NewPOA(validators),
		knownBlocks:       make(map[string]types.Block),
		finalityVotes:     make(map[string]map[string]types.FinalitySignature),
		finalityVoteIndex: make(map[uint64]map[string]finalityVoteRecord),
		finalityEvidence:  make(map[string]types.FinalityEquivocationEvidence),
		txIndex:           make(map[string]types.TransactionRecord),
		dataDir:           config.DataDir,
		blockGasLimit:     blockGasLimit,
	}
	genesisStore := newGenesisStore(config.GenesisBalance, validators)
	genesisState := genesisStore.Snapshot()

	if config.DataDir != "" {
		loaded, err := loadDiskSnapshot(config.DataDir)
		if err != nil {
			return nil, err
		}
		if loaded != nil {
			if loaded.ChainID != config.ChainID {
				return nil, errors.New("persisted chain id does not match config")
			}
			n.genesisState = loaded.GenesisState
			if snapshotIsEmpty(n.genesisState) {
				n.genesisState = genesisState
			}
			n.state = state.NewStoreFromSnapshot(loaded.State)
			if len(n.state.Validators()) == 0 {
				n.state.SetValidators(validators)
			}
			n.refreshConsensusLocked()
			n.blocks = append([]types.Block(nil), loaded.Blocks...)
			if len(n.blocks) == 0 {
				return nil, errors.New("persisted chain has no blocks")
			}
			for _, block := range loaded.KnownBlocks {
				n.knownBlocks[block.Hash()] = block
			}
			for _, block := range n.blocks {
				n.knownBlocks[block.Hash()] = block
			}
			for _, evidence := range loaded.FinalityEvidence {
				n.finalityEvidence[finalityEvidenceKey(evidence.Height, evidence.Validator)] = evidence
			}
			n.rebuildFinalityVoteIndex()
			n.rebuildTxIndex()
			return n, nil
		}
	}

	store := genesisStore
	genesis := types.GenesisBlock(config.ChainID, store.Root())
	genesis.Header.GasLimit = blockGasLimit
	genesis.Header.BaseFeePerGas = InitialBaseFeePerGas
	n.genesisState = genesisState
	n.state = store
	n.refreshConsensusLocked()
	n.blocks = []types.Block{genesis}
	n.knownBlocks[genesis.Hash()] = genesis
	if err := n.persistLocked(); err != nil {
		return nil, err
	}
	return n, nil
}

func newGenesisStore(balances map[string]uint64, validators []string) *state.Store {
	store := state.NewStore()
	for address, balance := range balances {
		store.SetBalance(address, balance)
	}
	store.SetValidators(validators)
	return store
}

func snapshotIsEmpty(snapshot state.Snapshot) bool {
	return len(snapshot.Accounts) == 0 && len(snapshot.Stakes) == 0 && len(snapshot.Proposals) == 0 && len(snapshot.Params) == 0 && len(snapshot.Validators) == 0
}

func (n *Node) SubmitTx(tx types.Transaction) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	return n.submitTxLocked(tx)
}

func (n *Node) RequestFaucet(to string, amount uint64) (types.Transaction, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	to = strings.TrimSpace(to)
	if to == "" {
		return types.Transaction{}, errors.New("faucet recipient is required")
	}
	if amount == 0 {
		return types.Transaction{}, errors.New("faucet amount must be positive")
	}

	working, err := n.pendingStateLocked()
	if err != nil {
		return types.Transaction{}, err
	}
	account := working.GetAccount(n.proposer)
	tx := types.Transaction{
		ChainID:  n.chainID,
		Type:     types.TxTransfer,
		From:     n.proposer,
		To:       to,
		Nonce:    account.Nonce,
		Value:    amount,
		GasLimit: 21_000,
		GasPrice: n.suggestedGasPriceLocked(),
	}
	signature, err := chaincrypto.Sign(n.proposerKey, tx.SigningBytes())
	if err != nil {
		return types.Transaction{}, err
	}
	tx.Signature = signature
	if err := n.submitTxLocked(tx); err != nil {
		return types.Transaction{}, err
	}
	return tx, nil
}

func (n *Node) submitTxLocked(tx types.Transaction) error {
	replacementIndex := n.mempoolReplacementIndex(tx)
	if replacementIndex >= 0 {
		if err := canReplacePendingTransaction(n.mempool[replacementIndex], tx); err != nil {
			return err
		}
		if err := n.validateMempoolWithReplacementLocked(replacementIndex, tx); err != nil {
			return err
		}
		n.mempool[replacementIndex] = tx
		return nil
	}

	working, err := n.pendingStateLocked()
	if err != nil {
		return err
	}
	blockHeight := n.blocks[len(n.blocks)-1].Header.Height + 1
	baseFee := n.nextBaseFeeLocked()
	if _, err := n.executor.ExecuteWithContext(working, tx, core.ExecutionContext{BlockHeight: blockHeight, BaseFeePerGas: baseFee}); err != nil {
		return err
	}
	n.mempool = append(n.mempool, tx)
	return nil
}

func (n *Node) ProduceBlock() (types.Block, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	working := n.state.Clone()
	receipts := make([]types.Receipt, 0, len(n.mempool))
	parent := n.blocks[len(n.blocks)-1]
	blockHeight := parent.Header.Height + 1
	baseFee := n.nextBaseFeeLocked()
	var gasUsed uint64
	for _, tx := range n.mempool {
		receipt, err := n.executor.ExecuteWithContext(working, tx, core.ExecutionContext{BlockHeight: blockHeight, BaseFeePerGas: baseFee})
		if err != nil {
			return types.Block{}, err
		}
		nextGasUsed, err := checkedAdd(gasUsed, receipt.GasUsed)
		if err != nil {
			return types.Block{}, err
		}
		if nextGasUsed > n.blockGasLimit {
			return types.Block{}, errors.New("block gas limit exceeded")
		}
		gasUsed = nextGasUsed
		receipts = append(receipts, receipt)
	}

	block := types.Block{
		Header: types.BlockHeader{
			ChainID:       n.chainID,
			Height:        blockHeight,
			ParentHash:    parent.Hash(),
			TimeUnix:      time.Now().Unix(),
			Proposer:      n.proposer,
			GasLimit:      n.blockGasLimit,
			GasUsed:       gasUsed,
			BaseFeePerGas: baseFee,
			TxRoot:        types.TransactionRoot(n.mempool),
			ReceiptRoot:   types.ReceiptRoot(receipts),
			StateRoot:     working.Root(),
		},
		Transactions: append([]types.Transaction(nil), n.mempool...),
		Receipts:     receipts,
	}
	if err := consensus.SignBlock(n.proposerKey, &block); err != nil {
		return types.Block{}, err
	}
	if err := n.consensus.ValidateBlock(parent, block); err != nil {
		return types.Block{}, err
	}

	n.state.ReplaceWith(working)
	n.refreshConsensusLocked()
	n.blocks = append(n.blocks, block)
	n.knownBlocks[block.Hash()] = block
	n.indexBlock(block)
	n.mempool = nil
	if err := n.persistLocked(); err != nil {
		return types.Block{}, err
	}
	return block, nil
}

func (n *Node) ImportBlock(block types.Block) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if block.Header.ChainID != n.chainID {
		return errors.New("imported block chain id does not match node")
	}
	blockHash := block.Hash()
	if _, ok := n.knownBlocks[blockHash]; ok {
		return nil
	}
	parent, ok := n.knownBlocks[block.Header.ParentHash]
	if !ok {
		return errors.New("imported block parent is unknown")
	}
	parentState, err := n.replayStateLocked(parent.Hash())
	if err != nil {
		return err
	}
	if _, err := n.validateBlockOnStateLocked(parent, parentState, block); err != nil {
		return err
	}

	n.knownBlocks[blockHash] = block
	if block.Header.Height > n.blocks[len(n.blocks)-1].Header.Height {
		chain, working, err := n.replayKnownChainLocked(blockHash)
		if err != nil {
			return err
		}
		n.blocks = chain
		n.state.ReplaceWith(working)
		n.refreshConsensusLocked()
		n.rebuildTxIndex()
		n.removeMempoolTransactions(transactionsInBlocks(chain))
	}
	return n.persistLocked()
}

func (n *Node) Head() types.Block {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.blocks[len(n.blocks)-1]
}

func (n *Node) Block(height uint64) (types.Block, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if height >= uint64(len(n.blocks)) {
		return types.Block{}, false
	}
	return n.blocks[height], true
}

func (n *Node) BlockByHash(hash string) (types.Block, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	block, ok := n.knownBlocks[strings.ToLower(strings.TrimSpace(hash))]
	return block, ok
}

func (n *Node) SubmitFinalityVote(vote types.FinalitySignature) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	vote.Validator = strings.ToLower(strings.TrimSpace(vote.Validator))
	if vote.Validator == "" {
		return errors.New("finality vote validator is required")
	}
	block, validators, err := n.matchFinalityVoteLocked(vote)
	if err != nil {
		return err
	}
	blockHash := block.Hash()
	if err := n.recordFinalityVoteLocked(block, vote); err != nil {
		return err
	}
	if n.finalityVotes == nil {
		n.finalityVotes = make(map[string]map[string]types.FinalitySignature)
	}
	votes := n.finalityVotes[blockHash]
	if votes == nil {
		votes = make(map[string]types.FinalitySignature)
		if block.FinalityCertificate != nil {
			for _, signature := range block.FinalityCertificate.Signatures {
				validator := strings.ToLower(strings.TrimSpace(signature.Validator))
				if validator != "" {
					votes[validator] = types.FinalitySignature{Validator: validator, Signature: signature.Signature}
				}
			}
		}
		n.finalityVotes[blockHash] = votes
	}
	votes[vote.Validator] = vote
	if len(votes) < consensus.FinalityQuorumSize(len(validators)) {
		return nil
	}
	certified := block
	certified.FinalityCertificate = &types.FinalityCertificate{
		ChainID:    block.Header.ChainID,
		Height:     block.Header.Height,
		BlockHash:  blockHash,
		Signatures: sortedFinalitySignatures(votes),
	}
	parent := n.blocks[certified.Header.Height-1]
	engine := consensus.NewPOA(validators)
	if err := engine.ValidateBlock(parent, certified); err != nil {
		return err
	}
	n.blocks[certified.Header.Height] = certified
	n.knownBlocks[blockHash] = certified
	return n.persistLocked()
}

func (n *Node) FinalityEvidence() []types.FinalityEquivocationEvidence {
	n.mu.Lock()
	defer n.mu.Unlock()

	evidence := make([]types.FinalityEquivocationEvidence, 0, len(n.finalityEvidence))
	for _, item := range n.finalityEvidence {
		evidence = append(evidence, item)
	}
	sort.Slice(evidence, func(i int, j int) bool {
		if evidence[i].Height != evidence[j].Height {
			return evidence[i].Height < evidence[j].Height
		}
		return evidence[i].Validator < evidence[j].Validator
	})
	return evidence
}

func (n *Node) Finality() FinalityCheckpoint {
	n.mu.Lock()
	defer n.mu.Unlock()

	head := n.blocks[len(n.blocks)-1]
	if certified, ok := n.highestCertifiedBlockLocked(); ok {
		depth := head.Header.Height - certified.Header.Height
		quorum := consensus.FinalityQuorumSize(len(n.validatorsForBlockOrCurrentLocked(certified)))
		return FinalityCheckpoint{
			HeadHeight:       head.Header.Height,
			HeadHash:         head.Hash(),
			SafeHeight:       certified.Header.Height,
			SafeHash:         certified.Hash(),
			SafeDepth:        depth,
			SafeSource:       "bft_certificate",
			FinalizedHeight:  certified.Header.Height,
			FinalizedHash:    certified.Hash(),
			FinalizedDepth:   depth,
			FinalizedSource:  "bft_certificate",
			CertifiedHeight:  certified.Header.Height,
			CertifiedHash:    certified.Hash(),
			CertifiedSigners: len(certified.FinalityCertificate.Signatures),
			CertifiedQuorum:  quorum,
		}
	}
	safe := n.blockAtDepthLocked(SafeBlockDepth)
	finalized := n.blockAtDepthLocked(FinalizedBlockDepth)
	return FinalityCheckpoint{
		HeadHeight:      head.Header.Height,
		HeadHash:        head.Hash(),
		SafeHeight:      safe.Header.Height,
		SafeHash:        safe.Hash(),
		SafeDepth:       SafeBlockDepth,
		SafeSource:      "depth_fallback",
		FinalizedHeight: finalized.Header.Height,
		FinalizedHash:   finalized.Hash(),
		FinalizedDepth:  FinalizedBlockDepth,
		FinalizedSource: "depth_fallback",
	}
}

func (n *Node) Transaction(hash string) (types.TransactionRecord, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	record, ok := n.txIndex[hash]
	return record, ok
}

func (n *Node) Events(filter EventFilter) []types.EventRecord {
	n.mu.Lock()
	defer n.mu.Unlock()

	toBlock := filter.ToBlock
	if !filter.HasToBlock && filter.ToBlock == 0 {
		toBlock = n.blocks[len(n.blocks)-1].Header.Height
	}
	if toBlock < filter.FromBlock {
		return []types.EventRecord{}
	}
	address := strings.ToLower(filter.Address)
	topic0 := strings.ToLower(filter.Topic0)
	events := make([]types.EventRecord, 0)
	add := func(record types.EventRecord) bool {
		if !eventMatchesFilter(record, filter.FromBlock, toBlock, address, topic0) {
			return false
		}
		events = append(events, cloneEventRecord(record))
		return filter.Limit > 0 && len(events) == filter.Limit
	}
	if filter.Descending {
		for i := len(n.eventIndex) - 1; i >= 0; {
			height := n.eventIndex[i].BlockHeight
			start := i
			for start >= 0 && n.eventIndex[start].BlockHeight == height {
				start--
			}
			for j := start + 1; j <= i; j++ {
				if add(n.eventIndex[j]) {
					return events
				}
			}
			i = start
		}
		return events
	}
	for _, record := range n.eventIndex {
		if add(record) {
			return events
		}
	}
	return events
}

func (n *Node) ChainID() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.chainID
}

func (n *Node) Proposer() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.proposer
}

func (n *Node) Account(address string) types.Account {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state.GetAccount(address)
}

func (n *Node) PendingAccount(address string) (types.Account, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	working, err := n.pendingStateLocked()
	if err != nil {
		return types.Account{}, err
	}
	return working.GetAccount(address), nil
}

func (n *Node) StateRoot() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state.Root()
}

func (n *Node) StakeOf(address string) uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state.StakeOf(address)
}

func (n *Node) Validators() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state.Validators()
}

func (n *Node) Mempool() []types.Transaction {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]types.Transaction(nil), n.mempool...)
}

func (n *Node) TxPool() MempoolSnapshot {
	pending := n.Mempool()
	return MempoolSnapshot{
		Pending:      pending,
		PendingCount: len(pending),
		QueuedCount:  0,
	}
}

func (n *Node) FeeMarket() FeeMarketSnapshot {
	n.mu.Lock()
	defer n.mu.Unlock()
	head := n.blocks[len(n.blocks)-1]
	return FeeMarketSnapshot{
		BaseFeePerGas:        effectiveHeaderBaseFee(head),
		NextBaseFeePerGas:    n.nextBaseFeeLocked(),
		MaxPriorityFeePerGas: DefaultMaxPriorityFeePerGas,
		GasPrice:             n.suggestedGasPriceLocked(),
		BlockGasLimit:        n.blockGasLimit,
		LastBlockGasUsed:     head.Header.GasUsed,
	}
}

func (n *Node) ReadContract(from string, to string, method string, args map[string]string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	runtime := contracts.NewRuntimeWithDefaults()
	return runtime.Read(n.state.Clone(), to, from, method, args)
}

func (n *Node) Proposal(id string) types.Proposal {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state.Proposal(id)
}

func (n *Node) Param(key string) string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state.Param(key)
}

func (n *Node) blockAtDepthLocked(depth uint64) types.Block {
	headHeight := uint64(len(n.blocks) - 1)
	if headHeight <= depth {
		return n.blocks[0]
	}
	return n.blocks[headHeight-depth]
}

func (n *Node) highestCertifiedBlockLocked() (types.Block, bool) {
	for i := len(n.blocks) - 1; i >= 1; i-- {
		block := n.blocks[i]
		if block.FinalityCertificate != nil {
			return block, true
		}
	}
	return types.Block{}, false
}

func (n *Node) matchFinalityVoteLocked(vote types.FinalitySignature) (types.Block, []string, error) {
	for i := len(n.blocks) - 1; i >= 1; i-- {
		block := n.blocks[i]
		if !consensus.VerifyFinalityVote(block, vote) {
			continue
		}
		validators, err := n.validatorsForBlockLocked(block)
		if err != nil {
			return types.Block{}, nil, err
		}
		if !validatorSetContains(validators, vote.Validator) {
			return types.Block{}, nil, errors.New("finality vote signer is not a validator for block")
		}
		return block, validators, nil
	}
	return types.Block{}, nil, errors.New("finality vote does not match a canonical block")
}

func (n *Node) validatorsForBlockOrCurrentLocked(block types.Block) []string {
	validators, err := n.validatorsForBlockLocked(block)
	if err != nil || len(validators) == 0 {
		return n.state.Validators()
	}
	return validators
}

func (n *Node) validatorsForBlockLocked(block types.Block) ([]string, error) {
	if block.Header.Height == 0 {
		return n.state.Validators(), nil
	}
	parentState, err := n.replayStateLocked(block.Header.ParentHash)
	if err != nil {
		return nil, err
	}
	return parentState.Validators(), nil
}

func validatorSetContains(validators []string, validator string) bool {
	validator = strings.ToLower(strings.TrimSpace(validator))
	for _, candidate := range validators {
		if strings.ToLower(strings.TrimSpace(candidate)) == validator {
			return true
		}
	}
	return false
}

func sortedFinalitySignatures(votes map[string]types.FinalitySignature) []types.FinalitySignature {
	validators := make([]string, 0, len(votes))
	for validator := range votes {
		validators = append(validators, validator)
	}
	sort.Strings(validators)
	signatures := make([]types.FinalitySignature, 0, len(validators))
	for _, validator := range validators {
		vote := votes[validator]
		vote.Validator = validator
		signatures = append(signatures, vote)
	}
	return signatures
}

func (n *Node) rebuildFinalityVoteIndex() {
	n.finalityVoteIndex = make(map[uint64]map[string]finalityVoteRecord)
	for _, block := range n.knownBlocks {
		if block.FinalityCertificate == nil {
			continue
		}
		for _, vote := range block.FinalityCertificate.Signatures {
			n.recordFinalityVoteInIndex(block, types.FinalitySignature{
				Validator: strings.ToLower(strings.TrimSpace(vote.Validator)),
				Signature: vote.Signature,
			})
		}
	}
	for _, evidence := range n.finalityEvidence {
		validator := strings.ToLower(strings.TrimSpace(evidence.Validator))
		heightVotes := n.finalityVoteIndex[evidence.Height]
		if heightVotes == nil {
			heightVotes = make(map[string]finalityVoteRecord)
			n.finalityVoteIndex[evidence.Height] = heightVotes
		}
		if _, ok := heightVotes[validator]; !ok {
			heightVotes[validator] = finalityVoteRecord{
				BlockHash: evidence.FirstBlockHash,
				Signature: evidence.FirstSignature,
			}
		}
	}
}

func (n *Node) recordFinalityVoteLocked(block types.Block, vote types.FinalitySignature) error {
	if n.finalityVoteIndex == nil {
		n.finalityVoteIndex = make(map[uint64]map[string]finalityVoteRecord)
	}
	if n.finalityEvidence == nil {
		n.finalityEvidence = make(map[string]types.FinalityEquivocationEvidence)
	}
	heightVotes := n.finalityVoteIndex[block.Header.Height]
	if heightVotes == nil {
		heightVotes = make(map[string]finalityVoteRecord)
		n.finalityVoteIndex[block.Header.Height] = heightVotes
	}
	existing, ok := heightVotes[vote.Validator]
	blockHash := block.Hash()
	if !ok {
		heightVotes[vote.Validator] = finalityVoteRecord{BlockHash: blockHash, Signature: vote.Signature}
		return nil
	}
	if existing.BlockHash == blockHash {
		return nil
	}
	evidence := types.FinalityEquivocationEvidence{
		Validator:       vote.Validator,
		Height:          block.Header.Height,
		FirstBlockHash:  existing.BlockHash,
		FirstSignature:  existing.Signature,
		SecondBlockHash: blockHash,
		SecondSignature: vote.Signature,
	}
	evidenceKey := finalityEvidenceKey(evidence.Height, evidence.Validator)
	var slashErr error
	if _, exists := n.finalityEvidence[evidenceKey]; !exists {
		n.finalityEvidence[evidenceKey] = evidence
		slashErr = n.enqueueFinalityEvidenceSlashLocked(evidence)
	}
	if err := n.persistLocked(); err != nil {
		return err
	}
	if slashErr != nil {
		return fmt.Errorf("finality equivocation: validator %s signed height %d for %s and %s; automatic slash skipped: %w", vote.Validator, block.Header.Height, existing.BlockHash, blockHash, slashErr)
	}
	return fmt.Errorf("finality equivocation: validator %s signed height %d for %s and %s", vote.Validator, block.Header.Height, existing.BlockHash, blockHash)
}

func (n *Node) recordFinalityVoteInIndex(block types.Block, vote types.FinalitySignature) {
	if vote.Validator == "" {
		return
	}
	heightVotes := n.finalityVoteIndex[block.Header.Height]
	if heightVotes == nil {
		heightVotes = make(map[string]finalityVoteRecord)
		n.finalityVoteIndex[block.Header.Height] = heightVotes
	}
	if _, ok := heightVotes[vote.Validator]; !ok {
		heightVotes[vote.Validator] = finalityVoteRecord{BlockHash: block.Hash(), Signature: vote.Signature}
	}
}

func finalityEvidenceKey(height uint64, validator string) string {
	return fmt.Sprintf("%d:%s", height, strings.ToLower(strings.TrimSpace(validator)))
}

func (n *Node) enqueueFinalityEvidenceSlashLocked(evidence types.FinalityEquivocationEvidence) error {
	target := strings.ToLower(strings.TrimSpace(evidence.Validator))
	if target == "" {
		return errors.New("slash evidence validator is required")
	}
	working, err := n.pendingStateLocked()
	if err != nil {
		return err
	}
	amount := working.StakeOf(target)
	if amount == 0 {
		return errors.New("slash target has no stake")
	}
	reporter := strings.ToLower(strings.TrimSpace(n.proposer))
	account := working.GetAccount(reporter)
	gasLimit, err := core.EstimateGas(types.TxValidatorSlash)
	if err != nil {
		return err
	}
	tx := types.Transaction{
		ChainID:  n.chainID,
		Type:     types.TxValidatorSlash,
		From:     reporter,
		Nonce:    account.Nonce,
		GasLimit: gasLimit,
		GasPrice: n.suggestedGasPriceLocked(),
		Payload: map[string]string{
			"target":   target,
			"amount":   strconv.FormatUint(amount, 10),
			"evidence": finalityEvidenceReference(evidence),
		},
	}
	signature, err := chaincrypto.Sign(n.proposerKey, tx.SigningBytes())
	if err != nil {
		return err
	}
	tx.Signature = signature
	return n.submitTxLocked(tx)
}

func finalityEvidenceReference(evidence types.FinalityEquivocationEvidence) string {
	return fmt.Sprintf("finality-equivocation:%d:%s:%s:%s",
		evidence.Height,
		strings.ToLower(strings.TrimSpace(evidence.Validator)),
		evidence.FirstBlockHash,
		evidence.SecondBlockHash,
	)
}

func (n *Node) pendingStateLocked() (*state.Store, error) {
	working := n.state.Clone()
	blockHeight := n.blocks[len(n.blocks)-1].Header.Height + 1
	baseFee := n.nextBaseFeeLocked()
	for _, pending := range n.mempool {
		if _, err := n.executor.ExecuteWithContext(working, pending, core.ExecutionContext{BlockHeight: blockHeight, BaseFeePerGas: baseFee}); err != nil {
			return nil, err
		}
	}
	return working, nil
}

func (n *Node) mempoolReplacementIndex(tx types.Transaction) int {
	from := normalizedAddress(tx.From)
	if from == "" {
		return -1
	}
	for i, pending := range n.mempool {
		if normalizedAddress(pending.From) == from && pending.Nonce == tx.Nonce {
			return i
		}
	}
	return -1
}

func (n *Node) validateMempoolWithReplacementLocked(index int, replacement types.Transaction) error {
	working := n.state.Clone()
	blockHeight := n.blocks[len(n.blocks)-1].Header.Height + 1
	baseFee := n.nextBaseFeeLocked()
	for i, pending := range n.mempool {
		candidate := pending
		if i == index {
			candidate = replacement
		}
		if _, err := n.executor.ExecuteWithContext(working, candidate, core.ExecutionContext{BlockHeight: blockHeight, BaseFeePerGas: baseFee}); err != nil {
			return err
		}
	}
	return nil
}

func normalizedAddress(address string) string {
	return strings.ToLower(strings.TrimSpace(address))
}

func (n *Node) replayStateLocked(blockHash string) (*state.Store, error) {
	_, working, err := n.replayKnownChainLocked(blockHash)
	return working, err
}

func (n *Node) replayKnownChainLocked(blockHash string) ([]types.Block, *state.Store, error) {
	chain, err := n.knownChainLocked(blockHash)
	if err != nil {
		return nil, nil, err
	}
	if len(chain) == 0 {
		return nil, nil, errors.New("known chain is empty")
	}
	working := state.NewStoreFromSnapshot(n.genesisState)
	if chain[0].Header.Height != 0 || chain[0].Header.StateRoot != working.Root() {
		return nil, nil, errors.New("genesis state root mismatch")
	}
	for i := 1; i < len(chain); i++ {
		next, err := n.validateBlockOnStateLocked(chain[i-1], working, chain[i])
		if err != nil {
			return nil, nil, err
		}
		working = next
	}
	return chain, working, nil
}

func (n *Node) knownChainLocked(blockHash string) ([]types.Block, error) {
	block, ok := n.knownBlocks[blockHash]
	if !ok {
		return nil, errors.New("block is unknown")
	}
	reversed := []types.Block{block}
	for block.Header.Height > 0 {
		parent, ok := n.knownBlocks[block.Header.ParentHash]
		if !ok {
			return nil, errors.New("known chain parent is missing")
		}
		reversed = append(reversed, parent)
		block = parent
	}
	chain := make([]types.Block, len(reversed))
	for i := range reversed {
		chain[len(reversed)-1-i] = reversed[i]
	}
	return chain, nil
}

func (n *Node) validateBlockOnStateLocked(parent types.Block, parentState *state.Store, block types.Block) (*state.Store, error) {
	working := parentState.Clone()
	executor := core.NewExecutor(n.chainID, block.Header.Proposer, contracts.NewRuntimeWithDefaults())
	receipts := make([]types.Receipt, 0, len(block.Transactions))
	if block.Header.GasLimit == 0 {
		return nil, errors.New("block gas limit is required")
	}
	if block.Header.BaseFeePerGas != NextBaseFee(parent, n.blockGasLimit) {
		return nil, errors.New("imported block base fee mismatch")
	}
	var gasUsed uint64
	for _, tx := range block.Transactions {
		receipt, err := executor.ExecuteWithContext(working, tx, core.ExecutionContext{BlockHeight: block.Header.Height, BaseFeePerGas: block.Header.BaseFeePerGas})
		if err != nil {
			return nil, err
		}
		gasUsed, err = checkedAdd(gasUsed, receipt.GasUsed)
		if err != nil {
			return nil, err
		}
		receipts = append(receipts, receipt)
	}
	if gasUsed != block.Header.GasUsed {
		return nil, errors.New("imported block gas used mismatch")
	}
	if gasUsed > block.Header.GasLimit {
		return nil, errors.New("imported block gas limit exceeded")
	}
	if types.ReceiptRoot(receipts) != block.Header.ReceiptRoot {
		return nil, errors.New("imported block receipt root mismatch")
	}
	if working.Root() != block.Header.StateRoot {
		return nil, errors.New("imported block state root mismatch")
	}
	engine := consensus.NewPOA(parentState.Validators())
	if err := engine.ValidateBlock(parent, block); err != nil {
		return nil, err
	}
	return working, nil
}

func (n *Node) nextBaseFeeLocked() uint64 {
	return NextBaseFee(n.blocks[len(n.blocks)-1], n.blockGasLimit)
}

func (n *Node) suggestedGasPriceLocked() uint64 {
	gasPrice, err := checkedAdd(n.nextBaseFeeLocked(), DefaultMaxPriorityFeePerGas)
	if err != nil {
		return math.MaxUint64
	}
	return gasPrice
}

func NextBaseFee(parent types.Block, fallbackGasLimit uint64) uint64 {
	parentBaseFee := effectiveHeaderBaseFee(parent)
	parentGasLimit := parent.Header.GasLimit
	if parentGasLimit == 0 {
		parentGasLimit = fallbackGasLimit
	}
	if parentGasLimit == 0 {
		parentGasLimit = DefaultBlockGasLimit
	}
	target := parentGasLimit / 2
	if target == 0 {
		target = 1
	}
	if parent.Header.GasUsed == target {
		return parentBaseFee
	}
	if parent.Header.GasUsed > target {
		delta := parent.Header.GasUsed - target
		increase := parentBaseFee * delta / target / baseFeeChangeDenominator
		if increase == 0 {
			increase = 1
		}
		if math.MaxUint64-parentBaseFee < increase {
			return math.MaxUint64
		}
		return parentBaseFee + increase
	}
	delta := target - parent.Header.GasUsed
	decrease := parentBaseFee * delta / target / baseFeeChangeDenominator
	if decrease >= parentBaseFee {
		return InitialBaseFeePerGas
	}
	next := parentBaseFee - decrease
	if next < InitialBaseFeePerGas {
		return InitialBaseFeePerGas
	}
	return next
}

func effectiveHeaderBaseFee(block types.Block) uint64 {
	if block.Header.BaseFeePerGas == 0 {
		return InitialBaseFeePerGas
	}
	return block.Header.BaseFeePerGas
}

func checkedAdd(left uint64, right uint64) (uint64, error) {
	if math.MaxUint64-left < right {
		return 0, errors.New("gas overflow")
	}
	return left + right, nil
}

func canReplacePendingTransaction(oldTx types.Transaction, newTx types.Transaction) error {
	oldMaxFee, oldPriorityFee := replacementFeeCaps(oldTx)
	newMaxFee, newPriorityFee := replacementFeeCaps(newTx)
	if !feeBumpedByPercent(oldMaxFee, newMaxFee, txpoolReplacementPriceBump) ||
		!feeBumpedByPercent(oldPriorityFee, newPriorityFee, txpoolReplacementPriceBump) {
		return errReplacementTransactionUnderpriced
	}
	return nil
}

func replacementFeeCaps(tx types.Transaction) (uint64, uint64) {
	if tx.MaxFeePerGas > 0 || tx.MaxPriorityFeePerGas > 0 {
		return tx.MaxFeePerGas, tx.MaxPriorityFeePerGas
	}
	return tx.GasPrice, tx.GasPrice
}

func feeBumpedByPercent(oldFee uint64, newFee uint64, percent uint64) bool {
	required, ok := bumpedFeeThreshold(oldFee, percent)
	return ok && newFee >= required
}

func bumpedFeeThreshold(oldFee uint64, percent uint64) (uint64, bool) {
	if oldFee == 0 {
		return 0, true
	}
	whole := oldFee / 100
	remainder := oldFee % 100
	if percent != 0 && whole > math.MaxUint64/percent {
		return 0, false
	}
	bump := whole * percent
	if percent != 0 && remainder > math.MaxUint64/percent {
		return 0, false
	}
	remainderProduct := remainder * percent
	if math.MaxUint64-bump < remainderProduct/100 {
		return 0, false
	}
	bump += remainderProduct / 100
	if remainderProduct%100 != 0 {
		bump++
	}
	if bump == 0 {
		bump = 1
	}
	if math.MaxUint64-oldFee < bump {
		return 0, false
	}
	return oldFee + bump, true
}

func (n *Node) refreshConsensusLocked() {
	n.consensus = consensus.NewPOA(n.state.Validators())
}

func (n *Node) rebuildTxIndex() {
	n.txIndex = make(map[string]types.TransactionRecord)
	n.eventIndex = nil
	for _, block := range n.blocks {
		n.indexBlock(block)
	}
}

func (n *Node) indexBlock(block types.Block) {
	blockHash := block.Hash()
	blockLogIndex := uint64(0)
	for i, tx := range block.Transactions {
		receipt := types.Receipt{TxHash: tx.Hash()}
		if i < len(block.Receipts) {
			receipt = block.Receipts[i]
		}
		n.txIndex[tx.Hash()] = types.TransactionRecord{
			Transaction: tx,
			Receipt:     receipt,
			BlockHeight: block.Header.Height,
			BlockHash:   blockHash,
			Index:       i,
		}
		address := strings.ToLower(eventAddress(tx, receipt))
		for eventIndex, event := range receipt.Events {
			record := types.EventRecord{
				Event:            cloneEvent(event),
				Address:          address,
				Topic0:           eventTopic0(event),
				BlockHeight:      block.Header.Height,
				BlockHash:        blockHash,
				TransactionHash:  tx.Hash(),
				TransactionIndex: i,
				EventIndex:       eventIndex,
			}
			if address != "" {
				record.LogIndex = blockLogIndex
				blockLogIndex++
			}
			n.eventIndex = append(n.eventIndex, record)
		}
	}
}

func eventMatchesFilter(record types.EventRecord, fromBlock uint64, toBlock uint64, address string, topic0 string) bool {
	if record.BlockHeight < fromBlock || record.BlockHeight > toBlock {
		return false
	}
	if address != "" && record.Address != address {
		return false
	}
	return topic0 == "" || strings.ToLower(record.Topic0) == topic0
}

func eventAddress(tx types.Transaction, receipt types.Receipt) string {
	if receipt.ContractAddress != "" {
		return receipt.ContractAddress
	}
	if tx.Type == types.TxCall {
		return tx.To
	}
	return ""
}

func eventTopic0(event types.Event) string {
	return hash.KeccakHex([]byte(event.Type))
}

func cloneEventRecord(record types.EventRecord) types.EventRecord {
	record.Event = cloneEvent(record.Event)
	return record
}

func cloneEvent(event types.Event) types.Event {
	if event.Attributes == nil {
		return event
	}
	event.Attributes = cloneStringMap(event.Attributes)
	return event
}

func cloneStringMap(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func (n *Node) removeMempoolTransactions(txs []types.Transaction) {
	if len(txs) == 0 || len(n.mempool) == 0 {
		return
	}
	included := make(map[string]struct{}, len(txs))
	for _, tx := range txs {
		included[tx.Hash()] = struct{}{}
	}
	remaining := n.mempool[:0]
	for _, tx := range n.mempool {
		if _, ok := included[tx.Hash()]; !ok {
			remaining = append(remaining, tx)
		}
	}
	n.mempool = remaining
}

func transactionsInBlocks(blocks []types.Block) []types.Transaction {
	var txs []types.Transaction
	for _, block := range blocks {
		txs = append(txs, block.Transactions...)
	}
	return txs
}

func (n *Node) persistLocked() error {
	if n.dataDir == "" {
		return nil
	}
	if err := os.MkdirAll(n.dataDir, 0o755); err != nil {
		return err
	}
	snapshot := diskSnapshot{
		ChainID:          n.chainID,
		GenesisState:     n.genesisState,
		State:            n.state.Snapshot(),
		Blocks:           n.blocks,
		KnownBlocks:      n.knownBlockListLocked(),
		FinalityEvidence: n.finalityEvidenceListLocked(),
	}
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	path := chainPath(n.dataDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (n *Node) knownBlockListLocked() []types.Block {
	blocks := make([]types.Block, 0, len(n.knownBlocks))
	for _, block := range n.knownBlocks {
		blocks = append(blocks, block)
	}
	sort.Slice(blocks, func(i int, j int) bool {
		if blocks[i].Header.Height != blocks[j].Header.Height {
			return blocks[i].Header.Height < blocks[j].Header.Height
		}
		return blocks[i].Hash() < blocks[j].Hash()
	})
	return blocks
}

func (n *Node) finalityEvidenceListLocked() []types.FinalityEquivocationEvidence {
	evidence := make([]types.FinalityEquivocationEvidence, 0, len(n.finalityEvidence))
	for _, item := range n.finalityEvidence {
		evidence = append(evidence, item)
	}
	sort.Slice(evidence, func(i int, j int) bool {
		if evidence[i].Height != evidence[j].Height {
			return evidence[i].Height < evidence[j].Height
		}
		return evidence[i].Validator < evidence[j].Validator
	})
	return evidence
}

func loadDiskSnapshot(dataDir string) (*diskSnapshot, error) {
	path := chainPath(dataDir)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var snapshot diskSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func chainPath(dataDir string) string {
	return filepath.Join(dataDir, "chain.json")
}
