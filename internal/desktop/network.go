package desktop

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	chainabci "chainlab/internal/abci"
	"chainlab/internal/cometnode"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/hash"
	"chainlab/internal/state"
	chaintypes "chainlab/internal/types"

	cmtsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	"github.com/cometbft/cometbft/p2p"
	"github.com/cometbft/cometbft/privval"
	cmttypes "github.com/cometbft/cometbft/types"
)

const (
	IdentityProtocol   = "chainlab-public-identity-v1"
	InvitationProtocol = "chainlab-public-invitation-v1"
	IdentityPath       = "identity.json"
	InvitationPath     = "invitation.json"
	identityHomePath   = "identity"
	maxIdentityBytes   = 64 * 1024
	maxInvitationBytes = 4 * 1024 * 1024
)

type PublicIdentity struct {
	Protocol           string             `json:"protocol"`
	Role               cometnode.NodeRole `json:"role"`
	Name               string             `json:"name"`
	NodeID             string             `json:"node_id"`
	P2PAddress         string             `json:"p2p_address"`
	ConsensusPublicKey string             `json:"consensus_public_key,omitempty"`
	ConsensusAddress   string             `json:"consensus_address,omitempty"`
	ChainLabAddress    string             `json:"chainlab_address,omitempty"`
	Checksum           string             `json:"checksum"`
}

type publicIdentityPayload struct {
	Protocol           string             `json:"protocol"`
	Role               cometnode.NodeRole `json:"role"`
	Name               string             `json:"name"`
	NodeID             string             `json:"node_id"`
	P2PAddress         string             `json:"p2p_address"`
	ConsensusPublicKey string             `json:"consensus_public_key,omitempty"`
	ConsensusAddress   string             `json:"consensus_address,omitempty"`
	ChainLabAddress    string             `json:"chainlab_address,omitempty"`
}

type Invitation struct {
	Protocol string            `json:"protocol"`
	Payload  InvitationPayload `json:"payload"`
	Checksum string            `json:"checksum"`
}

type InvitationPayload struct {
	Profile                  Profile          `json:"profile"`
	ChainID                  string           `json:"chain_id"`
	GenesisTime              string           `json:"genesis_time"`
	NetworkName              string           `json:"network_name"`
	ApplicationGenesisBase64 string           `json:"application_genesis_base64"`
	CometGenesisBase64       string           `json:"comet_genesis_base64"`
	GenesisSHA256            string           `json:"genesis_sha256"`
	Validators               []PublicIdentity `json:"validators"`
}

type LocalPorts struct {
	ABCI     int
	RPC      int
	Control  int
	Explorer int
}

func DefaultLocalPorts() LocalPorts {
	return LocalPorts{ABCI: 26658, RPC: 26670, Control: 26659, Explorer: 8547}
}

