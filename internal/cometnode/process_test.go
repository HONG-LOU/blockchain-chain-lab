package cometnode

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	chainabci "chainlab/internal/abci"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"
	chaintypes "chainlab/internal/types"
	chainproof "chainlab/pkg/proof"

	cmtcrypto "github.com/cometbft/cometbft/crypto"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/privval"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	cmttypes "github.com/cometbft/cometbft/types"
)

const (
	processHelperModeEnv         = "CHAINLAB_PROCESS_HELPER_MODE"
	processHelperHomeEnv         = "CHAINLAB_PROCESS_HELPER_HOME"
	processHelperGenesisEnv      = "CHAINLAB_PROCESS_HELPER_GENESIS"
	processHelperListenEnv       = "CHAINLAB_PROCESS_HELPER_LISTEN"
	processHelperDataDirEnv      = "CHAINLAB_PROCESS_HELPER_DATA_DIR"
	processHelperStateSyncRPCEnv = "CHAINLAB_PROCESS_HELPER_STATE_SYNC_RPC"
	processHelperTrustHeightEnv  = "CHAINLAB_PROCESS_HELPER_TRUST_HEIGHT"
	processHelperTrustHashEnv    = "CHAINLAB_PROCESS_HELPER_TRUST_HASH"
)

func TestFourValidatorProcessesRestartReplayBlockAndStateSync(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-process CometBFT network in short mode")
	}
	if processRaceEnabled {
		t.Skip("the process-boundary test is covered by the non-race run; package logic remains race-tested")
	}
	basePort := availablePortRange(t, 12)
	root := filepath.Join(t.TempDir(), "network")
	network, err := InitializeNetwork(NetworkConfig{
		OutputRoot:     root,
		ChainID:        "chainlab-four-process",
		ValidatorCount: 4,
		GenesisTime:    time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC),
		ABCIBasePort:   basePort,
		RPCBasePort:    basePort + 4,
		P2PBasePort:    basePort + 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	var processes []*helperProcess
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, process := range processes {
			t.Logf("%s log:\n%s", process.name, process.tailLog())
		}
	})

	apps := make([]*helperProcess, len(network.Nodes))
	cometNodes := make([]*helperProcess, len(network.Nodes))
	for index, generatedNode := range network.Nodes {
		home := filepath.Join(root, generatedNode.Home)
		apps[index] = startHelperProcess(t, root, fmt.Sprintf("app-%d", index), map[string]string{
			processHelperModeEnv:    "abci",
			processHelperGenesisEnv: filepath.Join(home, filepath.FromSlash(AppGenesisPath)),
			processHelperListenEnv:  generatedNode.ABCIListenAddress,
			processHelperDataDirEnv: filepath.Join(root, filepath.FromSlash(generatedNode.ApplicationData)),
		})
		processes = append(processes, apps[index])
	}
	for _, generatedNode := range network.Nodes {
		waitForTCP(t, generatedNode.ABCIListenAddress, 15*time.Second)
	}
	for index, generatedNode := range network.Nodes {
		cometNodes[index] = startHelperProcess(t, root, fmt.Sprintf("comet-%d", index), map[string]string{
			processHelperModeEnv: "comet",
			processHelperHomeEnv: filepath.Join(root, generatedNode.Home),
		})
		processes = append(processes, cometNodes[index])
	}

	clients := make([]*rpchttp.HTTP, len(network.Nodes))
	for index, generatedNode := range network.Nodes {
		client, err := rpchttp.New(httpAddress(generatedNode.RPCListenAddress), "/websocket")
		if err != nil {
			t.Fatal(err)
		}
		clients[index] = client
	}
	waitForRPCReady(t, clients, 45*time.Second)
	waitForPeerMesh(t, clients, 45*time.Second)

	senderKey := loadChainPrivateValidatorKey(t, filepath.Join(root, network.Nodes[0].Home, "config", "priv_validator_key.json"))
	sender := chaincrypto.AddressFromPrivateKey(senderKey)
	if sender != network.Nodes[0].ChainLabAddress {
		t.Fatalf("sender %s does not match network manifest %s", sender, network.Nodes[0].ChainLabAddress)
	}
	receiver := "0x2222222222222222222222222222222222222222"
	baselineHeight := waitForConsistentNetworkState(t, clients, 1, sender, 0, 45*time.Second)
	broadcastTransfer(t, clients[0], senderKey, network.ChainID, sender, receiver, 0, 1)
	firstHeight := waitForConsistentNetworkState(t, clients, baselineHeight+1, sender, 1, 45*time.Second)

	if err := cometNodes[3].stop(); err != nil {
		t.Fatal(err)
	}
	broadcastTransfer(t, clients[0], senderKey, network.ChainID, sender, receiver, 1, 2)
	secondHeight := waitForConsistentNetworkState(t, clients[:3], firstHeight+1, sender, 2, 45*time.Second)

	cometNodes[3] = startHelperProcess(t, root, "comet-3-block-sync", map[string]string{
		processHelperModeEnv: "comet",
		processHelperHomeEnv: filepath.Join(root, network.Nodes[3].Home),
	})
	processes = append(processes, cometNodes[3])
	blockSyncHeight := waitForConsistentNetworkState(t, clients, secondHeight, sender, 2, 45*time.Second)

	if err := cometNodes[3].stop(); err != nil {
		t.Fatal(err)
	}
	if err := apps[3].stop(); err != nil {
		t.Fatal(err)
	}
	home3 := filepath.Join(root, network.Nodes[3].Home)
	apps[3] = startHelperProcess(t, root, "app-3-durable-restart", map[string]string{
		processHelperModeEnv:    "abci",
		processHelperGenesisEnv: filepath.Join(home3, filepath.FromSlash(AppGenesisPath)),
		processHelperListenEnv:  network.Nodes[3].ABCIListenAddress,
		processHelperDataDirEnv: filepath.Join(root, filepath.FromSlash(network.Nodes[3].ApplicationData)),
	})
	processes = append(processes, apps[3])
	waitForTCP(t, network.Nodes[3].ABCIListenAddress, 15*time.Second)
	cometNodes[3] = startHelperProcess(t, root, "comet-3-durable-app", map[string]string{
		processHelperModeEnv: "comet",
		processHelperHomeEnv: home3,
	})
	processes = append(processes, cometNodes[3])
	durableRestartHeight := waitForConsistentNetworkState(t, clients, blockSyncHeight, sender, 2, 45*time.Second)
	if err := cometNodes[3].stop(); err != nil {
		t.Fatal(err)
	}
	if err := apps[3].stop(); err != nil {
		t.Fatal(err)
	}

	apps[3] = startHelperProcess(t, root, "app-3-fresh-replay", map[string]string{
		processHelperModeEnv:    "abci",
		processHelperGenesisEnv: filepath.Join(home3, filepath.FromSlash(AppGenesisPath)),
		processHelperListenEnv:  network.Nodes[3].ABCIListenAddress,
		processHelperDataDirEnv: filepath.Join(root, "fresh-app-3"),
	})
	processes = append(processes, apps[3])
	waitForTCP(t, network.Nodes[3].ABCIListenAddress, 15*time.Second)
	cometNodes[3] = startHelperProcess(t, root, "comet-3-app-replay", map[string]string{
		processHelperModeEnv: "comet",
		processHelperHomeEnv: home3,
	})
	processes = append(processes, cometNodes[3])
	replayHeight := waitForConsistentNetworkState(t, clients, durableRestartHeight, sender, 2, 45*time.Second)

	broadcastTransfer(t, clients[3], senderKey, network.ChainID, sender, receiver, 2, 3)
	thirdHeight := waitForConsistentNetworkState(t, clients, replayHeight+1, sender, 3, 45*time.Second)

	if err := cometNodes[3].stop(); err != nil {
		t.Fatal(err)
	}
	if err := apps[3].stop(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	trustedBlock, err := clients[0].Block(ctx, &thirdHeight)
	cancel()
	if err != nil || trustedBlock.Block == nil || len(trustedBlock.BlockID.Hash) != 32 {
		t.Fatalf("trusted state-sync block = %+v err=%v", trustedBlock, err)
	}
	resetNodeDataForStateSync(t, home3)
	apps[3] = startHelperProcess(t, root, "app-3-state-sync", map[string]string{
		processHelperModeEnv:    "abci",
		processHelperGenesisEnv: filepath.Join(home3, filepath.FromSlash(AppGenesisPath)),
		processHelperListenEnv:  network.Nodes[3].ABCIListenAddress,
		processHelperDataDirEnv: filepath.Join(root, filepath.FromSlash(network.Nodes[3].ApplicationData)),
	})
	processes = append(processes, apps[3])
	waitForTCP(t, network.Nodes[3].ABCIListenAddress, 15*time.Second)
	cometNodes[3] = startHelperProcess(t, root, "comet-3-state-sync", map[string]string{
		processHelperModeEnv:         "comet",
		processHelperHomeEnv:         home3,
		processHelperStateSyncRPCEnv: strings.Join([]string{httpAddress(network.Nodes[0].RPCListenAddress), httpAddress(network.Nodes[1].RPCListenAddress)}, ","),
		processHelperTrustHeightEnv:  strconv.FormatInt(thirdHeight, 10),
		processHelperTrustHashEnv:    hex.EncodeToString(trustedBlock.BlockID.Hash),
	})
	processes = append(processes, cometNodes[3])
	stateSyncHeight := waitForConsistentNetworkState(t, clients, thirdHeight, sender, 3, 120*time.Second)
	if !cometNodes[3].logContains("Snapshot restored") {
		t.Fatalf("state-sync node did not report snapshot restoration:\n%s", cometNodes[3].tailLog())
	}
	broadcastTransfer(t, clients[3], senderKey, network.ChainID, sender, receiver, 3, 4)
	_ = waitForConsistentNetworkState(t, clients, stateSyncHeight+1, sender, 4, 45*time.Second)
}

