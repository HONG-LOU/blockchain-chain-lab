package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	Role            NodeRole
	ChainID         string
	ProposerKey     chaincrypto.PrivateKey
	Validators      []string
	GenesisBalance  map[string]uint64
	GenesisTimeUnix int64
	FeeCollector    string
	DataDir         string
	BlockGasLimit   uint64
}

type NodeRole string

const (
	RoleValidator NodeRole = "validator"
	RoleObserver  NodeRole = "observer"
)

type Node struct {
	mu          sync.Mutex
	role        NodeRole
	chainID     string
	proposerKey chaincrypto.PrivateKey
	proposer    string
	// state is immutable after publication. Canonical commits replace the
	// pointer so concurrent read snapshots never observe in-place mutation.
	state              *state.Store
	runtime            *contracts.Runtime
	executor           transactionExecutor
	consensus          *consensus.POA
	blocks             []types.Block
	genesisState       state.Snapshot
	knownBlocks        map[string]types.Block
	knownBlockHeights  map[uint64]int
	finalityVotes      map[string]map[string]types.FinalitySignature
	finalityVoteIndex  map[uint64]map[string]finalityVoteRecord
	finalityEvidence   map[string]types.FinalityEquivocationEvidence
	finalityLock       finalityLock
	mempool            []types.Transaction
	txPoolRevision     uint64
	queued             []types.Transaction
	txIndex            map[string]types.TransactionRecord
	eventIndex         []types.EventRecord
	dataDir            string
	dataDirLock        *dataDirLock
	blockGasLimit      uint64
	genesisTimeUnix    int64
	snapshotGeneration uint64
	haltErr            error
	closed             bool
	closeErr           error
}

type transactionExecutor interface {
	ExecuteWithContext(*state.Store, types.Transaction, core.ExecutionContext) (types.Receipt, error)
}

type finalityVoteRecord struct {
	BlockHash string
	Signature string
}

type finalityLock struct {
	Height    uint64 `json:"height"`
	BlockHash string `json:"block_hash"`
}

const (
	SafeBlockDepth              uint64 = 1
	DefaultBlockGasLimit        uint64 = 30_000_000
	InitialBaseFeePerGas        uint64 = 1
	DefaultMaxPriorityFeePerGas uint64 = 1
	// DeterministicDevGenesisTimeUnix is a stable timestamp for tests and
	// isolated examples. Real networks must choose and distribute their own.
	DeterministicDevGenesisTimeUnix int64  = 1_700_000_000
	baseFeeChangeDenominator        uint64 = 8
	txpoolReplacementPriceBump      uint64 = 10
	maxTxPoolBytes                         = 128 * 1024 * 1024
	maxTxPoolTransactions                  = MaxTransactionsPerBlock
	maxQueuedTransactions                  = 2048
	maxTransactionsPerSender               = 64
	maxQueuedNonceGap               uint64 = 64
)

var errReplacementTransactionUnderpriced = errors.New("replacement transaction underpriced")
var errReplacementAuthorizationMismatch = errors.New("replacement transaction authorization differs")
var ErrFinalitySafetyViolation = errors.New("finality safety violation")
var ErrLocalSigningDisabled = errors.New("local signing is disabled for observer nodes")

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
	Queued       []types.Transaction `json:"queued"`
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
	FromBlock      uint64
	ToBlock        uint64
	HasToBlock     bool
	Address        string
	Addresses      []string
	RequireAddress bool
	Topic0         string
	Topic0s        []string
	Limit          int
	Descending     bool
}

type diskSnapshot struct {
	Version          uint64                               `json:"version"`
	Generation       uint64                               `json:"generation"`
	Checksum         string                               `json:"checksum"`
	ChainID          string                               `json:"chain_id"`
	GenesisState     state.Snapshot                       `json:"genesis_state"`
	State            state.Snapshot                       `json:"state"`
	Blocks           []types.Block                        `json:"blocks"`
	KnownBlocks      []types.Block                        `json:"known_blocks,omitempty"`
	FinalityLock     finalityLock                         `json:"finality_lock"`
	FinalityVotes    []types.FinalitySignature            `json:"finality_votes,omitempty"`
	FinalityEvidence []types.FinalityEquivocationEvidence `json:"finality_evidence,omitempty"`
}

func New(config Config) (result *Node, err error) {
	if err := types.ValidateChainID(config.ChainID); err != nil {
		return nil, err
	}
	if config.GenesisTimeUnix <= 0 {
		return nil, errors.New("genesis time unix must be positive")
	}
	if config.GenesisTimeUnix > math.MaxInt64-consensus.MaxBlockTimeStepSeconds {
		return nil, errors.New("genesis time unix leaves no valid block-time domain")
	}
	validators := config.Validators
	if len(validators) == 0 {
		return nil, errors.New("explicit genesis validator set is required")
	}
	if err := consensus.ValidateValidatorSet(validators); err != nil {
		return nil, fmt.Errorf("invalid validator set: %w", err)
	}
	var proposer string
	switch config.Role {
	case RoleValidator:
		if config.ProposerKey == nil {
			return nil, errors.New("validator role requires a proposer key")
		}
		proposer = chaincrypto.AddressFromPrivateKey(config.ProposerKey)
		if !validatorSetContains(validators, proposer) {
			return nil, errors.New("validator proposer key is not in the genesis validator set")
		}
	case RoleObserver:
		if config.ProposerKey != nil {
			return nil, errors.New("observer role must not configure a proposer key")
		}
		if config.FeeCollector != "" {
			return nil, errors.New("observer role must not configure a fee collector")
		}
	default:
		return nil, fmt.Errorf("unsupported node role %q", config.Role)
	}
	for address := range config.GenesisBalance {
		normalized, err := chaincrypto.NormalizeAddress(address)
		if err != nil || normalized != address {
			return nil, fmt.Errorf("genesis balance address %q is not canonically encoded", address)
		}
	}
	blockGasLimit := config.BlockGasLimit
	if blockGasLimit == 0 {
		blockGasLimit = DefaultBlockGasLimit
	}
	if config.FeeCollector != "" {
		configured, err := chaincrypto.NormalizeAddress(config.FeeCollector)
		if err != nil || configured != config.FeeCollector {
			return nil, errors.New("fee collector is not canonically encoded")
		}
		if configured != proposer {
			return nil, errors.New("fee collector must equal the block proposer")
		}
	}
	feeCollector := proposer
	var directoryLock *dataDirLock
	if config.DataDir != "" {
		preparedDataDir, prepareErr := prepareDataDirectory(config.DataDir)
		if prepareErr != nil {
			return nil, prepareErr
		}
		config.DataDir = preparedDataDir
		directoryLock, err = acquireDataDirLock(config.DataDir)
		if err != nil {
			return nil, err
		}
		defer func() {
			if err != nil {
				err = errors.Join(err, directoryLock.Close())
			}
		}()
	}

	runtime := contracts.NewRuntimeWithDefaults()
	n := &Node{
		role:              config.Role,
		chainID:           config.ChainID,
		proposerKey:       config.ProposerKey,
		proposer:          proposer,
		runtime:           runtime,
		executor:          core.NewExecutor(config.ChainID, feeCollector, runtime),
		consensus:         consensus.NewPOA(validators),
		knownBlocks:       make(map[string]types.Block),
		knownBlockHeights: make(map[uint64]int),
		finalityVotes:     make(map[string]map[string]types.FinalitySignature),
		finalityVoteIndex: make(map[uint64]map[string]finalityVoteRecord),
		finalityEvidence:  make(map[string]types.FinalityEquivocationEvidence),
		txIndex:           make(map[string]types.TransactionRecord),
		dataDir:           config.DataDir,
		dataDirLock:       directoryLock,
		blockGasLimit:     blockGasLimit,
		genesisTimeUnix:   config.GenesisTimeUnix,
	}
	genesisStore := newGenesisStore(config.GenesisBalance, validators)
	genesisState := genesisStore.Snapshot()

	if config.DataDir != "" {
		manifest, err := loadDataManifest(config.DataDir)
		if err != nil {
			return nil, err
		}
		loaded, err := loadDiskSnapshot(config.DataDir)
		if err != nil {
			return nil, err
		}
		if manifest != nil && loaded == nil {
			return nil, errors.New("initialized data directory is missing chain.json")
		}
		if manifest == nil && loaded != nil {
			return nil, errors.New("persisted chain is missing manifest.json; explicit migration is required")
		}
		if loaded != nil {
			if manifest.ChainID != loaded.ChainID || len(loaded.Blocks) == 0 || manifest.GenesisHash != loaded.Blocks[0].Hash() {
				return nil, errors.New("data manifest does not match persisted chain")
			}
			if err := n.restoreDiskSnapshot(loaded, genesisStore); err != nil {
				return nil, err
			}
			if err := n.restorePersistentHalt(); err != nil {
				return nil, err
			}
			if n.haltErr == nil && n.role == RoleValidator {
				if err := n.requeuePersistedFinalitySlashesLocked(); err != nil {
					n.recordRuntimeFaultLocked(err)
					return nil, err
				}
			}
			return n, nil
		}
	}

	store := genesisStore
	genesis := types.GenesisBlock(config.ChainID, store.Root(), config.GenesisTimeUnix)
	genesis.Header.GasLimit = blockGasLimit
	genesis.Header.BaseFeePerGas = InitialBaseFeePerGas
	n.genesisState = genesisState
	n.state = store
	n.refreshConsensusLocked()
	n.blocks = []types.Block{genesis}
	n.knownBlocks[genesis.Hash()] = genesis
	n.knownBlockHeights[0] = 1
	n.finalityLock = finalityLock{Height: 0, BlockHash: genesis.Hash()}
	if err := n.persistLocked(); err != nil {
		return nil, err
	}
	if err := n.restorePersistentHalt(); err != nil {
		return nil, err
	}
	return n, nil
}

