package desktop

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	chainabci "chainlab/internal/abci"
	"chainlab/internal/cometnode"
	"chainlab/internal/hash"

	cmtlog "github.com/cometbft/cometbft/libs/log"
	rpchttp "github.com/cometbft/cometbft/rpc/client/http"
)

const (
	RuntimeProtocol = "chainlab-desktop-runtime-v1"
	RuntimePath     = "runtime.json"
	startTimeout    = 45 * time.Second
)

type Runtime struct {
	Protocol     string `json:"protocol"`
	PID          int    `json:"pid"`
	ControlURL   string `json:"control_url"`
	ControlToken string `json:"control_token"`
	StartedAt    string `json:"started_at"`
}

type Status struct {
	Status            string                `json:"status"`
	ChainID           string                `json:"chain_id"`
	Profile           Profile               `json:"profile"`
	DataDir           string                `json:"data_dir"`
	ExplorerURL       string                `json:"explorer_url"`
	RPCURL            string                `json:"rpc_url"`
	Height            int64                 `json:"height"`
	BlockHash         string                `json:"block_hash,omitempty"`
	AppHash           string                `json:"app_hash,omitempty"`
	StartedAt         string                `json:"started_at,omitempty"`
	Storage           chainabci.StorageInfo `json:"storage"`
	CometRetainBlocks uint64                `json:"comet_retain_blocks"`
	DiskBytes         int64                 `json:"disk_bytes"`
	Backup            *BackupStatus         `json:"backup,omitempty"`
}

type supervisorState struct {
	mu     sync.RWMutex
	status Status
}

func (state *supervisorState) snapshot() Status {
	state.mu.RLock()
	defer state.mu.RUnlock()
	return state.status
}

func (state *supervisorState) phase(value string) {
	state.mu.Lock()
	state.status.Status = value
	state.mu.Unlock()
}

func (state *supervisorState) disk(bytes int64) {
	state.mu.Lock()
	state.status.DiskBytes = bytes
	state.mu.Unlock()
}