func GenerateIdentity(root string, role cometnode.NodeRole, name string, p2pAddress string) (PublicIdentity, error) {
	if role != cometnode.RoleValidator && role != cometnode.RoleObserver {
		return PublicIdentity{}, errors.New("identity role must be validator or observer")
	}
	if err := validateIdentityName(name); err != nil {
		return PublicIdentity{}, err
	}
	if err := validateAdvertisedAddress(p2pAddress); err != nil {
		return PublicIdentity{}, err
	}
	absoluteRoot, err := newAbsoluteDirectory(root, "identity")
	if err != nil {
		return PublicIdentity{}, err
	}
	parent := filepath.Dir(absoluteRoot)
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(absoluteRoot)+"-identity-")
	if err != nil {
		return PublicIdentity{}, fmt.Errorf("create identity staging directory: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(stage)
		}
	}()
	home := filepath.Join(stage, identityHomePath)
	if err := os.MkdirAll(filepath.Join(home, "config"), 0o700); err != nil {
		return PublicIdentity{}, err
	}
	if err := os.MkdirAll(filepath.Join(home, "data"), 0o700); err != nil {
		return PublicIdentity{}, err
	}
	nodeKey, err := p2p.LoadOrGenNodeKey(filepath.Join(home, "config", "node_key.json"))
	if err != nil {
		return PublicIdentity{}, fmt.Errorf("generate local P2P key: %w", err)
	}
	identity := PublicIdentity{
		Protocol: IdentityProtocol, Role: role, Name: name,
		NodeID: string(nodeKey.ID()), P2PAddress: p2pAddress,
	}
	if role == cometnode.RoleValidator {
		privateKey := cmtsecp256k1.GenPrivKey()
		privateValidator := privval.NewFilePV(
			privateKey,
			filepath.Join(home, "config", "priv_validator_key.json"),
			filepath.Join(home, "data", "priv_validator_state.json"),
		)
		if err := saveLocalPrivateValidator(privateValidator); err != nil {
			return PublicIdentity{}, err
		}
		publicKey := privateKey.PubKey()
		chainAddress, err := chaincrypto.AddressFromCompressedPublicKey(publicKey.Bytes())
		if err != nil {
			return PublicIdentity{}, err
		}
		identity.ConsensusPublicKey = hex.EncodeToString(publicKey.Bytes())
		identity.ConsensusAddress = strings.ToLower(publicKey.Address().String())
		identity.ChainLabAddress = chainAddress
	}
	identity.Checksum, err = identityChecksum(identity)
	if err != nil {
		return PublicIdentity{}, err
	}
	raw, err := identity.CanonicalBytes()
	if err != nil {
		return PublicIdentity{}, err
	}
	if err := writeNewFile(filepath.Join(stage, IdentityPath), raw, 0o644); err != nil {
		return PublicIdentity{}, err
	}
	if err := os.Rename(stage, absoluteRoot); err != nil {
		return PublicIdentity{}, fmt.Errorf("publish local identity: %w", err)
	}
	published = true
	return identity, nil
}

func LoadIdentity(path string) (PublicIdentity, error) {
	raw, err := readBoundedFile(path, maxIdentityBytes, "public identity")
	if err != nil {
		return PublicIdentity{}, err
	}
	var identity PublicIdentity
	if err := decodeStrictCanonical(raw, &identity); err != nil {
		return PublicIdentity{}, fmt.Errorf("decode public identity: %w", err)
	}
	if err := identity.Validate(); err != nil {
		return PublicIdentity{}, err
	}
	return identity, nil
}

func (identity PublicIdentity) CanonicalBytes() ([]byte, error) {
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	return hash.CanonicalBytes(identity)
}

func (identity PublicIdentity) Validate() error {
	if identity.Protocol != IdentityProtocol {
		return fmt.Errorf("identity protocol must be %q", IdentityProtocol)
	}
	if err := validateIdentityName(identity.Name); err != nil {
		return err
	}
	if _, err := p2p.NewNetAddressString(identity.NodeID + "@" + identity.P2PAddress); err != nil {
		return fmt.Errorf("invalid identity P2P endpoint: %w", err)
	}
	if err := validateAdvertisedAddress(identity.P2PAddress); err != nil {
		return err
	}
	switch identity.Role {
	case cometnode.RoleValidator:
		publicBytes, err := hex.DecodeString(identity.ConsensusPublicKey)
		if err != nil || len(publicBytes) != cmtsecp256k1.PubKeySize {
			return errors.New("validator consensus public key must be compressed secp256k1 hex")
		}
		publicKey := cmtsecp256k1.PubKey(publicBytes)
		chainAddress, err := chaincrypto.AddressFromCompressedPublicKey(publicBytes)
		if err != nil {
			return err
		}
		if identity.ConsensusAddress != strings.ToLower(publicKey.Address().String()) || identity.ChainLabAddress != chainAddress {
			return errors.New("validator public identity addresses do not match its public key")
		}
	case cometnode.RoleObserver:
		if identity.ConsensusPublicKey != "" || identity.ConsensusAddress != "" || identity.ChainLabAddress != "" {
			return errors.New("observer identity must not contain validator public material")
		}
	default:
		return errors.New("identity role must be validator or observer")
	}
	want, err := identityChecksum(identity)
	if err != nil {
		return err
	}
	if identity.Checksum != want {
		return errors.New("public identity checksum does not match")
	}
	return nil
}

