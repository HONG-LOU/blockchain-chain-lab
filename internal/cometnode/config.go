package cometnode

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	chainabci "chainlab/internal/abci"
	"chainlab/internal/hash"
	chaintypes "chainlab/internal/types"

	dbm "github.com/cometbft/cometbft-db"
	cmtcfg "github.com/cometbft/cometbft/config"
	cmtcrypto "github.com/cometbft/cometbft/crypto"
	cmtsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
	cmtflags "github.com/cometbft/cometbft/libs/cli/flags"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/node"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/privval"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	"github.com/cometbft/cometbft/proxy"
	cmttypes "github.com/cometbft/cometbft/types"
)

const (
	NodeProtocol          = "chainlab-comet-node-v2"
	NodeDocumentPath      = "config/chainlab-node.json"
	AppGenesisPath        = "config/chainlab-genesis.json"
	AppDataPath           = "data/chainlab-app"
	maxNodeDocumentBytes  = 64 * 1024
	maxCometGenesisBytes  = chainabci.MaxGenesisDocumentBytes + 1024*1024
	maxPrivateKeyBytes    = 16 * 1024
	maxLastSignStateBytes = 1024 * 1024
	maxNodeKeyBytes       = 16 * 1024
)

type NodeRole string

const (
	RoleValidator NodeRole = "validator"
	RoleObserver  NodeRole = "observer"
)

type NodeDocument struct {
	Protocol           string   `json:"protocol"`
	Role               NodeRole `json:"role"`
	ChainID            string   `json:"chain_id"`
	Moniker            string   `json:"moniker"`
	ProxyApp           string   `json:"proxy_app"`
	RPCListenAddress   string   `json:"rpc_listen_address"`
	P2PListenAddress   string   `json:"p2p_listen_address"`
	PersistentPeers    string   `json:"persistent_peers"`
	ApplicationGenesis string   `json:"application_genesis"`
}

type RunOptions struct {
	StateSync *StateSyncOptions
	Consensus *ConsensusOptions
}

type ConsensusOptions struct {
	CreateEmptyBlocks         bool
	CreateEmptyBlocksInterval time.Duration
	TimeoutCommit             time.Duration
}

type StateSyncOptions struct {
	RPCServers  []string
	TrustHeight int64
	TrustHash   string
}

type managedDBProvider struct {
	txIndex dbm.DB
}

func (provider *managedDBProvider) open(context *cmtcfg.DBContext) (dbm.DB, error) {
	database, err := cmtcfg.DefaultDBProvider(context)
	if err != nil {
		return nil, err
	}
	if context.ID == "tx_index" {
		provider.txIndex = database
	}
	return database, nil
}

func (provider *managedDBProvider) close() error {
	if provider.txIndex == nil {
		return nil
	}
	err := provider.txIndex.Close()
	provider.txIndex = nil
	return err
}

func (document NodeDocument) CanonicalBytes() ([]byte, error) {
	if err := validateNodeDocument(document); err != nil {
		return nil, err
	}
	return hash.CanonicalBytes(document)
}