func Run(ctx context.Context, root string, out io.Writer) (returnErr error) {
	if ctx == nil {
		return errors.New("desktop context is required")
	}
	if out == nil {
		return errors.New("desktop log writer is required")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve desktop data directory: %w", err)
	}
	config, err := Load(absoluteRoot)
	if err != nil {
		return err
	}
	lock, err := acquireRuntimeLock(absoluteRoot)
	if err != nil {
		return err
	}
	defer func() { returnErr = errors.Join(returnErr, lock.Close()) }()

	runtime, err := newRuntime(config.ControlURL)
	if err != nil {
		return err
	}
	runtimeRaw, err := hash.CanonicalBytes(runtime)
	if err != nil {
		return err
	}
	runtimePath := filepath.Join(absoluteRoot, RuntimePath)
	if err := os.WriteFile(runtimePath, runtimeRaw, 0o600); err != nil {
		return fmt.Errorf("write desktop runtime: %w", err)
	}
	defer os.Remove(runtimePath)

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	state := &supervisorState{status: Status{
		Status: "starting", ChainID: config.ChainID, Profile: config.Profile,
		DataDir: absoluteRoot, ExplorerURL: config.ExplorerURL,
		RPCURL: tcpHTTPURL(config.RPCAddress), StartedAt: runtime.StartedAt,
		Storage: chainabci.StorageInfo{
			Mode:               config.Contract.ApplicationStorage.Mode,
			RetainHeights:      config.Contract.ApplicationStorage.RetainHeights,
			CheckpointInterval: config.Contract.ApplicationStorage.CheckpointInterval,
		},
		CometRetainBlocks: config.Contract.CometRetainBlocks,
		Backup:            loadBackupStatus(absoluteRoot),
	}}
	state.disk(directorySize(absoluteRoot))
	go monitorDisk(runCtx, state, absoluteRoot)
	controlAddress, err := loopbackHTTPAddress(config.ControlURL)
	if err != nil {
		return err
	}
	explorerAddress, err := loopbackHTTPAddress(config.ExplorerURL)
	if err != nil {
		return err
	}
	control, err := listenHTTP(controlAddress, controlHandler(state, runtime.ControlToken, cancel))
	if err != nil {
		return fmt.Errorf("start desktop control endpoint: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, shutdownHTTP(control)) }()
	explorer, err := listenHTTP(explorerAddress, explorerHandler(state))
	if err != nil {
		return fmt.Errorf("start desktop Explorer: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, shutdownHTTP(explorer)) }()

	genesisPath := filepath.Join(absoluteRoot, filepath.FromSlash(config.NodeHome), filepath.FromSlash(cometnode.AppGenesisPath))
	genesis, err := chainabci.LoadGenesisDocument(genesisPath)
	if err != nil {
		return err
	}
	logger := cmtlog.NewFilter(cmtlog.NewTMLogger(cmtlog.NewSyncWriter(out)), cmtlog.AllowInfo())
	errorsChannel := make(chan error, 2)
	go func() {
		errorsChannel <- chainabci.ServeWithConfig(runCtx, config.ABCIAddress, chainabci.Config{
			Genesis:            genesis,
			DataDir:            filepath.Join(absoluteRoot, filepath.FromSlash(config.ApplicationData)),
			Storage:            config.Contract.ApplicationStorage,
			CometRetainHeights: config.Contract.CometRetainBlocks,
		}, logger)
	}()
	if err := waitForTCP(runCtx, strings.TrimPrefix(config.ABCIAddress, "tcp://"), startTimeout); err != nil {
		cancel()
		return fmt.Errorf("wait for desktop application: %w", err)
	}
	go func() {
		errorsChannel <- cometnode.RunWithOptions(
			runCtx,
			filepath.Join(absoluteRoot, filepath.FromSlash(config.NodeHome)),
			out,
			cometnode.RunOptions{Consensus: &cometnode.ConsensusOptions{
				CreateEmptyBlocks:         config.Contract.CreateEmptyBlocks,
				CreateEmptyBlocksInterval: time.Duration(config.Contract.BlockIntervalSecs) * time.Second,
				TimeoutCommit:             time.Duration(config.Contract.BlockIntervalSecs) * time.Second,
			}},
		)
	}()
	if err := waitForComet(runCtx, state, errorsChannel, startTimeout); err != nil {
		cancel()
		return fmt.Errorf("wait for desktop consensus: %w", err)
	}
	state.phase("running")
	fmt.Fprintf(out, "ChainLab desktop running data_dir=%s explorer=%s rpc=%s\n", absoluteRoot, config.ExplorerURL, tcpHTTPURL(config.RPCAddress))

	select {
	case <-runCtx.Done():
		cancel()
	case err := <-errorsChannel:
		cancel()
		if err != nil {
			return err
		}
		return errors.New("desktop managed service stopped unexpectedly")
	}
	state.phase("stopping")
	for range 2 {
		if err := <-errorsChannel; err != nil {
			returnErr = errors.Join(returnErr, err)
		}
	}
	return returnErr
}

func CurrentStatus(ctx context.Context, root string) (Status, error) {
	runtime, err := loadRuntime(root)
	if err != nil {
		return Status{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, runtime.ControlURL+"/status", nil)
	if err != nil {
		return Status{}, err
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(request)
	if err != nil {
		return Status{}, fmt.Errorf("desktop is not reachable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Status{}, fmt.Errorf("desktop status returned HTTP %d", response.StatusCode)
	}
	var status Status
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		return Status{}, fmt.Errorf("decode desktop status: %w", err)
	}
	return status, nil
}

func Stop(ctx context.Context, root string) error {
	runtime, err := loadRuntime(root)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, runtime.ControlURL+"/stop", nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+runtime.ControlToken)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return fmt.Errorf("stop desktop: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return fmt.Errorf("stop desktop returned HTTP %d", response.StatusCode)
	}
	return nil
}

func newRuntime(controlURL string) (Runtime, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return Runtime{}, fmt.Errorf("generate desktop control token: %w", err)
	}
	return Runtime{
		Protocol: RuntimeProtocol, PID: os.Getpid(), ControlURL: controlURL,
		ControlToken: hex.EncodeToString(token), StartedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func loadRuntime(root string) (Runtime, error) {
	raw, err := readBoundedFile(filepath.Join(root, RuntimePath), maxConfigBytes, "desktop runtime")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Runtime{}, errors.New("desktop is not running")
		}
		return Runtime{}, fmt.Errorf("read desktop runtime: %w", err)
	}
	var runtime Runtime
	if err := decodeStrictCanonical(raw, &runtime); err != nil {
		return Runtime{}, fmt.Errorf("decode desktop runtime: %w", err)
	}
	if runtime.Protocol != RuntimeProtocol || runtime.PID <= 0 || len(runtime.ControlToken) != 64 {
		return Runtime{}, errors.New("desktop runtime is invalid")
	}
	token, err := hex.DecodeString(runtime.ControlToken)
	if err != nil || len(token) != 32 {
		return Runtime{}, errors.New("desktop runtime control token is invalid")
	}
	if _, err := loopbackHTTPAddress(runtime.ControlURL); err != nil {
		return Runtime{}, errors.New("desktop runtime control URL is invalid")
	}
	config, err := Load(root)
	if err != nil || runtime.ControlURL != config.ControlURL {
		return Runtime{}, errors.New("desktop runtime control URL does not match desktop config")
	}
	return runtime, nil
}

func controlHandler(state *supervisorState, token string, cancel context.CancelFunc) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", func(writer http.ResponseWriter, request *http.Request) {
		status := state.snapshot()
		refreshCometStatus(request.Context(), &status)
		writeJSON(writer, http.StatusOK, status)
	})
	mux.HandleFunc("POST /stop", func(writer http.ResponseWriter, request *http.Request) {
		provided := request.Header.Get("Authorization")
		want := "Bearer " + token
		if subtle.ConstantTimeCompare([]byte(provided), []byte(want)) != 1 {
			writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "invalid desktop control token"})
			return
		}
		writeJSON(writer, http.StatusAccepted, map[string]string{"status": "stopping"})
		go cancel()
	})
	return mux
}

