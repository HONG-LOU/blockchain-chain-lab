package main

import (
	"encoding/json"
	"flag"
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

func TestPeerListFlagAcceptsRepeatedPeers(t *testing.T) {
	flags := flag.NewFlagSet("node", flag.ContinueOnError)
	var peers peerListFlag
	flags.Var(&peers, "peer", "peer URL")

	if err := flags.Parse([]string{"--peer", "http://127.0.0.1:18547", "--peer", "http://127.0.0.1:18548"}); err != nil {
		t.Fatal(err)
	}
	got := peers.Values()
	if len(got) != 2 {
		t.Fatalf("peer count = %d", len(got))
	}
	if got[0] != "http://127.0.0.1:18547" || got[1] != "http://127.0.0.1:18548" {
		t.Fatalf("peers = %#v", got)
	}
}
