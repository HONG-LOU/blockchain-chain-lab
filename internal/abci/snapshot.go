package abci

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"

	"chainlab/internal/hash"
	"chainlab/internal/state"
	"chainlab/internal/types"

	abcitypes "github.com/cometbft/cometbft/abci/types"
)

const (
	applicationSnapshotProtocol           = "chainlab-state-snapshot-v1"
	applicationSnapshotFormat      uint32 = 1
	applicationSnapshotChunkBytes         = 1024 * 1024
	maxApplicationSnapshotBytes           = 512 * 1024 * 1024
	maxApplicationSnapshotMetadata        = 64 * 1024
	applicationSnapshotInterval    uint64 = 100
)

type applicationSnapshotDocument struct {
	Protocol    string                `json:"protocol"`
	GenesisHash string                `json:"genesis_hash"`
	Height      int64                 `json:"height"`
	Initialized bool                  `json:"initialized"`
	Commitment  applicationCommitment `json:"commitment"`
	AppHash     string                `json:"app_hash"`
	Proposers   map[string]string     `json:"proposers"`
	State       state.Snapshot        `json:"state"`
	Txs         [][]byte              `json:"txs"`
	Receipts    []types.Receipt       `json:"receipts"`
	Checksum    string                `json:"checksum"`
}

type applicationSnapshotMetadata struct {
	Protocol    string `json:"protocol"`
	GenesisHash string `json:"genesis_hash"`
	Height      uint64 `json:"height"`
	AppHash     string `json:"app_hash"`
	StateRoot   string `json:"state_root"`
	TotalBytes  uint64 `json:"total_bytes"`
	Chunks      uint32 `json:"chunks"`
	Checksum    string `json:"checksum"`
}

type applicationSnapshotCache struct {
	height   uint64
	raw      []byte
	hash     []byte
	metadata []byte
	chunks   uint32
}

type incomingApplicationSnapshot struct {
	snapshot       abcitypes.Snapshot
	metadata       applicationSnapshotMetadata
	trustedAppHash []byte
	chunks         [][]byte
	received       []bool
	receivedCount  uint32
	receivedBytes  uint64
}

func (a *Application) ListSnapshots(
	context.Context,
	*abcitypes.RequestListSnapshots,
) (*abcitypes.ResponseListSnapshots, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errors.New("application is closed")
	}
	if a.persistence == nil || !a.initialized || a.committed.commitment.Height <= 0 || a.haltErr != nil {
		return &abcitypes.ResponseListSnapshots{}, nil
	}
	snapshot, err := a.snapshotLocked()
	if err != nil {
		a.haltErr = err
		return nil, err
	}
	return &abcitypes.ResponseListSnapshots{Snapshots: []*abcitypes.Snapshot{{
		Height: snapshot.height, Format: applicationSnapshotFormat, Chunks: snapshot.chunks,
		Hash: cloneBytes(snapshot.hash), Metadata: cloneBytes(snapshot.metadata),
	}}}, nil
}

func (a *Application) LoadSnapshotChunk(
	_ context.Context,
	req *abcitypes.RequestLoadSnapshotChunk,
) (*abcitypes.ResponseLoadSnapshotChunk, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil, errors.New("application is closed")
	}
	if req == nil || req.Format != applicationSnapshotFormat {
		return &abcitypes.ResponseLoadSnapshotChunk{}, nil
	}
	if a.persistence == nil || !a.initialized || a.committed.commitment.Height <= 0 || a.haltErr != nil {
		return &abcitypes.ResponseLoadSnapshotChunk{}, nil
	}
	snapshot := a.snapshotForHeightLocked(req.Height)
	if snapshot == nil {
		var err error
		snapshot, err = a.snapshotLocked()
		if err != nil {
			return nil, err
		}
	}
	if req.Height != snapshot.height || req.Chunk >= snapshot.chunks {
		return &abcitypes.ResponseLoadSnapshotChunk{}, nil
	}
	start := uint64(req.Chunk) * applicationSnapshotChunkBytes
	end := min(start+applicationSnapshotChunkBytes, uint64(len(snapshot.raw)))
	return &abcitypes.ResponseLoadSnapshotChunk{Chunk: cloneBytes(snapshot.raw[start:end])}, nil
}

