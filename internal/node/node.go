package node

import (
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"chainlab/internal/consensus"
	"chainlab/internal/contracts"
	"chainlab/internal/core"
	chaincrypto "chainlab/internal/crypto"
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
	mu            sync.Mutex
	chainID       string
	proposerKey   chaincrypto.PrivateKey
	proposer      string
	state         *state.Store
	executor      *core.Executor
	consensus     *consensus.POA
	blocks        []types.Block
	genesisState  state.Snapshot
	knownBlocks   map[string]types.Block
	finalityVotes map[string]map[string]types.FinalitySignature
	mempool       []types.Transaction
	txIndex       map[string]types.TransactionRecord
	dataDir       string
	blockGasLimit uint64
}

const (
	SafeBlockDepth              uint64 = 1
	FinalizedBlockDepth         uint64 = 2
	DefaultBlockGasLimit        uint64 = 30_000_000
	InitialBaseFeePerGas        uint64 = 1
	DefaultMaxPriorityFeePerGas uint64 = 1
	baseFeeChangeDenominator    uint64 = 8
)

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

type diskSnapshot struct {
	ChainID      string         `json:"chain_id"`
	GenesisState state.Snapshot `json:"genesis_state,omitempty"`
	State        state.Snapshot `json:"state"`
	Blocks       []types.Block  `json:"blocks"`
	KnownBlocks  []types.Block  `json:"known_blocks,omitempty"`
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
		chainID:       config.ChainID,
		proposerKey:   config.ProposerKey,
		proposer:      proposer,
		executor:      core.NewExecutor(config.ChainID, feeCollector, contracts.NewRuntimeWithDefaults()),
		consensus:     consensus.NewPOA(validators),
		knownBlocks:   make(map[string]types.Block),
		finalityVotes: make(map[string]map[string]types.FinalitySignature),
		txIndex:       make(map[string]types.TransactionRecord),
		dataDir:       config.DataDir,
		blockGasLimit: blockGasLimit,
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

func (n *Node) ChainID() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.chainID
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

func (n *Node) refreshConsensusLocked() {
	n.consensus = consensus.NewPOA(n.state.Validators())
}

func (n *Node) rebuildTxIndex() {
	n.txIndex = make(map[string]types.TransactionRecord)
	for _, block := range n.blocks {
		n.indexBlock(block)
	}
}

func (n *Node) indexBlock(block types.Block) {
	blockHash := block.Hash()
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
	}
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
		ChainID:      n.chainID,
		GenesisState: n.genesisState,
		State:        n.state.Snapshot(),
		Blocks:       n.blocks,
		KnownBlocks:  n.knownBlockListLocked(),
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
