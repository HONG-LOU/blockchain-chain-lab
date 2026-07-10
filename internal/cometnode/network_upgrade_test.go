package cometnode

import (
	"path/filepath"
	"testing"
	"time"

	chainabci "chainlab/internal/abci"
)

func TestInitializeNetworkPersistsProtocolUpgradeSchedule(t *testing.T) {
	policy := chainabci.DefaultValidatorPolicy()
	root := filepath.Join(t.TempDir(), "upgrade-network")
	document, err := InitializeNetwork(NetworkConfig{
		OutputRoot: root, ChainID: "chainlab-upgrade-network", ValidatorCount: 1,
		GenesisTime:  time.Date(2026, time.July, 11, 0, 0, 0, 0, time.UTC),
		ABCIBasePort: 35000, RPCBasePort: 35100, P2PBasePort: 35200,
		ApplicationProtocol: chainabci.ProtocolVersionV2, ValidatorPolicy: &policy,
		ProtocolUpgrades: []chainabci.ProtocolUpgrade{{Height: 10, Protocol: chainabci.ProtocolVersionV3}},
	})
	if err != nil {
		t.Fatal(err)
	}
	genesis, err := chainabci.LoadGenesisDocument(filepath.Join(
		root, document.Nodes[0].Home, filepath.FromSlash(AppGenesisPath),
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(genesis.Upgrades) != 1 || genesis.Upgrades[0].Height != 10 || genesis.Upgrades[0].Protocol != chainabci.ProtocolVersionV3 {
		t.Fatalf("generated upgrades = %+v", genesis.Upgrades)
	}
}