func LoadNodeDocument(home string) (NodeDocument, error) {
	if strings.TrimSpace(home) == "" {
		return NodeDocument{}, errors.New("CometBFT home is required")
	}
	raw, err := readRegularFile(filepath.Join(home, filepath.FromSlash(NodeDocumentPath)), maxNodeDocumentBytes, "node document")
	if err != nil {
		return NodeDocument{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document NodeDocument
	if err := decoder.Decode(&document); err != nil {
		return NodeDocument{}, fmt.Errorf("decode node document: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return NodeDocument{}, errors.New("node document must contain exactly one JSON value")
	}
	canonical, err := document.CanonicalBytes()
	if err != nil {
		return NodeDocument{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return NodeDocument{}, errors.New("node document is not canonically encoded")
	}
	return document, nil
}

func BuildConfig(home string, document NodeDocument) (*cmtcfg.Config, error) {
	if err := validateNodeDocument(document); err != nil {
		return nil, err
	}
	absoluteHome, err := filepath.Abs(home)
	if err != nil {
		return nil, fmt.Errorf("resolve CometBFT home: %w", err)
	}
	info, err := os.Stat(absoluteHome)
	if err != nil {
		return nil, fmt.Errorf("stat CometBFT home: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("CometBFT home must be a directory")
	}

	config := cmtcfg.DefaultConfig().SetRoot(absoluteHome)
	config.Moniker = document.Moniker
	config.ProxyApp = document.ProxyApp
	config.ABCI = "socket"
	config.RPC.ListenAddress = document.RPCListenAddress
	config.RPC.GRPCListenAddress = ""
	config.RPC.Unsafe = false
	config.RPC.MaxOpenConnections = 128
	config.RPC.MaxRequestBatchSize = 10
	config.RPC.MaxBodyBytes = 4 * 1024 * 1024
	config.P2P.ListenAddress = document.P2PListenAddress
	config.P2P.PersistentPeers = document.PersistentPeers
	config.P2P.AddrBookStrict = false
	config.P2P.AllowDuplicateIP = true
	config.P2P.PexReactor = false
	config.P2P.MaxNumInboundPeers = 16
	config.P2P.MaxNumOutboundPeers = 16
	config.Mempool.Type = cmtcfg.MempoolTypeFlood
	config.Mempool.Recheck = true
	config.Mempool.Broadcast = true
	config.Mempool.Size = chaintypes.MaxTransactionsPerBlock
	config.Mempool.MaxTxsBytes = 128 * 1024 * 1024
	config.Mempool.CacheSize = 2 * chaintypes.MaxTransactionsPerBlock
	config.Mempool.MaxTxBytes = chaintypes.MaxTransactionBytes
	config.Consensus.TimeoutPropose = 500 * time.Millisecond
	config.Consensus.TimeoutProposeDelta = 100 * time.Millisecond
	config.Consensus.TimeoutPrevote = 200 * time.Millisecond
	config.Consensus.TimeoutPrevoteDelta = 100 * time.Millisecond
	config.Consensus.TimeoutPrecommit = 200 * time.Millisecond
	config.Consensus.TimeoutPrecommitDelta = 100 * time.Millisecond
	config.Consensus.TimeoutCommit = 750 * time.Millisecond
	config.Consensus.SkipTimeoutCommit = false
	config.Consensus.CreateEmptyBlocks = true
	config.Consensus.CreateEmptyBlocksInterval = 0
	config.Consensus.DoubleSignCheckHeight = 0
	config.StateSync.Enable = false
	config.Storage.DiscardABCIResponses = false
	if err := config.ValidateBasic(); err != nil {
		return nil, fmt.Errorf("validate CometBFT config: %w", err)
	}
	if err := validateNodeFiles(config, document); err != nil {
		return nil, err
	}
	return config, nil
}

func Run(ctx context.Context, home string, out io.Writer) error {
	return RunWithOptions(ctx, home, out, RunOptions{})
}

func RunWithOptions(ctx context.Context, home string, out io.Writer, options RunOptions) error {
	if ctx == nil {
		return errors.New("node context is required")
	}
	if out == nil {
		return errors.New("node log writer is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	document, err := LoadNodeDocument(home)
	if err != nil {
		return err
	}
	config, err := BuildConfig(home, document)
	if err != nil {
		return err
	}
	if options.StateSync != nil {
		if err := applyStateSyncOptions(config, *options.StateSync); err != nil {
			return err
		}
	}
	if options.Consensus != nil {
		if err := applyConsensusOptions(config, *options.Consensus); err != nil {
			return err
		}
	}
	baseLogger := cmtlog.NewTMLogger(cmtlog.NewSyncWriter(out))
	logger, err := cmtflags.ParseLogLevel(config.LogLevel, baseLogger, cmtcfg.DefaultLogLevel)
	if err != nil {
		return fmt.Errorf("parse CometBFT log level: %w", err)
	}
	databaseProvider := &managedDBProvider{}
	var cometNode *node.Node
	if document.Role == RoleObserver {
		cometNode, err = newObserverNode(config, logger, databaseProvider.open)
	} else {
		cometNode, err = newValidatorNode(config, logger, databaseProvider.open)
	}
	if err != nil {
		return closeManagedDatabase(databaseProvider, fmt.Errorf("create CometBFT node: %w", err))
	}
	if err := cometNode.Start(); err != nil {
		return closeManagedDatabase(databaseProvider, fmt.Errorf("start CometBFT node: %w", err))
	}
	select {
	case <-ctx.Done():
		if err := cometNode.Stop(); err != nil {
			return closeManagedDatabase(databaseProvider, fmt.Errorf("stop CometBFT node: %w", err))
		}
		<-cometNode.Quit()
		return closeManagedDatabase(databaseProvider, nil)
	case <-cometNode.Quit():
		return closeManagedDatabase(databaseProvider, errors.New("CometBFT node stopped unexpectedly"))
	}
}

func closeManagedDatabase(provider *managedDBProvider, runErr error) error {
	if err := provider.close(); err != nil {
		closeErr := fmt.Errorf("close CometBFT transaction index: %w", err)
		if runErr != nil {
			return errors.Join(runErr, closeErr)
		}
		return closeErr
	}
	return runErr
}

func newValidatorNode(config *cmtcfg.Config, logger cmtlog.Logger, databaseProvider cmtcfg.DBProvider) (*node.Node, error) {
	nodeKey, err := p2p.LoadOrGenNodeKey(config.NodeKeyFile())
	if err != nil {
		return nil, fmt.Errorf("load or generate node key: %w", err)
	}
	return node.NewNode(
		config,
		privval.LoadOrGenFilePV(config.PrivValidatorKeyFile(), config.PrivValidatorStateFile()),
		nodeKey,
		proxy.DefaultClientCreator(config.ProxyApp, config.ABCI, config.DBDir()),
		node.DefaultGenesisDocProviderFunc(config),
		databaseProvider,
		node.DefaultMetricsProvider(config.Instrumentation),
		logger,
	)
}

type observerPrivValidator struct {
	publicKey cmtcrypto.PubKey
}

func (validator observerPrivValidator) GetPubKey() (cmtcrypto.PubKey, error) {
	return validator.publicKey, nil
}

func (observerPrivValidator) SignVote(string, *cmtproto.Vote) error {
	return errors.New("observer nodes cannot sign votes")
}

func (observerPrivValidator) SignProposal(string, *cmtproto.Proposal) error {
	return errors.New("observer nodes cannot sign proposals")
}

func newObserverNode(config *cmtcfg.Config, logger cmtlog.Logger, databaseProvider cmtcfg.DBProvider) (*node.Node, error) {
	nodeKey, err := p2p.LoadNodeKey(config.NodeKeyFile())
	if err != nil {
		return nil, fmt.Errorf("load observer P2P node key: %w", err)
	}
	return node.NewNode(
		config,
		observerPrivValidator{publicKey: nodeKey.PrivKey.PubKey()},
		nodeKey,
		proxy.DefaultClientCreator(config.ProxyApp, config.ABCI, config.DBDir()),
		node.DefaultGenesisDocProviderFunc(config),
		databaseProvider,
		node.DefaultMetricsProvider(config.Instrumentation),
		logger,
	)
}

func applyConsensusOptions(config *cmtcfg.Config, options ConsensusOptions) error {
	if options.CreateEmptyBlocksInterval < 0 {
		return errors.New("empty-block interval must not be negative")
	}
	if options.CreateEmptyBlocks && options.CreateEmptyBlocksInterval == 0 {
		return errors.New("managed empty blocks require a positive interval")
	}
	if options.TimeoutCommit <= 0 {
		return errors.New("managed commit timeout must be positive")
	}
	config.Consensus.CreateEmptyBlocks = options.CreateEmptyBlocks
	config.Consensus.CreateEmptyBlocksInterval = options.CreateEmptyBlocksInterval
	config.Consensus.TimeoutCommit = options.TimeoutCommit
	if err := config.ValidateBasic(); err != nil {
		return fmt.Errorf("validate managed consensus options: %w", err)
	}
	return nil
}

func applyStateSyncOptions(config *cmtcfg.Config, options StateSyncOptions) error {
	if len(options.RPCServers) != 2 {
		return errors.New("state sync requires exactly two RPC servers")
	}
	for index, server := range options.RPCServers {
		if !strings.HasPrefix(server, "http://") && !strings.HasPrefix(server, "https://") {
			return fmt.Errorf("state sync RPC server %d must use http or https", index)
		}
	}
	if options.TrustHeight <= 0 {
		return errors.New("state sync trust height must be positive")
	}
	trustHash := strings.ToUpper(strings.TrimSpace(options.TrustHash))
	decodedHash, err := hex.DecodeString(trustHash)
	if err != nil || len(decodedHash) != 32 {
		return errors.New("state sync trust hash must be a 32-byte hex value")
	}
	config.StateSync.Enable = true
	config.StateSync.RPCServers = append([]string(nil), options.RPCServers...)
	config.StateSync.TrustHeight = options.TrustHeight
	config.StateSync.TrustHash = trustHash
	config.StateSync.TrustPeriod = 7 * 24 * time.Hour
	config.StateSync.DiscoveryTime = 5 * time.Second
	config.StateSync.ChunkRequestTimeout = 10 * time.Second
	config.StateSync.ChunkFetchers = 4
	config.StateSync.MaxSnapshotChunks = 512
	config.StateSync.TempDir = filepath.Join(config.RootDir, "data", "state-sync")
	if err := config.ValidateBasic(); err != nil {
		return fmt.Errorf("validate state-sync CometBFT config: %w", err)
	}
	return nil
}

func validateNodeDocument(document NodeDocument) error {
	if document.Protocol != NodeProtocol {
		return fmt.Errorf("node protocol must be %q", NodeProtocol)
	}
	if document.Role != RoleValidator && document.Role != RoleObserver {
		return errors.New("node role must be validator or observer")
	}
	if err := chaintypes.ValidateChainID(document.ChainID); err != nil {
		return fmt.Errorf("invalid chain id: %w", err)
	}
	if len(document.ChainID) > cmttypes.MaxChainIDLen {
		return fmt.Errorf("chain id exceeds CometBFT limit %d", cmttypes.MaxChainIDLen)
	}
	if document.Moniker == "" || document.Moniker != strings.TrimSpace(document.Moniker) {
		return errors.New("node moniker must be canonical and non-empty")
	}
	for name, address := range map[string]string{
		"proxy app":  document.ProxyApp,
		"RPC listen": document.RPCListenAddress,
		"P2P listen": document.P2PListenAddress,
	} {
		if err := validateTCPAddress(address); err != nil {
			return fmt.Errorf("invalid %s address: %w", name, err)
		}
	}
	for _, peer := range strings.Split(document.PersistentPeers, ",") {
		if peer == "" {
			continue
		}
		if _, err := p2p.NewNetAddressString(peer); err != nil {
			return fmt.Errorf("invalid persistent peer %q: %w", peer, err)
		}
	}
	if document.ApplicationGenesis == "" || filepath.IsAbs(document.ApplicationGenesis) {
		return errors.New("application genesis must be a relative path")
	}
	cleanGenesis := filepath.Clean(filepath.FromSlash(document.ApplicationGenesis))
	if cleanGenesis == "." || cleanGenesis == ".." || strings.HasPrefix(cleanGenesis, ".."+string(filepath.Separator)) {
		return errors.New("application genesis escapes the CometBFT home")
	}
	if filepath.ToSlash(cleanGenesis) != document.ApplicationGenesis {
		return errors.New("application genesis path is not canonical")
	}
	return nil
}

func validateTCPAddress(address string) error {
	if !strings.HasPrefix(address, "tcp://") {
		return errors.New("address must use tcp://")
	}
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(address, "tcp://"))
	if err != nil || host == "" {
		return errors.New("address must contain a host and port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

func validateNodeFiles(config *cmtcfg.Config, document NodeDocument) error {
	genesisRaw, err := readRegularFile(config.GenesisFile(), maxCometGenesisBytes, "CometBFT genesis")
	if err != nil {
		return err
	}
	genesis, err := cmttypes.GenesisDocFromJSON(genesisRaw)
	if err != nil {
		return fmt.Errorf("decode CometBFT genesis: %w", err)
	}
	if genesis.ChainID != document.ChainID {
		return fmt.Errorf("CometBFT genesis chain id %q does not match node document %q", genesis.ChainID, document.ChainID)
	}
	applicationGenesis, err := chainabci.DecodeGenesisDocument(genesis.AppState)
	if err != nil {
		return fmt.Errorf("CometBFT app state: %w", err)
	}
	if applicationGenesis.ChainID != document.ChainID {
		return errors.New("application genesis chain id does not match node document")
	}
	protoParams := genesis.ConsensusParams.ToProto()
	if err := chainabci.ValidateGenesisConsensusParams(&protoParams, applicationGenesis); err != nil {
		return fmt.Errorf("CometBFT consensus params: %w", err)
	}
	appGenesisPath := filepath.Join(config.RootDir, filepath.FromSlash(document.ApplicationGenesis))
	fileGenesis, err := chainabci.LoadGenesisDocument(appGenesisPath)
	if err != nil {
		return err
	}
	appBytes, err := fileGenesis.CanonicalBytes()
	if err != nil {
		return err
	}
	embeddedBytes, err := applicationGenesis.CanonicalBytes()
	if err != nil {
		return err
	}
	if !bytes.Equal(appBytes, embeddedBytes) {
		return errors.New("application genesis file does not match CometBFT app state")
	}

	if document.Role == RoleObserver {
		for _, path := range []string{config.PrivValidatorKeyFile(), config.PrivValidatorStateFile()} {
			if _, err := os.Lstat(path); err == nil {
				return errors.New("observer home must not contain private validator key or signing state")
			} else if !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("inspect observer validator material: %w", err)
			}
		}
		return validateP2PNodeKey(config)
	}
	keyRaw, err := readRegularFile(config.PrivValidatorKeyFile(), maxPrivateKeyBytes, "private validator key")
	if err != nil {
		return err
	}
	var key privval.FilePVKey
	if err := cmtjson.Unmarshal(keyRaw, &key); err != nil {
		return fmt.Errorf("decode private validator key: %w", err)
	}
	if key.PrivKey == nil || key.PrivKey.Type() != cmtsecp256k1.KeyType {
		return errors.New("private validator key must use secp256k1")
	}
	derivedPublicKey := key.PrivKey.PubKey()
	if key.PubKey == nil || !bytes.Equal(key.PubKey.Bytes(), derivedPublicKey.Bytes()) || !bytes.Equal(key.Address, derivedPublicKey.Address()) {
		return errors.New("private validator key has inconsistent public identity")
	}
	foundValidator := false
	for _, validator := range genesis.Validators {
		if bytes.Equal(validator.Address, derivedPublicKey.Address()) && bytes.Equal(validator.PubKey.Bytes(), derivedPublicKey.Bytes()) && validator.Power == 1 {
			foundValidator = true
			break
		}
	}
	if !foundValidator {
		return errors.New("private validator key is not an equal-power genesis validator")
	}

	stateRaw, err := readRegularFile(config.PrivValidatorStateFile(), maxLastSignStateBytes, "private validator state")
	if err != nil {
		return err
	}
	var signState privval.FilePVLastSignState
	if err := cmtjson.Unmarshal(stateRaw, &signState); err != nil {
		return fmt.Errorf("decode private validator state: %w", err)
	}
	if signState.Height < 0 || signState.Round < 0 || signState.Step < 0 || signState.Step > 3 {
		return errors.New("private validator state has an invalid height/round/step")
	}
	return validateP2PNodeKey(config)
}

func validateP2PNodeKey(config *cmtcfg.Config) error {
	if _, err := readRegularFile(config.NodeKeyFile(), maxNodeKeyBytes, "P2P node key"); err != nil {
		return err
	}
	if _, err := p2p.LoadNodeKey(config.NodeKeyFile()); err != nil {
		return fmt.Errorf("decode P2P node key: %w", err)
	}
	return nil
}

func readRegularFile(path string, maximum int64, label string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", label, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", label, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", label)
	}
	if info.Size() <= 0 {
		return nil, fmt.Errorf("%s is empty", label)
	}
	if info.Size() > maximum {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maximum)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	if int64(len(raw)) > maximum {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, maximum)
	}
	return raw, nil
}
