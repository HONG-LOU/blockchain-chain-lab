package abci

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"sync"

	"chainlab/internal/contracts"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/hash"
	"chainlab/internal/state"
	"chainlab/internal/types"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmtcrypto "github.com/cometbft/cometbft/crypto"
	cmtsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/version"
)

const (
	ProtocolVersion = "chainlab-v1"
	AppVersion      = 1
	Codespace       = "chainlab"

	CodeOK              uint32 = 0
	CodeInvalidRequest  uint32 = 1
	CodeInvalidTx       uint32 = 2
	CodeExecutionFailed uint32 = 3
	CodeMempoolFull     uint32 = 4
	CodeUnsupported     uint32 = 5

	maxMempoolBytes        = 128 * 1024 * 1024
	maxEvidenceBytes int64 = 1024 * 1024
)

type GenesisDocument struct {
	Protocol      string         `json:"protocol"`
	ChainID       string         `json:"chain_id"`
	BlockGasLimit uint64         `json:"block_gas_limit"`
	State         state.Snapshot `json:"state"`
}

func NewGenesisDocument(chainID string, blockGasLimit uint64, store *state.Store) (GenesisDocument, error) {
	if store == nil {
		return GenesisDocument{}, errors.New("genesis state is required")
	}
	document := GenesisDocument{
		Protocol:      ProtocolVersion,
		ChainID:       chainID,
		BlockGasLimit: blockGasLimit,
		State:         store.Snapshot(),
	}
	if blockGasLimit == 0 {
		document.BlockGasLimit = types.DefaultBlockGasLimit
	}
	if _, err := validateGenesisDocument(document); err != nil {
		return GenesisDocument{}, err
	}
	return document, nil
}

func (g GenesisDocument) CanonicalBytes() ([]byte, error) {
	store, err := validateGenesisDocument(g)
	if err != nil {
		return nil, err
	}
	g.State = store.Snapshot()
	return hash.CanonicalBytes(g)
}

type Config struct {
	Genesis     GenesisDocument
	DataDir     string
	persistence applicationPersistence
}

type Application struct {
	mu                    sync.Mutex
	genesis               GenesisDocument
	committed             committedState
	runtime               *contracts.Runtime
	initialized           bool
	proposers             map[string]string
	candidate             *blockCandidate
	mempool               appMempool
	haltErr               error
	persistence           applicationPersistence
	closed                bool
	committedTxs          [][]byte
	committedReceipts     []types.Receipt
	snapshotCache         *applicationSnapshotCache
	previousSnapshotCache *applicationSnapshotCache
	incomingSnapshot      *incomingApplicationSnapshot
}

type committedState struct {
	store      *state.Store
	commitment applicationCommitment
	appHash    []byte
}

type applicationCommitment struct {
	Protocol          string `json:"protocol"`
	ChainID           string `json:"chain_id"`
	Height            int64  `json:"height"`
	BlockHash         string `json:"block_hash,omitempty"`
	Proposer          string `json:"proposer"`
	GasLimit          uint64 `json:"gas_limit"`
	GasUsed           uint64 `json:"gas_used"`
	BaseFeePerGas     uint64 `json:"base_fee_per_gas"`
	NextBaseFeePerGas uint64 `json:"next_base_fee_per_gas"`
	TxRoot            string `json:"tx_root"`
	ReceiptRoot       string `json:"receipt_root"`
	EvidenceRoot      string `json:"evidence_root"`
	StateRoot         string `json:"state_root"`
}

type blockCandidate struct {
	state    committedState
	txs      [][]byte
	receipts []types.Receipt
}