func (a *Application) OfferSnapshot(
	_ context.Context,
	req *abcitypes.RequestOfferSnapshot,
) (*abcitypes.ResponseOfferSnapshot, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	response := &abcitypes.ResponseOfferSnapshot{Result: abcitypes.ResponseOfferSnapshot_REJECT}
	if a.closed || a.haltErr != nil || a.persistence == nil {
		response.Result = abcitypes.ResponseOfferSnapshot_ABORT
		return response, nil
	}
	if req == nil || req.Snapshot == nil {
		return response, nil
	}
	offered := req.Snapshot
	if offered.Format != applicationSnapshotFormat {
		response.Result = abcitypes.ResponseOfferSnapshot_REJECT_FORMAT
		return response, nil
	}
	if a.initialized || a.committed.commitment.Height != 0 || a.candidate != nil {
		response.Result = abcitypes.ResponseOfferSnapshot_ABORT
		return response, nil
	}
	metadata, err := decodeApplicationSnapshotMetadata(offered.Metadata)
	if err != nil || offered.Height == 0 || offered.Height > math.MaxInt64 ||
		offered.Chunks == 0 || len(offered.Hash) != 32 || len(req.AppHash) != 32 {
		return response, nil
	}
	genesisHash, err := applicationGenesisHash(a.genesis)
	if err != nil {
		response.Result = abcitypes.ResponseOfferSnapshot_ABORT
		return response, nil
	}
	expectedAppHash, err := decodeCanonicalSnapshotAppHash(metadata.AppHash)
	expectedContentHash, contentHashErr := decodeCanonicalHash(metadata.Checksum)
	if err != nil || metadata.Protocol != applicationSnapshotProtocol ||
		metadata.GenesisHash != genesisHash || metadata.Height != offered.Height ||
		metadata.Chunks != offered.Chunks || metadata.StateRoot == "" ||
		metadata.TotalBytes == 0 || metadata.TotalBytes > maxApplicationSnapshotBytes ||
		contentHashErr != nil || !bytes.Equal(expectedContentHash, offered.Hash) ||
		!bytes.Equal(expectedAppHash, req.AppHash) {
		return response, nil
	}
	expectedChunks := uint32((metadata.TotalBytes + applicationSnapshotChunkBytes - 1) / applicationSnapshotChunkBytes)
	if expectedChunks != offered.Chunks {
		return response, nil
	}
	a.incomingSnapshot = &incomingApplicationSnapshot{
		snapshot: abcitypes.Snapshot{
			Height: offered.Height, Format: offered.Format, Chunks: offered.Chunks,
			Hash: cloneBytes(offered.Hash), Metadata: cloneBytes(offered.Metadata),
		},
		metadata: metadata, trustedAppHash: cloneBytes(req.AppHash),
		chunks: make([][]byte, offered.Chunks), received: make([]bool, offered.Chunks),
	}
	response.Result = abcitypes.ResponseOfferSnapshot_ACCEPT
	return response, nil
}

