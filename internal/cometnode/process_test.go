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

	abciserver "github.com/cometbft/cometbft/abci/server"
	abcitypes "github.com/cometbft/cometbft/abci/types"
	cmtcrypto "github.com/cometbft/cometbft/crypto"
	cmtjson "github.com/cometbft/cometbft/libs/json"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/privval"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	cmttypes "github.com/cometbft/cometbft/types"
)

const (
	processHelperModeEnv          = "CHAINLAB_PROCESS_HELPER_MODE"
	processHelperHomeEnv          = "CHAINLAB_PROCESS_HELPER_HOME"
	processHelperGenesisEnv       = "CHAINLAB_PROCESS_HELPER_GENESIS"
	processHelperListenEnv        = "CHAINLAB_PROCESS_HELPER_LISTEN"
	processHelperDataDirEnv       = "CHAINLAB_PROCESS_HELPER_DATA_DIR"
	processHelperMaxAppVersionEnv = "CHAINLAB_PROCESS_HELPER_MAX_APP_VERSION"
	processHelperStateSyncRPCEnv  = "CHAINLAB_PROCESS_HELPER_STATE_SYNC_RPC"
	processHelperTrustHeightEnv   = "CHAINLAB_PROCESS_HELPER_TRUST_HEIGHT"
	processHelperTrustHashEnv     = "CHAINLAB_PROCESS_HELPER_TRUST_HASH"
	processHelperPrepareDelayFile = "CHAINLAB_PROCESS_HELPER_PREPARE_DELAY_FILE"
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

func TestFourValidatorQuorumLossHaltsAndRecovers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-process CometBFT quorum-loss network in short mode")
	}
	if processRaceEnabled {
		t.Skip("the process-boundary test is covered by the non-race run; package logic remains race-tested")
	}
	basePort := availablePortRange(t, 12)
	root := filepath.Join(t.TempDir(), "quorum-loss-network")
	network, err := InitializeNetwork(NetworkConfig{
		OutputRoot: root, ChainID: "chainlab-quorum-loss", ValidatorCount: 4,
		GenesisTime:  time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC),
		ABCIBasePort: basePort, RPCBasePort: basePort + 4, P2PBasePort: basePort + 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	processes := make([]*helperProcess, 0, len(network.Nodes)*3)
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
		apps[index] = startHelperProcess(t, root, fmt.Sprintf("quorum-app-%d", index), map[string]string{
			processHelperModeEnv: "abci", processHelperGenesisEnv: filepath.Join(home, filepath.FromSlash(AppGenesisPath)),
			processHelperListenEnv: generatedNode.ABCIListenAddress, processHelperDataDirEnv: filepath.Join(root, filepath.FromSlash(generatedNode.ApplicationData)),
		})
		processes = append(processes, apps[index])
	}
	for _, generatedNode := range network.Nodes {
		waitForTCP(t, generatedNode.ABCIListenAddress, 15*time.Second)
	}
	for index, generatedNode := range network.Nodes {
		cometNodes[index] = startHelperProcess(t, root, fmt.Sprintf("quorum-comet-%d", index), map[string]string{
			processHelperModeEnv: "comet", processHelperHomeEnv: filepath.Join(root, generatedNode.Home),
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
	receiver := "0x4444444444444444444444444444444444444444"
	_ = waitForConsistentNetworkState(t, clients, 2, sender, 0, 45*time.Second)

	for _, index := range []int{2, 3} {
		if err := cometNodes[index].stop(); err != nil {
			t.Fatal(err)
		}
	}
	haltedHeight := waitForStableNetworkHeight(t, clients[:2], 2*time.Second, 20*time.Second)
	broadcastTransfer(t, clients[0], senderKey, network.ChainID, sender, receiver, 0, 1)
	assertNetworkHeightUnchanged(t, clients[:2], haltedHeight, 3*time.Second)
	if _, ready, detail := consistentNetworkState(clients[:2], haltedHeight, sender, 0); !ready {
		t.Fatalf("two-validator state changed without quorum: %s", detail)
	}

	home2 := filepath.Join(root, network.Nodes[2].Home)
	cometNodes[2] = startHelperProcess(t, root, "quorum-comet-2-recovered", map[string]string{
		processHelperModeEnv: "comet", processHelperHomeEnv: home2,
	})
	processes = append(processes, cometNodes[2])
	waitForRPCReady(t, []*rpchttp.HTTP{clients[2]}, 45*time.Second)
	recoveredHeight := waitForConsistentNetworkState(t, clients[:3], haltedHeight+1, sender, 1, 60*time.Second)

	home3 := filepath.Join(root, network.Nodes[3].Home)
	cometNodes[3] = startHelperProcess(t, root, "quorum-comet-3-recovered", map[string]string{
		processHelperModeEnv: "comet", processHelperHomeEnv: home3,
	})
	processes = append(processes, cometNodes[3])
	waitForRPCReady(t, []*rpchttp.HTTP{clients[3]}, 45*time.Second)
	waitForPeerMesh(t, clients, 45*time.Second)
	_ = waitForConsistentNetworkState(t, clients, recoveredHeight, sender, 1, 60*time.Second)
}

func TestFourValidatorP2PPartitionHaltsAndHeals(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-process CometBFT P2P partition network in short mode")
	}
	if processRaceEnabled {
		t.Skip("the process-boundary test is covered by the non-race run; package logic remains race-tested")
	}
	basePort := availablePortRange(t, 12)
	root := filepath.Join(t.TempDir(), "p2p-partition-network")
	network, err := InitializeNetwork(NetworkConfig{
		OutputRoot: root, ChainID: "chainlab-p2p-partition", ValidatorCount: 4,
		GenesisTime:  time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC),
		ABCIBasePort: basePort, RPCBasePort: basePort + 4, P2PBasePort: basePort + 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	processes := make([]*helperProcess, 0, len(network.Nodes)*4)
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
	fullPeers := make([]string, len(network.Nodes))
	for index, generatedNode := range network.Nodes {
		home := filepath.Join(root, generatedNode.Home)
		document, err := LoadNodeDocument(home)
		if err != nil {
			t.Fatal(err)
		}
		fullPeers[index] = document.PersistentPeers
		apps[index] = startHelperProcess(t, root, fmt.Sprintf("partition-app-%d", index), map[string]string{
			processHelperModeEnv: "abci", processHelperGenesisEnv: filepath.Join(home, filepath.FromSlash(AppGenesisPath)),
			processHelperListenEnv: generatedNode.ABCIListenAddress, processHelperDataDirEnv: filepath.Join(root, filepath.FromSlash(generatedNode.ApplicationData)),
		})
		processes = append(processes, apps[index])
	}
	for _, generatedNode := range network.Nodes {
		waitForTCP(t, generatedNode.ABCIListenAddress, 15*time.Second)
	}
	for index, generatedNode := range network.Nodes {
		cometNodes[index] = startHelperProcess(t, root, fmt.Sprintf("partition-full-comet-%d", index), map[string]string{
			processHelperModeEnv: "comet", processHelperHomeEnv: filepath.Join(root, generatedNode.Home),
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
	receiver := "0x5555555555555555555555555555555555555555"
	_ = waitForConsistentNetworkState(t, clients, 2, sender, 0, 45*time.Second)

	for index := range cometNodes {
		if err := cometNodes[index].stop(); err != nil {
			t.Fatal(err)
		}
	}
	partitionPeers := []string{
		networkPeerAddress(network.Nodes[1]),
		networkPeerAddress(network.Nodes[0]),
		networkPeerAddress(network.Nodes[3]),
		networkPeerAddress(network.Nodes[2]),
	}
	for index, generatedNode := range network.Nodes {
		setNodePersistentPeers(t, filepath.Join(root, generatedNode.Home), partitionPeers[index])
		cometNodes[index] = startHelperProcess(t, root, fmt.Sprintf("partition-split-comet-%d", index), map[string]string{
			processHelperModeEnv: "comet", processHelperHomeEnv: filepath.Join(root, generatedNode.Home),
		})
		processes = append(processes, cometNodes[index])
	}
	waitForRPCReady(t, clients, 45*time.Second)
	partitionTopology := [][]string{
		{network.Nodes[1].NodeID}, {network.Nodes[0].NodeID},
		{network.Nodes[3].NodeID}, {network.Nodes[2].NodeID},
	}
	waitForPeerTopology(t, clients, partitionTopology, 45*time.Second)
	heightA := waitForStableNetworkHeight(t, clients[:2], 2*time.Second, 20*time.Second)
	heightB := waitForStableNetworkHeight(t, clients[2:], 2*time.Second, 20*time.Second)

	broadcastTransfer(t, clients[0], senderKey, network.ChainID, sender, receiver, 0, 1)
	waitForMempoolCounts(t, clients, []int{1, 1, 0, 0}, 10*time.Second)
	waitForPeerTopology(t, clients, partitionTopology, 5*time.Second)
	assertNetworkHeightUnchanged(t, clients[:2], heightA, 3*time.Second)
	assertNetworkHeightUnchanged(t, clients[2:], heightB, 3*time.Second)

	for index, generatedNode := range network.Nodes {
		setNodePersistentPeers(t, filepath.Join(root, generatedNode.Home), fullPeers[index])
	}
	if err := cometNodes[1].stop(); err != nil {
		t.Fatal(err)
	}
	home1 := filepath.Join(root, network.Nodes[1].Home)
	cometNodes[1] = startHelperProcess(t, root, "partition-bridge-comet-1", map[string]string{
		processHelperModeEnv: "comet", processHelperHomeEnv: home1,
	})
	processes = append(processes, cometNodes[1])
	waitForRPCReady(t, []*rpchttp.HTTP{clients[1]}, 45*time.Second)
	healedHeight := waitForConsistentNetworkState(
		t, clients, max(heightA, heightB)+1, sender, 1, 60*time.Second,
	)

	for _, index := range []int{0, 2, 3} {
		if err := cometNodes[index].stop(); err != nil {
			t.Fatal(err)
		}
		home := filepath.Join(root, network.Nodes[index].Home)
		cometNodes[index] = startHelperProcess(t, root, fmt.Sprintf("partition-healed-comet-%d", index), map[string]string{
			processHelperModeEnv: "comet", processHelperHomeEnv: home,
		})
		processes = append(processes, cometNodes[index])
		waitForRPCReady(t, []*rpchttp.HTTP{clients[index]}, 45*time.Second)
	}
	waitForPeerMesh(t, clients, 45*time.Second)
	_ = waitForConsistentNetworkState(t, clients, healedHeight, sender, 1, 60*time.Second)
	waitForMempoolCounts(t, clients, []int{0, 0, 0, 0}, 10*time.Second)
}

func TestFourValidatorMissingProposerAdvancesRoundAndRecovers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-process CometBFT missing-proposer network in short mode")
	}
	if processRaceEnabled {
		t.Skip("the process-boundary test is covered by the non-race run; package logic remains race-tested")
	}
	basePort := availablePortRange(t, 12)
	root := filepath.Join(t.TempDir(), "missing-proposer-network")
	network, err := InitializeNetwork(NetworkConfig{
		OutputRoot: root, ChainID: "chainlab-missing-proposer", ValidatorCount: 4,
		GenesisTime:  time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC),
		ABCIBasePort: basePort, RPCBasePort: basePort + 4, P2PBasePort: basePort + 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	processes := make([]*helperProcess, 0, len(network.Nodes)*3)
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
		apps[index] = startHelperProcess(t, root, fmt.Sprintf("missing-proposer-app-%d", index), map[string]string{
			processHelperModeEnv: "abci", processHelperGenesisEnv: filepath.Join(home, filepath.FromSlash(AppGenesisPath)),
			processHelperListenEnv: generatedNode.ABCIListenAddress, processHelperDataDirEnv: filepath.Join(root, filepath.FromSlash(generatedNode.ApplicationData)),
		})
		processes = append(processes, apps[index])
	}
	for _, generatedNode := range network.Nodes {
		waitForTCP(t, generatedNode.ABCIListenAddress, 15*time.Second)
	}
	for index, generatedNode := range network.Nodes {
		cometNodes[index] = startHelperProcess(t, root, fmt.Sprintf("missing-proposer-comet-%d", index), map[string]string{
			processHelperModeEnv: "comet", processHelperHomeEnv: filepath.Join(root, generatedNode.Home),
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
	receiver := "0x6666666666666666666666666666666666666666"
	height := waitForConsistentNetworkState(t, clients, 2, sender, 0, 45*time.Second)

	currentSet := validatorSetAtHeight(t, clients[0], height)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	currentBlock, err := clients[0].Block(ctx, &height)
	cancel()
	if err != nil || currentBlock.Block == nil {
		t.Fatalf("current block=%+v err=%v", currentBlock, err)
	}
	if !bytes.Equal(currentSet.GetProposer().Address, currentBlock.Block.ProposerAddress) {
		t.Fatal("reconstructed validator set differs from the current block proposer")
	}
	nextProposer := currentSet.CopyIncrementProposerPriority(1).GetProposer()
	targetHeight := height + 2
	targetProposer := currentSet.CopyIncrementProposerPriority(2).GetProposer()
	targetIndex := validatorNodeIndex(t, network.Nodes, targetProposer.Address)
	if err := cometNodes[targetIndex].stop(); err != nil {
		t.Fatal(err)
	}

	activeClients := clientsExcept(clients, targetIndex)
	broadcastTransfer(t, activeClients[0], senderKey, network.ChainID, sender, receiver, 0, 1)
	activeHeight := waitForConsistentNetworkState(t, activeClients, targetHeight+1, sender, 1, 60*time.Second)
	nextHeight := height + 1
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	nextBlock, err := activeClients[0].Block(ctx, &nextHeight)
	cancel()
	if err != nil || nextBlock.Block == nil {
		t.Fatalf("next block=%+v err=%v", nextBlock, err)
	}
	if !bytes.Equal(nextBlock.Block.ProposerAddress, nextProposer.Address) {
		t.Fatal("next height block differs from the predicted round-0 proposer")
	}
	targetSet := validatorSetAtHeight(t, activeClients[0], nextHeight)
	if !bytes.Equal(targetSet.GetProposer().Address, nextProposer.Address) {
		t.Fatal("next height validator set differs from its block proposer")
	}
	if !bytes.Equal(targetSet.CopyIncrementProposerPriority(1).GetProposer().Address, targetProposer.Address) {
		t.Fatal("target height round-0 proposer differs from the predicted proposer")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	targetBlock, err := activeClients[0].Block(ctx, &targetHeight)
	cancel()
	if err != nil || targetBlock.Block == nil {
		t.Fatalf("target block=%+v err=%v", targetBlock, err)
	}
	if bytes.Equal(targetBlock.Block.ProposerAddress, targetProposer.Address) {
		t.Fatal("offline round-0 proposer produced the target block")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	targetCommit, err := activeClients[0].Commit(ctx, &targetHeight)
	cancel()
	if err != nil || targetCommit.Commit == nil || targetCommit.Commit.Round <= 0 {
		t.Fatalf("target commit=%+v err=%v", targetCommit, err)
	}

	targetHome := filepath.Join(root, network.Nodes[targetIndex].Home)
	cometNodes[targetIndex] = startHelperProcess(t, root, "missing-proposer-recovered", map[string]string{
		processHelperModeEnv: "comet", processHelperHomeEnv: targetHome,
	})
	processes = append(processes, cometNodes[targetIndex])
	waitForRPCReady(t, []*rpchttp.HTTP{clients[targetIndex]}, 45*time.Second)
	waitForPeerMesh(t, clients, 45*time.Second)
	_ = waitForConsistentNetworkState(t, clients, activeHeight, sender, 1, 60*time.Second)
}

func TestFourValidatorDelayedConnectedProposerAdvancesRound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-process CometBFT delayed-proposer network in short mode")
	}
	if processRaceEnabled {
		t.Skip("the process-boundary test is covered by the non-race run; package logic remains race-tested")
	}
	basePort := availablePortRange(t, 12)
	root := filepath.Join(t.TempDir(), "delayed-proposer-network")
	network, err := InitializeNetwork(NetworkConfig{
		OutputRoot: root, ChainID: "chainlab-delayed-proposer", ValidatorCount: 4,
		GenesisTime:  time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC),
		ABCIBasePort: basePort, RPCBasePort: basePort + 4, P2PBasePort: basePort + 8,
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
	delayFiles := make([]string, len(network.Nodes))
	for index, generatedNode := range network.Nodes {
		home := filepath.Join(root, generatedNode.Home)
		delayFiles[index] = filepath.Join(root, fmt.Sprintf("prepare-delay-%d", index))
		processes = append(processes, startHelperProcess(t, root, fmt.Sprintf("delayed-proposer-app-%d", index), map[string]string{
			processHelperModeEnv: "abci", processHelperGenesisEnv: filepath.Join(home, filepath.FromSlash(AppGenesisPath)),
			processHelperListenEnv: generatedNode.ABCIListenAddress, processHelperDataDirEnv: filepath.Join(root, filepath.FromSlash(generatedNode.ApplicationData)),
			processHelperPrepareDelayFile: delayFiles[index],
		}))
	}
	for _, generatedNode := range network.Nodes {
		waitForTCP(t, generatedNode.ABCIListenAddress, 15*time.Second)
	}
	for index, generatedNode := range network.Nodes {
		processes = append(processes, startHelperProcess(t, root, fmt.Sprintf("delayed-proposer-comet-%d", index), map[string]string{
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
	senderKey := loadChainPrivateValidatorKey(t, filepath.Join(root, network.Nodes[0].Home, "config", "priv_validator_key.json"))
	sender := chaincrypto.AddressFromPrivateKey(senderKey)
	height := waitForConsistentNetworkState(t, clients, 2, sender, 0, 45*time.Second)
	currentSet := validatorSetAtHeight(t, clients[0], height)
	nextProposer := currentSet.CopyIncrementProposerPriority(1).GetProposer()
	targetHeight := height + 2
	targetProposer := currentSet.CopyIncrementProposerPriority(2).GetProposer()
	targetIndex := validatorNodeIndex(t, network.Nodes, targetProposer.Address)
	if err := os.WriteFile(delayFiles[targetIndex], []byte(strconv.FormatInt(targetHeight, 10)), 0o600); err != nil {
		t.Fatal(err)
	}

	broadcastTransfer(t, clients[0], senderKey, network.ChainID, sender, "0x9999999999999999999999999999999999999999", 0, 1)
	activeHeight := waitForConsistentNetworkState(t, clients, targetHeight+1, sender, 1, 60*time.Second)
	waitForPeerMesh(t, clients, 10*time.Second)
	nextHeight := height + 1
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	nextBlock, err := clients[0].Block(ctx, &nextHeight)
	cancel()
	if err != nil || nextBlock == nil || nextBlock.Block == nil || !bytes.Equal(nextBlock.Block.ProposerAddress, nextProposer.Address) {
		t.Fatalf("next block=%+v predicted proposer=%X err=%v", nextBlock, nextProposer.Address, err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	targetBlock, err := clients[0].Block(ctx, &targetHeight)
	cancel()
	if err != nil || targetBlock == nil || targetBlock.Block == nil {
		t.Fatalf("target block=%+v err=%v", targetBlock, err)
	}
	if bytes.Equal(targetBlock.Block.ProposerAddress, targetProposer.Address) {
		t.Fatal("delayed round-0 proposer produced the target block")
	}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	targetCommit, err := clients[0].Commit(ctx, &targetHeight)
	cancel()
	if err != nil || targetCommit == nil || targetCommit.Commit == nil || targetCommit.Commit.Round <= 0 {
		t.Fatalf("target commit=%+v err=%v", targetCommit, err)
	}
	_ = waitForConsistentNetworkState(t, clients, activeHeight, sender, 1, 30*time.Second)
}

func TestFourValidatorV2EvidenceSlashingAndEpochRemoval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-process CometBFT evidence network in short mode")
	}
	if processRaceEnabled {
		t.Skip("the process-boundary test is covered by the non-race run; package logic remains race-tested")
	}
	root, network, clients := startFourValidatorV2EvidenceNetwork(t, "chainlab-v2-evidence", 2)

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

func TestFourValidatorV2SimultaneousEpochRemovals(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-process CometBFT simultaneous validator-removal network in short mode")
	}
	if processRaceEnabled {
		t.Skip("the process-boundary test is covered by the non-race run; package logic remains race-tested")
	}
	root, network, clients := startFourValidatorV2EvidenceNetwork(t, "chainlab-v2-simultaneous-removals", 8)
	evidenceHeight := stableEvidenceHeight(t, clients[0])
	targetKeys := make([]cmtcrypto.PrivKey, 0, 2)
	for _, targetIndex := range []int{2, 3} {
		targetHome := filepath.Join(root, network.Nodes[targetIndex].Home)
		privateKey := loadCometPrivateValidatorKey(t, filepath.Join(targetHome, "config", "priv_validator_key.json"))
		targetKeys = append(targetKeys, privateKey)
		evidence := duplicateVoteEvidence(t, clients[0], network.ChainID, evidenceHeight, privateKey)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		broadcast, err := clients[0].BroadcastEvidence(ctx, evidence)
		cancel()
		if err != nil || broadcast == nil || len(broadcast.Hash) != 32 {
			t.Fatalf("broadcast validator %d evidence response=%+v err=%v", targetIndex, broadcast, err)
		}
	}

	var removalHeight int64
	waitForCondition(t, 60*time.Second, func() (bool, string) {
		observedRemovalHeight := int64(0)
		for clientIndex, client := range clients {
			for _, targetIndex := range []int{2, 3} {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				query, err := client.ABCIQuery(ctx, "/validator", []byte(network.Nodes[targetIndex].ChainLabAddress))
				cancel()
				if err != nil || query == nil || query.Response.Code != chainabci.CodeOK {
					return false, fmt.Sprintf("node %d target %d validator query=%+v err=%v", clientIndex, targetIndex, query, err)
				}
				var result struct {
					Identity state.ValidatorIdentity `json:"identity"`
					Stake    uint64                  `json:"stake"`
				}
				if err := json.Unmarshal(query.Response.Value, &result); err != nil {
					return false, fmt.Sprintf("node %d target %d validator decode: %v", clientIndex, targetIndex, err)
				}
				if result.Stake != 0 || result.Identity.InactiveHeight <= 0 {
					return false, fmt.Sprintf("node %d target %d stake=%d inactive_height=%d", clientIndex, targetIndex, result.Stake, result.Identity.InactiveHeight)
				}
				if observedRemovalHeight == 0 {
					observedRemovalHeight = result.Identity.InactiveHeight
				} else if result.Identity.InactiveHeight != observedRemovalHeight {
					return false, fmt.Sprintf("node %d target %d removal height=%d want=%d", clientIndex, targetIndex, result.Identity.InactiveHeight, observedRemovalHeight)
				}
			}
		}
		removalHeight = observedRemovalHeight
		return true, ""
	})

	updateHeight := removalHeight - 2
	waitForCondition(t, 30*time.Second, func() (bool, string) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		results, err := clients[0].BlockResults(ctx, &updateHeight)
		cancel()
		if err != nil || results == nil {
			return false, fmt.Sprintf("block results height %d=%+v err=%v", updateHeight, results, err)
		}
		if len(results.ValidatorUpdates) != len(targetKeys) {
			return false, fmt.Sprintf("height %d validator updates=%+v", updateHeight, results.ValidatorUpdates)
		}
		expectedKeys := make(map[string]struct{}, len(targetKeys))
		for _, privateKey := range targetKeys {
			expectedKeys[hex.EncodeToString(privateKey.PubKey().Bytes())] = struct{}{}
		}
		for _, update := range results.ValidatorUpdates {
			if update.Power != 0 {
				return false, fmt.Sprintf("height %d contains non-zero validator update %+v", updateHeight, update)
			}
			key := hex.EncodeToString(update.PubKey.GetSecp256K1())
			if _, exists := expectedKeys[key]; !exists {
				return false, fmt.Sprintf("height %d contains unexpected validator key %s", updateHeight, key)
			}
			delete(expectedKeys, key)
		}
		if len(expectedKeys) != 0 {
			return false, fmt.Sprintf("height %d is missing %d validator updates", updateHeight, len(expectedKeys))
		}
		return true, ""
	})

	waitForCondition(t, 60*time.Second, func() (bool, string) {
		for index, client := range clients {
			page, perPage := 1, 100
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			validators, err := client.Validators(ctx, nil, &page, &perPage)
			cancel()
			if err != nil || validators == nil || validators.Total != 2 || len(validators.Validators) != 2 {
				return false, fmt.Sprintf("node %d validators=%+v err=%v", index, validators, err)
			}
			for _, targetKey := range targetKeys {
				for _, validator := range validators.Validators {
					if bytes.Equal(validator.Address, targetKey.PubKey().Address()) {
						return false, fmt.Sprintf("node %d still contains removed validator %X", index, validator.Address)
					}
				}
			}
		}
		return true, ""
	})

	senderKey := loadChainPrivateValidatorKey(t, filepath.Join(root, network.Nodes[0].Home, "config", "priv_validator_key.json"))
	sender := chaincrypto.AddressFromPrivateKey(senderKey)
	broadcastTransfer(t, clients[0], senderKey, network.ChainID, sender, "0x7777777777777777777777777777777777777777", 0, 1)
	_ = waitForConsistentNetworkState(t, clients, removalHeight+1, sender, 1, 60*time.Second)
}

func TestFourValidatorV2LightClientAttackRemoval(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-process CometBFT light-client-attack network in short mode")
	}
	if processRaceEnabled {
		t.Skip("the process-boundary test is covered by the non-race run; package logic remains race-tested")
	}
	root, network, clients := startFourValidatorV2EvidenceNetwork(t, "chainlab-v2-light-client-attack", 2)
	evidenceHeight := stableEvidenceHeight(t, clients[0])
	privateKeys := make([]cmtcrypto.PrivKey, len(network.Nodes))
	for index, node := range network.Nodes {
		privateKeys[index] = loadCometPrivateValidatorKey(t, filepath.Join(root, node.Home, "config", "priv_validator_key.json"))
	}
	evidence, byzantineKeys := lightClientAttackEvidence(t, clients[0], network.ChainID, evidenceHeight, privateKeys)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	broadcast, err := clients[0].BroadcastEvidence(ctx, evidence)
	cancel()
	if err != nil || broadcast == nil || len(broadcast.Hash) != 32 {
		t.Fatalf("broadcast light-client evidence response=%+v err=%v", broadcast, err)
	}

	byzantineAddresses := make(map[string]struct{}, len(byzantineKeys))
	for _, privateKey := range byzantineKeys {
		byzantineAddresses[hex.EncodeToString(privateKey.PubKey().Address())] = struct{}{}
	}
	remainingIndex := -1
	for index, node := range network.Nodes {
		if _, exists := byzantineAddresses[node.ConsensusAddress]; !exists {
			remainingIndex = index
			break
		}
	}
	if remainingIndex < 0 {
		t.Fatal("light-client attack evidence did not leave an active validator")
	}

	var removalHeight int64
	waitForCondition(t, 60*time.Second, func() (bool, string) {
		observedRemovalHeight := int64(0)
		for clientIndex, client := range clients {
			for targetIndex, node := range network.Nodes {
				if _, exists := byzantineAddresses[node.ConsensusAddress]; !exists {
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				query, err := client.ABCIQuery(ctx, "/validator", []byte(node.ChainLabAddress))
				cancel()
				if err != nil || query == nil || query.Response.Code != chainabci.CodeOK {
					return false, fmt.Sprintf("node %d target %d validator query=%+v err=%v", clientIndex, targetIndex, query, err)
				}
				var result struct {
					Identity state.ValidatorIdentity `json:"identity"`
					Stake    uint64                  `json:"stake"`
				}
				if err := json.Unmarshal(query.Response.Value, &result); err != nil {
					return false, fmt.Sprintf("node %d target %d validator decode: %v", clientIndex, targetIndex, err)
				}
				if result.Stake != 0 || result.Identity.InactiveHeight <= 0 {
					return false, fmt.Sprintf("node %d target %d stake=%d inactive_height=%d", clientIndex, targetIndex, result.Stake, result.Identity.InactiveHeight)
				}
				if observedRemovalHeight == 0 {
					observedRemovalHeight = result.Identity.InactiveHeight
				} else if result.Identity.InactiveHeight != observedRemovalHeight {
					return false, fmt.Sprintf("node %d target %d removal height=%d want=%d", clientIndex, targetIndex, result.Identity.InactiveHeight, observedRemovalHeight)
				}
			}
		}
		removalHeight = observedRemovalHeight
		return true, ""
	})
	waitForCondition(t, 60*time.Second, func() (bool, string) {
		for index, client := range clients {
			page, perPage := 1, 100
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			validators, err := client.Validators(ctx, nil, &page, &perPage)
			cancel()
			if err != nil || validators == nil || validators.Total != 1 || len(validators.Validators) != 1 {
				return false, fmt.Sprintf("node %d validators=%+v err=%v", index, validators, err)
			}
			if !bytes.Equal(validators.Validators[0].Address, privateKeys[remainingIndex].PubKey().Address()) {
				return false, fmt.Sprintf("node %d remaining validator=%X", index, validators.Validators[0].Address)
			}
		}
		return true, ""
	})

	senderKey := loadChainPrivateValidatorKey(t, filepath.Join(root, network.Nodes[remainingIndex].Home, "config", "priv_validator_key.json"))
	sender := chaincrypto.AddressFromPrivateKey(senderKey)
	broadcastTransfer(t, clients[remainingIndex], senderKey, network.ChainID, sender, "0x8888888888888888888888888888888888888888", 0, 1)
	_ = waitForConsistentNetworkState(t, clients, removalHeight+1, sender, 1, 60*time.Second)
}

func TestFourValidatorV3V4V5ScheduledUpgradesAndSparseProofs(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-process CometBFT upgrade network in short mode")
	}
	if processRaceEnabled {
		t.Skip("the process-boundary test is covered by the non-race run; package logic remains race-tested")
	}
	basePort := availablePortRange(t, 12)
	root := filepath.Join(t.TempDir(), "protocol-v4-network")
	policy := chainabci.DefaultValidatorPolicy()
	policy.EpochLength = 4
	network, err := InitializeNetwork(NetworkConfig{
		OutputRoot: root, ChainID: "chainlab-v4-upgrade", ValidatorCount: 4,
		GenesisTime:  time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC),
		ABCIBasePort: basePort, RPCBasePort: basePort + 4, P2PBasePort: basePort + 8,
		ApplicationProtocol: chainabci.ProtocolVersionV2, ValidatorPolicy: &policy,
		ProtocolUpgrades: []chainabci.ProtocolUpgrade{
			{Height: 4, Protocol: chainabci.ProtocolVersionV3},
			{Height: 6, Protocol: chainabci.ProtocolVersionV4},
			{Height: 8, Protocol: chainabci.ProtocolVersionV5},
		},
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
		processes = append(processes, startHelperProcess(t, root, fmt.Sprintf("v4-app-%d", index), map[string]string{
			processHelperModeEnv: "abci", processHelperGenesisEnv: filepath.Join(home, filepath.FromSlash(AppGenesisPath)),
			processHelperListenEnv:  generatedNode.ABCIListenAddress,
			processHelperDataDirEnv: filepath.Join(root, filepath.FromSlash(generatedNode.ApplicationData)),
		}))
	}
	for _, generatedNode := range network.Nodes {
		waitForTCP(t, generatedNode.ABCIListenAddress, 15*time.Second)
	}
	for index, generatedNode := range network.Nodes {
		processes = append(processes, startHelperProcess(t, root, fmt.Sprintf("v4-comet-%d", index), map[string]string{
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
	_ = waitForConsistentNetworkState(t, clients, 9, network.Nodes[0].ChainLabAddress, 0, 45*time.Second)
	assertV5NetworkStateAndProofs(t, clients, network.Nodes[0].ChainLabAddress)
}

func TestFourValidatorV5RollingApplicationUpgrade(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping multi-process CometBFT rolling upgrade network in short mode")
	}
	if processRaceEnabled {
		t.Skip("the process-boundary test is covered by the non-race run; package logic remains race-tested")
	}
	basePort := availablePortRange(t, 12)
	root := filepath.Join(t.TempDir(), "protocol-v5-rolling-network")
	policy := chainabci.DefaultValidatorPolicy()
	policy.EpochLength = 4
	network, err := InitializeNetwork(NetworkConfig{
		OutputRoot: root, ChainID: "chainlab-v5-rolling", ValidatorCount: 4,
		GenesisTime:  time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC),
		ABCIBasePort: basePort, RPCBasePort: basePort + 4, P2PBasePort: basePort + 8,
		ApplicationProtocol: chainabci.ProtocolVersionV2, ValidatorPolicy: &policy,
		ProtocolUpgrades: []chainabci.ProtocolUpgrade{
			{Height: 4, Protocol: chainabci.ProtocolVersionV3},
			{Height: 6, Protocol: chainabci.ProtocolVersionV4},
			{Height: 30, Protocol: chainabci.ProtocolVersionV5},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	processes := make([]*helperProcess, 0, len(network.Nodes)*4)
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
		apps[index] = startHelperProcess(t, root, fmt.Sprintf("rolling-v4-app-%d", index), map[string]string{
			processHelperModeEnv: "abci", processHelperGenesisEnv: filepath.Join(home, filepath.FromSlash(AppGenesisPath)),
			processHelperListenEnv: generatedNode.ABCIListenAddress, processHelperDataDirEnv: filepath.Join(root, filepath.FromSlash(generatedNode.ApplicationData)),
			processHelperMaxAppVersionEnv: strconv.FormatUint(chainabci.AppVersionV4, 10),
		})
		processes = append(processes, apps[index])
	}
	for _, generatedNode := range network.Nodes {
		waitForTCP(t, generatedNode.ABCIListenAddress, 15*time.Second)
	}
	for index, generatedNode := range network.Nodes {
		cometNodes[index] = startHelperProcess(t, root, fmt.Sprintf("rolling-comet-%d", index), map[string]string{
			processHelperModeEnv: "comet", processHelperHomeEnv: filepath.Join(root, generatedNode.Home),
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
	receiver := "0x3333333333333333333333333333333333333333"
	height := waitForConsistentNetworkState(t, clients, 8, sender, 0, 45*time.Second)

	for index, generatedNode := range network.Nodes {
		if err := cometNodes[index].stop(); err != nil {
			t.Fatal(err)
		}
		if err := apps[index].stop(); err != nil {
			t.Fatal(err)
		}
		activeClients := clientsExcept(clients, index)
		broadcastTransfer(t, activeClients[0], senderKey, network.ChainID, sender, receiver, uint64(index), uint64(index+1))
		height = waitForConsistentNetworkState(t, activeClients, height+1, sender, uint64(index+1), 45*time.Second)

		home := filepath.Join(root, generatedNode.Home)
		apps[index] = startHelperProcess(t, root, fmt.Sprintf("rolling-v5-app-%d", index), map[string]string{
			processHelperModeEnv: "abci", processHelperGenesisEnv: filepath.Join(home, filepath.FromSlash(AppGenesisPath)),
			processHelperListenEnv: generatedNode.ABCIListenAddress, processHelperDataDirEnv: filepath.Join(root, filepath.FromSlash(generatedNode.ApplicationData)),
			processHelperMaxAppVersionEnv: strconv.FormatUint(chainabci.AppVersionV5, 10),
		})
		processes = append(processes, apps[index])
		waitForTCP(t, generatedNode.ABCIListenAddress, 15*time.Second)
		cometNodes[index] = startHelperProcess(t, root, fmt.Sprintf("rolling-v5-comet-%d", index), map[string]string{
			processHelperModeEnv: "comet", processHelperHomeEnv: home,
		})
		processes = append(processes, cometNodes[index])
		waitForRPCReady(t, []*rpchttp.HTTP{clients[index]}, 45*time.Second)
		height = waitForConsistentNetworkState(t, clients, height, sender, uint64(index+1), 45*time.Second)
	}

	assertNetworkProtocol(t, clients, chainabci.ProtocolVersionV4)
	waitForPeerMesh(t, clients, 45*time.Second)
	_ = waitForConsistentNetworkState(t, clients, 31, sender, uint64(len(network.Nodes)), 60*time.Second)
	assertV5NetworkStateAndProofs(t, clients, sender)
}

func assertNetworkProtocol(t *testing.T, clients []*rpchttp.HTTP, want string) {
	t.Helper()
	for index, client := range clients {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		result, err := client.ABCIQuery(ctx, "/app", nil)
		cancel()
		if err != nil || result.Response.Code != chainabci.CodeOK {
			t.Fatalf("node %d app query=%+v err=%v", index, result, err)
		}
		var commitment struct {
			Protocol string `json:"protocol"`
		}
		if err := json.Unmarshal(result.Response.Value, &commitment); err != nil {
			t.Fatal(err)
		}
		if commitment.Protocol != want {
			t.Fatalf("node %d protocol=%q want=%q", index, commitment.Protocol, want)
		}
	}
}

func assertV5NetworkStateAndProofs(t *testing.T, clients []*rpchttp.HTTP, account string) {
	t.Helper()

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
		if commitment.Protocol != chainabci.ProtocolVersionV5 {
			t.Fatalf("node %d protocol = %q", index, commitment.Protocol)
		}
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		proofResult, err := client.ABCIQuery(ctx, "/proof/account", []byte(account))
		cancel()
		if err != nil || proofResult.Response.Code != chainabci.CodeOK {
			t.Fatalf("node %d proof query=%+v err=%v", index, proofResult, err)
		}
		var envelope chainproof.SparseEnvelope
		if err := json.Unmarshal(proofResult.Response.Value, &envelope); err != nil {
			t.Fatal(err)
		}
		expectedStateKey := "account:" + base64.RawURLEncoding.EncodeToString([]byte(account))
		if _, exists, err := chainproof.VerifySparseEnvelope(
			commitment.StateRoot, proofResult.Response.Height,
			chainproof.KindState, expectedStateKey, envelope,
		); err != nil || !exists {
			t.Fatalf("node %d proof verification exists=%t err=%v", index, exists, err)
		}
		proofHeight := proofResult.Response.Height
		ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
		params, err := client.ConsensusParams(ctx, &proofHeight)
		cancel()
		if err != nil || params == nil || params.ConsensusParams.Version.App != chainabci.AppVersionV5 {
			t.Fatalf("node %d consensus params=%+v err=%v", index, params, err)
		}
	}
}

func clientsExcept(clients []*rpchttp.HTTP, excluded int) []*rpchttp.HTTP {
	result := make([]*rpchttp.HTTP, 0, len(clients)-1)
	result = append(result, clients[:excluded]...)
	return append(result, clients[excluded+1:]...)
}

func startFourValidatorV2EvidenceNetwork(
	t *testing.T,
	chainID string,
	epochLength int64,
) (string, NetworkDocument, []*rpchttp.HTTP) {
	t.Helper()
	basePort := availablePortRange(t, 12)
	root := filepath.Join(t.TempDir(), "validator-v2-network")
	policy := chainabci.ValidatorPolicy{
		EpochLength: epochLength, DuplicateVoteSlashBasisPoints: 10_000,
		LightClientAttackSlashBasisPoints: 10_000,
		EvidenceMaxAgeNumBlocks:           1_000, EvidenceMaxAgeDurationNanos: int64(24 * time.Hour),
	}
	network, err := InitializeNetwork(NetworkConfig{
		OutputRoot: root, ChainID: chainID, ValidatorCount: 4,
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
	return root, network, clients
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

func validatorSetAtHeight(t *testing.T, client *rpchttp.HTTP, height int64) *cmttypes.ValidatorSet {
	t.Helper()
	page, perPage := 1, 100
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	result, err := client.Validators(ctx, &height, &page, &perPage)
	cancel()
	if err != nil || result == nil || result.Count != result.Total || result.Total == 0 {
		t.Fatalf("validators at height %d=%+v err=%v", height, result, err)
	}
	validatorSet, err := cmttypes.ValidatorSetFromExistingValidators(result.Validators)
	if err != nil {
		t.Fatalf("reconstruct validators at height %d: %v", height, err)
	}
	return validatorSet
}

func validatorNodeIndex(t *testing.T, nodes []NetworkNode, address []byte) int {
	t.Helper()
	want := hex.EncodeToString(address)
	for index, node := range nodes {
		if node.ConsensusAddress == want {
			return index
		}
	}
	t.Fatalf("validator %s is not in the generated network", want)
	return -1
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

func lightClientAttackEvidence(
	t *testing.T,
	client *rpchttp.HTTP,
	chainID string,
	height int64,
	privateKeys []cmtcrypto.PrivKey,
) (*cmttypes.LightClientAttackEvidence, []cmtcrypto.PrivKey) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	canonical, err := client.Commit(ctx, &height)
	cancel()
	if err != nil || canonical == nil || canonical.Header == nil || canonical.Commit == nil {
		t.Fatalf("load canonical commit %d response=%+v err=%v", height, canonical, err)
	}
	validatorSet := validatorSetAtHeight(t, client, height)
	keysByAddress := make(map[string]cmtcrypto.PrivKey, len(privateKeys))
	for _, privateKey := range privateKeys {
		keysByAddress[hex.EncodeToString(privateKey.PubKey().Address())] = privateKey
	}

	conflictingHeader := *canonical.Header
	conflictingHeader.DataHash = bytes.Repeat([]byte{0xdd}, 32)
	if bytes.Equal(conflictingHeader.DataHash, canonical.Header.DataHash) {
		conflictingHeader.DataHash[0] ^= 0xff
	}
	conflictingBlockID := cmttypes.BlockID{
		Hash: conflictingHeader.Hash(), PartSetHeader: canonical.Commit.BlockID.PartSetHeader,
	}
	voteSet := cmttypes.NewVoteSet(chainID, height, canonical.Commit.Round, cmtproto.PrecommitType, validatorSet)
	byzantineKeys := make([]cmtcrypto.PrivKey, 0, 3)
	for index, validator := range validatorSet.Validators {
		if len(byzantineKeys) == 3 {
			break
		}
		if index >= len(canonical.Commit.Signatures) || canonical.Commit.Signatures[index].BlockIDFlag != cmttypes.BlockIDFlagCommit {
			continue
		}
		privateKey, exists := keysByAddress[hex.EncodeToString(validator.Address)]
		if !exists {
			t.Fatalf("missing private key for validator %X", validator.Address)
		}
		vote := &cmttypes.Vote{
			Type: cmtproto.PrecommitType, Height: height, Round: canonical.Commit.Round,
			BlockID: conflictingBlockID, Timestamp: canonical.Header.Time,
			ValidatorAddress: validator.Address, ValidatorIndex: int32(index),
		}
		signature, err := privateKey.Sign(cmttypes.VoteSignBytes(chainID, vote.ToProto()))
		if err != nil {
			t.Fatal(err)
		}
		vote.Signature = signature
		added, err := voteSet.AddVote(vote)
		if err != nil || !added {
			t.Fatalf("add conflicting vote %d added=%t err=%v", index, added, err)
		}
		byzantineKeys = append(byzantineKeys, privateKey)
	}
	if len(byzantineKeys) != 3 {
		t.Fatalf("canonical commit has only %d usable signatures", len(byzantineKeys))
	}
	conflictingCommit := voteSet.MakeExtendedCommit(cmttypes.ABCIParams{}).ToCommit()
	evidence := &cmttypes.LightClientAttackEvidence{
		ConflictingBlock: &cmttypes.LightBlock{
			SignedHeader: &cmttypes.SignedHeader{Header: &conflictingHeader, Commit: conflictingCommit},
			ValidatorSet: validatorSet,
		},
		CommonHeight: height, TotalVotingPower: validatorSet.TotalVotingPower(), Timestamp: canonical.Header.Time,
	}
	evidence.ByzantineValidators = evidence.GetByzantineValidators(validatorSet, &canonical.SignedHeader)
	if len(evidence.ByzantineValidators) != len(byzantineKeys) {
		t.Fatalf("light-client evidence byzantine validators=%d want=%d", len(evidence.ByzantineValidators), len(byzantineKeys))
	}
	if err := evidence.ValidateBasic(); err != nil {
		t.Fatal(err)
	}
	return evidence, byzantineKeys
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
		var maxAppVersion uint64
		if raw := os.Getenv(processHelperMaxAppVersionEnv); raw != "" {
			maxAppVersion, err = strconv.ParseUint(raw, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
		}
		config := chainabci.Config{
			Genesis: genesis, DataDir: os.Getenv(processHelperDataDirEnv), MaxAppVersion: maxAppVersion,
		}
		if delayFile := os.Getenv(processHelperPrepareDelayFile); delayFile != "" {
			err = serveDelayedPrepareApplication(ctx, os.Getenv(processHelperListenEnv), config, delayFile, logger)
		} else {
			err = chainabci.ServeWithConfig(ctx, os.Getenv(processHelperListenEnv), config, logger)
		}
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

type delayedPrepareApplication struct {
	abcitypes.Application
	heightFile string
	mu         sync.Mutex
	delayed    bool
}

func (application *delayedPrepareApplication) PrepareProposal(
	ctx context.Context,
	request *abcitypes.RequestPrepareProposal,
) (*abcitypes.ResponsePrepareProposal, error) {
	raw, err := os.ReadFile(application.heightFile)
	if err == nil && request != nil {
		height, parseErr := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		application.mu.Lock()
		shouldDelay := parseErr == nil && request.Height == height && !application.delayed
		if shouldDelay {
			application.delayed = true
		}
		application.mu.Unlock()
		if shouldDelay {
			timer := time.NewTimer(2 * time.Second)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return application.Application.PrepareProposal(ctx, request)
}

func serveDelayedPrepareApplication(
	ctx context.Context,
	listen string,
	config chainabci.Config,
	heightFile string,
	logger cmtlog.Logger,
) (returnErr error) {
	application, err := chainabci.NewApplication(config)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, application.Close())
	}()
	server, err := abciserver.NewServer(listen, "socket", &delayedPrepareApplication{
		Application: application,
		heightFile:  heightFile,
	})
	if err != nil {
		return err
	}
	server.SetLogger(logger)
	if err := server.Start(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		if err := server.Stop(); err != nil {
			return err
		}
		<-server.Quit()
		return nil
	case <-server.Quit():
		return errors.New("delayed ABCI socket server stopped unexpectedly")
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

func waitForPeerTopology(
	t *testing.T,
	clients []*rpchttp.HTTP,
	expected [][]string,
	timeout time.Duration,
) {
	t.Helper()
	if len(expected) != len(clients) {
		t.Fatal("peer topology size does not match clients")
	}
	waitForCondition(t, timeout, func() (bool, string) {
		for index, client := range clients {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			info, err := client.NetInfo(ctx)
			cancel()
			if err != nil {
				return false, fmt.Sprintf("node %d net info: %v", index, err)
			}
			want := make(map[string]struct{}, len(expected[index]))
			for _, peerID := range expected[index] {
				want[peerID] = struct{}{}
			}
			if info.NPeers != len(want) {
				return false, fmt.Sprintf("node %d peers=%d want=%d", index, info.NPeers, len(want))
			}
			for _, peer := range info.Peers {
				peerID := string(peer.NodeInfo.ID())
				if _, exists := want[peerID]; !exists {
					return false, fmt.Sprintf("node %d has unexpected peer %s", index, peerID)
				}
				delete(want, peerID)
			}
			if len(want) != 0 {
				return false, fmt.Sprintf("node %d is missing expected peers", index)
			}
		}
		return true, ""
	})
}

func waitForMempoolCounts(
	t *testing.T,
	clients []*rpchttp.HTTP,
	expected []int,
	timeout time.Duration,
) {
	t.Helper()
	if len(expected) != len(clients) {
		t.Fatal("mempool count size does not match clients")
	}
	waitForCondition(t, timeout, func() (bool, string) {
		for index, client := range clients {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			result, err := client.NumUnconfirmedTxs(ctx)
			cancel()
			if err != nil {
				return false, fmt.Sprintf("node %d mempool: %v", index, err)
			}
			if result.Count != expected[index] || result.Total != expected[index] {
				return false, fmt.Sprintf(
					"node %d mempool count=%d total=%d want=%d",
					index, result.Count, result.Total, expected[index],
				)
			}
		}
		return true, ""
	})
}

func networkPeerAddress(node NetworkNode) string {
	return node.NodeID + "@" + strings.TrimPrefix(node.P2PListenAddress, "tcp://")
}

func setNodePersistentPeers(t *testing.T, home string, peers string) {
	t.Helper()
	document, err := LoadNodeDocument(home)
	if err != nil {
		t.Fatal(err)
	}
	document.PersistentPeers = peers
	raw, err := document.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, filepath.FromSlash(NodeDocumentPath))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, info.Mode().Perm()); err != nil {
		t.Fatal(err)
	}
}

func waitForStableNetworkHeight(
	t *testing.T,
	clients []*rpchttp.HTTP,
	stableFor time.Duration,
	timeout time.Duration,
) int64 {
	t.Helper()
	deadline := time.Now().Add(timeout)
	stableHeight := int64(-1)
	stableSince := time.Time{}
	last := "network height not checked"
	for time.Now().Before(deadline) {
		height, err := uniformNetworkHeight(clients)
		if err != nil {
			last = err.Error()
			stableHeight = -1
			stableSince = time.Time{}
		} else if height != stableHeight {
			stableHeight = height
			stableSince = time.Now()
			last = fmt.Sprintf("height %d has not been stable for %s", height, stableFor)
		} else if time.Since(stableSince) >= stableFor {
			return stableHeight
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("network height did not stabilize within %s: %s", timeout, last)
	return 0
}

func assertNetworkHeightUnchanged(
	t *testing.T,
	clients []*rpchttp.HTTP,
	want int64,
	duration time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		height, err := uniformNetworkHeight(clients)
		if err != nil {
			t.Fatal(err)
		}
		if height != want {
			t.Fatalf("network advanced without quorum to height %d, want %d", height, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func uniformNetworkHeight(clients []*rpchttp.HTTP) (int64, error) {
	var height int64 = -1
	for index, client := range clients {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		status, err := client.Status(ctx)
		cancel()
		if err != nil {
			return 0, fmt.Errorf("node %d status: %w", index, err)
		}
		if status.SyncInfo.CatchingUp {
			return 0, fmt.Errorf("node %d is catching up", index)
		}
		if height == -1 {
			height = status.SyncInfo.LatestBlockHeight
		} else if status.SyncInfo.LatestBlockHeight != height {
			return 0, fmt.Errorf(
				"node %d height=%d differs from %d", index, status.SyncInfo.LatestBlockHeight, height,
			)
		}
	}
	if height < 0 {
		return 0, errors.New("network height requires at least one client")
	}
	return height, nil
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
