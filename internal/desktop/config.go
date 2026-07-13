package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	chainabci "chainlab/internal/abci"
	"chainlab/internal/cometnode"
	"chainlab/internal/hash"
	chaintypes "chainlab/internal/types"
)

const (
	ConfigProtocol = "chainlab-desktop-config-v1"
	ConfigPath     = "desktop.json"

	ProfileDesktopSolo   Profile = "desktop-solo"
	ProfileHomeValidator Profile = "home-validator"
	ProfileObserver      Profile = "observer"

	defaultRPCPort      = 26670
	defaultP2PPort      = 26680
	defaultABCIPort     = 26658
	defaultRetainBlocks = 120_961
	maxConfigBytes      = 64 * 1024
)

type Profile string

type ProfileContract struct {
	Profile            Profile                  `json:"profile"`
	Validator          bool                     `json:"validator"`
	ValidatorCount     int                      `json:"validator_count"`
	RPCPublic          bool                     `json:"rpc_public"`
	P2PPublic          bool                     `json:"p2p_public"`
	ApplicationStorage chainabci.StorageProfile `json:"application_storage"`
	CometRetainBlocks  uint64                   `json:"comet_retain_blocks"`
	CreateEmptyBlocks  bool                     `json:"create_empty_blocks"`
	BlockIntervalSecs  uint64                   `json:"block_interval_seconds"`
}

type Config struct {
	Protocol        string          `json:"protocol"`
	ChainID         string          `json:"chain_id"`
	Profile         Profile         `json:"profile"`
	Contract        ProfileContract `json:"contract"`
	Network         string          `json:"network"`
	NodeHome        string          `json:"node_home"`
	ApplicationData string          `json:"application_data"`
	ABCIAddress     string          `json:"abci_address"`
	RPCAddress      string          `json:"rpc_address"`
	P2PAddress      string          `json:"p2p_address"`
	ControlURL      string          `json:"control_url"`
	ExplorerURL     string          `json:"explorer_url"`
}

func Contract(profile Profile) (ProfileContract, error) {
	base := ProfileContract{
		Profile: profile,
		ApplicationStorage: chainabci.StorageProfile{
			Mode: chainabci.StorageModeFull, RetainHeights: defaultRetainBlocks,
			CheckpointInterval: chainabci.DefaultCheckpointInterval,
		},
		CometRetainBlocks: defaultRetainBlocks,
		CreateEmptyBlocks: true,
		BlockIntervalSecs: 5,
	}
	switch profile {
	case ProfileDesktopSolo:
		base.Validator = true
		base.ValidatorCount = 1
	case ProfileHomeValidator:
		base.Validator = true
		base.ValidatorCount = 4
		base.P2PPublic = true
	case ProfileObserver:
		base.ValidatorCount = 4
		base.P2PPublic = true
	default:
		return ProfileContract{}, fmt.Errorf("unsupported desktop profile %q", profile)
	}
	return base, nil
}