func NewApplication(config Config) (*Application, error) {
	store, err := validateGenesisDocument(config.Genesis)
	if err != nil {
		return nil, err
	}
	genesis := config.Genesis
	genesis.State = store.Snapshot()
	commitment := applicationCommitment{
		Protocol:          ProtocolVersion,
		ChainID:           genesis.ChainID,
		Height:            0,
		Proposer:          "genesis",
		GasLimit:          genesis.BlockGasLimit,
		BaseFeePerGas:     types.InitialBaseFeePerGas,
		NextBaseFeePerGas: types.InitialBaseFeePerGas,
		TxRoot:            types.TransactionRoot(nil),
		ReceiptRoot:       types.ReceiptRoot(nil),
		EvidenceRoot:      evidenceRoot(nil),
		StateRoot:         store.Root(),
	}
	committed := committedState{
		store:      store,
		commitment: commitment,
		appHash:    applicationHash(commitment),
	}
	persisted := persistedApplicationState{committed: committed, proposers: make(map[string]string)}
	persistence := config.persistence
	if persistence == nil && config.DataDir != "" {
		persistence, err = openApplicationDB(config.DataDir)
		if err != nil {
			return nil, err
		}
	}
	if persistence != nil {
		persisted, err = persistence.LoadOrCreate(genesis, persisted)
		if err != nil {
			_ = persistence.Close()
			return nil, err
		}
		committed = persisted.committed
	}
	runtime := contracts.NewRuntimeWithDefaults()
	return &Application{
		genesis:           genesis,
		committed:         committed,
		runtime:           runtime,
		initialized:       persisted.initialized,
		proposers:         cloneStringMap(persisted.proposers),
		mempool:           newAppMempool(committed.store),
		persistence:       persistence,
		committedTxs:      cloneTransactions(persisted.txs),
		committedReceipts: cloneReceipts(persisted.receipts),
	}, nil
}

func validateGenesisDocument(document GenesisDocument) (*state.Store, error) {
	if document.Protocol != ProtocolVersion {
		return nil, fmt.Errorf("genesis protocol must be %q", ProtocolVersion)
	}
	if err := types.ValidateChainID(document.ChainID); err != nil {
		return nil, fmt.Errorf("invalid genesis chain id: %w", err)
	}
	if document.BlockGasLimit == 0 || document.BlockGasLimit > math.MaxInt64 {
		return nil, errors.New("genesis block gas limit must fit a positive int64")
	}
	store, err := state.NewStoreFromSnapshot(document.State)
	if err != nil {
		return nil, fmt.Errorf("invalid genesis state: %w", err)
	}
	if len(store.Validators()) == 0 {
		return nil, errors.New("genesis validator set is required")
	}
	return store, nil
}

func decodeGenesisDocument(raw []byte) (GenesisDocument, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document GenesisDocument
	if err := decoder.Decode(&document); err != nil {
		return GenesisDocument{}, fmt.Errorf("decode genesis app state: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return GenesisDocument{}, errors.New("genesis app state must contain exactly one JSON value")
	}
	if _, err := validateGenesisDocument(document); err != nil {
		return GenesisDocument{}, err
	}
	return document, nil
}

func DecodeGenesisDocument(raw []byte) (GenesisDocument, error) {
	return decodeGenesisDocument(raw)
}

func (a *Application) Info(context.Context, *abcitypes.RequestInfo) (*abcitypes.ResponseInfo, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errors.New("application is closed")
	}
	data, err := hash.CanonicalBytes(struct {
		Protocol  string `json:"protocol"`
		StateRoot string `json:"state_root"`
		Halted    bool   `json:"halted"`
	}{
		Protocol:  ProtocolVersion,
		StateRoot: a.committed.commitment.StateRoot,
		Halted:    a.haltErr != nil,
	})
	if err != nil {
		return nil, err
	}
	return &abcitypes.ResponseInfo{
		Data:             string(data),
		Version:          version.ABCIVersion,
		AppVersion:       AppVersion,
		LastBlockHeight:  a.committed.commitment.Height,
		LastBlockAppHash: cloneBytes(a.committed.appHash),
	}, nil
}

func (a *Application) InitChain(_ context.Context, req *abcitypes.RequestInitChain) (*abcitypes.ResponseInitChain, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errors.New("application is closed")
	}
	if req == nil {
		return nil, errors.New("init chain request is required")
	}
	if a.initialized {
		return nil, errors.New("application is already initialized")
	}
	if req.ChainId != a.genesis.ChainID {
		return nil, fmt.Errorf("init chain id %q does not match genesis %q", req.ChainId, a.genesis.ChainID)
	}
	if req.InitialHeight != 0 && req.InitialHeight != 1 {
		return nil, errors.New("protocol version 1 requires initial height 1")
	}
	if len(req.AppStateBytes) == 0 {
		return nil, errors.New("canonical genesis app state is required")
	}
	provided, err := decodeGenesisDocument(req.AppStateBytes)
	if err != nil {
		return nil, err
	}
	if !sameGenesisDocument(provided, a.genesis) {
		return nil, errors.New("init chain app state does not match configured genesis")
	}
	if err := validateConsensusParams(req.ConsensusParams, a.genesis.BlockGasLimit); err != nil {
		return nil, err
	}
	proposers, err := bindGenesisValidators(req.Validators, a.committed.store.Validators())
	if err != nil {
		return nil, err
	}
	if err := a.persistLocked(a.committed, true, proposers, nil, nil); err != nil {
		a.haltErr = err
		return nil, err
	}
	a.proposers = proposers
	a.initialized = true
	return &abcitypes.ResponseInitChain{AppHash: cloneBytes(a.committed.appHash)}, nil
}