// NewDevelopment creates the implicit single-validator configuration used by
// isolated tests and demos. Persistent or peered nodes must use New with an
// explicit shared genesis validator set.
func NewDevelopment(config Config) (*Node, error) {
	if config.DataDir != "" {
		return nil, errors.New("development nodes do not support persistent data directories")
	}
	if config.Role == "" {
		config.Role = RoleValidator
	}
	if config.Role != RoleValidator {
		return nil, errors.New("development nodes support only the validator role")
	}
	if len(config.Validators) == 0 && config.ProposerKey != nil {
		config.Validators = []string{chaincrypto.AddressFromPrivateKey(config.ProposerKey)}
	}
	return New(config)
}

func (n *Node) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return n.closeErr
	}
	n.closed = true
	n.closeErr = n.dataDirLock.Close()
	n.dataDirLock = nil
	return n.closeErr
}

func newGenesisStore(balances map[string]uint64, validators []string) *state.Store {
	store := state.NewStore()
	for address, balance := range balances {
		store.SetBalance(address, balance)
	}
	if err := store.SetValidators(validators); err != nil {
		panic(err)
	}
	return store
}

func (n *Node) SubmitTx(tx types.Transaction) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	return n.submitTxLocked(tx)
}

func (n *Node) RequestFaucet(to string, amount uint64) (types.Transaction, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.requireOpenLocked(); err != nil {
		return types.Transaction{}, err
	}
	if n.haltErr != nil {
		return types.Transaction{}, fmt.Errorf("node halted: %w", n.haltErr)
	}
	if err := n.requireLocalSigningLocked("faucet request"); err != nil {
		return types.Transaction{}, err
	}

	to = strings.TrimSpace(to)
	if to == "" {
		return types.Transaction{}, errors.New("faucet recipient is required")
	}
	if amount == 0 {
		return types.Transaction{}, errors.New("faucet amount must be positive")
	}

	working, err := n.pendingStateLocked()
	if err != nil {
		n.recordRuntimeFaultLocked(err)
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

func (n *Node) submitTxLocked(tx types.Transaction) (err error) {
	if err := n.requireOpenLocked(); err != nil {
		return err
	}
	if n.haltErr != nil {
		return fmt.Errorf("node halted: %w", n.haltErr)
	}
	defer func() {
		n.recordRuntimeFaultLocked(err)
	}()
	txSize, err := canonicalTransactionSize(tx)
	if err != nil {
		return err
	}
	tx = cloneTransaction(tx)
	if tx.GasLimit > n.blockGasLimit {
		return fmt.Errorf("transaction gas limit exceeds block gas limit %d", n.blockGasLimit)
	}
	pendingReplacementIndex := transactionReplacementIndex(n.mempool, tx)
	queuedReplacementIndex := transactionReplacementIndex(n.queued, tx)
	if pendingReplacementIndex >= 0 {
		if err := canReplacePendingTransaction(n.mempool[pendingReplacementIndex], tx); err != nil {
			return err
		}
		if err := n.validateReplacementTxPoolBytesLocked(n.mempool[pendingReplacementIndex], txSize); err != nil {
			return err
		}
		candidateMempool := append([]types.Transaction(nil), n.mempool...)
		candidateMempool[pendingReplacementIndex] = tx
		blockHeight := n.blocks[len(n.blocks)-1].Header.Height + 1
		baseFee := n.nextBaseFeeLocked()
		nextMempool, nextQueued, err := n.promoteQueuedForState(
			n.state,
			candidateMempool,
			n.queued,
			blockHeight,
			baseFee,
		)
		if err != nil {
			return err
		}
		n.setMempoolLocked(nextMempool)
		n.queued = nextQueued
		return nil
	}
	if queuedReplacementIndex < 0 {
		if err := n.validateNewTxPoolCapacityLocked(tx, txSize); err != nil {
			return err
		}
	}

	working, err := n.pendingStateLocked()
	if err != nil {
		return err
	}
	if queuedReplacementIndex >= 0 {
		if err := canReplacePendingTransaction(n.queued[queuedReplacementIndex], tx); err != nil {
			return err
		}
		if err := n.validateReplacementTxPoolBytesLocked(n.queued[queuedReplacementIndex], txSize); err != nil {
			return err
		}
		if err := n.submitQueuedReplacementLocked(queuedReplacementIndex, tx, working); err != nil {
			return err
		}
		return nil
	}

	blockHeight := n.blocks[len(n.blocks)-1].Header.Height + 1
	baseFee := n.nextBaseFeeLocked()
	pendingAccount := working.GetAccount(tx.From)
	if tx.Nonce > pendingAccount.Nonce {
		if tx.Nonce-pendingAccount.Nonce > maxQueuedNonceGap {
			return fmt.Errorf("transaction nonce gap exceeds %d", maxQueuedNonceGap)
		}
		if len(n.queued) >= maxQueuedTransactions {
			return fmt.Errorf("queued transaction limit %d reached", maxQueuedTransactions)
		}
		if err := n.validateQueuedTransactionLocked(tx, working); err != nil {
			return err
		}
		n.queued = append(n.queued, tx)
		return nil
	}
	if _, err := n.executor.ExecuteWithContext(working, tx, core.ExecutionContext{BlockHeight: blockHeight, BaseFeePerGas: baseFee}); err != nil {
		return err
	}
	candidateMempool := append([]types.Transaction(nil), n.mempool...)
	candidateMempool = append(candidateMempool, tx)
	nextMempool, nextQueued, err := n.promoteQueuedForState(
		n.state,
		candidateMempool,
		n.queued,
		blockHeight,
		baseFee,
	)
	if err != nil {
		return err
	}
	n.setMempoolLocked(nextMempool)
	n.queued = nextQueued
	return nil
}

func (n *Node) setMempoolLocked(mempool []types.Transaction) {
	if n.txPoolRevision == math.MaxUint64 {
		panic("transaction pool revision exhausted")
	}
	n.mempool = mempool
	n.txPoolRevision++
}

func (n *Node) validateNewTxPoolCapacityLocked(tx types.Transaction, txSize uint64) error {
	if len(n.mempool)+len(n.queued) >= maxTxPoolTransactions {
		return fmt.Errorf("transaction pool limit %d reached", maxTxPoolTransactions)
	}
	currentBytes := n.txPoolBytesLocked()
	if currentBytes >= maxTxPoolBytes || txSize > maxTxPoolBytes-currentBytes {
		return fmt.Errorf("transaction pool byte limit %d reached", maxTxPoolBytes)
	}
	from := normalizedAddress(tx.From)
	count := 0
	for _, existing := range n.mempool {
		if normalizedAddress(existing.From) == from {
			count++
		}
	}
	for _, existing := range n.queued {
		if normalizedAddress(existing.From) == from {
			count++
		}
	}
	if count >= maxTransactionsPerSender {
		return fmt.Errorf("sender transaction limit %d reached", maxTransactionsPerSender)
	}
	return nil
}

func (n *Node) validateReplacementTxPoolBytesLocked(previous types.Transaction, replacementSize uint64) error {
	previousSize := uint64(len(hash.MustCanonicalBytes(previous)))
	currentSize := n.txPoolBytesLocked()
	if currentSize < previousSize {
		return errors.New("transaction pool byte accounting underflow")
	}
	retainedSize := currentSize - previousSize
	if retainedSize >= maxTxPoolBytes || replacementSize > maxTxPoolBytes-retainedSize {
		return fmt.Errorf("transaction pool byte limit %d reached", maxTxPoolBytes)
	}
	return nil
}

func (n *Node) txPoolBytesLocked() uint64 {
	var total uint64
	for _, tx := range n.mempool {
		total += uint64(len(hash.MustCanonicalBytes(tx)))
	}
	for _, tx := range n.queued {
		total += uint64(len(hash.MustCanonicalBytes(tx)))
	}
	return total
}

func (n *Node) ProduceBlock() (types.Block, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.requireOpenLocked(); err != nil {
		return types.Block{}, err
	}
	if n.haltErr != nil {
		return types.Block{}, fmt.Errorf("node halted: %w", n.haltErr)
	}
	if err := n.requireLocalSigningLocked("block production"); err != nil {
		return types.Block{}, err
	}

	working := n.state.Clone()
	receipts := make([]types.Receipt, 0, len(n.mempool))
	included := make([]types.Transaction, 0, len(n.mempool))
	remaining := make([]types.Transaction, 0, len(n.mempool))
	parent := n.blocks[len(n.blocks)-1]
	blockHeight := parent.Header.Height + 1
	baseFee := n.nextBaseFeeLocked()
	blockTime := time.Now().Unix()
	if parent.Header.TimeUnix > math.MaxInt64-consensus.MaxBlockTimeStepSeconds {
		return types.Block{}, errors.New("block time domain exhausted")
	}
	maxBlockTime := parent.Header.TimeUnix + consensus.MaxBlockTimeStepSeconds/2
	if blockTime > maxBlockTime {
		blockTime = maxBlockTime
	}
	if blockTime <= parent.Header.TimeUnix {
		blockTime = parent.Header.TimeUnix + 1
	}
	var gasUsed uint64
	var blockComponentBytes uint64
	blockFull := false
	for index, tx := range n.mempool {
		if blockFull {
			remaining = append(remaining, n.mempool[index:]...)
			break
		}
		if len(included) >= MaxTransactionsPerBlock {
			remaining = append(remaining, n.mempool[index:]...)
			break
		}
		txSize, err := canonicalTransactionSize(tx)
		if err != nil {
			return types.Block{}, fmt.Errorf("mempool transaction %d: %w", index, err)
		}
		candidate := working.Clone()
		receipt, err := n.executor.ExecuteWithContext(candidate, tx, core.ExecutionContext{BlockHeight: blockHeight, BaseFeePerGas: baseFee})
		if err != nil {
			if core.IsFatalExecutionError(err) {
				n.recordRuntimeFaultLocked(err)
				return types.Block{}, err
			}
			remaining = append(remaining, tx)
			continue
		}
		nextGasUsed, err := checkedAdd(gasUsed, receipt.GasUsed)
		if err != nil {
			return types.Block{}, err
		}
		if nextGasUsed > n.blockGasLimit {
			remaining = append(remaining, tx)
			blockFull = true
			continue
		}
		receiptBytes, err := hash.CanonicalBytes(receipt)
		if err != nil {
			return types.Block{}, fmt.Errorf("encode produced receipt: %w", err)
		}
		nextBlockBytes, err := checkedSizeAdd(blockComponentBytes, txSize)
		if err != nil {
			return types.Block{}, err
		}
		nextBlockBytes, err = checkedSizeAdd(nextBlockBytes, uint64(len(receiptBytes)))
		if err != nil {
			return types.Block{}, err
		}
		if nextBlockBytes > MaxUncertifiedBlockBytes-maxProducedBlockEnvelopeBytes {
			remaining = append(remaining, tx)
			blockFull = true
			continue
		}
		working = candidate
		gasUsed = nextGasUsed
		blockComponentBytes = nextBlockBytes
		included = append(included, tx)
		receipts = append(receipts, receipt)
	}

	block := types.Block{
		Header: types.BlockHeader{
			ChainID:       n.chainID,
			Height:        blockHeight,
			ParentHash:    parent.Hash(),
			TimeUnix:      blockTime,
			Proposer:      n.proposer,
			GasLimit:      n.blockGasLimit,
			GasUsed:       gasUsed,
			BaseFeePerGas: baseFee,
			TxRoot:        types.TransactionRoot(included),
			ReceiptRoot:   types.ReceiptRoot(receipts),
			StateRoot:     working.Root(),
		},
		Transactions: append([]types.Transaction(nil), included...),
		Receipts:     receipts,
	}
	nextMempool, demoted, err := n.revalidateMempoolForState(
		working,
		remaining,
		blockHeight+1,
		NextBaseFee(block, n.blockGasLimit),
	)
	if err != nil {
		n.recordRuntimeFaultLocked(err)
		return types.Block{}, err
	}
	candidateQueued := append([]types.Transaction(nil), n.queued...)
	candidateQueued = append(candidateQueued, demoted...)
	candidateQueued, err = n.revalidateQueuedForState(
		working,
		nextMempool,
		candidateQueued,
		blockHeight+1,
		NextBaseFee(block, n.blockGasLimit),
	)
	if err != nil {
		n.recordRuntimeFaultLocked(err)
		return types.Block{}, err
	}
	nextMempool, candidateQueued, err = n.promoteQueuedForState(
		working,
		nextMempool,
		candidateQueued,
		blockHeight+1,
		NextBaseFee(block, n.blockGasLimit),
	)
	if err != nil {
		n.recordRuntimeFaultLocked(err)
		return types.Block{}, err
	}
	if err := consensus.SignBlock(n.proposerKey, &block); err != nil {
		return types.Block{}, err
	}
	if err := validateBlockSize(block); err != nil {
		return types.Block{}, fmt.Errorf("produced block violates size limits: %w", err)
	}
	if err := n.consensus.ValidateBlock(parent, block); err != nil {
		return types.Block{}, err
	}

	blockHash := block.Hash()
	candidateBlocks := append([]types.Block(nil), n.blocks...)
	candidateBlocks = append(candidateBlocks, block)
	n.knownBlocks[blockHash] = block
	n.knownBlockHeights[block.Header.Height]++
	if err := n.persistCandidateLocked(working, candidateBlocks); err != nil {
		delete(n.knownBlocks, blockHash)
		n.knownBlockHeights[block.Header.Height]--
		if n.knownBlockHeights[block.Header.Height] == 0 {
			delete(n.knownBlockHeights, block.Header.Height)
		}
		return types.Block{}, err
	}
	n.state = working
	n.refreshConsensusLocked()
	n.blocks = candidateBlocks
	n.indexBlock(block)
	n.setMempoolLocked(nextMempool)
	n.queued = candidateQueued
	return cloneBlock(block), nil
}

func (n *Node) revalidateMempoolForState(base *state.Store, txs []types.Transaction, blockHeight uint64, baseFee uint64) ([]types.Transaction, []types.Transaction, error) {
	working := base.Clone()
	pending := make([]types.Transaction, 0, len(txs))
	demoted := make([]types.Transaction, 0)
	for _, tx := range txs {
		account := working.GetAccount(tx.From)
		if tx.Nonce > account.Nonce {
			demoted = append(demoted, tx)
			continue
		}
		if tx.Nonce < account.Nonce {
			continue
		}
		if _, err := n.executor.ExecuteWithContext(working, tx, core.ExecutionContext{BlockHeight: blockHeight, BaseFeePerGas: baseFee}); err != nil {
			if core.IsFatalExecutionError(err) {
				return nil, nil, err
			}
			continue
		}
		pending = append(pending, tx)
	}
	return pending, demoted, nil
}

func (n *Node) revalidateQueuedForState(base *state.Store, mempool []types.Transaction, queued []types.Transaction, blockHeight uint64, baseFee uint64) ([]types.Transaction, error) {
	working := base.Clone()
	poolBytes := uint64(0)
	senderCounts := make(map[string]int)
	for _, tx := range mempool {
		if _, err := n.executor.ExecuteWithContext(working, tx, core.ExecutionContext{BlockHeight: blockHeight, BaseFeePerGas: baseFee}); err != nil {
			return nil, err
		}
		poolBytes += uint64(len(hash.MustCanonicalBytes(tx)))
		senderCounts[normalizedAddress(tx.From)]++
	}

	type senderNonce struct {
		sender string
		nonce  uint64
	}
	seen := make(map[senderNonce]struct{})
	validated := make([]types.Transaction, 0, min(len(queued), maxQueuedTransactions))
	for _, tx := range queued {
		sender := normalizedAddress(tx.From)
		key := senderNonce{sender: sender, nonce: tx.Nonce}
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		account := working.GetAccount(tx.From)
		if tx.Nonce < account.Nonce {
			continue
		}
		if tx.Nonce > account.Nonce && tx.Nonce-account.Nonce > maxQueuedNonceGap {
			continue
		}
		if len(validated) >= maxQueuedTransactions || len(mempool)+len(validated) >= maxTxPoolTransactions || senderCounts[sender] >= maxTransactionsPerSender {
			continue
		}
		txSize := uint64(len(hash.MustCanonicalBytes(tx)))
		if poolBytes >= maxTxPoolBytes || txSize > maxTxPoolBytes-poolBytes {
			continue
		}
		check := working.Clone()
		if tx.Nonce > account.Nonce {
			check.SetNonce(tx.From, tx.Nonce)
		}
		if _, err := n.executor.ExecuteWithContext(check, tx, core.ExecutionContext{BlockHeight: blockHeight, BaseFeePerGas: baseFee}); err != nil {
			if core.IsFatalExecutionError(err) {
				return nil, err
			}
			continue
		}
		seen[key] = struct{}{}
		validated = append(validated, tx)
		poolBytes += txSize
		senderCounts[sender]++
	}
	return validated, nil
}

func (n *Node) ImportBlock(block types.Block) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.requireOpenLocked(); err != nil {
		return err
	}
	if n.haltErr != nil {
		return fmt.Errorf("node halted: %w", n.haltErr)
	}
	if block.Header.ChainID != n.chainID {
		return errors.New("imported block chain id does not match node")
	}
	if err := validateBlockCheapBounds(block); err != nil {
		return fmt.Errorf("imported block violates size limits: %w", err)
	}
	if err := consensus.ValidateBlockSignature(block); err != nil {
		return err
	}
	if err := validateBlockSize(block); err != nil {
		return fmt.Errorf("imported block violates size limits: %w", err)
	}
	block = cloneBlock(block)
	blockHash := block.Hash()
	existing, alreadyKnown := n.knownBlocks[blockHash]
	if alreadyKnown && block.FinalityCertificate == nil {
		if !n.blockCompatibleWithFinalityLockLocked(blockHash) {
			return errors.New("imported block conflicts with the finality lock")
		}
		return nil
	}
	parent, ok := n.knownBlocks[block.Header.ParentHash]
	if !ok {
		return errors.New("imported block parent is unknown")
	}
	if !alreadyKnown && block.FinalityCertificate == nil {
		if n.knownBlockHeights[block.Header.Height] >= MaxKnownBlocksPerHeight {
			return fmt.Errorf("known block count at height %d exceeds %d", block.Header.Height, MaxKnownBlocksPerHeight)
		}
		canonicalCountAfter := uint64(len(n.blocks))
		if block.Header.Height > n.blocks[len(n.blocks)-1].Header.Height {
			canonicalCountAfter = block.Header.Height + 1
		}
		knownCountAfter := uint64(len(n.knownBlocks) + 1)
		if knownCountAfter > canonicalCountAfter && knownCountAfter-canonicalCountAfter > MaxKnownSideBlocks {
			return fmt.Errorf("known side block count exceeds %d", MaxKnownSideBlocks)
		}
	}
	if block.FinalityCertificate == nil && !n.prospectiveBlockCompatibleWithFinalityLockLocked(blockHash, block.Header.Height, parent.Hash()) {
		return errors.New("imported block conflicts with the finality lock")
	}
	parentState, err := n.replayStateLocked(parent.Hash())
	if err != nil {
		n.recordRuntimeFaultLocked(err)
		return err
	}
	if _, err := n.validateBlockOnStateLocked(parent, parentState, block); err != nil {
		n.recordRuntimeFaultLocked(err)
		return err
	}
	if alreadyKnown {
		equal, err := blocksEqualIgnoringFinalityCertificate(existing, block)
		if err != nil {
			return err
		}
		if !equal {
			return errors.New("imported finality certificate conflicts with the known block envelope")
		}
		if existing.FinalityCertificate != nil {
			merged, changed := mergeFinalityCertificates(existing.FinalityCertificate, block.FinalityCertificate)
			if !changed {
				return nil
			}
			block.FinalityCertificate = merged
			if err := validateBlockSize(block); err != nil {
				return fmt.Errorf("merged certified block violates size limits: %w", err)
			}
			if err := consensus.NewPOA(parentState.Validators()).ValidateBlock(parent, block); err != nil {
				return err
			}
		}
	}

	previousKnown, hadPreviousKnown := n.knownBlocks[blockHash]
	previousLock := n.finalityLock
	previousVotes := cloneFinalityVotes(n.finalityVotes)
	previousVoteIndex := cloneFinalityVoteIndex(n.finalityVoteIndex)
	previousEvidence := cloneFinalityEvidence(n.finalityEvidence)
	previousMempool := append([]types.Transaction(nil), n.mempool...)
	previousQueued := append([]types.Transaction(nil), n.queued...)
	n.knownBlocks[blockHash] = block
	if !hadPreviousKnown {
		n.knownBlockHeights[block.Header.Height]++
	}
	rollbackKnownAndLock := func() {
		if hadPreviousKnown {
			n.knownBlocks[blockHash] = previousKnown
		} else {
			delete(n.knownBlocks, blockHash)
			n.knownBlockHeights[block.Header.Height]--
			if n.knownBlockHeights[block.Header.Height] == 0 {
				delete(n.knownBlockHeights, block.Header.Height)
			}
		}
		n.finalityLock = previousLock
		n.finalityVotes = previousVotes
		n.finalityVoteIndex = previousVoteIndex
		n.finalityEvidence = previousEvidence
		n.setMempoolLocked(previousMempool)
		n.queued = previousQueued
	}
	if !n.blockCompatibleWithFinalityLockLocked(blockHash) {
		rollbackKnownAndLock()
		if block.FinalityCertificate != nil {
			safetyErr := fmt.Errorf("%w: certified block %s at height %d conflicts with locked block %s at height %d",
				ErrFinalitySafetyViolation,
				blockHash,
				block.Header.Height,
				previousLock.BlockHash,
				previousLock.Height,
			)
			n.setPersistentHaltLocked("finality_safety", safetyErr)
			return n.haltErr
		}
		return errors.New("imported block conflicts with the finality lock")
	}
	lockAdvanced := false
	if block.FinalityCertificate != nil && block.Header.Height > n.finalityLock.Height {
		n.finalityLock = finalityLock{Height: block.Header.Height, BlockHash: blockHash}
		lockAdvanced = true
	}
	if block.FinalityCertificate != nil {
		if err := n.recordAcceptedFinalityCertificateLocked(block); err != nil {
			rollbackKnownAndLock()
			n.recordRuntimeFaultLocked(err)
			return err
		}
	}

	candidateState := n.state
	candidateBlocks := n.blocks
	nextMempool := n.mempool
	nextQueued := n.queued
	canonicalChange := false
	canonicalEnvelopeChange := alreadyKnown && block.Header.Height < uint64(len(n.blocks)) && n.blocks[block.Header.Height].Hash() == blockHash
	desiredHeadHash := ""
	if lockAdvanced && !canonicalBlocksContainLock(n.blocks, n.finalityLock) {
		desiredHeadHash = n.highestKnownDescendantLocked(n.finalityLock)
	} else if block.Header.Height > n.blocks[len(n.blocks)-1].Header.Height {
		desiredHeadHash = blockHash
	}
	if desiredHeadHash != "" && desiredHeadHash != n.blocks[len(n.blocks)-1].Hash() {
		chain, working, candidateMempool, candidateQueued, err := n.prepareCanonicalChainLocked(desiredHeadHash)
		if err != nil {
			n.recordRuntimeFaultLocked(err)
			rollbackKnownAndLock()
			return err
		}
		candidateState = working
		candidateBlocks = chain
		nextMempool = candidateMempool
		nextQueued = candidateQueued
		canonicalChange = true
	} else if canonicalEnvelopeChange {
		candidateBlocks = append([]types.Block(nil), n.blocks...)
		candidateBlocks[block.Header.Height] = block
	}
	if err := n.persistCandidateLocked(candidateState, candidateBlocks); err != nil {
		rollbackKnownAndLock()
		return err
	}
	if canonicalChange {
		n.blocks = candidateBlocks
		n.state = candidateState
		n.refreshConsensusLocked()
		n.rebuildTxIndex()
		n.setMempoolLocked(nextMempool)
		n.queued = nextQueued
	} else if canonicalEnvelopeChange {
		n.blocks = candidateBlocks
	}
	return nil
}

func (n *Node) prepareCanonicalChainLocked(blockHash string) ([]types.Block, *state.Store, []types.Transaction, []types.Transaction, error) {
	chain, working, err := n.replayKnownChainLocked(blockHash)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	included := transactionHashSet(transactionsInBlocks(chain))
	candidateMempool := filterTransactionsByHash(n.mempool, included)
	candidateQueued := filterTransactionsByHash(n.queued, included)
	head := chain[len(chain)-1]
	candidateMempool, demoted, err := n.revalidateMempoolForState(
		working,
		candidateMempool,
		head.Header.Height+1,
		NextBaseFee(head, n.blockGasLimit),
	)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	candidateQueued = append(candidateQueued, demoted...)
	candidateQueued, err = n.revalidateQueuedForState(
		working,
		candidateMempool,
		candidateQueued,
		head.Header.Height+1,
		NextBaseFee(head, n.blockGasLimit),
	)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	candidateMempool, candidateQueued, err = n.promoteQueuedForState(
		working,
		candidateMempool,
		candidateQueued,
		head.Header.Height+1,
		NextBaseFee(head, n.blockGasLimit),
	)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return chain, working, candidateMempool, candidateQueued, nil
}

func (n *Node) blockCompatibleWithFinalityLockLocked(blockHash string) bool {
	block, ok := n.knownBlocks[blockHash]
	if !ok {
		return false
	}
	if block.Header.Height >= n.finalityLock.Height {
		return knownBlockDescendsFrom(n.knownBlocks, blockHash, n.finalityLock)
	}
	return knownBlockDescendsFrom(n.knownBlocks, n.finalityLock.BlockHash, finalityLock{
		Height:    block.Header.Height,
		BlockHash: blockHash,
	})
}

func (n *Node) prospectiveBlockCompatibleWithFinalityLockLocked(blockHash string, height uint64, parentHash string) bool {
	if height == n.finalityLock.Height {
		return blockHash == n.finalityLock.BlockHash
	}
	if height > n.finalityLock.Height {
		if height == n.finalityLock.Height+1 {
			return parentHash == n.finalityLock.BlockHash
		}
		return knownBlockDescendsFrom(n.knownBlocks, parentHash, n.finalityLock)
	}
	return knownBlockDescendsFrom(n.knownBlocks, n.finalityLock.BlockHash, finalityLock{
		Height:    height,
		BlockHash: blockHash,
	})
}

func canonicalBlocksContainLock(blocks []types.Block, lock finalityLock) bool {
	return lock.Height < uint64(len(blocks)) && blocks[lock.Height].Hash() == lock.BlockHash
}

func (n *Node) highestKnownDescendantLocked(lock finalityLock) string {
	bestHash := lock.BlockHash
	bestHeight := lock.Height
	currentHeadHash := n.blocks[len(n.blocks)-1].Hash()
	if knownBlockDescendsFrom(n.knownBlocks, currentHeadHash, lock) {
		bestHash = currentHeadHash
		bestHeight = n.blocks[len(n.blocks)-1].Header.Height
	}
	for blockHash, block := range n.knownBlocks {
		if !knownBlockDescendsFrom(n.knownBlocks, blockHash, lock) {
			continue
		}
		if block.Header.Height > bestHeight || (block.Header.Height == bestHeight && bestHash != currentHeadHash && blockHash < bestHash) {
			bestHash = blockHash
			bestHeight = block.Header.Height
		}
	}
	return bestHash
}

func blocksEqualIgnoringFinalityCertificate(left types.Block, right types.Block) (bool, error) {
	left.FinalityCertificate = nil
	right.FinalityCertificate = nil
	return canonicalBlocksEqual(left, right)
}

func mergeFinalityCertificates(existing *types.FinalityCertificate, incoming *types.FinalityCertificate) (*types.FinalityCertificate, bool) {
	if existing == nil {
		return incoming, incoming != nil
	}
	if incoming == nil {
		return existing, false
	}
	votes := make(map[string]types.FinalitySignature, len(existing.Signatures)+len(incoming.Signatures))
	for _, vote := range existing.Signatures {
		votes[vote.Validator] = vote
	}
	changed := false
	for _, vote := range incoming.Signatures {
		if _, ok := votes[vote.Validator]; !ok {
			votes[vote.Validator] = vote
			changed = true
		}
	}
	if !changed {
		return existing, false
	}
	return &types.FinalityCertificate{
		ChainID:    existing.ChainID,
		Height:     existing.Height,
		BlockHash:  existing.BlockHash,
		Signatures: sortedFinalitySignatures(votes),
	}, true
}

func (n *Node) recordAcceptedFinalityCertificateLocked(block types.Block) error {
	if block.FinalityCertificate == nil {
		return nil
	}
	if n.finalityVotes == nil {
		n.finalityVotes = make(map[string]map[string]types.FinalitySignature)
	}
	blockHash := block.Hash()
	blockVotes := n.finalityVotes[blockHash]
	if blockVotes == nil {
		blockVotes = make(map[string]types.FinalitySignature)
		n.finalityVotes[blockHash] = blockVotes
	}
	for _, vote := range block.FinalityCertificate.Signatures {
		if err := n.recordFinalityVoteLocked(block, vote); core.IsFatalExecutionError(err) {
			return err
		}
		blockVotes[vote.Validator] = vote
	}
	return nil
}

func (n *Node) Head() types.Block {
	n.mu.Lock()
	defer n.mu.Unlock()
	return cloneBlock(n.blocks[len(n.blocks)-1])
}

func (n *Node) Block(height uint64) (types.Block, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if height >= uint64(len(n.blocks)) {
		return types.Block{}, false
	}
	return cloneBlock(n.blocks[height]), true
}

func (n *Node) BlockByHash(hash string) (types.Block, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	block, ok := n.knownBlocks[strings.ToLower(strings.TrimSpace(hash))]
	return cloneBlock(block), ok
}

func (n *Node) SubmitFinalityVote(vote types.FinalitySignature) (err error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.requireOpenLocked(); err != nil {
		return err
	}
	if n.haltErr != nil {
		return fmt.Errorf("node halted: %w", n.haltErr)
	}
	defer func() {
		n.recordRuntimeFaultLocked(err)
	}()

	validator, normalizeErr := chaincrypto.NormalizeAddress(vote.Validator)
	if normalizeErr != nil || validator != vote.Validator {
		return errors.New("finality vote validator is not canonically encoded")
	}
	if err := types.ValidateCanonicalHash("finality vote block hash", vote.BlockHash); err != nil {
		return err
	}
	if err := types.ValidateCanonicalSignature("finality vote signature", vote.Signature); err != nil {
		return err
	}
	block, validators, err := n.matchFinalityVoteLocked(vote)
	if err != nil {
		return err
	}
	blockHash := block.Hash()
	previousVotes := cloneFinalityVotes(n.finalityVotes)
	previousVoteIndex := cloneFinalityVoteIndex(n.finalityVoteIndex)
	previousEvidence := cloneFinalityEvidence(n.finalityEvidence)
	previousMempool := append([]types.Transaction(nil), n.mempool...)
	previousQueued := append([]types.Transaction(nil), n.queued...)
	previousLock := n.finalityLock
	previousKnown := n.knownBlocks[blockHash]
	rollback := func() {
		n.finalityVotes = previousVotes
		n.finalityVoteIndex = previousVoteIndex
		n.finalityEvidence = previousEvidence
		n.setMempoolLocked(previousMempool)
		n.queued = previousQueued
		n.finalityLock = previousLock
		n.knownBlocks[blockHash] = previousKnown
	}
	if equivocationErr := n.recordFinalityVoteLocked(block, vote); equivocationErr != nil {
		if persistErr := n.persistLocked(); persistErr != nil {
			rollback()
			return errors.Join(persistErr, equivocationErr)
		}
		return equivocationErr
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
					votes[validator] = signature
				}
			}
		}
		n.finalityVotes[blockHash] = votes
	}
	votes[vote.Validator] = vote
	if len(votes) < consensus.FinalityQuorumSize(len(validators)) {
		if err := n.persistLocked(); err != nil {
			rollback()
			return err
		}
		return nil
	}
	certified := block
	certified.FinalityCertificate = &types.FinalityCertificate{
		ChainID:    block.Header.ChainID,
		Height:     block.Header.Height,
		BlockHash:  blockHash,
		Signatures: sortedFinalitySignatures(votes),
	}
	if err := validateBlockSize(certified); err != nil {
		rollback()
		return fmt.Errorf("certified block violates size limits: %w", err)
	}
	parent := n.blocks[certified.Header.Height-1]
	engine := consensus.NewPOA(validators)
	if err := engine.ValidateBlock(parent, certified); err != nil {
		rollback()
		return err
	}
	if !n.blockCompatibleWithFinalityLockLocked(blockHash) {
		rollback()
		return fmt.Errorf("%w: canonical certificate conflicts with the current lock", ErrFinalitySafetyViolation)
	}
	candidateBlocks := append([]types.Block(nil), n.blocks...)
	candidateBlocks[certified.Header.Height] = certified
	n.knownBlocks[blockHash] = certified
	if certified.Header.Height > n.finalityLock.Height {
		n.finalityLock = finalityLock{Height: certified.Header.Height, BlockHash: blockHash}
	}
	if err := n.persistCandidateLocked(n.state, candidateBlocks); err != nil {
		rollback()
		return err
	}
	n.blocks = candidateBlocks
	return nil
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
	return FinalityCheckpoint{
		HeadHeight:      head.Header.Height,
		HeadHash:        head.Hash(),
		SafeHeight:      safe.Header.Height,
		SafeHash:        safe.Hash(),
		SafeDepth:       SafeBlockDepth,
		SafeSource:      "depth_confirmation",
		FinalizedHeight: 0,
		FinalizedHash:   n.blocks[0].Hash(),
		FinalizedDepth:  head.Header.Height,
		FinalizedSource: "genesis_without_certificate",
	}
}