func TestFourValidatorV2EvidenceSlashingAndEpochRemoval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-process CometBFT evidence network in short mode")
	}
	if processRaceEnabled {
		t.Skip("the process-boundary test is covered by the non-race run; package logic remains race-tested")
	}
	basePort := availablePortRange(t, 12)
	root := filepath.Join(t.TempDir(), "validator-v2-network")
	policy := chainabci.ValidatorPolicy{
		EpochLength: 2, DuplicateVoteSlashBasisPoints: 10_000,
		LightClientAttackSlashBasisPoints: 10_000,
		EvidenceMaxAgeNumBlocks:           1_000, EvidenceMaxAgeDurationNanos: int64(24 * time.Hour),
	}
	network, err := InitializeNetwork(NetworkConfig{
		OutputRoot: root, ChainID: "chainlab-v2-evidence", ValidatorCount: 4,
		GenesisTime:  time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC),
		ABCIBasePort: basePort, RPCBasePort: basePort + 4, P2PBasePort: basePort + 8,
		ApplicationProtocol: chainabci.ProtocolVersionV2, ValidatorPolicy: &policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	processes := make([]*helperProcess, 0, len(network.Nodes)*2)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, process := range processes {
			t.Logf("%s log:\n%s", process.name, process.tailLog())
		}
	})
	for index, generatedNode := range network.Nodes {
		home := filepath.Join(root, generatedNode.Home)
		processes = append(processes, startHelperProcess(t, root, fmt.Sprintf("v2-app-%d", index), map[string]string{
			processHelperModeEnv: "abci", processHelperGenesisEnv: filepath.Join(home, filepath.FromSlash(AppGenesisPath)),
			processHelperListenEnv:  generatedNode.ABCIListenAddress,
			processHelperDataDirEnv: filepath.Join(root, filepath.FromSlash(generatedNode.ApplicationData)),
		}))
	}
	for _, generatedNode := range network.Nodes {
		waitForTCP(t, generatedNode.ABCIListenAddress, 15*time.Second)
	}
	for index, generatedNode := range network.Nodes {
		processes = append(processes, startHelperProcess(t, root, fmt.Sprintf("v2-comet-%d", index), map[string]string{
			processHelperModeEnv: "comet", processHelperHomeEnv: filepath.Join(root, generatedNode.Home),
		}))
	}
	clients := make([]*rpchttp.HTTP, len(network.Nodes))
	for index, generatedNode := range network.Nodes {
		client, err := rpchttp.New(httpAddress(generatedNode.RPCListenAddress), "/websocket")
		if err != nil {
			t.Fatal(err)
		}
		clients[index] = client
	}
	waitForRPCReady(t, clients, 45*time.Second)
	waitForPeerMesh(t, clients, 45*time.Second)

	evidenceHeight := stableEvidenceHeight(t, clients[0])
	targetHome := filepath.Join(root, network.Nodes[3].Home)
	privateKey := loadCometPrivateValidatorKey(t, filepath.Join(targetHome, "config", "priv_validator_key.json"))
	evidence := duplicateVoteEvidence(t, clients[0], network.ChainID, evidenceHeight, privateKey)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	broadcast, err := clients[0].BroadcastEvidence(ctx, evidence)
	cancel()
	if err != nil || broadcast == nil || len(broadcast.Hash) != 32 {
		t.Fatalf("broadcast evidence response=%+v err=%v", broadcast, err)
	}

	targetAccount := network.Nodes[3].ChainLabAddress
	waitForCondition(t, 60*time.Second, func() (bool, string) {
		for index, client := range clients {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			query, err := client.ABCIQuery(ctx, "/validator", []byte(targetAccount))
			cancel()
			if err != nil || query == nil || query.Response.Code != chainabci.CodeOK {
				return false, fmt.Sprintf("node %d validator query response=%+v err=%v", index, query, err)
			}
			var result struct {
				Identity state.ValidatorIdentity `json:"identity"`
				Stake    uint64                  `json:"stake"`
			}
			if err := json.Unmarshal(query.Response.Value, &result); err != nil {
				return false, fmt.Sprintf("node %d validator query decode: %v", index, err)
			}
			if result.Stake != 0 || result.Identity.InactiveHeight <= 0 {
				return false, fmt.Sprintf("node %d stake=%d inactive_height=%d", index, result.Stake, result.Identity.InactiveHeight)
			}
			ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			validators, err := client.Validators(ctx, nil, nil, nil)
			cancel()
			if err != nil || validators == nil || validators.Total != 3 {
				return false, fmt.Sprintf("node %d validators=%+v err=%v", index, validators, err)
			}
			for _, validator := range validators.Validators {
				if bytes.Equal(validator.Address, privateKey.PubKey().Address()) {
					return false, fmt.Sprintf("node %d still contains removed validator", index)
				}
			}
		}
		return true, ""
	})
}