func CreateInvitation(path string, chainID string, networkName string, identities []PublicIdentity, genesisTime time.Time) (Invitation, error) {
	if len(identities) != 4 {
		return Invitation{}, errors.New("home-validator invitation requires exactly four validator identities")
	}
	if err := chaintypes.ValidateChainID(chainID); err != nil {
		return Invitation{}, fmt.Errorf("invalid chain id: %w", err)
	}
	if strings.TrimSpace(networkName) == "" || len(networkName) > 64 || networkName != strings.TrimSpace(networkName) {
		return Invitation{}, errors.New("network name must be canonical and between 1 and 64 bytes")
	}
	validators := append([]PublicIdentity(nil), identities...)
	sort.Slice(validators, func(left, right int) bool { return validators[left].Name < validators[right].Name })
	if err := validateValidatorIdentities(validators); err != nil {
		return Invitation{}, err
	}
	if genesisTime.IsZero() {
		genesisTime = time.Now().UTC()
	} else {
		genesisTime = genesisTime.UTC()
	}
	applicationGenesis, cometGenesis, err := invitationGenesis(chainID, validators, genesisTime)
	if err != nil {
		return Invitation{}, err
	}
	applicationRaw, err := applicationGenesis.CanonicalBytes()
	if err != nil {
		return Invitation{}, err
	}
	cometRaw, err := cmtjson.Marshal(cometGenesis)
	if err != nil {
		return Invitation{}, fmt.Errorf("encode CometBFT genesis: %w", err)
	}
	payload := InvitationPayload{
		Profile: ProfileHomeValidator, ChainID: chainID,
		GenesisTime: genesisTime.Format(time.RFC3339Nano), NetworkName: networkName,
		ApplicationGenesisBase64: base64.StdEncoding.EncodeToString(applicationRaw),
		CometGenesisBase64:       base64.StdEncoding.EncodeToString(cometRaw),
		GenesisSHA256:            sha256Hex(cometRaw), Validators: validators,
	}
	invitation := Invitation{Protocol: InvitationProtocol, Payload: payload}
	invitation.Checksum, err = invitationChecksum(payload)
	if err != nil {
		return Invitation{}, err
	}
	raw, err := invitation.CanonicalBytes()
	if err != nil {
		return Invitation{}, err
	}
	if err := writeNewFile(path, raw, 0o644); err != nil {
		return Invitation{}, err
	}
	return invitation, nil
}

func LoadInvitation(path string) (Invitation, error) {
	raw, err := readBoundedFile(path, maxInvitationBytes, "public invitation")
	if err != nil {
		return Invitation{}, err
	}
	var invitation Invitation
	if err := decodeStrictCanonical(raw, &invitation); err != nil {
		return Invitation{}, fmt.Errorf("decode public invitation: %w", err)
	}
	if err := invitation.Validate(); err != nil {
		return Invitation{}, err
	}
	return invitation, nil
}

func (invitation Invitation) CanonicalBytes() ([]byte, error) {
	if err := invitation.Validate(); err != nil {
		return nil, err
	}
	return hash.CanonicalBytes(invitation)
}