func (a *Application) ApplySnapshotChunk(
	_ context.Context,
	req *abcitypes.RequestApplySnapshotChunk,
) (*abcitypes.ResponseApplySnapshotChunk, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.haltErr != nil {
		return &abcitypes.ResponseApplySnapshotChunk{Result: abcitypes.ResponseApplySnapshotChunk_ABORT}, nil
	}
	incoming := a.incomingSnapshot
	if req == nil || incoming == nil {
		return &abcitypes.ResponseApplySnapshotChunk{Result: abcitypes.ResponseApplySnapshotChunk_ABORT}, nil
	}
	if req.Index >= incoming.snapshot.Chunks {
		a.incomingSnapshot = nil
		return rejectApplicationSnapshot(req.Sender), nil
	}
	expectedBytes := uint64(applicationSnapshotChunkBytes)
	if req.Index == incoming.snapshot.Chunks-1 {
		expectedBytes = incoming.metadata.TotalBytes - uint64(req.Index)*applicationSnapshotChunkBytes
	}
	if uint64(len(req.Chunk)) != expectedBytes {
		a.incomingSnapshot = nil
		return rejectApplicationSnapshot(req.Sender), nil
	}
	if incoming.received[req.Index] {
		if bytes.Equal(incoming.chunks[req.Index], req.Chunk) {
			return &abcitypes.ResponseApplySnapshotChunk{Result: abcitypes.ResponseApplySnapshotChunk_ACCEPT}, nil
		}
		incoming.received[req.Index] = false
		incoming.receivedCount--
		incoming.receivedBytes -= uint64(len(incoming.chunks[req.Index]))
		incoming.chunks[req.Index] = nil
		return &abcitypes.ResponseApplySnapshotChunk{
			Result:        abcitypes.ResponseApplySnapshotChunk_RETRY,
			RefetchChunks: []uint32{req.Index}, RejectSenders: nonEmptySender(req.Sender),
		}, nil
	}
	incoming.chunks[req.Index] = cloneBytes(req.Chunk)
	incoming.received[req.Index] = true
	incoming.receivedCount++
	incoming.receivedBytes += uint64(len(req.Chunk))
	if incoming.receivedBytes > incoming.metadata.TotalBytes {
		a.incomingSnapshot = nil
		return rejectApplicationSnapshot(req.Sender), nil
	}
	if incoming.receivedCount != incoming.snapshot.Chunks {
		return &abcitypes.ResponseApplySnapshotChunk{Result: abcitypes.ResponseApplySnapshotChunk_ACCEPT}, nil
	}
	raw := make([]byte, 0, incoming.metadata.TotalBytes)
	for _, chunk := range incoming.chunks {
		raw = append(raw, chunk...)
	}
	if uint64(len(raw)) != incoming.metadata.TotalBytes ||
		!bytes.Equal(hash.Keccak(raw), incoming.snapshot.Hash) ||
		hash.KeccakHex(raw) != incoming.metadata.Checksum {
		a.incomingSnapshot = nil
		return rejectApplicationSnapshot(req.Sender), nil
	}
	restored, err := decodeApplicationSnapshotDocument(a.genesis, raw)
	if err != nil || restored.committed.commitment.Height != int64(incoming.snapshot.Height) ||
		restored.committed.commitment.StateRoot != incoming.metadata.StateRoot ||
		!bytes.Equal(restored.committed.appHash, incoming.trustedAppHash) {
		a.incomingSnapshot = nil
		return rejectApplicationSnapshot(req.Sender), nil
	}
	if err := a.persistence.Restore(restored); err != nil {
		a.haltErr = fmt.Errorf("restore application snapshot: %w", err)
		a.incomingSnapshot = nil
		return &abcitypes.ResponseApplySnapshotChunk{Result: abcitypes.ResponseApplySnapshotChunk_ABORT}, nil
	}
	a.committed = restored.committed
	a.committedTxs = cloneTransactions(restored.txs)
	a.committedReceipts = cloneReceipts(restored.receipts)
	a.initialized = restored.initialized
	a.proposers = cloneStringMap(restored.proposers)
	a.candidate = nil
	a.mempool = newAppMempool(restored.committed.store)
	a.snapshotCache = &applicationSnapshotCache{
		height: incoming.snapshot.Height, raw: cloneBytes(raw), hash: cloneBytes(incoming.snapshot.Hash),
		metadata: cloneBytes(incoming.snapshot.Metadata), chunks: incoming.snapshot.Chunks,
	}
	a.previousSnapshotCache = nil
	a.incomingSnapshot = nil
	return &abcitypes.ResponseApplySnapshotChunk{Result: abcitypes.ResponseApplySnapshotChunk_ACCEPT}, nil
}

