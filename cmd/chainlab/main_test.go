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
