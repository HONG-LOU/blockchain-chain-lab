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
	chainproof "chainlab/pkg/proof"

	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmtcrypto "github.com/cometbft/cometbft/crypto"
	cmtsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/version"
)

const (
	ProtocolVersion   = "chainlab-v1"
	ProtocolVersionV2 = "chainlab-v2"
	ProtocolVersionV3 = "chainlab-v3"
	ProtocolVersionV4 = "chainlab-v4"
	AppVersion        = 1
	AppVersionV2      = 2
	AppVersionV3      = 3
	AppVersionV4      = 4
	Codespace         = "chainlab"

	CodeOK              uint32 = 0
	CodeInvalidRequest  uint32 = 1
	CodeInvalidTx       uint32 = 2
	CodeExecutionFailed uint32 = 3
	CodeMempoolFull     uint32 = 4
	CodeUnsupported     uint32 = 5

	maxMempoolBytes               = 128 * 1024 * 1024
	maxEvidenceBytes        int64 = 1024 * 1024
	maxValidatorEpochLength int64 = 1_000_000
	maxQueryPathBytes             = 128
	maxQueryDataBytes             = 1024
)

type ValidatorPolicy struct {
	EpochLength                       int64  `json:"epoch_length"`
	DuplicateVoteSlashBasisPoints     uint32 `json:"duplicate_vote_slash_basis_points"`
	LightClientAttackSlashBasisPoints uint32 `json:"light_client_attack_slash_basis_points"`
	EvidenceMaxAgeNumBlocks           int64  `json:"evidence_max_age_num_blocks"`
	EvidenceMaxAgeDurationNanos       int64  `json:"evidence_max_age_duration_nanos"`
}

type GenesisDocument struct {
	Protocol        string            `json:"protocol"`
	ChainID         string            `json:"chain_id"`
	BlockGasLimit   uint64            `json:"block_gas_limit"`
	State           state.Snapshot    `json:"state"`
	ValidatorPolicy *ValidatorPolicy  `json:"validator_policy,omitempty"`
	Upgrades        []ProtocolUpgrade `json:"upgrades,omitempty"`
}

func NewGenesisDocument(chainID string, blockGasLimit uint64, store *state.Store) (GenesisDocument, error) {
	return newGenesisDocument(ProtocolVersion, chainID, blockGasLimit, store, nil)
}

func NewGenesisDocumentV2(
	chainID string,
	blockGasLimit uint64,
	store *state.Store,
	policy ValidatorPolicy,
) (GenesisDocument, error) {
	return newGenesisDocument(ProtocolVersionV2, chainID, blockGasLimit, store, &policy)
}