func (invitation Invitation) Validate() error {
	if invitation.Protocol != InvitationProtocol {
		return fmt.Errorf("invitation protocol must be %q", InvitationProtocol)
	}
	if invitation.Payload.Profile != ProfileHomeValidator {
		return errors.New("invitation profile must be home-validator")
	}
	if err := chaintypes.ValidateChainID(invitation.Payload.ChainID); err != nil {
		return fmt.Errorf("invalid invitation chain id: %w", err)
	}
	if len(invitation.Payload.Validators) != 4 {
		return errors.New("invitation must contain exactly four validators")
	}
	if err := validateValidatorIdentities(invitation.Payload.Validators); err != nil {
		return err
	}
	genesisTime, err := time.Parse(time.RFC3339Nano, invitation.Payload.GenesisTime)
	if err != nil || genesisTime.Location() != time.UTC {
		return errors.New("invitation genesis time must be canonical UTC RFC3339Nano")
	}
	applicationRaw, err := base64.StdEncoding.Strict().DecodeString(invitation.Payload.ApplicationGenesisBase64)
	if err != nil {
		return errors.New("invitation application genesis is not canonical base64")
	}
	applicationGenesis, err := chainabci.ParseGenesisDocument(applicationRaw)
	if err != nil || applicationGenesis.ChainID != invitation.Payload.ChainID {
		return errors.New("invitation application genesis is invalid or wrong-chain")
	}
	cometRaw, err := base64.StdEncoding.Strict().DecodeString(invitation.Payload.CometGenesisBase64)
	if err != nil || sha256Hex(cometRaw) != invitation.Payload.GenesisSHA256 {
		return errors.New("invitation CometBFT genesis checksum does not match")
	}
	cometGenesis, err := cmttypes.GenesisDocFromJSON(cometRaw)
	if err != nil || cometGenesis.ChainID != invitation.Payload.ChainID || !bytes.Equal(cometGenesis.AppState, applicationRaw) {
		return errors.New("invitation CometBFT genesis is invalid, wrong-chain, or has different application bytes")
	}
	if len(cometGenesis.Validators) != len(invitation.Payload.Validators) {
		return errors.New("invitation genesis validator count does not match identities")
	}
	for index, identity := range invitation.Payload.Validators {
		validator := cometGenesis.Validators[index]
		publicBytes, _ := hex.DecodeString(identity.ConsensusPublicKey)
		if validator.Name != identity.Name || validator.Power != 1 || !bytes.Equal(validator.PubKey.Bytes(), publicBytes) {
			return errors.New("invitation genesis validator set does not match public identities")
		}
	}
	want, err := invitationChecksum(invitation.Payload)
	if err != nil {
		return err
	}
	if invitation.Checksum != want {
		return errors.New("invitation checksum does not match")
	}
	return nil
}

