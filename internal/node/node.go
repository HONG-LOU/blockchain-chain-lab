package node

import (
	"errors"
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

	store := state.NewStore()
	for address, balance := range config.GenesisBalance {
		store.SetBalance(address, balance)
	}
	genesis := types.GenesisBlock(config.ChainID, store.Root())

	return &Node{
		chainID:     config.ChainID,
		proposerKey: config.ProposerKey,
		proposer:    proposer,
		state:       store,
		executor:    core.NewExecutor(config.ChainID, feeCollector, contracts.NewRuntimeWithDefaults()),
		consensus:   consensus.NewPOA(validators),
		blocks:      []types.Block{genesis},
	}, nil
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
	n.mempool = nil
	return block, nil
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
