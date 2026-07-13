package desktop

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"chainlab/internal/cometnode"
)

func TestPublicInvitationAndLocalJoinBoundaries(t *testing.T) {
	parent := t.TempDir()
	identities := make([]PublicIdentity, 4)
	identityRoots := make([]string, 4)
	for index := range identities {
		root := filepath.Join(parent, "validator-"+string(rune('0'+index)))
		identity, err := GenerateIdentity(
			root, cometnode.RoleValidator, "validator-"+string(rune('0'+index)),
			"127.0.0.1:"+stringPort(26700+index),
		)
		if err != nil {
			t.Fatal(err)
		}
		identities[index] = identity
		identityRoots[index] = root
	}
	invitationPath := filepath.Join(parent, "invitation.json")
	invitation, err := CreateInvitation(
		invitationPath, "chainlab-home-test", "Home test",
		identities, time.Date(2026, 7, 13, 5, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadInvitation(invitationPath)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Checksum != invitation.Checksum || loaded.Payload.GenesisSHA256 == "" {
		t.Fatalf("loaded invitation = %+v", loaded)
	}
	raw, err := os.ReadFile(invitationPath)
	if err != nil {
		t.Fatal(err)
	}
	lower := bytes.ToLower(raw)
	for _, secretMarker := range [][]byte{[]byte("priv_key"), []byte("private_key"), []byte("mnemonic"), []byte("seed_phrase")} {
		if bytes.Contains(lower, secretMarker) {
			t.Fatalf("invitation contains secret marker %q", secretMarker)
		}
	}

	validatorConfig, err := Join(identityRoots[0], invitationPath, LocalPorts{ABCI: 27100, RPC: 27101, Control: 27102, Explorer: 27103})
	if err != nil {
		t.Fatal(err)
	}
	if validatorConfig.Profile != ProfileHomeValidator || validatorConfig.ChainID != invitation.Payload.ChainID {
		t.Fatalf("validator config = %+v", validatorConfig)
	}
	if _, err := os.Stat(filepath.Join(identityRoots[0], identityHomePath, "config", "priv_validator_key.json")); err != nil {
		t.Fatalf("validator key should remain local: %v", err)
	}

	observerRoot := filepath.Join(parent, "observer")
	if _, err := GenerateIdentity(observerRoot, cometnode.RoleObserver, "observer-0", "127.0.0.1:26710"); err != nil {
		t.Fatal(err)
	}
	observerConfig, err := Join(observerRoot, invitationPath, LocalPorts{ABCI: 27200, RPC: 27201, Control: 27202, Explorer: 27203})
	if err != nil {
		t.Fatal(err)
	}
	if observerConfig.Profile != ProfileObserver {
		t.Fatalf("observer config = %+v", observerConfig)
	}
	for _, path := range []string{
		filepath.Join(observerRoot, identityHomePath, "config", "priv_validator_key.json"),
		filepath.Join(observerRoot, identityHomePath, "data", "priv_validator_state.json"),
	} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("observer signing material exists at %s: %v", path, err)
		}
	}
}

func TestInvitationRejectsTamperDuplicateAndUnsafeIdentity(t *testing.T) {
	parent := t.TempDir()
	if _, err := GenerateIdentity(filepath.Join(parent, "unsafe"), cometnode.RoleValidator, "unsafe", "0.0.0.0:26680"); err == nil {
		t.Fatal("unspecified advertised address should fail")
	}
	identities := testValidatorIdentities(t, parent)
	duplicate := append([]PublicIdentity(nil), identities...)
	duplicate[3] = duplicate[0]
	if _, err := CreateInvitation(filepath.Join(parent, "duplicate.json"), "chainlab-duplicate", "Duplicate", duplicate, time.Now()); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate identity error = %v", err)
	}

	path := filepath.Join(parent, "valid.json")
	invitation, err := CreateInvitation(path, "chainlab-tamper", "Tamper", identities, time.Date(2026, 7, 13, 5, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	invitation.Payload.ChainID = "chainlab-wrong"
	tampered, err := json.Marshal(invitation)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadInvitation(path); err == nil {
		t.Fatal("tampered invitation should fail")
	}
}

func TestJoinRejectsDifferentLocalValidatorAndObserverSecrets(t *testing.T) {
	parent := t.TempDir()
	identities := testValidatorIdentities(t, parent)
	path := filepath.Join(parent, "invitation.json")
	if _, err := CreateInvitation(path, "chainlab-membership", "Membership", identities, time.Now()); err != nil {
		t.Fatal(err)
	}
	outsiderRoot := filepath.Join(parent, "outsider")
	if _, err := GenerateIdentity(outsiderRoot, cometnode.RoleValidator, "outsider", "127.0.0.1:26900"); err != nil {
		t.Fatal(err)
	}
	if _, err := Join(outsiderRoot, path, LocalPorts{ABCI: 27300, RPC: 27301, Control: 27302, Explorer: 27303}); err == nil || !strings.Contains(err.Error(), "not an exact member") {
		t.Fatalf("outsider join error = %v", err)
	}

	observerRoot := filepath.Join(parent, "observer-secret")
	if _, err := GenerateIdentity(observerRoot, cometnode.RoleObserver, "observer-secret", "127.0.0.1:26901"); err != nil {
		t.Fatal(err)
	}
	validatorKey := filepath.Join(observerRoot, identityHomePath, "config", "priv_validator_key.json")
	if err := os.WriteFile(validatorKey, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Join(observerRoot, path, LocalPorts{ABCI: 27400, RPC: 27401, Control: 27402, Explorer: 27403}); err == nil || !strings.Contains(err.Error(), "signing material") {
		t.Fatalf("observer secret error = %v", err)
	}
}

func testValidatorIdentities(t *testing.T, parent string) []PublicIdentity {
	t.Helper()
	identities := make([]PublicIdentity, 4)
	for index := range identities {
		identity, err := GenerateIdentity(
			filepath.Join(parent, "set-"+string(rune('0'+index))),
			cometnode.RoleValidator,
			"set-"+string(rune('0'+index)),
			"127.0.0.1:"+stringPort(26800+index),
		)
		if err != nil {
			t.Fatal(err)
		}
		identities[index] = identity
	}
	return identities
}

func stringPort(port int) string {
	return strconv.Itoa(port)
}