func Join(root string, invitationPath string, ports LocalPorts) (Config, error) {
	if ports == (LocalPorts{}) {
		ports = DefaultLocalPorts()
	}
	if err := validateLocalPorts(ports); err != nil {
		return Config{}, err
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return Config{}, err
	}
	if _, err := os.Stat(filepath.Join(absoluteRoot, ConfigPath)); err == nil {
		return Config{}, errors.New("desktop data directory is already joined")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, err
	}
	identity, err := LoadIdentity(filepath.Join(absoluteRoot, IdentityPath))
	if err != nil {
		return Config{}, err
	}
	invitation, err := LoadInvitation(invitationPath)
	if err != nil {
		return Config{}, err
	}
	profile := ProfileObserver
	if identity.Role == cometnode.RoleValidator {
		profile = ProfileHomeValidator
		matched := false
		for _, validator := range invitation.Payload.Validators {
			if validator.NodeID == identity.NodeID && validator.ConsensusAddress == identity.ConsensusAddress {
				matched = validator == identity
				break
			}
		}
		if !matched {
			return Config{}, errors.New("local validator identity is not an exact member of the invitation")
		}
	}
	contract, err := Contract(profile)
	if err != nil {
		return Config{}, err
	}
	home := filepath.Join(absoluteRoot, identityHomePath)
	if err := validateLocalIdentitySecrets(home, identity); err != nil {
		return Config{}, err
	}
	persistentPeers := make([]string, 0, len(invitation.Payload.Validators))
	for _, validator := range invitation.Payload.Validators {
		if validator.NodeID != identity.NodeID {
			persistentPeers = append(persistentPeers, validator.NodeID+"@"+validator.P2PAddress)
		}
	}
	sort.Strings(persistentPeers)
	nodeDocument := cometnode.NodeDocument{
		Protocol: cometnode.NodeProtocol, Role: identity.Role,
		ChainID: invitation.Payload.ChainID, Moniker: identity.Name,
		ProxyApp:           "tcp://127.0.0.1:" + strconv.Itoa(ports.ABCI),
		RPCListenAddress:   "tcp://127.0.0.1:" + strconv.Itoa(ports.RPC),
		P2PListenAddress:   "tcp://" + identity.P2PAddress,
		PersistentPeers:    strings.Join(persistentPeers, ","),
		ApplicationGenesis: cometnode.AppGenesisPath,
	}
	nodeRaw, err := nodeDocument.CanonicalBytes()
	if err != nil {
		return Config{}, err
	}
	applicationRaw, _ := base64.StdEncoding.DecodeString(invitation.Payload.ApplicationGenesisBase64)
	cometRaw, _ := base64.StdEncoding.DecodeString(invitation.Payload.CometGenesisBase64)
	invitationRaw, err := invitation.CanonicalBytes()
	if err != nil {
		return Config{}, err
	}
	for _, file := range []struct {
		path string
		raw  []byte
		mode os.FileMode
	}{
		{filepath.Join(home, filepath.FromSlash(cometnode.NodeDocumentPath)), nodeRaw, 0o644},
		{filepath.Join(home, filepath.FromSlash(cometnode.AppGenesisPath)), applicationRaw, 0o644},
		{filepath.Join(home, "config", "genesis.json"), cometRaw, 0o644},
		{filepath.Join(absoluteRoot, InvitationPath), invitationRaw, 0o644},
	} {
		if err := writeExpectedFile(file.path, file.raw, file.mode); err != nil {
			return Config{}, err
		}
	}
	config := Config{
		Protocol: ConfigProtocol, ChainID: invitation.Payload.ChainID,
		Profile: profile, Contract: contract, Network: InvitationPath,
		NodeHome:        identityHomePath,
		ApplicationData: filepath.ToSlash(filepath.Join(identityHomePath, filepath.FromSlash(cometnode.AppDataPath))),
		ABCIAddress:     nodeDocument.ProxyApp, RPCAddress: nodeDocument.RPCListenAddress,
		P2PAddress:  nodeDocument.P2PListenAddress,
		ControlURL:  "http://127.0.0.1:" + strconv.Itoa(ports.Control),
		ExplorerURL: "http://127.0.0.1:" + strconv.Itoa(ports.Explorer) + "/",
	}
	configRaw, err := config.CanonicalBytes()
	if err != nil {
		return Config{}, err
	}
	if err := writeNewFile(filepath.Join(absoluteRoot, ConfigPath), configRaw, 0o600); err != nil {
		return Config{}, err
	}
	if _, err := cometnode.BuildConfig(home, nodeDocument); err != nil {
		_ = os.Remove(filepath.Join(absoluteRoot, ConfigPath))
		return Config{}, err
	}
	return config, nil
}