func (n *Node) Transaction(hash string) (types.TransactionRecord, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	record, ok := n.txIndex[hash]
	return cloneTransactionRecord(record), ok
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
	addresses := eventFilterValues(filter.Address, filter.Addresses)
	topics := eventFilterValues(filter.Topic0, filter.Topic0s)
	start := sort.Search(len(n.eventIndex), func(index int) bool {
		return n.eventIndex[index].BlockHeight >= filter.FromBlock
	})
	end := sort.Search(len(n.eventIndex), func(index int) bool {
		return n.eventIndex[index].BlockHeight > toBlock
	})
	capacity := end - start
	if filter.Limit > 0 && capacity > filter.Limit {
		capacity = filter.Limit
	}
	events := make([]types.EventRecord, 0, capacity)
	add := func(record types.EventRecord) bool {
		if !eventMatchesFilter(record, filter.FromBlock, toBlock, addresses, topics, filter.RequireAddress) {
			return false
		}
		events = append(events, cloneEventRecord(record))
		return filter.Limit > 0 && len(events) == filter.Limit
	}
	if filter.Descending {
		for i := end - 1; i >= start; {
			height := n.eventIndex[i].BlockHeight
			groupStart := i
			for groupStart >= start && n.eventIndex[groupStart].BlockHeight == height {
				groupStart--
			}
			for j := groupStart + 1; j <= i; j++ {
				if add(n.eventIndex[j]) {
					return events
				}
			}
			i = groupStart
		}
		return events
	}
	for index := start; index < end; index++ {
		if add(n.eventIndex[index]) {
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

func (n *Node) Role() NodeRole {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.role
}

func (n *Node) HaltError() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.haltErr
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

// PendingNonce avoids replaying the entire mempool for the public nonce query.
// The pending pool already contains only executable, contiguous transactions.
func (n *Node) PendingNonce(address string) uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	address = normalizedAddress(address)
	nonce := n.state.GetAccount(address).Nonce
	pending := make(map[uint64]struct{})
	for _, tx := range n.mempool {
		if normalizedAddress(tx.From) == address {
			pending[tx.Nonce] = struct{}{}
		}
	}
	for {
		if _, ok := pending[nonce]; !ok {
			return nonce
		}
		if nonce == math.MaxUint64 {
			return nonce
		}
		nonce++
	}
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
	return cloneTransactions(n.mempool)
}

func (n *Node) TxPool() MempoolSnapshot {
	n.mu.Lock()
	defer n.mu.Unlock()

	pending := cloneTransactions(n.mempool)
	queued := cloneTransactions(n.queued)
	return MempoolSnapshot{
		Pending:      pending,
		Queued:       queued,
		PendingCount: len(pending),
		QueuedCount:  len(queued),
	}
}

func (n *Node) TxPoolBounded(maxBytes uint64) (MempoolSnapshot, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	var total uint64
	for _, txs := range [][]types.Transaction{n.mempool, n.queued} {
		for _, tx := range txs {
			size, err := canonicalTransactionSize(tx)
			if err != nil {
				return MempoolSnapshot{}, err
			}
			if total > maxBytes || size > maxBytes-total {
				return MempoolSnapshot{}, fmt.Errorf("transaction pool query exceeds %d bytes", maxBytes)
			}
			total += size
		}
	}
	pending := cloneTransactions(n.mempool)
	queued := cloneTransactions(n.queued)
	return MempoolSnapshot{
		Pending:      pending,
		Queued:       queued,
		PendingCount: len(pending),
		QueuedCount:  len(queued),
	}, nil
}

func (n *Node) TxPoolCounts() (pending int, queued int) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.mempool), len(n.queued)
}

