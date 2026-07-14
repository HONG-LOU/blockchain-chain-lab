//go:build windows

package cometnode

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	chainabci "chainlab/internal/abci"

	cmtlog "github.com/cometbft/cometbft/libs/log"
	"golang.org/x/sys/windows"
)

func TestWriteSignStateRetriesWindowsSharingViolation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "priv_validator_state.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { result <- writeSignStateAtomically(path, []byte("new"), 0o600) }()
	select {
	case err := <-result:
		_ = windows.CloseHandle(handle)
		t.Fatalf("atomic replacement returned before lock release: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := windows.CloseHandle(handle); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("atomic replacement did not recover after lock release")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "new" {
		t.Fatalf("private validator state = %q", raw)
	}
}

func TestRunExitsInsteadOfStallingWhenValidatorStateRemainsLocked(t *testing.T) {
	basePort := availablePortRange(t, 3)
	root := filepath.Join(t.TempDir(), "network")
	network, err := InitializeNetwork(NetworkConfig{
		OutputRoot: root, ChainID: "chainlab-locked-validator-state", ValidatorCount: 1,
		ABCIBasePort: basePort, RPCBasePort: basePort + 1, P2PBasePort: basePort + 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	node := network.Nodes[0]
	home := filepath.Join(root, node.Home)
	genesis, err := chainabci.LoadGenesisDocument(filepath.Join(home, filepath.FromSlash(AppGenesisPath)))
	if err != nil {
		t.Fatal(err)
	}
	applicationContext, cancelApplication := context.WithCancel(context.Background())
	applicationResult := make(chan error, 1)
	go func() {
		applicationResult <- chainabci.Serve(applicationContext, node.ABCIListenAddress, genesis, cmtlog.NewNopLogger())
	}()
	waitForTCP(t, node.ABCIListenAddress, 30*time.Second)
	t.Cleanup(func() {
		cancelApplication()
		select {
		case <-applicationResult:
		case <-time.After(10 * time.Second):
			t.Error("application did not stop")
		}
	})

	config, err := BuildConfig(home, mustLoadNodeDocument(t, home))
	if err != nil {
		t.Fatal(err)
	}
	pointer, err := windows.UTF16PtrFromString(config.PrivValidatorStateFile())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)

	nodeContext, cancelNode := context.WithCancel(context.Background())
	defer cancelNode()
	nodeResult := make(chan error, 1)
	go func() {
		nodeResult <- RunWithOptions(nodeContext, home, io.Discard, RunOptions{Consensus: &ConsensusOptions{
			CreateEmptyBlocks: true, CreateEmptyBlocksInterval: 10 * time.Millisecond, TimeoutCommit: 10 * time.Millisecond,
		}})
	}()
	select {
	case err := <-nodeResult:
		if err == nil || !strings.Contains(err.Error(), "failed signing proposal") || !strings.Contains(err.Error(), "replace private validator state") {
			t.Fatalf("locked validator state error = %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("node remained alive after persistent validator state replacement failure")
	}
}

func mustLoadNodeDocument(t *testing.T, home string) NodeDocument {
	t.Helper()
	document, err := LoadNodeDocument(home)
	if err != nil {
		t.Fatal(err)
	}
	return document
}