func invitationGenesis(chainID string, identities []PublicIdentity, genesisTime time.Time) (chainabci.GenesisDocument, *cmttypes.GenesisDoc, error) {
	validators := make([]string, len(identities))
	genesisValidators := make([]cmttypes.GenesisValidator, len(identities))
	store := state.NewStore()
	for index, identity := range identities {
		publicBytes, _ := hex.DecodeString(identity.ConsensusPublicKey)
		publicKey := cmtsecp256k1.PubKey(publicBytes)
		validators[index] = identity.ChainLabAddress
		genesisValidators[index] = cmttypes.GenesisValidator{Address: publicKey.Address(), PubKey: publicKey, Power: 1, Name: identity.Name}
	}
	if err := store.SetValidators(validators); err != nil {
		return chainabci.GenesisDocument{}, nil, err
	}
	for _, validator := range validators {
		store.SetBalance(validator, cometnode.DefaultValidatorBalance)
		if err := store.AddStake(validator, cometnode.DefaultValidatorBalance); err != nil {
			return chainabci.GenesisDocument{}, nil, err
		}
	}
	policy := chainabci.DefaultValidatorPolicy()
	application, err := chainabci.NewGenesisDocumentV2(chainID, chaintypes.DefaultBlockGasLimit, store, policy)
	if err != nil {
		return chainabci.GenesisDocument{}, nil, err
	}
	application.Upgrades = []chainabci.ProtocolUpgrade{
		{Height: 2, Protocol: chainabci.ProtocolVersionV3},
		{Height: 3, Protocol: chainabci.ProtocolVersionV4},
		{Height: 4, Protocol: chainabci.ProtocolVersionV5},
	}
	applicationRaw, err := application.CanonicalBytes()
	if err != nil {
		return chainabci.GenesisDocument{}, nil, err
	}
	params := cmttypes.DefaultConsensusParams()
	params.Block.MaxBytes = chaintypes.MaxBlockBytes
	params.Block.MaxGas = int64(chaintypes.DefaultBlockGasLimit)
	params.Evidence.MaxBytes = 1024 * 1024
	params.Evidence.MaxAgeNumBlocks = policy.EvidenceMaxAgeNumBlocks
	params.Evidence.MaxAgeDuration = time.Duration(policy.EvidenceMaxAgeDurationNanos)
	params.Validator.PubKeyTypes = []string{cmtsecp256k1.KeyType}
	params.Version.App = chainabci.AppVersionV2
	params.ABCI.VoteExtensionsEnableHeight = 0
	comet := &cmttypes.GenesisDoc{
		GenesisTime: genesisTime, ChainID: chainID, InitialHeight: 1,
		ConsensusParams: params, Validators: genesisValidators, AppState: json.RawMessage(applicationRaw),
	}
	if err := comet.ValidateAndComplete(); err != nil {
		return chainabci.GenesisDocument{}, nil, err
	}
	return application, comet, nil
}

func validateValidatorIdentities(identities []PublicIdentity) error {
	names := make(map[string]struct{}, len(identities))
	nodeIDs := make(map[string]struct{}, len(identities))
	consensus := make(map[string]struct{}, len(identities))
	chainAddresses := make(map[string]struct{}, len(identities))
	p2pAddresses := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		if err := identity.Validate(); err != nil {
			return err
		}
		if identity.Role != cometnode.RoleValidator {
			return errors.New("invitation identities must all be validators")
		}
		for value, values := range map[string]map[string]struct{}{
			identity.Name: names, identity.NodeID: nodeIDs, identity.ConsensusAddress: consensus,
			identity.ChainLabAddress: chainAddresses, identity.P2PAddress: p2pAddresses,
		} {
			if _, exists := values[value]; exists {
				return fmt.Errorf("duplicate invitation identity value %q", value)
			}
			values[value] = struct{}{}
		}
	}
	return nil
}

