package cometnode

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	chainabci "chainlab/internal/abci"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/hash"
	"chainlab/internal/state"
	chaintypes "chainlab/internal/types"

	cmtsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/privval"
	cmttypes "github.com/cometbft/cometbft/types"
)

const (
	NetworkProtocol          = "chainlab-comet-network-v1"
	NetworkDocumentPath      = "network.json"
	DefaultValidatorCount    = 4
	DefaultValidatorBalance  = 1_000_000_000
	defaultPublicFileMode    = 0o644
	defaultDirectoryFileMode = 0o700
)

type NetworkConfig struct {
	OutputRoot          string
	ChainID             string
	ValidatorCount      int
	GenesisTime         time.Time
	ValidatorBalance    uint64
	BlockGasLimit       uint64
	ABCIBasePort        int
	RPCBasePort         int
	P2PBasePort         int
	ApplicationProtocol string
	ValidatorPolicy     *chainabci.ValidatorPolicy
	ProtocolUpgrades    []chainabci.ProtocolUpgrade
}

type NetworkDocument struct {
	Protocol    string        `json:"protocol"`
	ChainID     string        `json:"chain_id"`
	GenesisTime string        `json:"genesis_time"`
	Nodes       []NetworkNode `json:"nodes"`
}

type NetworkNode struct {
	Name               string `json:"name"`
	Home               string `json:"home"`
	ApplicationGenesis string `json:"application_genesis"`
	ApplicationData    string `json:"application_data"`
	ABCIListenAddress  string `json:"abci_listen_address"`
	RPCListenAddress   string `json:"rpc_listen_address"`
	P2PListenAddress   string `json:"p2p_listen_address"`
	NodeID             string `json:"node_id"`
	ConsensusAddress   string `json:"consensus_address"`
	ChainLabAddress    string `json:"chainlab_address"`
}

type generatedIdentity struct {
	name             string
	home             string
	nodeID           string
	consensusAddress string
	chainAddress     string
	publicKey        cmtsecp256k1.PubKey
}

func (document NetworkDocument) CanonicalBytes() ([]byte, error) {
	if document.Protocol != NetworkProtocol {
		return nil, fmt.Errorf("network protocol must be %q", NetworkProtocol)
	}
	if len(document.Nodes) == 0 {
		return nil, errors.New("network must contain nodes")
	}
	return hash.CanonicalBytes(document)
}