func TestFourValidatorV3ScheduledUpgradeAndProofs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-process CometBFT upgrade network in short mode")
	}
	if processRaceEnabled {
		t.Skip("the process-boundary test is covered by the non-race run; package logic remains race-tested")
	}
	basePort := availablePortRange(t, 12)
	root := filepath.Join(t.TempDir(), "protocol-v3-network")
	policy := chainabci.DefaultValidatorPolicy()
	policy.EpochLength = 4
	network, err := InitializeNetwork(NetworkConfig{
		OutputRoot: root, ChainID: "chainlab-v3-upgrade", ValidatorCount: 4,
		GenesisTime:  time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC),
		ABCIBasePort: basePort, RPCBasePort: basePort + 4, P2PBasePort: basePort + 8,
		ApplicationProtocol: chainabci.ProtocolVersionV2, ValidatorPolicy: &policy,
		ProtocolUpgrades: []chainabci.ProtocolUpgrade{{Height: 4, Protocol: chainabci.ProtocolVersionV3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	processes := make([]*helperProcess, 0, len(network.Nodes)*2)
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		for _, process := range processes {
			t.Logf("%s log:\n%s", process.name, process.tailLog())
		}
	})
	for index, generatedNode := range network.Nodes {
		home := filepath.Join(root, generatedNode.Home)
		processes = append(processes, startHelperProcess(t, root, fmt.Sprintf("v3-app-%d", index), map[string]string{
			processHelperModeEnv: "abci", processHelperGenesisEnv: filepath.Join(home, filepath.FromSlash(AppGenesisPath)),
			processHelperListenEnv:  generatedNode.ABCIListenAddress,
			processHelperDataDirEnv: filepath.Join(root, filepath.FromSlash(generatedNode.ApplicationData)),
		}))
	}
	for _, generatedNode := range network.Nodes {
		waitForTCP(t, generatedNode.ABCIListenAddress, 15*time.Second)
	}
	for index, generatedNode := range network.Nodes {
		processes = append(processes, startHelperProcess(t, root, fmt.Sprintf("v3-comet-%d", index), map[string]string{
			processHelperModeEnv: "comet", processHelperHomeEnv: filepath.Join(root, generatedNode.Home),
		}))
	}
	clients := make([]*rpchttp.HTTP, len(network.Nodes))
	for index, generatedNode := range network.Nodes {
		client, err := rpchttp.New(httpAddress(generatedNode.RPCListenAddress), "/websocket")
		if err != nil {
			t.Fatal(err)
		}
		clients[index] = client
	}
	waitForRPCReady(t, clients, 45*time.Second)
	waitForPeerMesh(t, clients, 45*time.Second)
	_ = waitForConsistentNetworkState(t, clients, 5, network.Nodes[0].ChainLabAddress, 0, 45*time.Second)

	for index, client := range clients {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		appResult, err := client.ABCIQuery(ctx, "/app", nil)
		cancel()
		if err != nil || appResult.Response.Code != chainabci.CodeOK {
			t.Fatalf("node %d app query=%+v err=%v", index, appResult, err)
		}
		var commitment struct {
			Protocol  string `json:"protocol"`
			StateRoot string `json:"state_root"`
		}
		if err := json.Unmarshal(appResult.Response.Value, &commitment); err != nil {
			t.Fatal(err)
		}
		if commitment.Protocol != chainabci.ProtocolVersionV3 {
			t.Fatalf("node %d protocol = %q", index, commitment.Protocol)
		}
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		proofResult, err := client.ABCIQuery(ctx, "/proof/account", []byte(network.Nodes[0].ChainLabAddress))
		cancel()
		if err != nil || proofResult.Response.Code != chainabci.CodeOK {
			t.Fatalf("node %d proof query=%+v err=%v", index, proofResult, err)
		}
		var envelope chainproof.Envelope
		if err := json.Unmarshal(proofResult.Response.Value, &envelope); err != nil {
			t.Fatal(err)
		}
		expectedStateKey := "account:" + base64.RawURLEncoding.EncodeToString([]byte(network.Nodes[0].ChainLabAddress))
		if _, err := chainproof.VerifyEnvelope(
			commitment.StateRoot, proofResult.Response.Height,
			chainproof.KindState, expectedStateKey, envelope,
		); err != nil {
			t.Fatalf("node %d proof verification: %v", index, err)
		}
		proofHeight := proofResult.Response.Height
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		params, err := client.ConsensusParams(ctx, &proofHeight)
		cancel()
		if err != nil || params == nil || params.ConsensusParams.Version.App != chainabci.AppVersionV3 {
			t.Fatalf("node %d consensus params=%+v err=%v", index, params, err)
		}
	}
}