func validateLocalIdentitySecrets(home string, identity PublicIdentity) error {
	nodeKey, err := p2p.LoadNodeKey(filepath.Join(home, "config", "node_key.json"))
	if err != nil || string(nodeKey.ID()) != identity.NodeID {
		return errors.New("local P2P private key does not match public identity")
	}
	validatorKey := filepath.Join(home, "config", "priv_validator_key.json")
	validatorState := filepath.Join(home, "data", "priv_validator_state.json")
	if identity.Role == cometnode.RoleObserver {
		for _, path := range []string{validatorKey, validatorState} {
			if _, err := os.Lstat(path); err == nil {
				return errors.New("observer identity contains validator signing material")
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		return nil
	}
	raw, err := readBoundedFile(validatorKey, 16*1024, "local validator key")
	if err != nil {
		return err
	}
	var key privval.FilePVKey
	if err := cmtjson.Unmarshal(raw, &key); err != nil || key.PrivKey == nil || hex.EncodeToString(key.PrivKey.PubKey().Bytes()) != identity.ConsensusPublicKey {
		return errors.New("local validator private key does not match public identity")
	}
	if _, err := readBoundedFile(validatorState, 1024*1024, "local validator signing state"); err != nil {
		return err
	}
	return nil
}

func validateIdentityName(name string) error {
	if name == "" || len(name) > 64 || name != strings.TrimSpace(name) {
		return errors.New("identity name must be canonical and between 1 and 64 bytes")
	}
	for _, character := range name {
		if !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') && character != '-' {
			return errors.New("identity name may contain only lowercase ASCII letters, digits, and hyphens")
		}
	}
	return nil
}

func validateAdvertisedAddress(address string) error {
	if strings.Contains(address, "://") {
		return errors.New("advertised P2P address must be host:port without a scheme")
	}
	host, portText, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return errors.New("advertised P2P address must contain host and port")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("advertised P2P port must be between 1 and 65535")
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsUnspecified() || ip.IsMulticast() {
			return errors.New("advertised P2P address must not be unspecified or multicast")
		}
		return nil
	}
	if len(host) > 253 || strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return errors.New("advertised P2P hostname is invalid")
	}
	for _, character := range host {
		if !(character >= 'a' && character <= 'z') && !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') && character != '-' && character != '.' {
			return errors.New("advertised P2P hostname contains invalid characters")
		}
	}
	return nil
}

func validateLocalPorts(ports LocalPorts) error {
	values := []int{ports.ABCI, ports.RPC, ports.Control, ports.Explorer}
	seen := make(map[int]struct{}, len(values))
	for _, port := range values {
		if port < 1 || port > 65535 {
			return errors.New("local service ports must be between 1 and 65535")
		}
		if _, exists := seen[port]; exists {
			return errors.New("local service ports must be distinct")
		}
		seen[port] = struct{}{}
	}
	return nil
}

func identityChecksum(identity PublicIdentity) (string, error) {
	payload := publicIdentityPayload{
		Protocol: identity.Protocol, Role: identity.Role, Name: identity.Name,
		NodeID: identity.NodeID, P2PAddress: identity.P2PAddress,
		ConsensusPublicKey: identity.ConsensusPublicKey,
		ConsensusAddress:   identity.ConsensusAddress, ChainLabAddress: identity.ChainLabAddress,
	}
	raw, err := hash.CanonicalBytes(payload)
	if err != nil {
		return "", err
	}
	return sha256Hex(raw), nil
}

func invitationChecksum(payload InvitationPayload) (string, error) {
	raw, err := hash.CanonicalBytes(payload)
	if err != nil {
		return "", err
	}
	return sha256Hex(raw), nil
}

func sha256Hex(raw []byte) string {
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func saveLocalPrivateValidator(privateValidator *privval.FilePV) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("save local validator key: %v", recovered)
		}
	}()
	privateValidator.Save()
	return nil
}

func newAbsoluteDirectory(path string, label string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("%s directory is required", label)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(absolute); err == nil {
		return "", fmt.Errorf("%s directory already exists: %s", label, absolute)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if info, err := os.Stat(filepath.Dir(absolute)); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s directory parent must exist", label)
	}
	return absolute, nil
}

func readBoundedFile(path string, maximum int64, label string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", label, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > maximum {
		return nil, fmt.Errorf("%s must be a non-empty regular file no larger than %d bytes", label, maximum)
	}
	return io.ReadAll(io.LimitReader(file, maximum+1))
}

func decodeStrictCanonical(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("document must contain exactly one JSON value")
	}
	canonical, err := hash.CanonicalBytes(target)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, canonical) {
		return errors.New("document is not canonically encoded")
	}
	return nil
}

func writeNewFile(path string, raw []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

func writeExpectedFile(path string, raw []byte, mode os.FileMode) error {
	if existing, err := os.ReadFile(path); err == nil {
		if bytes.Equal(existing, raw) {
			return nil
		}
		return fmt.Errorf("existing file differs from invitation material: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writeNewFile(path, raw, mode)
}
