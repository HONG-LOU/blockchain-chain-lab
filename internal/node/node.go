package node

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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
}

type Node struct {
	mu          sync.Mutex
	chainID     string
	proposerKey chaincrypto.PrivateKey
	proposer    string
	state       *state.Store
	executor    *core.Executor
	consensus   *consensus.POA
	blocks      []types.Block
	mempool     []types.Transaction
	txIndex     map[string]types.TransactionRecord
	dataDir     string
}

type diskSnapshot struct {
	ChainID string         `json:"chain_id"`
	State   state.Snapshot `json:"state"`
	Blocks  []types.Block  `json:"blocks"`
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
	feeCollector := config.FeeCollector
	if feeCollector == "" {
		feeCollector = proposer
	}

	n := &Node{
		chainID:     config.ChainID,
		proposerKey: config.ProposerKey,
		proposer:    proposer,
		executor:    core.NewExecutor(config.ChainID, feeCollector, contracts.NewRuntimeWithDefaults()),
		consensus:   consensus.NewPOA(validators),
		txIndex:     make(map[string]types.TransactionRecord),
		dataDir:     config.DataDir,
	}

	if config.DataDir != "" {
		loaded, err := loadDiskSnapshot(config.DataDir)
		if err != nil {
			return nil, err
		}
		if loaded != nil {
			if loaded.ChainID != config.ChainID {
				return nil, errors.New("persisted chain id does not match config")
			}
			n.state = state.NewStoreFromSnapshot(loaded.State)
			n.blocks = append([]types.Block(nil), loaded.Blocks...)
			if len(n.blocks) == 0 {
				return nil, errors.New("persisted chain has no blocks")
			}
			n.rebuildTxIndex()
			return n, nil
		}
	}

	store := state.NewStore()
	for address, balance := range config.GenesisBalance {
		store.SetBalance(address, balance)
	}
	genesis := types.GenesisBlock(config.ChainID, store.Root())
	n.state = store
	n.blocks = []types.Block{genesis}
	if err := n.persistLocked(); err != nil {
		return nil, err
	}
	return n, nil
}

func (n *Node) SubmitTx(tx types.Transaction) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	working := n.state.Clone()
	for _, pending := range n.mempool {
		if _, err := n.executor.Execute(working, pending); err != nil {
			return err
		}
	}
	if _, err := n.executor.Execute(working, tx); err != nil {
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
	for _, tx := range n.mempool {
		receipt, err := n.executor.Execute(working, tx)
		if err != nil {
			return types.Block{}, err
		}
		receipts = append(receipts, receipt)
	}

	parent := n.blocks[len(n.blocks)-1]
	block := types.Block{
		Header: types.BlockHeader{
			ChainID:     n.chainID,
			Height:      parent.Header.Height + 1,
			ParentHash:  parent.Hash(),
			TimeUnix:    time.Now().Unix(),
			Proposer:    n.proposer,
			TxRoot:      types.TransactionRoot(n.mempool),
			ReceiptRoot: types.ReceiptRoot(receipts),
			StateRoot:   working.Root(),
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
	n.blocks = append(n.blocks, block)
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
	head := n.blocks[len(n.blocks)-1]
	if block.Header.Height <= head.Header.Height {
		if block.Hash() == head.Hash() {
			return nil
		}
		return errors.New("imported block is not ahead of local head")
	}

	working := n.state.Clone()
	executor := core.NewExecutor(n.chainID, block.Header.Proposer, contracts.NewRuntimeWithDefaults())
	receipts := make([]types.Receipt, 0, len(block.Transactions))
	for _, tx := range block.Transactions {
		receipt, err := executor.Execute(working, tx)
		if err != nil {
			return err
		}
		receipts = append(receipts, receipt)
	}
	if types.ReceiptRoot(receipts) != block.Header.ReceiptRoot {
		return errors.New("imported block receipt root mismatch")
	}
	if working.Root() != block.Header.StateRoot {
		return errors.New("imported block state root mismatch")
	}
	if err := n.consensus.ValidateBlock(head, block); err != nil {
		return err
	}

	n.state.ReplaceWith(working)
	n.blocks = append(n.blocks, block)
	n.indexBlock(block)
	n.removeMempoolTransactions(block.Transactions)
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

func (n *Node) Proposal(id string) types.Proposal {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.state.Proposal(id)
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

func (n *Node) persistLocked() error {
	if n.dataDir == "" {
		return nil
	}
	if err := os.MkdirAll(n.dataDir, 0o755); err != nil {
		return err
	}
	snapshot := diskSnapshot{
		ChainID: n.chainID,
		State:   n.state.Snapshot(),
		Blocks:  n.blocks,
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