func stableEvidenceHeight(t *testing.T, client *rpchttp.HTTP) int64 {
	t.Helper()
	var height int64
	waitForCondition(t, 20*time.Second, func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		status, err := client.Status(ctx)
		cancel()
		if err != nil || status == nil || status.SyncInfo.LatestBlockHeight < 3 {
			return false, fmt.Sprintf("status=%+v err=%v", status, err)
		}
		height = status.SyncInfo.LatestBlockHeight - 1
		return true, ""
	})
	return height
}

func loadCometPrivateValidatorKey(t *testing.T, path string) cmtcrypto.PrivKey {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var key privval.FilePVKey
	if err := cmtjson.Unmarshal(raw, &key); err != nil {
		t.Fatal(err)
	}
	return key.PrivKey
}

func duplicateVoteEvidence(
	t *testing.T,
	client *rpchttp.HTTP,
	chainID string,
	height int64,
	privateKey cmtcrypto.PrivKey,
) *cmttypes.DuplicateVoteEvidence {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	block, err := client.Block(ctx, &height)
	cancel()
	if err != nil || block == nil || block.Block == nil {
		t.Fatalf("load evidence block %d response=%+v err=%v", height, block, err)
	}
	page, perPage := 1, 100
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	validators, err := client.Validators(ctx, &height, &page, &perPage)
	cancel()
	if err != nil || validators == nil || validators.Total != len(validators.Validators) {
		t.Fatalf("load evidence validators response=%+v err=%v", validators, err)
	}
	validatorSet := cmttypes.NewValidatorSet(validators.Validators)
	validatorIndex, validator := validatorSet.GetByAddress(privateKey.PubKey().Address())
	if validatorIndex < 0 || validator == nil {
		t.Fatal("evidence signer is not in the historical validator set")
	}
	conflictingHash := bytes.Repeat([]byte{0xee}, 32)
	if bytes.Equal(conflictingHash, block.BlockID.Hash) {
		conflictingHash[0] ^= 0xff
	}
	first := &cmttypes.Vote{
		Type: cmtproto.PrevoteType, Height: height, Round: 0, BlockID: block.BlockID,
		Timestamp: block.Block.Time, ValidatorAddress: privateKey.PubKey().Address(), ValidatorIndex: int32(validatorIndex),
	}
	second := first.Copy()
	second.BlockID = cmttypes.BlockID{Hash: conflictingHash, PartSetHeader: block.BlockID.PartSetHeader}
	for _, vote := range []*cmttypes.Vote{first, second} {
		signature, err := privateKey.Sign(cmttypes.VoteSignBytes(chainID, vote.ToProto()))
		if err != nil {
			t.Fatal(err)
		}
		vote.Signature = signature
	}
	evidence, err := cmttypes.NewDuplicateVoteEvidence(first, second, block.Block.Time, validatorSet)
	if err != nil {
		t.Fatal(err)
	}
	if err := evidence.ValidateBasic(); err != nil {
		t.Fatal(err)
	}
	return evidence
}