func (a *Application) snapshotLocked() (*applicationSnapshotCache, error) {
	currentHeight := uint64(a.committed.commitment.Height)
	if a.snapshotCache != nil && currentHeight >= a.snapshotCache.height &&
		currentHeight-a.snapshotCache.height < applicationSnapshotInterval {
		return a.snapshotCache, nil
	}
	genesisHash, err := applicationGenesisHash(a.genesis)
	if err != nil {
		return nil, err
	}
	document := applicationSnapshotDocument{
		Protocol: applicationSnapshotProtocol, GenesisHash: genesisHash,
		Height: a.committed.commitment.Height, Initialized: a.initialized,
		Commitment: a.committed.commitment, AppHash: hex.EncodeToString(a.committed.appHash),
		Proposers: cloneStringMap(a.proposers), State: a.committed.store.Snapshot(),
		Txs: cloneTransactions(a.committedTxs), Receipts: cloneReceipts(a.committedReceipts),
	}
	persisted := persistedApplicationState{
		committed: a.committed, initialized: a.initialized, proposers: cloneStringMap(a.proposers),
		txs: cloneTransactions(a.committedTxs), receipts: cloneReceipts(a.committedReceipts),
	}
	if err := validatePersistedApplicationState(a.genesis, genesisHash, persisted); err != nil {
		return nil, err
	}
	document.Checksum, err = applicationSnapshotChecksum(document)
	if err != nil {
		return nil, err
	}
	raw, err := hash.CanonicalBytes(document)
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 || len(raw) > maxApplicationSnapshotBytes {
		return nil, fmt.Errorf("application snapshot exceeds %d bytes", maxApplicationSnapshotBytes)
	}
	chunks := uint32((uint64(len(raw)) + applicationSnapshotChunkBytes - 1) / applicationSnapshotChunkBytes)
	contentHash := hash.Keccak(raw)
	metadata := applicationSnapshotMetadata{
		Protocol: applicationSnapshotProtocol, GenesisHash: genesisHash,
		Height: uint64(document.Height), AppHash: document.AppHash,
		StateRoot: document.Commitment.StateRoot, TotalBytes: uint64(len(raw)),
		Chunks: chunks, Checksum: hash.KeccakHex(raw),
	}
	metadataRaw, err := hash.CanonicalBytes(metadata)
	if err != nil {
		return nil, err
	}
	if len(metadataRaw) > maxApplicationSnapshotMetadata {
		return nil, errors.New("application snapshot metadata is too large")
	}
	next := &applicationSnapshotCache{
		height: uint64(document.Height), raw: raw, hash: contentHash,
		metadata: metadataRaw, chunks: chunks,
	}
	if a.snapshotCache != nil && len(a.snapshotCache.raw)+len(next.raw) <= maxApplicationSnapshotBytes {
		a.previousSnapshotCache = a.snapshotCache
	} else {
		a.previousSnapshotCache = nil
	}
	a.snapshotCache = next
	return a.snapshotCache, nil
}

func (a *Application) snapshotForHeightLocked(height uint64) *applicationSnapshotCache {
	if a.snapshotCache != nil && a.snapshotCache.height == height {
		return a.snapshotCache
	}
	if a.previousSnapshotCache != nil && a.previousSnapshotCache.height == height {
		return a.previousSnapshotCache
	}
	return nil
}