func InitializeNetwork(config NetworkConfig) (NetworkDocument, error) {
	config = withNetworkDefaults(config)
	root, err := validateNetworkConfig(config)
	if err != nil {
		return NetworkDocument{}, err
	}
	parent := filepath.Dir(root)
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(root)+"-stage-")
	if err != nil {
		return NetworkDocument{}, fmt.Errorf("create network staging directory: %w", err)
	}
	keepStage := false
	defer func() {
		if !keepStage {
			_ = os.RemoveAll(stage)
		}
	}()

	identities, err := generateIdentities(stage, config.ValidatorCount)
	if err != nil {
		return NetworkDocument{}, err
	}
	validators := make([]string, len(identities))
	genesisValidators := make([]cmttypes.GenesisValidator, len(identities))
	for index, identity := range identities {
		validators[index] = identity.chainAddress
		genesisValidators[index] = cmttypes.GenesisValidator{
			Address: identity.publicKey.Address(),
			PubKey:  identity.publicKey,
			Power:   1,
			Name:    identity.name,
		}
	}
	store := state.NewStore()
	if err := store.SetValidators(validators); err != nil {
		return NetworkDocument{}, err
	}
	for _, validator := range validators {
		store.SetBalance(validator, config.ValidatorBalance)
		if config.ApplicationProtocol == chainabci.ProtocolVersionV2 {
			if err := store.AddStake(validator, config.ValidatorBalance); err != nil {
				return NetworkDocument{}, err
			}
		}
	}
	var appGenesis chainabci.GenesisDocument
	if config.ApplicationProtocol == chainabci.ProtocolVersionV2 {
		appGenesis, err = chainabci.NewGenesisDocumentV2(
			config.ChainID, config.BlockGasLimit, store, *config.ValidatorPolicy,
		)
	} else {
		appGenesis, err = chainabci.NewGenesisDocument(config.ChainID, config.BlockGasLimit, store)
	}
	if err != nil {
		return NetworkDocument{}, err
	}
	appGenesis.Upgrades = append([]chainabci.ProtocolUpgrade(nil), config.ProtocolUpgrades...)
	appGenesisBytes, err := appGenesis.CanonicalBytes()
	if err != nil {
		return NetworkDocument{}, err
	}
	consensusParams := cmttypes.DefaultConsensusParams()
	consensusParams.Block.MaxBytes = chaintypes.MaxBlockBytes
	consensusParams.Block.MaxGas = int64(config.BlockGasLimit)
	consensusParams.Evidence.MaxBytes = 1024 * 1024
	consensusParams.Validator.PubKeyTypes = []string{cmtsecp256k1.KeyType}
	consensusParams.Version.App = chainabci.AppVersion
	if appGenesis.Protocol == chainabci.ProtocolVersionV2 {
		consensusParams.Version.App = chainabci.AppVersionV2
		consensusParams.Evidence.MaxAgeNumBlocks = appGenesis.ValidatorPolicy.EvidenceMaxAgeNumBlocks
		consensusParams.Evidence.MaxAgeDuration = time.Duration(appGenesis.ValidatorPolicy.EvidenceMaxAgeDurationNanos)
	}
	consensusParams.ABCI.VoteExtensionsEnableHeight = 0
	cometGenesis := &cmttypes.GenesisDoc{
		GenesisTime:     config.GenesisTime,
		ChainID:         config.ChainID,
		InitialHeight:   1,
		ConsensusParams: consensusParams,
		Validators:      genesisValidators,
		AppState:        json.RawMessage(appGenesisBytes),
	}
	if err := cometGenesis.ValidateAndComplete(); err != nil {
		return NetworkDocument{}, fmt.Errorf("validate CometBFT genesis: %w", err)
	}

	document := NetworkDocument{
		Protocol:    NetworkProtocol,
		ChainID:     config.ChainID,
		GenesisTime: config.GenesisTime.Format(time.RFC3339Nano),
		Nodes:       make([]NetworkNode, len(identities)),
	}
	for index, identity := range identities {
		peers := persistentPeers(identities, index, config.P2PBasePort)
		nodeDocument := NodeDocument{
			Protocol:           NodeProtocol,
			Role:               RoleValidator,
			ChainID:            config.ChainID,
			Moniker:            identity.name,
			ProxyApp:           tcpAddress(config.ABCIBasePort + index),
			RPCListenAddress:   tcpAddress(config.RPCBasePort + index),
			P2PListenAddress:   tcpAddress(config.P2PBasePort + index),
			PersistentPeers:    strings.Join(peers, ","),
			ApplicationGenesis: AppGenesisPath,
		}
		nodeBytes, err := nodeDocument.CanonicalBytes()
		if err != nil {
			return NetworkDocument{}, err
		}
		if err := writeExclusiveFile(filepath.Join(identity.home, filepath.FromSlash(NodeDocumentPath)), nodeBytes, defaultPublicFileMode); err != nil {
			return NetworkDocument{}, err
		}
		if err := writeExclusiveFile(filepath.Join(identity.home, filepath.FromSlash(AppGenesisPath)), appGenesisBytes, defaultPublicFileMode); err != nil {
			return NetworkDocument{}, err
		}
		if err := cometGenesis.SaveAs(filepath.Join(identity.home, "config", "genesis.json")); err != nil {
			return NetworkDocument{}, fmt.Errorf("write %s CometBFT genesis: %w", identity.name, err)
		}
		document.Nodes[index] = NetworkNode{
			Name:               identity.name,
			Home:               identity.name,
			ApplicationGenesis: filepath.ToSlash(filepath.Join(identity.name, filepath.FromSlash(AppGenesisPath))),
			ApplicationData:    filepath.ToSlash(filepath.Join(identity.name, filepath.FromSlash(AppDataPath))),
			ABCIListenAddress:  nodeDocument.ProxyApp,
			RPCListenAddress:   nodeDocument.RPCListenAddress,
			P2PListenAddress:   nodeDocument.P2PListenAddress,
			NodeID:             identity.nodeID,
			ConsensusAddress:   identity.consensusAddress,
			ChainLabAddress:    identity.chainAddress,
		}
	}
	networkBytes, err := document.CanonicalBytes()
	if err != nil {
		return NetworkDocument{}, err
	}
	if err := writeExclusiveFile(filepath.Join(stage, NetworkDocumentPath), networkBytes, defaultPublicFileMode); err != nil {
		return NetworkDocument{}, err
	}
	for _, identity := range identities {
		nodeDocument, err := LoadNodeDocument(identity.home)
		if err != nil {
			return NetworkDocument{}, err
		}
		if _, err := BuildConfig(identity.home, nodeDocument); err != nil {
			return NetworkDocument{}, fmt.Errorf("validate generated %s: %w", identity.name, err)
		}
	}
	if err := os.Rename(stage, root); err != nil {
		return NetworkDocument{}, fmt.Errorf("publish network directory: %w", err)
	}
	keepStage = true
	return document, nil
}

