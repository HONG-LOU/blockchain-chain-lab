package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"chainlab/internal/contracts"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/types"
)

func TestConflictingFinalityCertificatePersistsStickyHaltAcrossRestart(t *testing.T) {
	h := newFinalityAtomicHarness(t, 1, t.TempDir())
	genesis, ok := h.n.Block(0)
	if !ok {
		t.Fatal("genesis should exist")
	}
	locked, err := h.n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := h.n.SubmitFinalityVote(signFinalityAtomicVote(t, h.keys[0], locked)); err != nil {
		t.Fatal(err)
	}
	if checkpoint := h.n.Finality(); checkpoint.CertifiedHash != locked.Hash() {
		t.Fatalf("finality lock = %+v, want certified block %s", checkpoint, locked.Hash())
	}

	conflicting := signedRestartSecurityEmptyBlock(t, h.keys[0], genesis, locked.Header.TimeUnix+10)
	vote := signFinalityAtomicVote(t, h.keys[0], conflicting)
	conflicting.FinalityCertificate = &types.FinalityCertificate{
		ChainID:    conflicting.Header.ChainID,
		Height:     conflicting.Header.Height,
		BlockHash:  conflicting.Hash(),
		Signatures: []types.FinalitySignature{vote},
	}
	if err := h.n.ImportBlock(conflicting); !errors.Is(err, ErrFinalitySafetyViolation) {
		t.Fatalf("conflicting certificate import error = %v, want finality safety violation", err)
	}

	record, err := loadHaltRecord(h.config.DataDir)
	if err != nil {
		t.Fatalf("load HALT.json: %v", err)
	}
	if record == nil {
		t.Fatal("valid conflicting certificate did not create HALT.json")
	}
	if record.Kind != "finality_safety" || record.ChainID != h.config.ChainID ||
		!strings.Contains(record.Reason, ErrFinalitySafetyViolation.Error()) {
		t.Fatalf("halt record = %+v", record)
	}

	closeTestNode(t, h.n)
	reloaded, err := New(h.config)
	if err != nil {
		t.Fatalf("restart halted node: %v", err)
	}
	registerTestNodeClose(t, reloaded)
	requirePersistentHaltRejectsConsensusEntryPoints(t, reloaded, "persisted finality_safety halt")
}

func TestFatalRuntimeAndStateFaultHaltsPersistAcrossRestart(t *testing.T) {
	tests := []struct {
		name  string
		fault error
	}{
		{name: "runtime fault", fault: contracts.ErrWasmRuntimeFault},
		{name: "state fault", fault: contracts.ErrContractStateFault},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := newHaltPersistenceConfig(t, "chainlab-persistent-"+strings.ReplaceAll(test.name, " ", "-"))
			n, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			fault := fmt.Errorf("injected %s: %w", test.name, test.fault)
			n.mu.Lock()
			classified := n.recordRuntimeFaultLocked(fault)
			n.mu.Unlock()
			if !classified {
				t.Fatalf("%v was not classified as fatal", test.fault)
			}

			record, err := loadHaltRecord(config.DataDir)
			if err != nil {
				t.Fatal(err)
			}
			if record == nil || record.Kind != "runtime_fault" || !strings.Contains(record.Reason, test.fault.Error()) {
				t.Fatalf("halt record = %+v", record)
			}

			closeTestNode(t, n)
			reloaded, err := New(config)
			if err != nil {
				t.Fatalf("restart halted node: %v", err)
			}
			registerTestNodeClose(t, reloaded)
			requirePersistentHaltRejectsConsensusEntryPoints(t, reloaded, "persisted runtime_fault halt")
		})
	}
}

func TestRestartFailsClosedOnTamperedHaltMarker(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*testing.T, Config)
		wantErr string
	}{
		{
			name: "unknown field",
			mutate: func(t *testing.T, config Config) {
				fields := readHaltPersistenceFields(t, config.DataDir)
				fields["unexpected_consensus_state"] = json.RawMessage(`true`)
				writeHaltPersistenceJSON(t, config.DataDir, fields)
			},
			wantErr: "unknown field",
		},
		{
			name: "unsupported version",
			mutate: func(t *testing.T, config Config) {
				fields := readHaltPersistenceFields(t, config.DataDir)
				fields["version"] = json.RawMessage(`2`)
				writeHaltPersistenceJSON(t, config.DataDir, fields)
			},
			wantErr: "unsupported persisted halt marker version",
		},
		{
			name: "trailing JSON",
			mutate: func(t *testing.T, config Config) {
				raw := readHaltPersistenceRaw(t, config.DataDir)
				writeHaltPersistenceRaw(t, config.DataDir, append(raw, []byte("{}\n")...))
			},
			wantErr: "exactly one JSON value",
		},
		{
			name: "checksum tampering",
			mutate: func(t *testing.T, config Config) {
				fields := readHaltPersistenceFields(t, config.DataDir)
				fields["reason"] = json.RawMessage(`"tampered reason"`)
				writeHaltPersistenceJSON(t, config.DataDir, fields)
			},
			wantErr: "checksum mismatch",
		},
		{
			name: "chain ID tampering",
			mutate: func(t *testing.T, config Config) {
				if err := persistHaltRecord(config.DataDir, haltRecord{
					Version: haltRecordVersion,
					ChainID: config.ChainID + "-other",
					Kind:    "runtime_fault",
					Reason:  "tampered chain id",
				}); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "chain id does not match config",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := newHaltPersistenceConfig(t, "chainlab-halt-marker")
			n, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			closeTestNode(t, n)
			if err := persistHaltRecord(config.DataDir, haltRecord{
				Version: haltRecordVersion,
				ChainID: config.ChainID,
				Kind:    "runtime_fault",
				Reason:  "fixture fault",
			}); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, config)

			if _, err := New(config); err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("restart error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func newHaltPersistenceConfig(t *testing.T, chainID string) Config {
	t.Helper()
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validator := chaincrypto.AddressFromPrivateKey(key)
	return Config{
		Role:           RoleValidator,
		ChainID:        chainID,
		ProposerKey:    key,
		Validators:     []string{validator},
		GenesisBalance: map[string]uint64{validator: 1_000_000},
		DataDir:        t.TempDir(), GenesisTimeUnix: DeterministicDevGenesisTimeUnix,
	}
}

func requirePersistentHaltRejectsConsensusEntryPoints(t *testing.T, n *Node, wantReason string) {
	t.Helper()
	assertRejected := func(operation string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), wantReason) {
			t.Fatalf("%s error = %v, want persistent halt containing %q", operation, err, wantReason)
		}
	}
	assertRejected("submit", n.SubmitTx(types.Transaction{}))
	_, err := n.ProduceBlock()
	assertRejected("produce", err)
	assertRejected("import", n.ImportBlock(n.Head()))
}

func readHaltPersistenceRaw(t *testing.T, dataDir string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dataDir, "HALT.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func readHaltPersistenceFields(t *testing.T, dataDir string) map[string]json.RawMessage {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(readHaltPersistenceRaw(t, dataDir), &fields); err != nil {
		t.Fatal(err)
	}
	return fields
}

func writeHaltPersistenceJSON(t *testing.T, dataDir string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeHaltPersistenceRaw(t, dataDir, append(raw, '\n'))
}

func writeHaltPersistenceRaw(t *testing.T, dataDir string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dataDir, "HALT.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
