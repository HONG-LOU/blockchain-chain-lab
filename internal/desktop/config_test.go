package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileContractsAreExplicit(t *testing.T) {
	tests := []struct {
		profile        Profile
		validator      bool
		validatorCount int
		p2pPublic      bool
	}{
		{ProfileDesktopSolo, true, 1, false},
		{ProfileHomeValidator, true, 4, true},
		{ProfileObserver, false, 4, true},
	}
	for _, test := range tests {
		t.Run(string(test.profile), func(t *testing.T) {
			contract, err := Contract(test.profile)
			if err != nil {
				t.Fatal(err)
			}
			if contract.Validator != test.validator || contract.ValidatorCount != test.validatorCount || contract.P2PPublic != test.p2pPublic {
				t.Fatalf("contract = %+v", contract)
			}
			if contract.RPCPublic || contract.ApplicationStorage.RetainHeights == 0 || contract.CometRetainBlocks == 0 || !contract.CreateEmptyBlocks || contract.BlockIntervalSecs == 0 {
				t.Fatalf("contract is not bounded and loopback-safe: %+v", contract)
			}
		})
	}
}

func TestInitializeDesktopSoloPublishesRestartSafeConfig(t *testing.T) {
	root := filepath.Join(t.TempDir(), "desktop")
	config, err := Initialize(root, ProfileDesktopSolo, "chainlab-desktop-test")
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != config {
		t.Fatalf("loaded config = %+v, want %+v", loaded, config)
	}
	if config.ABCIAddress != "tcp://127.0.0.1:26658" ||
		config.RPCAddress != "tcp://127.0.0.1:26670" ||
		config.P2PAddress != "tcp://127.0.0.1:26680" ||
		config.ControlURL != "http://127.0.0.1:26659" ||
		config.ExplorerURL != "http://127.0.0.1:8547/" {
		t.Fatalf("default solo endpoints changed: %+v", config)
	}
	for _, path := range []string{config.Network, config.NodeHome} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err != nil {
			t.Fatalf("generated path %q: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(config.ApplicationData))); !os.IsNotExist(err) {
		t.Fatalf("application data must be created only on first start, error = %v", err)
	}
	if _, err := Initialize(root, ProfileDesktopSolo, "chainlab-other"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second initialization error = %v", err)
	}
}

func TestInitializeRejectsJoinProfilesWithoutInvitation(t *testing.T) {
	for _, profile := range []Profile{ProfileHomeValidator, ProfileObserver} {
		_, err := Initialize(filepath.Join(t.TempDir(), string(profile)), profile, "chainlab-test")
		if err == nil || !strings.Contains(err.Error(), "verified public invitation") {
			t.Fatalf("profile %q error = %v", profile, err)
		}
	}
}

func TestLoadRejectsNonCanonicalOrChangedContract(t *testing.T) {
	root := filepath.Join(t.TempDir(), "desktop")
	config, err := Initialize(root, ProfileDesktopSolo, "chainlab-desktop-test")
	if err != nil {
		t.Fatal(err)
	}
	config.Contract.ValidatorCount = 4
	raw, err := jsonMarshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ConfigPath), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "contract") {
		t.Fatalf("changed contract error = %v", err)
	}
}

func jsonMarshal(value Config) ([]byte, error) {
	return json.Marshal(value)
}