func Initialize(root string, profile Profile, chainID string) (Config, error) {
	contract, err := Contract(profile)
	if err != nil {
		return Config{}, err
	}
	if profile != ProfileDesktopSolo {
		return Config{}, fmt.Errorf("profile %q requires a verified public invitation", profile)
	}
	if strings.TrimSpace(root) == "" {
		return Config{}, errors.New("desktop data directory is required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return Config{}, fmt.Errorf("resolve desktop data directory: %w", err)
	}
	if _, err := os.Lstat(absoluteRoot); err == nil {
		return Config{}, fmt.Errorf("desktop data directory already exists: %s", absoluteRoot)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("inspect desktop data directory: %w", err)
	}
	parent := filepath.Dir(absoluteRoot)
	if info, err := os.Stat(parent); err != nil || !info.IsDir() {
		return Config{}, errors.New("desktop data directory parent must exist")
	}
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(absoluteRoot)+"-stage-")
	if err != nil {
		return Config{}, fmt.Errorf("create desktop staging directory: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(stage)
		}
	}()

	policy := chainabci.DefaultValidatorPolicy()
	network, err := cometnode.InitializeNetwork(cometnode.NetworkConfig{
		OutputRoot:          filepath.Join(stage, "network"),
		ChainID:             chainID,
		ValidatorCount:      contract.ValidatorCount,
		ABCIBasePort:        defaultABCIPort,
		RPCBasePort:         defaultRPCPort,
		P2PBasePort:         defaultP2PPort,
		ApplicationProtocol: chainabci.ProtocolVersionV2,
		ValidatorPolicy:     &policy,
		ProtocolUpgrades: []chainabci.ProtocolUpgrade{
			{Height: 2, Protocol: chainabci.ProtocolVersionV3},
			{Height: 3, Protocol: chainabci.ProtocolVersionV4},
			{Height: 4, Protocol: chainabci.ProtocolVersionV5},
		},
	})
	if err != nil {
		return Config{}, err
	}
	node := network.Nodes[0]
	config := Config{
		Protocol:        ConfigProtocol,
		ChainID:         network.ChainID,
		Profile:         profile,
		Contract:        contract,
		Network:         "network/network.json",
		NodeHome:        filepath.ToSlash(filepath.Join("network", node.Home)),
		ApplicationData: filepath.ToSlash(filepath.Join("network", node.ApplicationData)),
		ABCIAddress:     node.ABCIListenAddress,
		RPCAddress:      node.RPCListenAddress,
		P2PAddress:      node.P2PListenAddress,
		ControlURL:      "http://127.0.0.1:26659",
		ExplorerURL:     "http://127.0.0.1:8547/",
	}
	raw, err := config.CanonicalBytes()
	if err != nil {
		return Config{}, err
	}
	if err := os.WriteFile(filepath.Join(stage, ConfigPath), raw, 0o600); err != nil {
		return Config{}, fmt.Errorf("write desktop config: %w", err)
	}
	if err := os.Rename(stage, absoluteRoot); err != nil {
		return Config{}, fmt.Errorf("publish desktop data directory: %w", err)
	}
	published = true
	return config, nil
}

func (config Config) CanonicalBytes() ([]byte, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return hash.CanonicalBytes(config)
}

func (config Config) Validate() error {
	if config.Protocol != ConfigProtocol {
		return fmt.Errorf("desktop config protocol must be %q", ConfigProtocol)
	}
	if err := chaintypes.ValidateChainID(config.ChainID); err != nil {
		return fmt.Errorf("invalid chain id: %w", err)
	}
	contract, err := Contract(config.Profile)
	if err != nil {
		return err
	}
	if config.Contract != contract {
		return errors.New("desktop profile contract does not match the selected profile")
	}
	for name, value := range map[string]string{
		"network": config.Network, "node home": config.NodeHome, "application data": config.ApplicationData,
	} {
		if value == "" || filepath.IsAbs(value) || filepath.ToSlash(filepath.Clean(filepath.FromSlash(value))) != value || value == ".." || strings.HasPrefix(value, "../") {
			return fmt.Errorf("%s path must be canonical and relative", name)
		}
	}
	if !strings.HasPrefix(config.ABCIAddress, "tcp://127.0.0.1:") || !strings.HasPrefix(config.RPCAddress, "tcp://127.0.0.1:") {
		return errors.New("ABCI and RPC addresses must bind to loopback")
	}
	if config.Profile == ProfileDesktopSolo && !strings.HasPrefix(config.P2PAddress, "tcp://127.0.0.1:") {
		return errors.New("desktop-solo P2P address must bind to loopback")
	}
	if _, err := loopbackHTTPAddress(config.ControlURL); err != nil {
		return fmt.Errorf("invalid desktop control URL: %w", err)
	}
	if _, err := loopbackHTTPAddress(config.ExplorerURL); err != nil {
		return fmt.Errorf("invalid desktop Explorer URL: %w", err)
	}
	return nil
}

func Load(root string) (Config, error) {
	raw, err := os.ReadFile(filepath.Join(root, ConfigPath))
	if err != nil {
		return Config{}, fmt.Errorf("read desktop config: %w", err)
	}
	if len(raw) == 0 || len(raw) > maxConfigBytes {
		return Config{}, fmt.Errorf("desktop config size must be between 1 and %d bytes", maxConfigBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode desktop config: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Config{}, errors.New("desktop config must contain exactly one JSON value")
	}
	canonical, err := config.CanonicalBytes()
	if err != nil {
		return Config{}, err
	}
	if !bytes.Equal(raw, canonical) {
		return Config{}, errors.New("desktop config is not canonically encoded")
	}
	return config, nil
}
