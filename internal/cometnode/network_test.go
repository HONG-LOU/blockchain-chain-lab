package cometnode

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	chainabci "chainlab/internal/abci"
	chaintypes "chainlab/internal/types"

	cmtcfg "github.com/cometbft/cometbft/config"
	cmtsecp256k1 "github.com/cometbft/cometbft/crypto/secp256k1"
	cmttypes "github.com/cometbft/cometbft/types"
)

func TestInitializeNetworkBuildsFourStrictIndependentValidatorHomes(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "network")
	genesisTime := time.Date(2026, time.July, 10, 12, 0, 0, 0, time.UTC)
	document, err := InitializeNetwork(NetworkConfig{
		OutputRoot:     root,
		ChainID:        "chainlab-process-test",
		ValidatorCount: 4,
		GenesisTime:    genesisTime,
		ABCIBasePort:   31000,
		RPCBasePort:    31100,
		P2PBasePort:    31200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if document.Protocol != NetworkProtocol || document.GenesisTime != genesisTime.Format(time.RFC3339Nano) || len(document.Nodes) != 4 {
		t.Fatalf("network document = %+v", document)
	}

	var canonicalAppGenesis []byte
	seenChainAddresses := make(map[string]struct{}, len(document.Nodes))
	seenConsensusAddresses := make(map[string]struct{}, len(document.Nodes))
	for index, generatedNode := range document.Nodes {
		home := filepath.Join(root, generatedNode.Home)
		nodeDocument, err := LoadNodeDocument(home)
		if err != nil {
			t.Fatal(err)
		}
		config, err := BuildConfig(home, nodeDocument)
		if err != nil {
			t.Fatal(err)
		}
		if config.Mempool.Type != cmtcfg.MempoolTypeFlood || config.Mempool.Size != chaintypes.MaxTransactionsPerBlock || config.Mempool.MaxTxBytes != chaintypes.MaxTransactionBytes {
			t.Fatalf("node %d mempool config = %+v", index, config.Mempool)
		}
		if !config.Consensus.CreateEmptyBlocks || config.Consensus.SkipTimeoutCommit || config.Consensus.TimeoutCommit != 750*time.Millisecond {
			t.Fatalf("node %d consensus timing = %+v", index, config.Consensus)
		}
		if config.ProxyApp != generatedNode.ABCIListenAddress || config.RPC.ListenAddress != generatedNode.RPCListenAddress || config.P2P.ListenAddress != generatedNode.P2PListenAddress {
			t.Fatalf("node %d addresses do not match manifest", index)
		}
		if peers := strings.Split(nodeDocument.PersistentPeers, ","); len(peers) != 3 {
			t.Fatalf("node %d persistent peers = %q", index, nodeDocument.PersistentPeers)
		}

		appGenesisRaw, err := os.ReadFile(filepath.Join(home, filepath.FromSlash(AppGenesisPath)))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(appGenesisRaw, []byte("priv_key")) || bytes.Contains(appGenesisRaw, []byte("private_key")) {
			t.Fatal("application genesis contains private-key material")
		}
		appGenesis, err := chainabci.ParseGenesisDocument(appGenesisRaw)
		if err != nil {
			t.Fatal(err)
		}
		if len(appGenesis.State.Validators) != 4 || appGenesis.BlockGasLimit != chaintypes.DefaultBlockGasLimit {
			t.Fatalf("application genesis = %+v", appGenesis)
		}
		if index == 0 {
			canonicalAppGenesis = appGenesisRaw
		} else if !bytes.Equal(canonicalAppGenesis, appGenesisRaw) {
			t.Fatalf("node %d application genesis differs", index)
		}

		cometGenesis, err := cmttypes.GenesisDocFromFile(config.GenesisFile())
		if err != nil {
			t.Fatal(err)
		}
		if len(cometGenesis.Validators) != 4 || cometGenesis.ConsensusParams.Block.MaxBytes != chaintypes.MaxBlockBytes || cometGenesis.ConsensusParams.Block.MaxGas != int64(chaintypes.DefaultBlockGasLimit) {
			t.Fatalf("CometBFT genesis = %+v", cometGenesis)
		}
		if len(cometGenesis.ConsensusParams.Validator.PubKeyTypes) != 1 || cometGenesis.ConsensusParams.Validator.PubKeyTypes[0] != cmtsecp256k1.KeyType {
			t.Fatalf("validator key types = %v", cometGenesis.ConsensusParams.Validator.PubKeyTypes)
		}
		if _, exists := seenChainAddresses[generatedNode.ChainLabAddress]; exists {
			t.Fatalf("duplicate ChainLab address %s", generatedNode.ChainLabAddress)
		}
		seenChainAddresses[generatedNode.ChainLabAddress] = struct{}{}
		if _, exists := seenConsensusAddresses[generatedNode.ConsensusAddress]; exists {
			t.Fatalf("duplicate consensus address %s", generatedNode.ConsensusAddress)
		}
		seenConsensusAddresses[generatedNode.ConsensusAddress] = struct{}{}
	}

	networkRaw, err := os.ReadFile(filepath.Join(root, NetworkDocumentPath))
	if err != nil {
		t.Fatal(err)
	}
	canonicalNetwork, err := document.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(networkRaw, canonicalNetwork) {
		t.Fatal("network manifest is not canonical")
	}
}

func TestInitializeNetworkRefusesExistingOutputWithoutMutation(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "marker")
	if err := os.WriteFile(marker, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := InitializeNetwork(NetworkConfig{
		OutputRoot:   root,
		ChainID:      "chainlab-existing-output",
		ABCIBasePort: 32000,
		RPCBasePort:  32100,
		P2PBasePort:  32200,
	})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("unexpected error: %v", err)
	}
	raw, err := os.ReadFile(marker)
	if err != nil || string(raw) != "preserve" {
		t.Fatalf("existing output was changed: %q err=%v", raw, err)
	}
}

func TestLoadNodeDocumentRejectsNonCanonicalAndUnknownInput(t *testing.T) {
	root := filepath.Join(t.TempDir(), "network")
	_, err := InitializeNetwork(NetworkConfig{
		OutputRoot:     root,
		ChainID:        "chainlab-node-document",
		ValidatorCount: 1,
		ABCIBasePort:   33000,
		RPCBasePort:    33100,
		P2PBasePort:    33200,
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "node0", filepath.FromSlash(NodeDocumentPath))
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadNodeDocument(filepath.Join(root, "node0")); err == nil || !strings.Contains(err.Error(), "not canonically encoded") {
		t.Fatalf("unexpected non-canonical error: %v", err)
	}
	unknown := bytes.Replace(raw, []byte("}"), []byte(",\"unknown\":true}"), 1)
	if err := os.WriteFile(path, unknown, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadNodeDocument(filepath.Join(root, "node0")); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unexpected unknown-field error: %v", err)
	}
}