func sameGenesisDocument(left GenesisDocument, right GenesisDocument) bool {
	leftBytes, leftErr := left.CanonicalBytes()
	rightBytes, rightErr := right.CanonicalBytes()
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}

func validateConsensusParams(params *cmtproto.ConsensusParams, blockGasLimit uint64) error {
	if params == nil || params.Block == nil {
		return errors.New("CometBFT block consensus parameters are required")
	}
	if params.Block.MaxBytes != types.MaxBlockBytes {
		return fmt.Errorf("CometBFT max block bytes must equal %d", types.MaxBlockBytes)
	}
	if params.Block.MaxGas != int64(blockGasLimit) {
		return fmt.Errorf("CometBFT max block gas must equal %d", blockGasLimit)
	}
	if params.Validator == nil || len(params.Validator.PubKeyTypes) != 1 || params.Validator.PubKeyTypes[0] != cmtsecp256k1.KeyType {
		return errors.New("CometBFT validators must use only secp256k1 public keys")
	}
	if params.Abci != nil && params.Abci.VoteExtensionsEnableHeight != 0 {
		return errors.New("vote extensions are disabled in protocol version 1")
	}
	if params.Evidence == nil || params.Evidence.MaxAgeNumBlocks <= 0 || params.Evidence.MaxAgeDuration <= 0 {
		return errors.New("positive CometBFT evidence retention is required")
	}
	if params.Evidence.MaxBytes <= 0 || params.Evidence.MaxBytes > maxEvidenceBytes {
		return fmt.Errorf("CometBFT evidence max bytes must be between 1 and %d", maxEvidenceBytes)
	}
	return nil
}

func ValidateConsensusParams(params *cmtproto.ConsensusParams, blockGasLimit uint64) error {
	return validateConsensusParams(params, blockGasLimit)
}

func bindGenesisValidators(updates []abcitypes.ValidatorUpdate, expected []string) (map[string]string, error) {
	if len(updates) == 0 || len(updates) != len(expected) {
		return nil, errors.New("CometBFT genesis validators must exactly match ChainLab validators")
	}
	if len(updates) > types.MaxValidators {
		return nil, fmt.Errorf("genesis validator set exceeds %d entries", types.MaxValidators)
	}
	bindings := make(map[string]string, len(updates))
	accounts := make([]string, 0, len(updates))
	for index, update := range updates {
		if update.Power != 1 {
			return nil, fmt.Errorf("genesis validator %d must have voting power 1", index)
		}
		compressed := update.PubKey.GetSecp256K1()
		account, err := chaincrypto.AddressFromCompressedPublicKey(compressed)
		if err != nil {
			return nil, fmt.Errorf("genesis validator %d: %w", index, err)
		}
		consensusAddress := cmtsecp256k1.PubKey(compressed).Address()
		key := hex.EncodeToString(consensusAddress)
		if _, exists := bindings[key]; exists {
			return nil, fmt.Errorf("genesis validator %d has a duplicate consensus address", index)
		}
		bindings[key] = account
		accounts = append(accounts, account)
	}
	sort.Strings(accounts)
	expected = append([]string(nil), expected...)
	sort.Strings(expected)
	if len(accounts) != len(expected) {
		return nil, errors.New("CometBFT genesis validator count does not match ChainLab state")
	}
	for index := range accounts {
		if accounts[index] != expected[index] {
			return nil, errors.New("CometBFT genesis validator accounts do not match ChainLab state")
		}
	}
	return bindings, nil
}