func TestChainLabProcessHelper(t *testing.T) {
	mode := os.Getenv(processHelperModeEnv)
	if mode == "" {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		cancel()
	}()
	logger := cmtlog.NewTMLogger(cmtlog.NewSyncWriter(os.Stdout))
	var err error
	switch mode {
	case "abci":
		genesis, loadErr := chainabci.LoadGenesisDocument(os.Getenv(processHelperGenesisEnv))
		if loadErr != nil {
			t.Fatal(loadErr)
		}
		err = chainabci.ServeWithConfig(ctx, os.Getenv(processHelperListenEnv), chainabci.Config{
			Genesis: genesis,
			DataDir: os.Getenv(processHelperDataDirEnv),
		}, logger)
	case "comet":
		options := RunOptions{}
		if rpcServers := os.Getenv(processHelperStateSyncRPCEnv); rpcServers != "" {
			trustHeight, parseErr := strconv.ParseInt(os.Getenv(processHelperTrustHeightEnv), 10, 64)
			if parseErr != nil {
				t.Fatal(parseErr)
			}
			options.StateSync = &StateSyncOptions{
				RPCServers: strings.Split(rpcServers, ","), TrustHeight: trustHeight,
				TrustHash: os.Getenv(processHelperTrustHashEnv),
			}
		}
		err = RunWithOptions(ctx, os.Getenv(processHelperHomeEnv), os.Stdout, options)
	default:
		t.Fatalf("unknown process helper mode %q", mode)
	}
	if err != nil {
		t.Fatal(err)
	}
}