func explorerHandler(state *supervisorState) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(writer http.ResponseWriter, request *http.Request) {
		status := state.snapshot()
		refreshCometStatus(request.Context(), &status)
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = desktopExplorerTemplate.Execute(writer, status)
	})
	mux.HandleFunc("GET /api/status", func(writer http.ResponseWriter, request *http.Request) {
		status := state.snapshot()
		refreshCometStatus(request.Context(), &status)
		writeJSON(writer, http.StatusOK, status)
	})
	mux.HandleFunc("GET /health", func(writer http.ResponseWriter, request *http.Request) {
		status := state.snapshot()
		if status.Status != "running" {
			writeJSON(writer, http.StatusServiceUnavailable, status)
			return
		}
		writeJSON(writer, http.StatusOK, status)
	})
	return mux
}

func refreshCometStatus(ctx context.Context, status *Status) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, status.RPCURL+"/status", nil)
	if err != nil {
		return
	}
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	var envelope struct {
		Result struct {
			NodeInfo struct {
				Network string `json:"network"`
			} `json:"node_info"`
			SyncInfo struct {
				LatestBlockHash   string `json:"latest_block_hash"`
				LatestAppHash     string `json:"latest_app_hash"`
				LatestBlockHeight string `json:"latest_block_height"`
			} `json:"sync_info"`
		} `json:"result"`
	}
	if json.NewDecoder(response.Body).Decode(&envelope) != nil || envelope.Result.NodeInfo.Network != status.ChainID {
		return
	}
	height, err := strconv.ParseInt(envelope.Result.SyncInfo.LatestBlockHeight, 10, 64)
	if err != nil {
		return
	}
	status.Height = height
	status.BlockHash = envelope.Result.SyncInfo.LatestBlockHash
	status.AppHash = envelope.Result.SyncInfo.LatestAppHash
	client, err := rpchttp.New(status.RPCURL, "/websocket")
	if err != nil {
		return
	}
	storageResult, err := client.ABCIQuery(ctx, "/storage", nil)
	if err != nil || storageResult.Response.Code != chainabci.CodeOK {
		return
	}
	var storage chainabci.StorageInfo
	if json.Unmarshal(storageResult.Response.Value, &storage) == nil {
		status.Storage = storage
	}
}

func monitorDisk(ctx context.Context, state *supervisorState, root string) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			state.disk(directorySize(root))
		}
	}
}

func directorySize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, entry os.DirEntry, err error) error {
		if err != nil || !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err == nil && info.Size() <= math.MaxInt64-total {
			total += info.Size()
		}
		return nil
	})
	return total
}