func withNetworkDefaults(config NetworkConfig) NetworkConfig {
	if config.ValidatorCount == 0 {
		config.ValidatorCount = DefaultValidatorCount
	}
	if config.ValidatorBalance == 0 {
		config.ValidatorBalance = DefaultValidatorBalance
	}
	if config.BlockGasLimit == 0 {
		config.BlockGasLimit = chaintypes.DefaultBlockGasLimit
	}
	if config.ApplicationProtocol == "" {
		config.ApplicationProtocol = chainabci.ProtocolVersion
	}
	if config.GenesisTime.IsZero() {
		config.GenesisTime = time.Now().UTC()
	} else {
		config.GenesisTime = config.GenesisTime.UTC()
	}
	return config
}

func validateNetworkConfig(config NetworkConfig) (string, error) {
	if strings.TrimSpace(config.OutputRoot) == "" {
		return "", errors.New("network output root is required")
	}
	root, err := filepath.Abs(config.OutputRoot)
	if err != nil {
		return "", fmt.Errorf("resolve network output root: %w", err)
	}
	if _, err := os.Lstat(root); err == nil {
		return "", fmt.Errorf("network output root already exists: %s", root)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect network output root: %w", err)
	}
	parentInfo, err := os.Stat(filepath.Dir(root))
	if err != nil || !parentInfo.IsDir() {
		return "", errors.New("network output parent must be an existing directory")
	}
	if err := chaintypes.ValidateChainID(config.ChainID); err != nil {
		return "", fmt.Errorf("invalid chain id: %w", err)
	}
	if len(config.ChainID) > cmttypes.MaxChainIDLen {
		return "", fmt.Errorf("chain id exceeds CometBFT limit %d", cmttypes.MaxChainIDLen)
	}
	if config.ValidatorCount < 1 || config.ValidatorCount > chaintypes.MaxValidators {
		return "", fmt.Errorf("validator count must be between 1 and %d", chaintypes.MaxValidators)
	}
	if config.BlockGasLimit == 0 || config.BlockGasLimit > math.MaxInt64 {
		return "", errors.New("block gas limit must fit a positive int64")
	}
	switch config.ApplicationProtocol {
	case chainabci.ProtocolVersion:
		if config.ValidatorPolicy != nil {
			return "", errors.New("protocol version 1 must not define a validator policy")
		}
		if len(config.ProtocolUpgrades) != 0 {
			return "", errors.New("protocol version 1 must not define protocol upgrades")
		}
	case chainabci.ProtocolVersionV2:
		if config.ValidatorPolicy == nil {
			return "", errors.New("protocol version 2 requires a validator policy")
		}
	default:
		return "", errors.New("application protocol is unsupported")
	}
	if config.GenesisTime.IsZero() {
		return "", errors.New("genesis time is required")
	}
	ports := make(map[int]string, 3*config.ValidatorCount)
	for name, base := range map[string]int{
		"ABCI": config.ABCIBasePort,
		"RPC":  config.RPCBasePort,
		"P2P":  config.P2PBasePort,
	} {
		if base < 1 || base > 65535-config.ValidatorCount+1 {
			return "", fmt.Errorf("%s port range is invalid", name)
		}
		for offset := 0; offset < config.ValidatorCount; offset++ {
			port := base + offset
			if previous, exists := ports[port]; exists {
				return "", fmt.Errorf("%s and %s port ranges overlap at %d", previous, name, port)
			}
			ports[port] = name
		}
	}
	return root, nil
}

