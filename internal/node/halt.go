package node

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"chainlab/internal/hash"
	"chainlab/internal/types"
)

const (
	haltRecordVersion  uint64 = 1
	maxHaltRecordBytes int64  = 64 * 1024
)

type haltRecord struct {
	Version  uint64 `json:"version"`
	ChainID  string `json:"chain_id"`
	Kind     string `json:"kind"`
	Reason   string `json:"reason"`
	Checksum string `json:"checksum"`
}

func (n *Node) setPersistentHaltLocked(kind string, cause error) {
	if cause == nil || n.haltErr != nil {
		return
	}
	n.haltErr = cause
	if n.dataDir == "" {
		return
	}
	if err := persistHaltRecord(n.dataDir, haltRecord{
		Version: haltRecordVersion,
		ChainID: n.chainID,
		Kind:    kind,
		Reason:  cause.Error(),
	}); err != nil {
		n.haltErr = errors.Join(cause, fmt.Errorf("persist halt marker: %w", err))
	}
}

func (n *Node) restorePersistentHalt() error {
	if n.dataDir == "" {
		return nil
	}
	record, err := loadHaltRecord(n.dataDir)
	if err != nil {
		return err
	}
	if record == nil {
		return nil
	}
	if record.ChainID != n.chainID {
		return errors.New("persisted halt marker chain id does not match config")
	}
	n.haltErr = fmt.Errorf("persisted %s halt: %s", record.Kind, record.Reason)
	return nil
}

func persistHaltRecord(dataDir string, record haltRecord) error {
	if record.Version != haltRecordVersion || record.ChainID == "" || record.Kind == "" || record.Reason == "" {
		return errors.New("halt marker is incomplete")
	}
	record.Checksum = ""
	checksum, err := hash.Hex(record)
	if err != nil {
		return fmt.Errorf("checksum halt marker: %w", err)
	}
	record.Checksum = checksum
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	if int64(len(raw)+1) > maxHaltRecordBytes {
		return fmt.Errorf("halt marker exceeds %d bytes", maxHaltRecordBytes)
	}
	return writeFileAtomically(filepath.Join(dataDir, "HALT.json"), append(raw, '\n'), 0o600)
}

func loadHaltRecord(dataDir string) (*haltRecord, error) {
	raw, err := readBoundedRegularFile(filepath.Join(dataDir, "HALT.json"), "persisted halt marker", maxHaltRecordBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var record haltRecord
	if err := decoder.Decode(&record); err != nil {
		return nil, fmt.Errorf("decode persisted halt marker: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("persisted halt marker must contain exactly one JSON value")
		}
		return nil, fmt.Errorf("decode trailing persisted halt marker data: %w", err)
	}
	if record.Version != haltRecordVersion {
		return nil, fmt.Errorf("unsupported persisted halt marker version %d", record.Version)
	}
	if record.ChainID == "" || record.Kind == "" || record.Reason == "" {
		return nil, errors.New("persisted halt marker is incomplete")
	}
	if err := types.ValidateCanonicalHash("persisted halt marker checksum", record.Checksum); err != nil {
		return nil, err
	}
	persistedChecksum := record.Checksum
	record.Checksum = ""
	expectedChecksum, err := hash.Hex(record)
	if err != nil {
		return nil, err
	}
	record.Checksum = persistedChecksum
	if persistedChecksum != expectedChecksum {
		return nil, errors.New("persisted halt marker checksum mismatch")
	}
	return &record, nil
}
