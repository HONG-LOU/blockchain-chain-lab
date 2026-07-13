package cometnode

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	cmtcfg "github.com/cometbft/cometbft/config"
)

func TestObserverConfigRequiresNoValidatorSigningMaterial(t *testing.T) {
	root := filepath.Join(t.TempDir(), "network")
	network, err := InitializeNetwork(NetworkConfig{
		OutputRoot: root, ChainID: "chainlab-observer-config", ValidatorCount: 1,
		ABCIBasePort: 26658, RPCBasePort: 26670, P2PBasePort: 26680,
	})
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, network.Nodes[0].Home)
	document, err := LoadNodeDocument(home)
	if err != nil {
		t.Fatal(err)
	}
	validatorConfig, err := BuildConfig(home, document)
	if err != nil {
		t.Fatal(err)
	}
	document.Role = RoleObserver
	raw, err := document.CanonicalBytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, filepath.FromSlash(NodeDocumentPath)), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{validatorConfig.PrivValidatorKeyFile(), validatorConfig.PrivValidatorStateFile()} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := BuildConfig(home, document); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(validatorConfig.PrivValidatorKeyFile()); !os.IsNotExist(err) {
		t.Fatalf("observer validator key exists: %v", err)
	}
}

func TestApplyConsensusOptions(t *testing.T) {
	config := cmtcfg.DefaultConfig()
	options := ConsensusOptions{CreateEmptyBlocks: true, CreateEmptyBlocksInterval: 5 * time.Second, TimeoutCommit: 5 * time.Second}
	if err := applyConsensusOptions(config, options); err != nil {
		t.Fatal(err)
	}
	if !config.Consensus.CreateEmptyBlocks || config.Consensus.CreateEmptyBlocksInterval != 5*time.Second || config.Consensus.TimeoutCommit != 5*time.Second {
		t.Fatalf("consensus config = %+v", config.Consensus)
	}
}

func TestApplyConsensusOptionsRejectsUnboundedManagedCadence(t *testing.T) {
	config := cmtcfg.DefaultConfig()
	if err := applyConsensusOptions(config, ConsensusOptions{CreateEmptyBlocks: true}); err == nil {
		t.Fatal("expected zero managed empty-block interval to fail")
	}
}

func TestManagedDBProviderReleasesTransactionIndex(t *testing.T) {
	root := filepath.Join(t.TempDir(), "comet-home")
	config := cmtcfg.DefaultConfig().SetRoot(root)
	provider := &managedDBProvider{}
	database, err := provider.open(&cmtcfg.DBContext{ID: "tx_index", Config: config})
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Set([]byte("key"), []byte("value")); err != nil {
		t.Fatal(err)
	}
	if err := provider.close(); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatalf("remove closed transaction index: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("transaction index root still exists: %v", err)
	}
}