func newGenesisDocument(
	protocol string,
	chainID string,
	blockGasLimit uint64,
	store *state.Store,
	policy *ValidatorPolicy,
) (GenesisDocument, error) {
	if store == nil {
		return GenesisDocument{}, errors.New("genesis state is required")
	}
	document := GenesisDocument{
		Protocol:        protocol,
		ChainID:         chainID,
		BlockGasLimit:   blockGasLimit,
		State:           store.Snapshot(),
		ValidatorPolicy: policy,
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
	Genesis       GenesisDocument
	DataDir       string
	Storage       StorageProfile
	MaxAppVersion uint64
	persistence   applicationPersistence
}

type Application struct {
	mu                    sync.Mutex
	genesis               GenesisDocument
	maxAppVersion         uint64
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
	flatTree   *chainproof.SparseTree
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
	Epoch             uint64 `json:"epoch,omitempty"`
	ValidatorRoot     string `json:"validator_root,omitempty"`
}

type blockCandidate struct {
	state    committedState
	txs      [][]byte
	receipts []types.Receipt
}

func NewApplication(config Config) (*Application, error) {
	maxAppVersion, err := normalizeMaxAppVersion(config.MaxAppVersion)
	if err != nil {
		return nil, err
	}
	store, err := validateGenesisDocument(config.Genesis)
	if err != nil {
		return nil, err
	}
	genesis := config.Genesis
	genesis.State = store.Snapshot()
	genesis.Upgrades = append([]ProtocolUpgrade(nil), config.Genesis.Upgrades...)
	if config.Genesis.ValidatorPolicy != nil {
		policy := *config.Genesis.ValidatorPolicy
		genesis.ValidatorPolicy = &policy
	}
	commitment := applicationCommitment{
		Protocol:          genesis.Protocol,
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
		profile, profileErr := normalizeStorageProfile(config.Storage)
		if profileErr != nil {
			return nil, profileErr
		}
		persistence, err = openApplicationDB(config.DataDir, profile)
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
	committed.flatTree, err = buildStoreSparseTree(committed.store)
	if err != nil {
		if persistence != nil {
			_ = persistence.Close()
		}
		return nil, fmt.Errorf("build committed sparse state tree: %w", err)
	}
	if required := appVersionAtHeight(genesis, nextApplicationHeight(committed.commitment.Height)); required > maxAppVersion {
		if persistence != nil {
			_ = persistence.Close()
		}
		return nil, fmt.Errorf(
			"application binary supports app version %d but the next height requires version %d",
			maxAppVersion, required,
		)
	}
	runtime := contracts.NewRuntimeWithDefaults()
	return &Application{
		genesis:           genesis,
		maxAppVersion:     maxAppVersion,
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
	switch document.Protocol {
	case ProtocolVersion:
		if document.ValidatorPolicy != nil {
			return nil, errors.New("protocol version 1 must not define a validator policy")
		}
	case ProtocolVersionV2:
		if document.ValidatorPolicy == nil {
			return nil, errors.New("protocol version 2 requires a validator policy")
		}
		if err := validateValidatorPolicy(*document.ValidatorPolicy); err != nil {
			return nil, fmt.Errorf("invalid validator policy: %w", err)
		}
	default:
		return nil, fmt.Errorf("genesis protocol must be %q or %q", ProtocolVersion, ProtocolVersionV2)
	}
	if err := validateProtocolUpgrades(document); err != nil {
		return nil, err
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
	if _, exists := store.ValidatorLifecycle(); exists {
		return nil, errors.New("genesis state must not contain an initialized validator lifecycle")
	}
	return store, nil
}

func validateValidatorPolicy(policy ValidatorPolicy) error {
	if policy.EpochLength < 2 || policy.EpochLength > maxValidatorEpochLength {
		return fmt.Errorf("epoch length must be between 2 and %d", maxValidatorEpochLength)
	}
	if policy.DuplicateVoteSlashBasisPoints == 0 || policy.DuplicateVoteSlashBasisPoints > 10_000 {
		return errors.New("duplicate-vote slash ratio must be between 1 and 10000 basis points")
	}
	if policy.LightClientAttackSlashBasisPoints == 0 || policy.LightClientAttackSlashBasisPoints > 10_000 {
		return errors.New("light-client-attack slash ratio must be between 1 and 10000 basis points")
	}
	if policy.EvidenceMaxAgeNumBlocks <= 0 || policy.EvidenceMaxAgeDurationNanos <= 0 {
		return errors.New("evidence retention must be positive")
	}
	return nil
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
		Protocol:  a.committed.commitment.Protocol,
		StateRoot: a.committed.commitment.StateRoot,
		Halted:    a.haltErr != nil,
	})
	if err != nil {
		return nil, err
	}
	return &abcitypes.ResponseInfo{
		Data:             string(data),
		Version:          version.ABCIVersion,
		AppVersion:       appVersionAtHeight(a.genesis, nextApplicationHeight(a.committed.commitment.Height)),
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
	if err := validateConsensusParamsForGenesis(req.ConsensusParams, a.genesis); err != nil {
		return nil, err
	}
	proposers, identities, err := bindGenesisValidators(req.Validators, a.committed.store.Validators())
	if err != nil {
		return nil, err
	}
	nextCommitted := a.committed
	if a.genesis.Protocol == ProtocolVersionV2 {
		nextStore := a.committed.store.Clone()
		if err := nextStore.InitializeValidatorLifecycle(identities); err != nil {
			return nil, err
		}
		nextTree := a.committed.flatTree.Clone()
		if err := applyStoreMutationsToSparseTree(nextTree, nextStore); err != nil {
			return nil, fmt.Errorf("update genesis sparse state tree: %w", err)
		}
		nextCommitted.store = nextStore
		nextCommitted.flatTree = nextTree
		nextCommitted.commitment.StateRoot = nextStore.Root()
		nextCommitted.commitment.ValidatorRoot = nextStore.ValidatorRoot()
		nextCommitted.appHash = applicationHash(nextCommitted.commitment)
	}
	if err := a.persistLocked(nextCommitted, true, proposers, nil, nil); err != nil {
		a.haltErr = err
		return nil, err
	}
	nextCommitted.flatTree = nextCommitted.flatTree.Publish()
	a.committed = nextCommitted
	a.proposers = proposers
	a.initialized = true
	a.mempool = newAppMempool(nextCommitted.store)
	return &abcitypes.ResponseInitChain{AppHash: cloneBytes(a.committed.appHash)}, nil
}

func sameGenesisDocument(left GenesisDocument, right GenesisDocument) bool {
	leftBytes, leftErr := left.CanonicalBytes()
	rightBytes, rightErr := right.CanonicalBytes()
	return leftErr == nil && rightErr == nil && bytes.Equal(leftBytes, rightBytes)
}

func validateConsensusParams(params *cmtproto.ConsensusParams, blockGasLimit uint64) error {
	return validateConsensusParamsForGenesis(params, GenesisDocument{Protocol: ProtocolVersion, BlockGasLimit: blockGasLimit})
}

func validateConsensusParamsForGenesis(params *cmtproto.ConsensusParams, genesis GenesisDocument) error {
	if params == nil || params.Block == nil {
		return errors.New("CometBFT block consensus parameters are required")
	}
	if params.Block.MaxBytes != types.MaxBlockBytes {
		return fmt.Errorf("CometBFT max block bytes must equal %d", types.MaxBlockBytes)
	}
	if params.Block.MaxGas != int64(genesis.BlockGasLimit) {
		return fmt.Errorf("CometBFT max block gas must equal %d", genesis.BlockGasLimit)
	}
	if params.Validator == nil || len(params.Validator.PubKeyTypes) != 1 || params.Validator.PubKeyTypes[0] != cmtsecp256k1.KeyType {
		return errors.New("CometBFT validators must use only secp256k1 public keys")
	}
	if params.Abci != nil && params.Abci.VoteExtensionsEnableHeight != 0 {
		return fmt.Errorf("vote extensions are disabled in %s", genesis.Protocol)
	}
	if params.Evidence == nil || params.Evidence.MaxAgeNumBlocks <= 0 || params.Evidence.MaxAgeDuration <= 0 {
		return errors.New("positive CometBFT evidence retention is required")
	}
	if params.Evidence.MaxBytes <= 0 || params.Evidence.MaxBytes > maxEvidenceBytes {
		return fmt.Errorf("CometBFT evidence max bytes must be between 1 and %d", maxEvidenceBytes)
	}
	if genesis.Protocol == ProtocolVersionV2 {
		policy := genesis.ValidatorPolicy
		if policy == nil {
			return errors.New("protocol version 2 validator policy is missing")
		}
		if params.Evidence.MaxAgeNumBlocks != policy.EvidenceMaxAgeNumBlocks ||
			int64(params.Evidence.MaxAgeDuration) != policy.EvidenceMaxAgeDurationNanos {
			return errors.New("CometBFT evidence retention does not match the protocol version 2 validator policy")
		}
		if params.Version == nil || params.Version.App != AppVersionV2 {
			return fmt.Errorf("CometBFT application version must equal %d", AppVersionV2)
		}
	}
	return nil
}

func ValidateConsensusParams(params *cmtproto.ConsensusParams, blockGasLimit uint64) error {
	return validateConsensusParams(params, blockGasLimit)
}

func ValidateGenesisConsensusParams(params *cmtproto.ConsensusParams, genesis GenesisDocument) error {
	if _, err := validateGenesisDocument(genesis); err != nil {
		return err
	}
	return validateConsensusParamsForGenesis(params, genesis)
}

func bindGenesisValidators(
	updates []abcitypes.ValidatorUpdate,
	expected []string,
) (map[string]string, []state.ValidatorIdentity, error) {
	if len(updates) == 0 || len(updates) != len(expected) {
		return nil, nil, errors.New("CometBFT genesis validators must exactly match ChainLab validators")
	}
	if len(updates) > types.MaxValidators {
		return nil, nil, fmt.Errorf("genesis validator set exceeds %d entries", types.MaxValidators)
	}
	bindings := make(map[string]string, len(updates))
	identities := make([]state.ValidatorIdentity, 0, len(updates))
	accounts := make([]string, 0, len(updates))
	for index, update := range updates {
		if update.Power != 1 {
			return nil, nil, fmt.Errorf("genesis validator %d must have voting power 1", index)
		}
		compressed := update.PubKey.GetSecp256K1()
		account, err := chaincrypto.AddressFromCompressedPublicKey(compressed)
		if err != nil {
			return nil, nil, fmt.Errorf("genesis validator %d: %w", index, err)
		}
		consensusAddress := cmtsecp256k1.PubKey(compressed).Address()
		key := hex.EncodeToString(consensusAddress)
		if _, exists := bindings[key]; exists {
			return nil, nil, fmt.Errorf("genesis validator %d has a duplicate consensus address", index)
		}
		bindings[key] = account
		identities = append(identities, state.ValidatorIdentity{
			Account: account, ConsensusAddress: key, PublicKey: hex.EncodeToString(compressed),
			Power: update.Power, ActiveHeight: 1,
		})
		accounts = append(accounts, account)
	}
	sort.Strings(accounts)
	expected = append([]string(nil), expected...)
	sort.Strings(expected)
	if len(accounts) != len(expected) {
		return nil, nil, errors.New("CometBFT genesis validator count does not match ChainLab state")
	}
	for index := range accounts {
		if accounts[index] != expected[index] {
			return nil, nil, errors.New("CometBFT genesis validator accounts do not match ChainLab state")
		}
	}
	return bindings, identities, nil
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
	queryState := a.committed
	queryTxs := a.committedTxs
	queryReceipts := a.committedReceipts
	height := queryState.commitment.Height
	response := &abcitypes.ResponseQuery{Height: height, Codespace: Codespace}
	if len(req.Path) > maxQueryPathBytes || len(req.Data) > maxQueryDataBytes {
		response.Code = CodeInvalidRequest
		response.Log = "query path or data exceeds the protocol limit"
		return response, nil
	}
	if req.Prove {
		response.Code = CodeUnsupported
		response.Log = "ABCI proof ops are unavailable; use chainlab-v3 proof query paths"
		return response, nil
	}
	if req.Path == "/storage" && req.Height != 0 && req.Height != height {
		response.Code = CodeUnsupported
		response.Log = "storage information is available only at the latest height"
		return response, nil
	}
	if req.Height != 0 && req.Height != height {
		history, available := a.persistence.(historicalApplicationPersistence)
		if !available {
			response.Code = CodeUnsupported
			response.Log = "historical application state is unavailable"
			return response, nil
		}
		historical, err := history.LoadHeight(req.Height)
		if err != nil {
			response.Code = CodeUnsupported
			switch {
			case errors.Is(err, ErrHistoricalStatePruned):
				response.Log = "historical application state was pruned"
			case errors.Is(err, ErrHistoricalStateUnavailable):
				response.Log = "historical application state is unavailable"
			default:
				return nil, fmt.Errorf("load historical application height %d: %w", req.Height, err)
			}
			return response, nil
		}
		queryState = historical.committed
		queryTxs = historical.txs
		queryReceipts = historical.receipts
		height = queryState.commitment.Height
		response.Height = height
	}
	switch req.Path {
	case "/app":
		value, err := hash.CanonicalBytes(queryState.commitment)
		if err != nil {
			return nil, err
		}
		response.Key = []byte("app")
		response.Value = value
	case "/state/root":
		response.Key = []byte("state_root")
		response.Value = []byte(queryState.commitment.StateRoot)
	case "/account":
		address, err := chaincrypto.NormalizeAddress(string(req.Data))
		if err != nil || address != string(req.Data) {
			response.Code = CodeInvalidRequest
			response.Log = "account query requires a canonical address"
			return response, nil
		}
		value, err := hash.CanonicalBytes(queryState.store.GetAccount(address))
		if err != nil {
			return nil, err
		}
		response.Key = []byte(address)
		response.Value = value
	case "/validator":
		if a.genesis.Protocol != ProtocolVersionV2 {
			response.Code = CodeUnsupported
			response.Log = "validator lifecycle queries require protocol version 2"
			return response, nil
		}
		address, err := chaincrypto.NormalizeAddress(string(req.Data))
		if err != nil || address != string(req.Data) {
			response.Code = CodeInvalidRequest
			response.Log = "validator query requires a canonical account address"
			return response, nil
		}
		identity, found := queryState.store.ValidatorIdentityByAccount(address)
		if !found {
			response.Code = CodeInvalidRequest
			response.Log = "validator identity is unknown"
			return response, nil
		}
		value, err := hash.CanonicalBytes(struct {
			Identity state.ValidatorIdentity `json:"identity"`
			Stake    uint64                  `json:"stake"`
		}{Identity: identity, Stake: queryState.store.StakeOf(address)})
		if err != nil {
			return nil, err
		}
		response.Key = []byte(address)
		response.Value = value
	case "/storage":
		history, available := a.persistence.(historicalApplicationPersistence)
		if !available {
			response.Code = CodeUnsupported
			response.Log = "persistent storage information is unavailable"
			return response, nil
		}
		value, err := hash.CanonicalBytes(history.HistoryRange())
		if err != nil {
			return nil, err
		}
		response.Key = []byte("storage")
		response.Value = value
	case "/proof/transaction", "/proof/receipt", "/proof/account", "/proof/state":
		if !protocolUsesMerkleProofs(queryState.commitment.Protocol) {
			response.Code = CodeUnsupported
			response.Log = "inclusion proofs require chainlab-v3 or later"
			return response, nil
		}
		if req.Path == "/proof/state" && !protocolUsesSparseState(queryState.commitment.Protocol) {
			response.Code = CodeUnsupported
			response.Log = "general state proofs require chainlab-v4"
			return response, nil
		}
		if protocolUsesSparseState(queryState.commitment.Protocol) &&
			(req.Path == "/proof/account" || req.Path == "/proof/state") {
			envelope, key, err := a.buildSparseProofEnvelope(req.Path, req.Data, height, queryState)
			if err != nil {
				if errors.Is(err, ErrInvalidProofQuery) {
					response.Code = CodeInvalidRequest
					response.Log = err.Error()
					return response, nil
				}
				return nil, err
			}
			value, err := hash.CanonicalBytes(envelope)
			if err != nil {
				return nil, err
			}
			response.Key = key
			response.Value = value
			return response, nil
		}
		envelope, key, err := a.buildProofEnvelope(
			req.Path, req.Data, height, queryState, queryTxs, queryReceipts,
		)
		if err != nil {
			if errors.Is(err, ErrInvalidProofQuery) {
				response.Code = CodeInvalidRequest
				response.Log = err.Error()
				return response, nil
			}
			return nil, err
		}
		value, err := hash.CanonicalBytes(envelope)
		if err != nil {
			return nil, err
		}
		response.Key = key
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
	a.candidate.state.flatTree = a.candidate.state.flatTree.Publish()
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
	if a.persistence != nil {
		if err := a.persistence.Save(persistedApplicationState{
			committed: committed, initialized: initialized, proposers: cloneStringMap(proposers),
			txs: cloneTransactions(txs), receipts: cloneReceipts(receipts),
		}); err != nil {
			return fmt.Errorf("persist committed application state: %w", err)
		}
	}
	committed.store.ResetMutations()
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

func (a *Application) Backup(path string) error {
	if a == nil {
		return errors.New("application is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return errors.New("application is closed")
	}
	history, available := a.persistence.(historicalApplicationPersistence)
	if !available {
		return errors.New("persistent application storage is required")
	}
	return history.Backup(path)
}

func (a *Application) CompactStorage() error {
	if a == nil {
		return errors.New("application is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return errors.New("application is closed")
	}
	history, available := a.persistence.(historicalApplicationPersistence)
	if !available {
		return errors.New("persistent application storage is required")
	}
	return history.Compact()
}

func (a *Application) proposerLocked(address []byte, height int64) (string, error) {
	if len(address) != cmtcrypto.AddressSize {
		return "", errors.New("proposal has an invalid proposer address")
	}
	proposer, exists := a.proposers[hex.EncodeToString(address)]
	if !exists {
		return "", errors.New("proposal proposer is not in the fixed validator set")
	}
	if a.genesis.Protocol == ProtocolVersionV2 {
		identity, exists := a.committed.store.ValidatorIdentityByConsensusAddress(hex.EncodeToString(address))
		if !exists || !validatorIdentityActive(identity, height) {
			return "", errors.New("proposal proposer is not active at this height")
		}
	}
	return proposer, nil
}

func appVersion(protocol string) uint64 {
	switch protocol {
	case ProtocolVersionV4:
		return AppVersionV4
	case ProtocolVersionV3:
		return AppVersionV3
	case ProtocolVersionV2:
		return AppVersionV2
	default:
		return AppVersion
	}
}

func applicationHash(commitment applicationCommitment) []byte {
	return hash.Keccak(hash.MustCanonicalBytes(commitment))
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}