func (n *Node) PendingTransaction(hashValue string) (types.Transaction, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	hashValue = strings.ToLower(strings.TrimSpace(hashValue))
	for _, txs := range [][]types.Transaction{n.mempool, n.queued} {
		for _, tx := range txs {
			if tx.Hash() == hashValue {
				return cloneTransaction(tx), true
			}
		}
	}
	return types.Transaction{}, false
}

func (n *Node) PendingTransactionHashes() []string {
	_, hashes := n.PendingTransactionHashesSnapshot()
	return hashes
}

func (n *Node) TxPoolRevision() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.txPoolRevision
}

func (n *Node) PendingTransactionHashesSnapshot() (uint64, []string) {
	return n.pendingTransactionHashesSnapshot(func(tx types.Transaction) string {
		return tx.Hash()
	})
}

func (n *Node) pendingTransactionHashesSnapshot(hashTransaction func(types.Transaction) string) (uint64, []string) {
	n.mu.Lock()
	revision := n.txPoolRevision
	pending := append([]types.Transaction(nil), n.mempool...)
	n.mu.Unlock()
	hashes := make([]string, len(pending))
	for index, tx := range pending {
		hashes[index] = hashTransaction(tx)
	}
	return revision, hashes
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
	snapshot := n.state.ReadView()
	n.mu.Unlock()
	return n.runtime.ReadImmutableSnapshot(snapshot, to, from, method, args)
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
	if vote.ChainID != n.chainID {
		return types.Block{}, nil, errors.New("finality vote chain id mismatch")
	}
	if vote.Height == 0 || vote.Height >= uint64(len(n.blocks)) {
		return types.Block{}, nil, errors.New("finality vote height is not canonical")
	}
	block := n.blocks[vote.Height]
	if block.Hash() != vote.BlockHash {
		return types.Block{}, nil, errors.New("finality vote block hash is not canonical")
	}
	if !consensus.VerifyFinalityVote(block, vote) {
		return types.Block{}, nil, errors.New("invalid finality vote signature")
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

func (n *Node) validatorsForBlockOrCurrentLocked(block types.Block) []string {
	validators, err := n.validatorsForBlockLocked(block)
	if err != nil {
		n.recordRuntimeFaultLocked(err)
		return n.state.Validators()
	}
	if len(validators) == 0 {
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
			n.recordFinalityVoteInIndex(block, vote)
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
	if slashErr != nil {
		return fmt.Errorf("finality equivocation: validator %s signed height %d for %s and %s; automatic slash skipped: %w", vote.Validator, block.Header.Height, existing.BlockHash, blockHash, slashErr)
	}
	return fmt.Errorf("finality equivocation: validator %s signed height %d for %s and %s", vote.Validator, block.Header.Height, existing.BlockHash, blockHash)
}

func cloneFinalityVotes(source map[string]map[string]types.FinalitySignature) map[string]map[string]types.FinalitySignature {
	cloned := make(map[string]map[string]types.FinalitySignature, len(source))
	for blockHash, votes := range source {
		clonedVotes := make(map[string]types.FinalitySignature, len(votes))
		for validator, vote := range votes {
			clonedVotes[validator] = vote
		}
		cloned[blockHash] = clonedVotes
	}
	return cloned
}

func cloneFinalityVoteIndex(source map[uint64]map[string]finalityVoteRecord) map[uint64]map[string]finalityVoteRecord {
	cloned := make(map[uint64]map[string]finalityVoteRecord, len(source))
	for height, votes := range source {
		clonedVotes := make(map[string]finalityVoteRecord, len(votes))
		for validator, vote := range votes {
			clonedVotes[validator] = vote
		}
		cloned[height] = clonedVotes
	}
	return cloned
}

func cloneFinalityEvidence(source map[string]types.FinalityEquivocationEvidence) map[string]types.FinalityEquivocationEvidence {
	cloned := make(map[string]types.FinalityEquivocationEvidence, len(source))
	for key, evidence := range source {
		cloned[key] = evidence
	}
	return cloned
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
	if err := n.requireLocalSigningLocked("automatic finality slash"); err != nil {
		return err
	}
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
			"target":            target,
			"height":            strconv.FormatUint(evidence.Height, 10),
			"first_block_hash":  evidence.FirstBlockHash,
			"first_signature":   evidence.FirstSignature,
			"second_block_hash": evidence.SecondBlockHash,
			"second_signature":  evidence.SecondSignature,
		},
	}
	signature, err := chaincrypto.Sign(n.proposerKey, tx.SigningBytes())
	if err != nil {
		return err
	}
	tx.Signature = signature
	return n.submitTxLocked(tx)
}

func (n *Node) requireLocalSigningLocked(action string) error {
	if n.role != RoleValidator || n.proposerKey == nil {
		return fmt.Errorf("%w: %s", ErrLocalSigningDisabled, action)
	}
	return nil
}

func (n *Node) requireOpenLocked() error {
	if n.closed {
		return ErrNodeClosed
	}
	return nil
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

func transactionReplacementIndex(txs []types.Transaction, tx types.Transaction) int {
	from := normalizedAddress(tx.From)
	if from == "" {
		return -1
	}
	for i, pending := range txs {
		if normalizedAddress(pending.From) == from && pending.Nonce == tx.Nonce {
			return i
		}
	}
	return -1
}

func (n *Node) submitQueuedReplacementLocked(index int, replacement types.Transaction, working *state.Store) error {
	pendingAccount := working.GetAccount(replacement.From)
	if replacement.Nonce == pendingAccount.Nonce {
		blockHeight := n.blocks[len(n.blocks)-1].Header.Height + 1
		baseFee := n.nextBaseFeeLocked()
		candidateQueued := append([]types.Transaction(nil), n.queued...)
		candidateQueued = append(candidateQueued[:index], candidateQueued[index+1:]...)
		candidateMempool := append([]types.Transaction(nil), n.mempool...)
		candidateMempool = append(candidateMempool, replacement)
		nextMempool, nextQueued, err := n.promoteQueuedForState(
			n.state,
			candidateMempool,
			candidateQueued,
			blockHeight,
			baseFee,
		)
		if err != nil {
			return err
		}
		n.setMempoolLocked(nextMempool)
		n.queued = nextQueued
		return nil
	}
	if replacement.Nonce < pendingAccount.Nonce {
		return fmt.Errorf("bad nonce: got %d want %d", replacement.Nonce, pendingAccount.Nonce)
	}
	if err := n.validateQueuedTransactionLocked(replacement, working); err != nil {
		return err
	}
	n.queued[index] = replacement
	return nil
}

func (n *Node) validateQueuedTransactionLocked(tx types.Transaction, working *state.Store) error {
	account := working.GetAccount(tx.From)
	if tx.Nonce <= account.Nonce {
		return fmt.Errorf("bad nonce: got %d want %d", tx.Nonce, account.Nonce)
	}
	check := working.Clone()
	check.SetNonce(tx.From, tx.Nonce)
	blockHeight := n.blocks[len(n.blocks)-1].Header.Height + 1
	baseFee := n.nextBaseFeeLocked()
	_, err := n.executor.ExecuteWithContext(check, tx, core.ExecutionContext{BlockHeight: blockHeight, BaseFeePerGas: baseFee})
	return err
}

func (n *Node) promoteQueuedForState(
	base *state.Store,
	mempool []types.Transaction,
	queued []types.Transaction,
	blockHeight uint64,
	baseFee uint64,
) ([]types.Transaction, []types.Transaction, error) {
	working := base.Clone()
	for _, tx := range mempool {
		if _, err := n.executor.ExecuteWithContext(working, tx, core.ExecutionContext{BlockHeight: blockHeight, BaseFeePerGas: baseFee}); err != nil {
			return nil, nil, err
		}
	}

	nextMempool := append([]types.Transaction(nil), mempool...)
	nextQueued := append([]types.Transaction(nil), queued...)
	for {
		filtered := nextQueued[:0]
		for _, tx := range nextQueued {
			if tx.Nonce < working.GetAccount(tx.From).Nonce {
				continue
			}
			filtered = append(filtered, tx)
		}
		nextQueued = filtered
		if len(nextQueued) > maxQueuedTransactions {
			nextQueued = nextQueued[:maxQueuedTransactions]
		}

		promoted := false
		for i, tx := range nextQueued {
			account := working.GetAccount(tx.From)
			if tx.Nonce != account.Nonce {
				continue
			}
			candidate := working.Clone()
			if _, err := n.executor.ExecuteWithContext(candidate, tx, core.ExecutionContext{BlockHeight: blockHeight, BaseFeePerGas: baseFee}); err != nil {
				if core.IsFatalExecutionError(err) {
					return nil, nil, err
				}
				continue
			}
			nextQueued = append(nextQueued[:i], nextQueued[i+1:]...)
			nextMempool = append(nextMempool, tx)
			working = candidate
			promoted = true
			break
		}
		if !promoted {
			return nextMempool, nextQueued, nil
		}
	}
}

func (n *Node) recordRuntimeFaultLocked(err error) bool {
	if !core.IsFatalExecutionError(err) {
		return false
	}
	n.setPersistentHaltLocked("runtime_fault", err)
	return true
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
	working, err := state.NewStoreFromSnapshot(n.genesisState)
	if err != nil {
		return nil, nil, fmt.Errorf("restore genesis state: %w", err)
	}
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
	if err := validateBlockSize(block); err != nil {
		return nil, fmt.Errorf("block violates size limits: %w", err)
	}
	engine := consensus.NewPOA(parentState.Validators())
	if err := engine.ValidateBlock(parent, block); err != nil {
		return nil, err
	}
	working := parentState.Clone()
	executor := core.NewExecutor(n.chainID, block.Header.Proposer, n.runtime)
	receipts := make([]types.Receipt, 0, len(block.Transactions))
	if block.Header.GasLimit == 0 {
		return nil, errors.New("block gas limit is required")
	}
	if block.Header.GasLimit != parent.Header.GasLimit {
		return nil, errors.New("imported block gas limit mismatch")
	}
	if block.Header.BaseFeePerGas != NextBaseFee(parent, n.blockGasLimit) {
		return nil, errors.New("imported block base fee mismatch")
	}
	var gasUsed uint64
	for _, tx := range block.Transactions {
		if tx.GasLimit > block.Header.GasLimit {
			return nil, errors.New("transaction gas limit exceeds block gas limit")
		}
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
		increase := baseFeeChange(parentBaseFee, delta, target, baseFeeChangeDenominator)
		if increase == 0 {
			increase = 1
		}
		if math.MaxUint64-parentBaseFee < increase {
			return math.MaxUint64
		}
		return parentBaseFee + increase
	}
	delta := target - parent.Header.GasUsed
	decrease := baseFeeChange(parentBaseFee, delta, target, baseFeeChangeDenominator)
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
	if replacementAuthorizationIdentity(oldTx) != replacementAuthorizationIdentity(newTx) {
		return errReplacementAuthorizationMismatch
	}
	oldMaxFee, oldPriorityFee := replacementFeeCaps(oldTx)
	newMaxFee, newPriorityFee := replacementFeeCaps(newTx)
	if !feeBumpedByPercent(oldMaxFee, newMaxFee, txpoolReplacementPriceBump) ||
		!feeBumpedByPercent(oldPriorityFee, newPriorityFee, txpoolReplacementPriceBump) {
		return errReplacementTransactionUnderpriced
	}
	return nil
}

func replacementAuthorizationIdentity(tx types.Transaction) string {
	if len(tx.Authorizations) > 0 {
		signers := make([]string, 0, len(tx.Authorizations))
		for _, authorization := range tx.Authorizations {
			signers = append(signers, normalizedAddress(authorization.Signer))
		}
		sort.Strings(signers)
		return "multisig:" + strings.Join(signers, ",")
	}
	signer := normalizedAddress(tx.Signer)
	if signer == "" {
		signer = normalizedAddress(tx.From)
	}
	return strings.TrimSpace(tx.SignatureKind) + ":" + signer
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
		address := strings.ToLower(types.EventSourceAddress(tx, receipt))
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

func eventFilterValues(single string, multiple []string) map[string]struct{} {
	values := make(map[string]struct{}, len(multiple)+1)
	if single != "" {
		values[strings.ToLower(single)] = struct{}{}
	}
	for _, value := range multiple {
		values[strings.ToLower(value)] = struct{}{}
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func eventMatchesFilter(record types.EventRecord, fromBlock uint64, toBlock uint64, addresses map[string]struct{}, topics map[string]struct{}, requireAddress bool) bool {
	if record.BlockHeight < fromBlock || record.BlockHeight > toBlock {
		return false
	}
	if requireAddress && record.Address == "" {
		return false
	}
	if len(addresses) > 0 {
		if _, ok := addresses[strings.ToLower(record.Address)]; !ok {
			return false
		}
	}
	if len(topics) > 0 {
		_, ok := topics[strings.ToLower(record.Topic0)]
		return ok
	}
	return true
}

func eventTopic0(event types.Event) string {
	return hash.KeccakHex([]byte(event.Type))
}

func cloneEventRecord(record types.EventRecord) types.EventRecord {
	record.Event = cloneEvent(record.Event)
	return record
}

func cloneTransaction(tx types.Transaction) types.Transaction {
	tx.Payload = cloneStringMap(tx.Payload)
	if tx.Batch != nil {
		tx.Batch = append([]types.BatchOperation(nil), tx.Batch...)
		for index := range tx.Batch {
			tx.Batch[index].Payload = cloneStringMap(tx.Batch[index].Payload)
		}
	}
	if tx.Authorizations != nil {
		tx.Authorizations = append([]types.Authorization(nil), tx.Authorizations...)
	}
	return tx
}

func cloneTransactions(txs []types.Transaction) []types.Transaction {
	if txs == nil {
		return nil
	}
	cloned := make([]types.Transaction, len(txs))
	for index, tx := range txs {
		cloned[index] = cloneTransaction(tx)
	}
	return cloned
}

func cloneReceipt(receipt types.Receipt) types.Receipt {
	if receipt.Events != nil {
		receipt.Events = append([]types.Event(nil), receipt.Events...)
		for index := range receipt.Events {
			receipt.Events[index] = cloneEvent(receipt.Events[index])
		}
	}
	return receipt
}

func cloneBlock(block types.Block) types.Block {
	block.Transactions = cloneTransactions(block.Transactions)
	if block.Receipts != nil {
		block.Receipts = append([]types.Receipt(nil), block.Receipts...)
		for index := range block.Receipts {
			block.Receipts[index] = cloneReceipt(block.Receipts[index])
		}
	}
	if block.FinalityCertificate != nil {
		certificate := *block.FinalityCertificate
		certificate.Signatures = append([]types.FinalitySignature(nil), certificate.Signatures...)
		block.FinalityCertificate = &certificate
	}
	return block
}

func cloneTransactionRecord(record types.TransactionRecord) types.TransactionRecord {
	record.Transaction = cloneTransaction(record.Transaction)
	record.Receipt = cloneReceipt(record.Receipt)
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
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func transactionHashSet(txs []types.Transaction) map[string]struct{} {
	hashes := make(map[string]struct{}, len(txs))
	for _, tx := range txs {
		hashes[tx.Hash()] = struct{}{}
	}
	return hashes
}

func filterTransactionsByHash(txs []types.Transaction, excluded map[string]struct{}) []types.Transaction {
	filtered := make([]types.Transaction, 0, len(txs))
	for _, tx := range txs {
		if _, skip := excluded[tx.Hash()]; !skip {
			filtered = append(filtered, tx)
		}
	}
	return filtered
}

func transactionsInBlocks(blocks []types.Block) []types.Transaction {
	var txs []types.Transaction
	for _, block := range blocks {
		txs = append(txs, block.Transactions...)
	}
	return txs
}

func (n *Node) persistLocked() error {
	return n.persistCandidateLocked(n.state, n.blocks)
}

func (n *Node) persistCandidateLocked(candidateState *state.Store, candidateBlocks []types.Block) error {
	if err := n.requireOpenLocked(); err != nil {
		return err
	}
	if n.dataDir == "" {
		return nil
	}
	if n.dataDirLock == nil {
		return errors.New("persistent node does not hold its data directory lock")
	}
	if n.snapshotGeneration == math.MaxUint64 {
		return errors.New("persisted snapshot generation overflow")
	}
	if len(candidateBlocks) == 0 {
		return errors.New("cannot persist an empty canonical chain")
	}
	if err := ensureDataManifest(n.dataDir, n.chainID, candidateBlocks[0].Hash()); err != nil {
		return n.handlePersistenceErrorLocked(err)
	}
	snapshot := diskSnapshot{
		Version:          diskSnapshotVersion,
		Generation:       n.snapshotGeneration + 1,
		ChainID:          n.chainID,
		GenesisState:     n.genesisState,
		State:            candidateState.Snapshot(),
		Blocks:           candidateBlocks,
		KnownBlocks:      n.knownBlockListLocked(),
		FinalityLock:     n.finalityLock,
		FinalityVotes:    n.finalityVoteListLocked(),
		FinalityEvidence: n.finalityEvidenceListLocked(),
	}
	checksum, err := diskSnapshotChecksum(snapshot)
	if err != nil {
		return err
	}
	snapshot.Checksum = checksum
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	path := chainPath(n.dataDir)
	contents := append(raw, '\n')
	if err := validateDiskSnapshotEncodedSize(len(contents)); err != nil {
		return err
	}
	if err := writeFileAtomically(path, contents, 0o600); err != nil {
		return n.handlePersistenceErrorLocked(err)
	}
	n.snapshotGeneration = snapshot.Generation
	return nil
}

func (n *Node) handlePersistenceErrorLocked(err error) error {
	if !errors.Is(err, ErrAtomicCommitUncertain) {
		return err
	}
	n.setPersistentHaltLocked("storage_commit_uncertain", err)
	return n.haltErr
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

func (n *Node) finalityVoteListLocked() []types.FinalitySignature {
	unique := make(map[string]types.FinalitySignature)
	for _, votes := range n.finalityVotes {
		for _, vote := range votes {
			key := fmt.Sprintf("%d:%s:%s", vote.Height, vote.Validator, vote.BlockHash)
			unique[key] = vote
		}
	}
	votes := make([]types.FinalitySignature, 0, len(unique))
	for _, vote := range unique {
		votes = append(votes, vote)
	}
	sort.Slice(votes, func(i int, j int) bool {
		if votes[i].Height != votes[j].Height {
			return votes[i].Height < votes[j].Height
		}
		if votes[i].Validator != votes[j].Validator {
			return votes[i].Validator < votes[j].Validator
		}
		return votes[i].BlockHash < votes[j].BlockHash
	})
	return votes
}

func chainPath(dataDir string) string {
	return filepath.Join(dataDir, "chain.json")
}