type helperProcess struct {
	name    string
	command *exec.Cmd
	stdin   io.WriteCloser
	done    chan error
	logPath string
	logFile *os.File
	mu      sync.Mutex
	stopped bool
	stopErr error
}

func startHelperProcess(t *testing.T, logRoot string, name string, environment map[string]string) *helperProcess {
	t.Helper()
	logPath := filepath.Join(logRoot, name+".log")
	logFile, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(os.Args[0], "-test.run=^TestChainLabProcessHelper$", "-test.v")
	command.Env = append([]string(nil), os.Environ()...)
	for key, value := range environment {
		command.Env = append(command.Env, key+"="+value)
	}
	stdin, err := command.StdinPipe()
	if err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		_ = logFile.Close()
		t.Fatal(err)
	}
	process := &helperProcess{
		name: name, command: command, stdin: stdin, done: make(chan error, 1), logPath: logPath, logFile: logFile,
	}
	go func() {
		process.done <- command.Wait()
	}()
	t.Cleanup(func() {
		if err := process.stop(); err != nil {
			t.Errorf("stop %s: %v", name, err)
		}
	})
	return process
}

func (process *helperProcess) stop() error {
	process.mu.Lock()
	if process.stopped {
		err := process.stopErr
		process.mu.Unlock()
		return err
	}
	process.stopped = true
	process.mu.Unlock()

	_ = process.stdin.Close()
	var err error
	select {
	case err = <-process.done:
	case <-time.After(20 * time.Second):
		killErr := process.command.Process.Kill()
		waitErr := <-process.done
		err = errors.Join(errors.New("process did not stop gracefully"), killErr, waitErr)
	}
	closeErr := process.logFile.Close()
	if err != nil {
		err = fmt.Errorf("%s exited: %w", process.name, err)
	}
	err = errors.Join(err, closeErr)
	process.mu.Lock()
	process.stopErr = err
	process.mu.Unlock()
	return err
}

func (process *helperProcess) tailLog() string {
	raw, err := os.ReadFile(process.logPath)
	if err != nil {
		return err.Error()
	}
	const maximum = 16 * 1024
	if len(raw) > maximum {
		raw = raw[len(raw)-maximum:]
	}
	return string(raw)
}

func (process *helperProcess) logContains(text string) bool {
	raw, err := os.ReadFile(process.logPath)
	return err == nil && bytes.Contains(raw, []byte(text))
}

func resetNodeDataForStateSync(t *testing.T, home string) {
	t.Helper()
	dataDir := filepath.Join(home, "data")
	statePath := filepath.Join(dataDir, "priv_validator_state.json")
	stateRaw, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, stateRaw, info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
}

