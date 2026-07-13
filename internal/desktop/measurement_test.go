package desktop

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	chainabci "chainlab/internal/abci"
	"chainlab/internal/cometnode"

	cmtlog "github.com/cometbft/cometbft/libs/log"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
)

const tenThousandBlockMeasurementEnv = "CHAINLAB_DESKTOP_10000_BLOCK_MEASURE"

type tenThousandBlockMeasurement struct {
	Protocol        string  `json:"protocol"`
	Mode            string  `json:"mode"`
	StartedAt       string  `json:"started_at"`
	DurationSeconds float64 `json:"duration_seconds"`
	StartHeight     int64   `json:"start_height"`
	EndHeight       int64   `json:"end_height"`
	HeightDelta     int64   `json:"height_delta"`
	DiskBytesStart  int64   `json:"disk_bytes_start"`
	DiskBytesEnd    int64   `json:"disk_bytes_end"`
	DiskBytesGrowth int64   `json:"disk_bytes_growth"`
	RetainHeights   uint64  `json:"retain_heights"`
	CommitInterval  string  `json:"commit_interval"`
	Limitation      string  `json:"limitation"`
	DataRoot        string  `json:"data_root"`
}

func TestMeasureTenThousandEmptyBlocks(t *testing.T) {
	artifact := os.Getenv(tenThousandBlockMeasurementEnv)
	if artifact == "" {
		t.Skip("set CHAINLAB_DESKTOP_10000_BLOCK_MEASURE to an artifact path")
	}
	base := availablePortBlock(t, 3)
	root, err := os.MkdirTemp("", "chainlab-10000-block-measure-")
	if err != nil {
		t.Fatal(err)
	}
	policy := chainabci.DefaultValidatorPolicy()
	networkRoot := filepath.Join(root, "network")
	network, err := cometnode.InitializeNetwork(cometnode.NetworkConfig{
		OutputRoot: networkRoot, ChainID: "chainlab-desktop-10000", ValidatorCount: 1,
		ABCIBasePort: base, RPCBasePort: base + 1, P2PBasePort: base + 2,
		ApplicationProtocol: chainabci.ProtocolVersionV2, ValidatorPolicy: &policy,
		ProtocolUpgrades: []chainabci.ProtocolUpgrade{
			{Height: 2, Protocol: chainabci.ProtocolVersionV3},
			{Height: 3, Protocol: chainabci.ProtocolVersionV4},
			{Height: 4, Protocol: chainabci.ProtocolVersionV5},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	node := network.Nodes[0]
	genesis, err := chainabci.LoadGenesisDocument(filepath.Join(networkRoot, filepath.FromSlash(node.ApplicationGenesis)))
	if err != nil {
		t.Fatal(err)
	}
	profile := chainabci.StorageProfile{
		Mode: chainabci.StorageModeFull, RetainHeights: defaultRetainBlocks,
		CheckpointInterval: chainabci.DefaultCheckpointInterval,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errorsChannel := make(chan error, 2)
	go func() {
		errorsChannel <- chainabci.ServeWithConfig(ctx, node.ABCIListenAddress, chainabci.Config{
			Genesis: genesis, DataDir: filepath.Join(networkRoot, filepath.FromSlash(node.ApplicationData)),
			Storage: profile, CometRetainHeights: defaultRetainBlocks,
		}, cmtlog.NewNopLogger())
	}()
	if err := waitForTCP(ctx, node.ABCIListenAddress[len("tcp://"):], 15*time.Second); err != nil {
		t.Fatal(err)
	}
	go func() {
		errorsChannel <- cometnode.RunWithOptions(ctx, filepath.Join(networkRoot, filepath.FromSlash(node.Home)), io.Discard, cometnode.RunOptions{
			Consensus: &cometnode.ConsensusOptions{
				CreateEmptyBlocks: true, CreateEmptyBlocksInterval: 5 * time.Millisecond,
				TimeoutCommit: 5 * time.Millisecond,
			},
		})
	}()
	client, err := rpchttp.New(tcpHTTPURL(node.RPCListenAddress), "/websocket")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	waitForMeasurementHeight(t, client, 1, 30*time.Second)
	diskStart := directorySize(networkRoot)
	waitForMeasurementHeight(t, client, 10_001, 10*time.Minute)
	duration := time.Since(start)
	diskEnd := directorySize(networkRoot)
	result := tenThousandBlockMeasurement{
		Protocol: "chainlab-desktop-measurement-v1", Mode: "accelerated-storage-only",
		StartedAt: start.UTC().Format(time.RFC3339Nano), DurationSeconds: duration.Seconds(),
		StartHeight: 1, EndHeight: 10_001, HeightDelta: 10_000,
		DiskBytesStart: diskStart, DiskBytesEnd: diskEnd, DiskBytesGrowth: diskEnd - diskStart,
		RetainHeights: defaultRetainBlocks, CommitInterval: "5ms",
		Limitation: "Storage-only accelerated cadence; not release-cadence CPU, RSS, network, or soak evidence.",
		DataRoot:   root,
	}
	raw, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(artifact), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cancel()
	for range 2 {
		if err := <-errorsChannel; err != nil {
			t.Fatal(err)
		}
	}
}

func waitForMeasurementHeight(t *testing.T, client *rpchttp.HTTP, height int64, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		status, err := client.Status(ctx)
		cancel()
		if err == nil && status.SyncInfo.LatestBlockHeight >= height {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("network did not reach height %d within %s", height, timeout)
}
