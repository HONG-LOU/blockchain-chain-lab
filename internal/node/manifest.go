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
	dataManifestVersion  uint64 = 1
	maxDataManifestBytes int64  = 4 * 1024
)

type dataManifest struct {
	Version         uint64 `json:"version"`
	SnapshotVersion uint64 `json:"snapshot_version"`
	ChainID         string `json:"chain_id"`
	GenesisHash     string `json:"genesis_hash"`
	Checksum        string `json:"checksum"`
}

func ensureDataManifest(dataDir string, chainID string, genesisHash string) error {
	expected := dataManifest{
		Version:         dataManifestVersion,
		SnapshotVersion: diskSnapshotVersion,
		ChainID:         chainID,
		GenesisHash:     genesisHash,
	}
	existing, err := loadDataManifest(dataDir)
	if err != nil {
		return err
	}
	if existing != nil {
		if existing.Version != expected.Version ||
			existing.SnapshotVersion != expected.SnapshotVersion ||
			existing.ChainID != expected.ChainID ||
			existing.GenesisHash != expected.GenesisHash {
			return errors.New("data manifest does not match the candidate chain")
		}
		return nil
	}
	return writeDataManifest(dataDir, expected)
}

func migrateDataManifest(dataDir string, chainID string, genesisHash string) error {
	existing, err := loadDataManifest(dataDir)
	if err != nil {
		return err
	}
	if existing == nil {
		return errors.New("cannot migrate a missing data manifest")
	}
	if existing.ChainID != chainID || existing.GenesisHash != genesisHash {
		return errors.New("legacy data manifest does not match the persisted chain")
	}
	if existing.SnapshotVersion == diskSnapshotVersion {
		return nil
	}
	if existing.SnapshotVersion != legacyDiskSnapshotVersion {
		return errors.New("data manifest snapshot version requires an explicit migration")
	}
	return writeDataManifest(dataDir, dataManifest{
		Version:         dataManifestVersion,
		SnapshotVersion: diskSnapshotVersion,
		ChainID:         chainID,
		GenesisHash:     genesisHash,
	})
}

func writeDataManifest(dataDir string, manifest dataManifest) error {
	checksum, err := dataManifestChecksum(manifest)
	if err != nil {
		return err
	}
	manifest.Checksum = checksum
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if int64(len(raw)+1) > maxDataManifestBytes {
		return fmt.Errorf("data manifest exceeds %d bytes", maxDataManifestBytes)
	}
	return writeFileAtomically(filepath.Join(dataDir, "manifest.json"), append(raw, '\n'), 0o600)
}

func loadDataManifest(dataDir string) (*dataManifest, error) {
	raw, err := readBoundedRegularFile(filepath.Join(dataDir, "manifest.json"), "data manifest", maxDataManifestBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var manifest dataManifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("decode data manifest: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("data manifest must contain exactly one JSON value")
		}
		return nil, fmt.Errorf("decode trailing data manifest data: %w", err)
	}
	if manifest.Version != dataManifestVersion || !supportedDiskSnapshotVersion(manifest.SnapshotVersion) {
		return nil, errors.New("data manifest version requires an explicit migration")
	}
	if err := types.ValidateChainID(manifest.ChainID); err != nil {
		return nil, fmt.Errorf("data manifest chain id: %w", err)
	}
	if err := types.ValidateCanonicalHash("data manifest genesis hash", manifest.GenesisHash); err != nil {
		return nil, err
	}
	if err := types.ValidateCanonicalHash("data manifest checksum", manifest.Checksum); err != nil {
		return nil, err
	}
	expected, err := dataManifestChecksum(manifest)
	if err != nil {
		return nil, err
	}
	if manifest.Checksum != expected {
		return nil, errors.New("data manifest checksum mismatch")
	}
	return &manifest, nil
}

func validateDataManifestSnapshotCompatibility(manifest dataManifest, snapshot diskSnapshot) error {
	if manifest.SnapshotVersion == diskSnapshotVersion && snapshot.Version == legacyDiskSnapshotVersion {
		return errors.New("persisted snapshot version is older than the data manifest; rollback is not allowed")
	}
	if manifest.SnapshotVersion == legacyDiskSnapshotVersion &&
		(snapshot.Version == legacyDiskSnapshotVersion || snapshot.Version == diskSnapshotVersion) {
		return nil
	}
	if manifest.SnapshotVersion != snapshot.Version {
		return errors.New("data manifest snapshot version does not match persisted snapshot")
	}
	return nil
}

func dataManifestChecksum(manifest dataManifest) (string, error) {
	manifest.Checksum = ""
	checksum, err := hash.Hex(manifest)
	if err != nil {
		return "", fmt.Errorf("checksum data manifest: %w", err)
	}
	return checksum, nil
}