func decodeApplicationSnapshotMetadata(raw []byte) (applicationSnapshotMetadata, error) {
	if len(raw) == 0 || len(raw) > maxApplicationSnapshotMetadata {
		return applicationSnapshotMetadata{}, errors.New("application snapshot metadata size is invalid")
	}
	var metadata applicationSnapshotMetadata
	if err := decodeCanonicalJSON(raw, &metadata); err != nil {
		return applicationSnapshotMetadata{}, err
	}
	if err := types.ValidateCanonicalHash("application snapshot state root", metadata.StateRoot); err != nil {
		return applicationSnapshotMetadata{}, err
	}
	if err := types.ValidateCanonicalHash("application snapshot checksum", metadata.Checksum); err != nil {
		return applicationSnapshotMetadata{}, err
	}
	return metadata, nil
}

func decodeApplicationSnapshotDocument(
	genesis GenesisDocument,
	raw []byte,
) (persistedApplicationState, error) {
	if len(raw) == 0 || len(raw) > maxApplicationSnapshotBytes {
		return persistedApplicationState{}, errors.New("application snapshot size is invalid")
	}
	var document applicationSnapshotDocument
	if err := decodeCanonicalJSON(raw, &document); err != nil {
		return persistedApplicationState{}, err
	}
	genesisHash, err := applicationGenesisHash(genesis)
	if err != nil {
		return persistedApplicationState{}, err
	}
	checksum, err := applicationSnapshotChecksum(document)
	if err != nil {
		return persistedApplicationState{}, err
	}
	if document.Protocol != applicationSnapshotProtocol || document.GenesisHash != genesisHash ||
		document.Height <= 0 || document.Height != document.Commitment.Height ||
		document.Checksum != checksum || !document.Initialized {
		return persistedApplicationState{}, errors.New("application snapshot identity or checksum is invalid")
	}
	store, err := state.NewStoreFromSnapshot(document.State)
	if err != nil {
		return persistedApplicationState{}, err
	}
	flatTree, err := buildStoreSparseTree(store)
	if err != nil {
		return persistedApplicationState{}, err
	}
	appHash, err := decodeCanonicalSnapshotAppHash(document.AppHash)
	if err != nil {
		return persistedApplicationState{}, err
	}
	value := persistedApplicationState{
		committed: committedState{
			store: store, flatTree: flatTree, commitment: document.Commitment, appHash: appHash,
		},
		initialized: true, proposers: cloneStringMap(document.Proposers),
		txs: cloneTransactions(document.Txs), receipts: cloneReceipts(document.Receipts),
	}
	if err := validatePersistedApplicationState(genesis, genesisHash, value); err != nil {
		return persistedApplicationState{}, err
	}
	return value, nil
}

func applicationSnapshotChecksum(document applicationSnapshotDocument) (string, error) {
	document.Checksum = ""
	return hash.Hex(document)
}

func applicationGenesisHash(genesis GenesisDocument) (string, error) {
	raw, err := genesis.CanonicalBytes()
	if err != nil {
		return "", err
	}
	return hash.KeccakHex(raw), nil
}

func decodeCanonicalSnapshotAppHash(value string) ([]byte, error) {
	raw, err := hex.DecodeString(value)
	if err != nil || len(raw) != 32 || hex.EncodeToString(raw) != value {
		return nil, errors.New("application snapshot app hash is not canonical")
	}
	return raw, nil
}

func decodeCanonicalHash(value string) ([]byte, error) {
	if err := types.ValidateCanonicalHash("application snapshot hash", value); err != nil {
		return nil, err
	}
	raw, err := hex.DecodeString(value[2:])
	if err != nil || len(raw) != 32 {
		return nil, errors.New("application snapshot hash is invalid")
	}
	return raw, nil
}

func rejectApplicationSnapshot(sender string) *abcitypes.ResponseApplySnapshotChunk {
	return &abcitypes.ResponseApplySnapshotChunk{
		Result:        abcitypes.ResponseApplySnapshotChunk_REJECT_SNAPSHOT,
		RejectSenders: nonEmptySender(sender),
	}
}

func nonEmptySender(sender string) []string {
	if sender == "" {
		return nil
	}
	return []string{sender}
}
