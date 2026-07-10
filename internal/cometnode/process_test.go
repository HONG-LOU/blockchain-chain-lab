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
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	chainabci "chainlab/internal/abci"
	chaincrypto "chainlab/internal/crypto"
	chaintypes "chainlab/internal/types"

	cmtjson "github.com/cometbft/cometbft/libs/json"
	cmtlog "github.com/cometbft/cometbft/libs/log"
	"github.com/cometbft/cometbft/privval"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
	cmttypes "github.com/cometbft/cometbft/types"
)

const (
	processHelperModeEnv    = "CHAINLAB_PROCESS_HELPER_MODE"
	processHelperHomeEnv    = "CHAINLAB_PROCESS_HELPER_HOME"
	processHelperGenesisEnv = "CHAINLAB_PROCESS_HELPER_GENESIS"
	processHelperListenEnv  = "CHAINLAB_PROCESS_HELPER_LISTEN"
)

func TestFourValidatorProcessesRestartReplayAndBlockSync(t *testing.T) {
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
	apps[3] = startHelperProcess(t, root, "app-3-fresh-replay", map[string]string{
		processHelperModeEnv:    "abci",
		processHelperGenesisEnv: filepath.Join(home3, filepath.FromSlash(AppGenesisPath)),
		processHelperListenEnv:  network.Nodes[3].ABCIListenAddress,
	})
	processes = append(processes, apps[3])
	waitForTCP(t, network.Nodes[3].ABCIListenAddress, 15*time.Second)
	cometNodes[3] = startHelperProcess(t, root, "comet-3-app-replay", map[string]string{
		processHelperModeEnv: "comet",
		processHelperHomeEnv: home3,
	})
	processes = append(processes, cometNodes[3])
	replayHeight := waitForConsistentNetworkState(t, clients, blockSyncHeight, sender, 2, 45*time.Second)

	broadcastTransfer(t, clients[3], senderKey, network.ChainID, sender, receiver, 2, 3)
	_ = waitForConsistentNetworkState(t, clients, replayHeight+1, sender, 3, 45*time.Second)
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
		err = chainabci.Serve(ctx, os.Getenv(processHelperListenEnv), genesis, logger)
	case "comet":
		err = Run(ctx, os.Getenv(processHelperHomeEnv), os.Stdout)
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
