package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAndReadGenesisFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "genesis.json")
	genesis, err := createGenesisFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if genesis.ChainID != "chainlab-local" {
		t.Fatalf("chain id = %q", genesis.ChainID)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fromDisk GenesisFile
	if err := json.Unmarshal(raw, &fromDisk); err != nil {
		t.Fatal(err)
	}
	if fromDisk.Proposer == "" || fromDisk.PrivateKey == "" {
		t.Fatalf("genesis missing proposer credentials: %+v", fromDisk)
	}

	loaded, err := readGenesisFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Proposer != fromDisk.Proposer {
		t.Fatalf("loaded proposer = %q", loaded.Proposer)
	}
}

func TestBuildNodeConfigUsesDataDir(t *testing.T) {
	key, err := createGenesisFile(filepath.Join(t.TempDir(), "genesis.json"))
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	config, err := buildNodeConfig(nodeOptions{
		GenesisPath: keyPath(t, key),
		DataDir:     dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.DataDir != dataDir {
		t.Fatalf("data dir = %q", config.DataDir)
	}
	if config.ChainID != "chainlab-local" {
		t.Fatalf("chain id = %q", config.ChainID)
	}
}

func keyPath(t *testing.T, genesis GenesisFile) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "genesis.json")
	raw, err := json.Marshal(genesis)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