func availablePortRange(t *testing.T, count int) int {
	t.Helper()
	for attempt := 0; attempt < 100; attempt++ {
		seed, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		base := seed.Addr().(*net.TCPAddr).Port
		_ = seed.Close()
		if base+count > 65535 {
			continue
		}
		listeners := make([]net.Listener, 0, count)
		available := true
		for offset := 0; offset < count; offset++ {
			listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", base+offset))
			if err != nil {
				available = false
				break
			}
			listeners = append(listeners, listener)
		}
		for _, listener := range listeners {
			_ = listener.Close()
		}
		if available {
			return base
		}
	}
	t.Fatal("could not reserve a contiguous TCP port range")
	return 0
}

func waitForTCP(t *testing.T, address string, timeout time.Duration) {
	t.Helper()
	hostPort := strings.TrimPrefix(address, "tcp://")
	waitForCondition(t, timeout, func() (bool, string) {
		connection, err := net.DialTimeout("tcp", hostPort, 200*time.Millisecond)
		if err != nil {
			return false, err.Error()
		}
		_ = connection.Close()
		return true, ""
	})
}

func waitForRPCReady(t *testing.T, clients []*rpchttp.HTTP, timeout time.Duration) {
	t.Helper()
	waitForCondition(t, timeout, func() (bool, string) {
		for index, client := range clients {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			status, err := client.Status(ctx)
			cancel()
			if err != nil {
				return false, fmt.Sprintf("node %d: %v", index, err)
			}
			if status.NodeInfo.Network == "" {
				return false, fmt.Sprintf("node %d has empty network", index)
			}
		}
		return true, ""
	})
}

func waitForPeerMesh(t *testing.T, clients []*rpchttp.HTTP, timeout time.Duration) {
	t.Helper()
	waitForCondition(t, timeout, func() (bool, string) {
		for index, client := range clients {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			info, err := client.NetInfo(ctx)
			cancel()
			if err != nil {
				return false, fmt.Sprintf("node %d: %v", index, err)
			}
			if info.NPeers != len(clients)-1 {
				return false, fmt.Sprintf("node %d peers=%d", index, info.NPeers)
			}
		}
		return true, ""
	})
}

