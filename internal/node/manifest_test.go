package node

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	chaincrypto "chainlab/internal/crypto"
)

func TestInitializedDataDirectoryRejectsMissingChainOrManifest(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := chaincrypto.AddressFromPrivateKey(key)
	config := Config{Role: RoleValidator, ChainID: "chainlab-local", ProposerKey: key, Validators: []string{proposer}, GenesisTimeUnix: DeterministicDevGenesisTimeUnix}

	for _, missing := range []string{"chain.json", "manifest.json"} {
		t.Run(missing, func(t *testing.T) {
			dataDir := t.TempDir()
			config.DataDir = dataDir
			n, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			closeTestNode(t, n)
			if err := os.Remove(filepath.Join(dataDir, missing)); err != nil {
				t.Fatal(err)
			}
			if _, err := New(config); err == nil || !strings.Contains(err.Error(), "missing") {
				t.Fatalf("missing %s error = %v", missing, err)
			}
		})
	}
}

func TestDataManifestTamperingFailsClosed(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	proposer := chaincrypto.AddressFromPrivateKey(key)
	config := Config{Role: RoleValidator, ChainID: "chainlab-local", ProposerKey: key, Validators: []string{proposer}, DataDir: dataDir, GenesisTimeUnix: DeterministicDevGenesisTimeUnix}
	n, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	closeTestNode(t, n)
	path := filepath.Join(dataDir, "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	tampered := strings.Replace(string(raw), `"chain_id": "chainlab-local"`, `"chain_id": "other"`, 1)
	if err := os.WriteFile(path, []byte(tampered), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(config); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered manifest error = %v", err)
	}
}

func TestLoadDiskSnapshotRejectsOversizeBeforeRead(t *testing.T) {
	dataDir := t.TempDir()
	path := chainPath(dataDir)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(MaxDiskSnapshotBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := loadDiskSnapshot(dataDir); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize snapshot error = %v", err)
	}
}
