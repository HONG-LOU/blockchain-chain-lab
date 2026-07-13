package desktop

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	chainabci "chainlab/internal/abci"
	"chainlab/internal/cometnode"

	cmtlog "github.com/cometbft/cometbft/libs/log"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
)

const (
	tenThousandBlockMeasurementEnv  = "CHAINLAB_DESKTOP_10000_BLOCK_MEASURE"
	retentionBoundaryMeasurementEnv = "CHAINLAB_DESKTOP_RETENTION_MEASURE"
)

type tenThousandBlockMeasurement struct {
	Protocol        string  `json:"protocol"`
	Mode            string  `json:"mode"`
	SourceRevision  string  `json:"source_revision"`
	SourceModified  bool    `json:"source_modified"`
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

type retentionCheckpoint struct {
	ObservedHeight           int64 `json:"observed_height"`
	ApplicationMinimumHeight int64 `json:"application_minimum_height"`
	DiskBytes                int64 `json:"disk_bytes"`
}

type retentionBoundaryMeasurement struct {
	Protocol                string              `json:"protocol"`
	Mode                    string              `json:"mode"`
	SourceRevision          string              `json:"source_revision"`
	SourceModified          bool                `json:"source_modified"`
	StartedAt               string              `json:"started_at"`
	DurationSeconds         float64             `json:"duration_seconds"`
	RetainHeights           uint64              `json:"retain_heights"`
	CommitInterval          string              `json:"commit_interval"`
	Initial                 retentionCheckpoint `json:"initial"`
	FirstRetentionWindow    retentionCheckpoint `json:"first_retention_window"`
	SecondRetentionWindow   retentionCheckpoint `json:"second_retention_window"`
	FirstWindowGrowthBytes  int64               `json:"first_window_growth_bytes"`
	SecondWindowGrowthBytes int64               `json:"second_window_growth_bytes"`
	CometHeightOnePruned    bool                `json:"comet_height_one_pruned"`
	ApplicationRangeBounded bool                `json:"application_range_bounded"`
	Limitation              string              `json:"limitation"`
	DataRoot                string              `json:"data_root"`
}

type emptyBlockMeasurementNetwork struct {
	root              string
	networkRoot       string
	client            *rpchttp.HTTP
	cancelApplication context.CancelFunc
	cancelComet       context.CancelFunc
	applicationResult chan error
	cometResult       chan error
}

func TestMeasureTenThousandEmptyBlocks(t *testing.T) {
	artifact := os.Getenv(tenThousandBlockMeasurementEnv)
	if artifact == "" {
		t.Skip("set CHAINLAB_DESKTOP_10000_BLOCK_MEASURE to an artifact path")
	}
	network := startEmptyBlockMeasurementNetwork(t, "chainlab-10000-block-measure-", "chainlab-desktop-10000", 5*time.Millisecond)
	start := time.Now()
	waitForMeasurementHeight(t, network.client, 1, 30*time.Second)
	diskStart := directorySize(network.networkRoot)
	waitForMeasurementHeight(t, network.client, 10_001, 10*time.Minute)
	duration := time.Since(start)
	diskEnd := directorySize(network.networkRoot)
	result := tenThousandBlockMeasurement{
		Protocol: "chainlab-desktop-measurement-v1", Mode: "accelerated-storage-only",
		StartedAt: start.UTC().Format(time.RFC3339Nano), DurationSeconds: duration.Seconds(),
		StartHeight: 1, EndHeight: 10_001, HeightDelta: 10_000,
		DiskBytesStart: diskStart, DiskBytesEnd: diskEnd, DiskBytesGrowth: diskEnd - diskStart,
		RetainHeights: defaultRetainBlocks, CommitInterval: "5ms",
		Limitation: "Storage-only accelerated cadence; not release-cadence CPU, RSS, network, or soak evidence.",
		DataRoot:   network.root,
	}
	result.SourceRevision, result.SourceModified = measurementSource()
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
	network.stop(t)
}

func TestMeasureRetentionBoundary(t *testing.T) {
	artifact := os.Getenv(retentionBoundaryMeasurementEnv)
	if artifact == "" {
		t.Skip("set CHAINLAB_DESKTOP_RETENTION_MEASURE to an artifact path")
	}
	network := startEmptyBlockMeasurementNetwork(t, "chainlab-retention-measure-", "chainlab-desktop-retention", time.Millisecond)
	defer os.RemoveAll(network.root)
	start := time.Now()
	waitForMeasurementHeight(t, network.client, 1, 30*time.Second)
	initial := retentionMeasurementCheckpoint(t, network.client, network.networkRoot)
	firstTarget := int64(defaultRetainBlocks) + 1
	waitForMeasurementHeight(t, network.client, firstTarget, 45*time.Minute)
	first := waitForRetentionMinimum(t, network.client, network.networkRoot, 2, 2*time.Minute)
	secondTarget := 2*int64(defaultRetainBlocks) + 1
	waitForMeasurementHeight(t, network.client, secondTarget, 90*time.Minute)
	secondMinimum := secondTarget - int64(defaultRetainBlocks) + 1
	second := waitForRetentionMinimum(t, network.client, network.networkRoot, secondMinimum, 2*time.Minute)
	heightOnePruned := waitForCometHeightPruned(t, network.client, 1, 2*time.Minute)
	result := retentionBoundaryMeasurement{
		Protocol: "chainlab-desktop-measurement-v1", Mode: "accelerated-retention-boundary",
		StartedAt: start.UTC().Format(time.RFC3339Nano), DurationSeconds: time.Since(start).Seconds(),
		RetainHeights: defaultRetainBlocks, CommitInterval: "1ms",
		Initial: initial, FirstRetentionWindow: first, SecondRetentionWindow: second,
		FirstWindowGrowthBytes:  first.DiskBytes - initial.DiskBytes,
		SecondWindowGrowthBytes: second.DiskBytes - first.DiskBytes,
		CometHeightOnePruned:    heightOnePruned,
		ApplicationRangeBounded: second.ObservedHeight-second.ApplicationMinimumHeight+1 <= int64(defaultRetainBlocks),
		Limitation:              "Accelerated empty-block retention evidence; not release-cadence CPU, RSS, network, sleep/resume, or 24-hour soak evidence.",
		DataRoot:                network.root,
	}
	result.SourceRevision, result.SourceModified = measurementSource()
	if !result.ApplicationRangeBounded || !result.CometHeightOnePruned {
		t.Fatalf("retention boundary not enforced: %+v", result)
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
	network.stop(t)
}

func measurementSource() (string, bool) {
	revisionRaw, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "", true
	}
	statusRaw, err := exec.Command("git", "status", "--porcelain", "--untracked-files=no").Output()
	if err != nil {
		return strings.TrimSpace(string(revisionRaw)), true
	}
	return strings.TrimSpace(string(revisionRaw)), len(statusRaw) != 0
}

func startEmptyBlockMeasurementNetwork(t *testing.T, prefix string, chainID string, interval time.Duration) *emptyBlockMeasurementNetwork {
	t.Helper()
	base := availablePortBlock(t, 3)
	root, err := os.MkdirTemp("", prefix)
	if err != nil {
		t.Fatal(err)
	}
	policy := chainabci.DefaultValidatorPolicy()
	networkRoot := filepath.Join(root, "network")
	generated, err := cometnode.InitializeNetwork(cometnode.NetworkConfig{
		OutputRoot: networkRoot, ChainID: chainID, ValidatorCount: 1,
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
	node := generated.Nodes[0]
	genesis, err := chainabci.LoadGenesisDocument(filepath.Join(networkRoot, filepath.FromSlash(node.ApplicationGenesis)))
	if err != nil {
		t.Fatal(err)
	}
	profile := chainabci.StorageProfile{
		Mode: chainabci.StorageModeFull, RetainHeights: defaultRetainBlocks,
		CheckpointInterval: chainabci.DefaultCheckpointInterval,
	}
	applicationContext, cancelApplication := context.WithCancel(context.Background())
	cometContext, cancelComet := context.WithCancel(context.Background())
	applicationResult := make(chan error, 1)
	cometResult := make(chan error, 1)
	go func() {
		applicationResult <- chainabci.ServeWithConfig(applicationContext, node.ABCIListenAddress, chainabci.Config{
			Genesis: genesis, DataDir: filepath.Join(networkRoot, filepath.FromSlash(node.ApplicationData)),
			Storage: profile, CometRetainHeights: defaultRetainBlocks,
		}, cmtlog.NewNopLogger())
	}()
	if err := waitForTCP(applicationContext, node.ABCIListenAddress[len("tcp://"):], 15*time.Second); err != nil {
		cancelApplication()
		t.Fatal(err)
	}
	go func() {
		cometResult <- cometnode.RunWithOptions(cometContext, filepath.Join(networkRoot, filepath.FromSlash(node.Home)), io.Discard, cometnode.RunOptions{
			Consensus: &cometnode.ConsensusOptions{
				CreateEmptyBlocks: true, CreateEmptyBlocksInterval: interval,
				TimeoutCommit: interval,
			},
		})
	}()
	client, err := rpchttp.New(tcpHTTPURL(node.RPCListenAddress), "/websocket")
	if err != nil {
		cancelComet()
		cancelApplication()
		t.Fatal(err)
	}
	return &emptyBlockMeasurementNetwork{
		root: root, networkRoot: networkRoot, client: client,
		cancelApplication: cancelApplication, cancelComet: cancelComet,
		applicationResult: applicationResult, cometResult: cometResult,
	}
}

func (network *emptyBlockMeasurementNetwork) stop(t *testing.T) {
	t.Helper()
	network.cancelComet()
	if err := <-network.cometResult; err != nil {
		t.Fatal(err)
	}
	network.cancelApplication()
	if err := <-network.applicationResult; err != nil {
		t.Fatal(err)
	}
}

func retentionMeasurementCheckpoint(t *testing.T, client *rpchttp.HTTP, networkRoot string) retentionCheckpoint {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := client.ABCIQuery(ctx, "/storage", nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Response.Code != chainabci.CodeOK {
		t.Fatalf("storage query failed: %s", result.Response.Log)
	}
	var storage chainabci.StorageInfo
	if err := json.Unmarshal(result.Response.Value, &storage); err != nil {
		t.Fatal(err)
	}
	return retentionCheckpoint{
		ObservedHeight: storage.CurrentHeight, ApplicationMinimumHeight: storage.MinimumHeight,
		DiskBytes: directorySize(networkRoot),
	}
}

func waitForRetentionMinimum(t *testing.T, client *rpchttp.HTTP, networkRoot string, minimum int64, timeout time.Duration) retentionCheckpoint {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		checkpoint := retentionMeasurementCheckpoint(t, client, networkRoot)
		if checkpoint.ApplicationMinimumHeight >= minimum {
			return checkpoint
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("application minimum height did not reach %d within %s", minimum, timeout)
	return retentionCheckpoint{}
}

func waitForCometHeightPruned(t *testing.T, client *rpchttp.HTTP, height int64, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := client.Block(ctx, &height)
		cancel()
		if err != nil {
			statusContext, statusCancel := context.WithTimeout(context.Background(), time.Second)
			status, statusErr := client.Status(statusContext)
			statusCancel()
			if statusErr == nil && status.SyncInfo.LatestBlockHeight > height {
				latest := status.SyncInfo.LatestBlockHeight
				blockContext, blockCancel := context.WithTimeout(context.Background(), time.Second)
				_, latestErr := client.Block(blockContext, &latest)
				blockCancel()
				if latestErr == nil {
					return true
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
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