func waitForCondition(t *testing.T, timeout time.Duration, check func() (bool, string)) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := "condition not checked"
	for time.Now().Before(deadline) {
		if ready, detail := check(); ready {
			return
		} else if detail != "" {
			last = detail
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s: %s", timeout, last)
}

func loadChainPrivateValidatorKey(t *testing.T, path string) chaincrypto.PrivateKey {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var key privval.FilePVKey
	if err := cmtjson.Unmarshal(raw, &key); err != nil {
		t.Fatal(err)
	}
	chainKey, err := chaincrypto.PrivateKeyFromHex(hex.EncodeToString(key.PrivKey.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return chainKey
}

func broadcastTransfer(
	t *testing.T,
	client *rpchttp.HTTP,
	key chaincrypto.PrivateKey,
	chainID string,
	from string,
	to string,
	nonce uint64,
	value uint64,
) {
	t.Helper()
	transaction := chaintypes.Transaction{
		ChainID:  chainID,
		Type:     chaintypes.TxTransfer,
		From:     from,
		To:       to,
		Nonce:    nonce,
		Value:    value,
		GasLimit: 21_000,
		GasPrice: 1,
	}
	signature, err := chaincrypto.Sign(key, transaction.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	transaction.Signature = signature
	encoded, err := chaintypes.EncodeRawTransaction(transaction)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(strings.TrimPrefix(encoded, "0x"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	result, err := client.BroadcastTxSync(ctx, cmttypes.Tx(raw))
	cancel()
	if err != nil || result.Code != chainabci.CodeOK {
		t.Fatalf("broadcast tx nonce %d = %+v err=%v", nonce, result, err)
	}
}

func waitForConsistentNetworkState(
	t *testing.T,
	clients []*rpchttp.HTTP,
	minimumHeight int64,
	sender string,
	nonce uint64,
	timeout time.Duration,
) int64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	last := "network state not checked"
	for time.Now().Before(deadline) {
		height, ready, detail := consistentNetworkState(clients, minimumHeight, sender, nonce)
		if ready {
			return height
		}
		last = detail
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("consistent network state not reached within %s: %s", timeout, last)
	return 0
}

func consistentNetworkState(clients []*rpchttp.HTTP, minimumHeight int64, sender string, nonce uint64) (int64, bool, string) {
	type nodeSnapshot struct {
		height    int64
		stateRoot string
	}
	snapshots := make([]nodeSnapshot, len(clients))
	commonBlockHeight := int64(^uint64(0) >> 1)
	var expectedStateRoot string
	for index, client := range clients {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		status, err := client.Status(ctx)
		cancel()
		if err != nil {
			return 0, false, fmt.Sprintf("node %d status: %v", index, err)
		}
		if status.SyncInfo.LatestBlockHeight < minimumHeight || status.SyncInfo.CatchingUp || len(status.SyncInfo.LatestAppHash) != 32 {
			return 0, false, fmt.Sprintf("node %d height=%d catching_up=%t", index, status.SyncInfo.LatestBlockHeight, status.SyncInfo.CatchingUp)
		}
		if status.SyncInfo.LatestBlockHeight < commonBlockHeight {
			commonBlockHeight = status.SyncInfo.LatestBlockHeight
		}

		ctx, cancel = context.WithTimeout(context.Background(), time.Second)
		appResult, err := client.ABCIQuery(ctx, "/app", nil)
		cancel()
		if err != nil {
			return 0, false, fmt.Sprintf("node %d app query: %v", index, err)
		}
		if appResult.Response.Code != chainabci.CodeOK || appResult.Response.Height != status.SyncInfo.LatestBlockHeight {
			return 0, false, fmt.Sprintf("node %d app query moved from status height %d to %d", index, status.SyncInfo.LatestBlockHeight, appResult.Response.Height)
		}
		var commitment struct {
			Height    int64  `json:"height"`
			StateRoot string `json:"state_root"`
		}
		if err := json.Unmarshal(appResult.Response.Value, &commitment); err != nil {
			return 0, false, fmt.Sprintf("node %d app commitment decode: %v", index, err)
		}
		if commitment.Height != appResult.Response.Height || commitment.StateRoot == "" {
			return 0, false, fmt.Sprintf("node %d app commitment is inconsistent", index)
		}
		if index == 0 {
			expectedStateRoot = commitment.StateRoot
		} else if commitment.StateRoot != expectedStateRoot {
			return 0, false, fmt.Sprintf("node %d state root differs", index)
		}
		snapshots[index] = nodeSnapshot{height: commitment.Height, stateRoot: commitment.StateRoot}

		ctx, cancel = context.WithTimeout(context.Background(), time.Second)
		accountResult, err := client.ABCIQuery(ctx, "/account", []byte(sender))
		cancel()
		if err != nil {
			return 0, false, fmt.Sprintf("node %d account query: %v", index, err)
		}
		if accountResult.Response.Code != chainabci.CodeOK || accountResult.Response.Height != commitment.Height {
			return 0, false, fmt.Sprintf("node %d account query height=%d code=%d err=%v", index, accountResult.Response.Height, accountResult.Response.Code, err)
		}
		var account chaintypes.Account
		if err := json.Unmarshal(accountResult.Response.Value, &account); err != nil {
			return 0, false, fmt.Sprintf("node %d account decode: %v", index, err)
		}
		if account.Nonce != nonce {
			return 0, false, fmt.Sprintf("node %d sender nonce=%d want=%d", index, account.Nonce, nonce)
		}
	}
	var expectedBlockID []byte
	var expectedHistoricalAppHash []byte
	for index, client := range clients {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		blockResult, err := client.Block(ctx, &commonBlockHeight)
		cancel()
		if err != nil {
			return 0, false, fmt.Sprintf("node %d common-height block query: %v", index, err)
		}
		if blockResult.Block == nil || blockResult.Block.Height != commonBlockHeight || len(blockResult.Block.Header.AppHash) != 32 {
			return 0, false, fmt.Sprintf("node %d common-height block is invalid", index)
		}
		if index == 0 {
			expectedBlockID = append([]byte(nil), blockResult.BlockID.Hash...)
			expectedHistoricalAppHash = append([]byte(nil), blockResult.Block.Header.AppHash...)
		} else if !bytes.Equal(expectedBlockID, blockResult.BlockID.Hash) || !bytes.Equal(expectedHistoricalAppHash, blockResult.Block.Header.AppHash) {
			return 0, false, fmt.Sprintf("node %d common-height block/app hash differs", index)
		}
	}
	for index, snapshot := range snapshots {
		if snapshot.height < commonBlockHeight || snapshot.stateRoot != expectedStateRoot {
			return 0, false, fmt.Sprintf("node %d snapshot regressed", index)
		}
	}
	return commonBlockHeight, true, ""
}

func httpAddress(address string) string {
	return "http://" + strings.TrimPrefix(address, "tcp://")
}