func generateIdentities(stage string, count int) ([]generatedIdentity, error) {
	identities := make([]generatedIdentity, count)
	for index := 0; index < count; index++ {
		name := fmt.Sprintf("node%d", index)
		home := filepath.Join(stage, name)
		if err := os.MkdirAll(filepath.Join(home, "config"), defaultDirectoryFileMode); err != nil {
			return nil, fmt.Errorf("create %s config directory: %w", name, err)
		}
		if err := os.MkdirAll(filepath.Join(home, "data"), defaultDirectoryFileMode); err != nil {
			return nil, fmt.Errorf("create %s data directory: %w", name, err)
		}
		privateKey := cmtsecp256k1.GenPrivKey()
		privateValidator := privval.NewFilePV(
			privateKey,
			filepath.Join(home, "config", "priv_validator_key.json"),
			filepath.Join(home, "data", "priv_validator_state.json"),
		)
		if err := savePrivateValidator(privateValidator); err != nil {
			return nil, fmt.Errorf("save %s private validator: %w", name, err)
		}
		nodeKey, err := p2p.LoadOrGenNodeKey(filepath.Join(home, "config", "node_key.json"))
		if err != nil {
			return nil, fmt.Errorf("save %s P2P node key: %w", name, err)
		}
		publicKey, ok := privateKey.PubKey().(cmtsecp256k1.PubKey)
		if !ok {
			return nil, errors.New("generated validator key is not secp256k1")
		}
		chainAddress, err := chaincrypto.AddressFromCompressedPublicKey(publicKey.Bytes())
		if err != nil {
			return nil, err
		}
		identities[index] = generatedIdentity{
			name:             name,
			home:             home,
			nodeID:           string(nodeKey.ID()),
			consensusAddress: strings.ToLower(publicKey.Address().String()),
			chainAddress:     chainAddress,
			publicKey:        publicKey,
		}
	}
	return identities, nil
}

func savePrivateValidator(privateValidator *privval.FilePV) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("CometBFT private validator save panic: %v", recovered)
		}
	}()
	privateValidator.Save()
	return nil
}

func persistentPeers(identities []generatedIdentity, self int, basePort int) []string {
	peers := make([]string, 0, len(identities)-1)
	for index, identity := range identities {
		if index == self {
			continue
		}
		peers = append(peers, fmt.Sprintf("%s@127.0.0.1:%d", identity.nodeID, basePort+index))
	}
	sort.Strings(peers)
	return peers
}

func tcpAddress(port int) string {
	return fmt.Sprintf("tcp://127.0.0.1:%d", port)
}

func writeExclusiveFile(path string, raw []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}