func waitForTCP(ctx context.Context, address string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		connection, err := (&net.Dialer{Timeout: 250 * time.Millisecond}).DialContext(ctx, "tcp", address)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func waitForComet(ctx context.Context, state *supervisorState, serviceErrors <-chan error, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		select {
		case err := <-serviceErrors:
			if err == nil {
				return errors.New("desktop managed service stopped during startup")
			}
			return err
		default:
		}
		status := state.snapshot()
		refreshCometStatus(ctx, &status)
		if status.Height > 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("CometBFT RPC did not report a committed height")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func listenHTTP(address string, handler http.Handler) (*http.Server, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	server := &http.Server{Addr: address, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second}
	go func() { _ = server.Serve(listener) }()
	return server, nil
}

func shutdownHTTP(server *http.Server) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := server.Shutdown(ctx)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func tcpHTTPURL(address string) string {
	return "http://" + strings.TrimPrefix(address, "tcp://")
}

func loopbackHTTPAddress(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("URL must use plain HTTP without credentials, query, or fragment")
	}
	if parsed.Hostname() != "127.0.0.1" || parsed.Port() == "" {
		return "", errors.New("URL must use an explicit 127.0.0.1 port")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("URL port must be between 1 and 65535")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("URL path must be empty or root")
	}
	return parsed.Host, nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func formatByteCount(value int64) string {
	const unit = int64(1024)
	if value < unit {
		return strconv.FormatInt(value, 10) + " B"
	}
	divisor := unit
	exponent := 0
	for quotient := value / unit; quotient >= unit && exponent < 5; quotient /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f %ciB", float64(value)/float64(divisor), "KMGTPE"[exponent])
}

var desktopExplorerTemplate = template.Must(template.New("desktop-explorer").Funcs(template.FuncMap{"bytes": formatByteCount}).Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>ChainLab Desktop Explorer</title><style>
*{box-sizing:border-box}body{font-family:Segoe UI,Arial,sans-serif;margin:0;background:#f4f5f7;color:#17202a;letter-spacing:0}main{max-width:1040px;margin:auto;padding:28px 20px 40px}header{display:flex;justify-content:space-between;align-items:center;border-bottom:3px solid #1d6f42;padding-bottom:16px}h1{font-size:26px;margin:0;font-weight:650}.badge{background:#e7f4ec;color:#155b36;padding:6px 10px;border:1px solid #b7dcc6;border-radius:4px;font-weight:600}.section-title{font-size:14px;color:#4e5965;margin:24px 0 8px;text-transform:uppercase}.grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:1px;background:#d7dce1;border:1px solid #d7dce1}.field{background:white;padding:16px;min-height:82px}.wide{grid-column:span 3}.label{color:#66717d;font-size:12px;margin-bottom:7px}.value{font-family:Consolas,monospace;font-size:14px;overflow-wrap:anywhere}.primary{font-family:Segoe UI,Arial,sans-serif;font-size:20px;font-weight:650;color:#155b36}footer{color:#66717d;margin-top:24px;font-size:13px;border-top:1px solid #d7dce1;padding-top:16px}@media(max-width:720px){.grid{grid-template-columns:1fr}.wide{grid-column:span 1}header{align-items:flex-start;gap:12px;flex-direction:column}main{padding:20px 14px}h1{font-size:22px}}
</style></head><body><main><header><h1>ChainLab Desktop Explorer</h1><span class="badge">{{.Status}}</span></header><section class="grid">
<div class="field"><div class="label">Chain ID</div><div class="value">{{.ChainID}}</div></div>
<div class="field"><div class="label">Profile</div><div class="value">{{.Profile}}</div></div>
<div class="field"><div class="label">Height</div><div class="value primary">{{.Height}}</div></div>
<div class="field"><div class="label">Storage</div><div class="value">{{.Storage.Mode}}</div></div>
<div class="field"><div class="label">Retained range</div><div class="value">{{.Storage.MinimumHeight}} - {{.Storage.CurrentHeight}}</div></div>
<div class="field"><div class="label">Disk use</div><div class="value">{{bytes .DiskBytes}}</div></div>
<div class="field"><div class="label">Application retention</div><div class="value">{{.Storage.RetainHeights}} heights</div></div>
<div class="field"><div class="label">Comet retention</div><div class="value">{{.CometRetainBlocks}} blocks</div></div>
<div class="field"><div class="label">Checkpoint interval</div><div class="value">{{.Storage.CheckpointInterval}} heights</div></div>
<div class="field wide"><div class="label">Data path</div><div class="value">{{.DataDir}}</div></div>
<div class="field wide"><div class="label">RPC</div><div class="value">{{.RPCURL}}</div></div>
<div class="field wide"><div class="label">Block hash</div><div class="value">{{.BlockHash}}</div></div>
<div class="field wide"><div class="label">Application hash</div><div class="value">{{.AppHash}}</div></div>
{{if .Backup}}<div class="field wide"><div class="label">Latest backup</div><div class="value">{{.Backup.CreatedAt}} / {{bytes .Backup.ArchiveBytes}} / {{.Backup.ArchiveSHA256}}</div></div>{{end}}
</section><footer>Experimental local/community network. Test units have no promised financial value.</footer></main></body></html>`))