func (a *Application) Query(_ context.Context, req *abcitypes.RequestQuery) (*abcitypes.ResponseQuery, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errors.New("application is closed")
	}
	if req == nil {
		return nil, errors.New("query request is required")
	}
	height := a.committed.commitment.Height
	response := &abcitypes.ResponseQuery{Height: height, Codespace: Codespace}
	if req.Prove {
		response.Code = CodeUnsupported
		response.Log = "state proofs are not available in protocol version 1"
		return response, nil
	}
	if req.Height != 0 && req.Height != height {
		response.Code = CodeUnsupported
		response.Log = "only the latest committed height is available"
		return response, nil
	}
	switch req.Path {
	case "/app":
		value, err := hash.CanonicalBytes(a.committed.commitment)
		if err != nil {
			return nil, err
		}
		response.Key = []byte("app")
		response.Value = value
	case "/state/root":
		response.Key = []byte("state_root")
		response.Value = []byte(a.committed.commitment.StateRoot)
	case "/account":
		address, err := chaincrypto.NormalizeAddress(string(req.Data))
		if err != nil || address != string(req.Data) {
			response.Code = CodeInvalidRequest
			response.Log = "account query requires a canonical address"
			return response, nil
		}
		value, err := hash.CanonicalBytes(a.committed.store.GetAccount(address))
		if err != nil {
			return nil, err
		}
		response.Key = []byte(address)
		response.Value = value
	default:
		response.Code = CodeUnsupported
		response.Log = "unsupported query path"
	}
	return response, nil
}

func (a *Application) Commit(context.Context, *abcitypes.RequestCommit) (*abcitypes.ResponseCommit, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.requireReadyLocked(); err != nil {
		return nil, err
	}
	if a.candidate == nil {
		return nil, errors.New("commit requires a finalized block candidate")
	}
	if a.candidate.state.commitment.Height != a.committed.commitment.Height+1 {
		return nil, errors.New("finalized block candidate height is out of sequence")
	}
	nextPool := newAppMempool(a.candidate.state.store)
	if a.candidate.state.commitment.Height < math.MaxInt64 {
		var err error
		nextPool, err = a.mempool.rebuild(
			a.candidate.state.store,
			a.genesis.ChainID,
			a.candidate.state.commitment.Height+1,
			a.candidate.state.commitment.NextBaseFeePerGas,
			a.genesis.BlockGasLimit,
			a.runtime,
			a.candidate.txs,
		)
		if err != nil {
			if isFatal(err) {
				a.haltErr = err
			}
			return nil, err
		}
	}
	if err := a.persistLocked(
		a.candidate.state,
		true,
		a.proposers,
		a.candidate.txs,
		a.candidate.receipts,
	); err != nil {
		a.haltErr = err
		return nil, err
	}
	a.committed = a.candidate.state
	a.committedTxs = cloneTransactions(a.candidate.txs)
	a.committedReceipts = cloneReceipts(a.candidate.receipts)
	a.candidate = nil
	a.mempool = nextPool
	return &abcitypes.ResponseCommit{}, nil
}

func (a *Application) requireReadyLocked() error {
	if a.closed {
		return errors.New("application is closed")
	}
	if a.haltErr != nil {
		return fmt.Errorf("application is halted: %w", a.haltErr)
	}
	if !a.initialized {
		return errors.New("application is not initialized")
	}
	if a.committed.commitment.Height >= math.MaxInt64 {
		return errors.New("application height domain is exhausted")
	}
	return nil
}

func (a *Application) persistLocked(
	committed committedState,
	initialized bool,
	proposers map[string]string,
	txs [][]byte,
	receipts []types.Receipt,
) error {
	if a.persistence == nil {
		return nil
	}
	if err := a.persistence.Save(persistedApplicationState{
		committed: committed, initialized: initialized, proposers: cloneStringMap(proposers),
		txs: cloneTransactions(txs), receipts: cloneReceipts(receipts),
	}); err != nil {
		return fmt.Errorf("persist committed application state: %w", err)
	}
	return nil
}

func cloneReceipts(receipts []types.Receipt) []types.Receipt {
	cloned := make([]types.Receipt, len(receipts))
	for index, receipt := range receipts {
		cloned[index] = receipt
		cloned[index].Events = make([]types.Event, len(receipt.Events))
		for eventIndex, event := range receipt.Events {
			cloned[index].Events[eventIndex] = event
			cloned[index].Events[eventIndex].Attributes = cloneStringMap(event.Attributes)
		}
	}
	return cloned
}

func (a *Application) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	if a.persistence == nil {
		return nil
	}
	return a.persistence.Close()
}

func (a *Application) proposerLocked(address []byte) (string, error) {
	if len(address) != cmtcrypto.AddressSize {
		return "", errors.New("proposal has an invalid proposer address")
	}
	proposer, exists := a.proposers[hex.EncodeToString(address)]
	if !exists {
		return "", errors.New("proposal proposer is not in the fixed validator set")
	}
	return proposer, nil
}

func applicationHash(commitment applicationCommitment) []byte {
	return hash.Keccak(hash.MustCanonicalBytes(commitment))
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}
