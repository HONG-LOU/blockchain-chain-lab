package node

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"chainlab/internal/consensus"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"
	"chainlab/internal/types"
)

func TestRestartRejectsTamperedPersistedState(t *testing.T) {
	fixture := newRestartSecurityFixture(t)
	account := fixture.snapshot.State.Accounts[fixture.proposer]
	account.Balance++
	fixture.snapshot.State.Accounts[fixture.proposer] = account
	writeRestartSecuritySnapshot(t, fixture.dataDir, fixture.snapshot)

	requireRestartSecurityFailure(t, fixture.config, "persisted state root does not match canonical replay")
}

func TestRestartRejectsTamperedCanonicalBlock(t *testing.T) {
	t.Run("signature", func(t *testing.T) {
		fixture := newRestartSecurityFixture(t)
		fixture.snapshot.Blocks[1].Signature = corruptRestartSecuritySignature(fixture.snapshot.Blocks[1].Signature)
		writeRestartSecuritySnapshot(t, fixture.dataDir, fixture.snapshot)

		requireRestartSecurityFailure(t, fixture.config, "invalid block signature")
	})

	t.Run("transaction body", func(t *testing.T) {
		fixture := newRestartSecurityFixture(t)
		fixture.snapshot.Blocks[1].Transactions[0].Value++
		writeRestartSecuritySnapshot(t, fixture.dataDir, fixture.snapshot)

		requireRestartSecurityFailure(t, fixture.config, "transaction root mismatch")
	})
}

func TestRestartRejectsMalformedSnapshotEnvelope(t *testing.T) {
	t.Run("missing version", func(t *testing.T) {
		fixture := newRestartSecurityFixture(t)
		fixture.snapshot.Version = 0
		raw := restartSecuritySnapshotJSON(t, fixture.snapshot)
		writeRestartSecurityJSONWithoutField(t, fixture.dataDir, raw, "version")

		requireRestartSecurityFailure(t, fixture.config, "unsupported persisted snapshot version 0")
	})

	t.Run("missing genesis state", func(t *testing.T) {
		fixture := newRestartSecurityFixture(t)
		fixture.snapshot.GenesisState = state.Snapshot{}
		raw := restartSecuritySnapshotJSON(t, fixture.snapshot)
		writeRestartSecurityJSONWithoutField(t, fixture.dataDir, raw, "genesis_state")

		requireRestartSecurityFailure(t, fixture.config, "restore persisted genesis state")
	})

	t.Run("unknown field", func(t *testing.T) {
		fixture := newRestartSecurityFixture(t)
		raw := restartSecuritySnapshotJSON(t, fixture.snapshot)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		fields["unexpected_consensus_state"] = json.RawMessage(`true`)
		writeRestartSecurityRawFields(t, fixture.dataDir, fields)

		requireRestartSecurityFailure(t, fixture.config, "unknown field")
	})

	t.Run("trailing JSON", func(t *testing.T) {
		fixture := newRestartSecurityFixture(t)
		raw := append(restartSecuritySnapshotJSON(t, fixture.snapshot), []byte("\n{}\n")...)
		writeRestartSecurityRaw(t, fixture.dataDir, raw)

		requireRestartSecurityFailure(t, fixture.config, "exactly one JSON value")
	})
}

func TestRestartRejectsInvalidKnownSideBlock(t *testing.T) {
	t.Run("missing parent", func(t *testing.T) {
		fixture := newRestartSecurityFixture(t)
		head := fixture.snapshot.Blocks[len(fixture.snapshot.Blocks)-1]
		orphan := signedRestartSecurityEmptyBlock(t, fixture.key, head, head.Header.TimeUnix+1)
		orphan.Header.ParentHash = "0x" + strings.Repeat("f", 64)
		if err := consensus.SignBlock(fixture.key, &orphan); err != nil {
			t.Fatal(err)
		}
		fixture.snapshot.KnownBlocks = append(fixture.snapshot.KnownBlocks, orphan)
		writeRestartSecuritySnapshot(t, fixture.dataDir, fixture.snapshot)

		requireRestartSecurityFailure(t, fixture.config, "parent is missing")
	})

	t.Run("bad signature", func(t *testing.T) {
		fixture := newRestartSecurityFixture(t)
		side := restartSecuritySideBlock(t, fixture)
		side.Signature = corruptRestartSecuritySignature(side.Signature)
		fixture.snapshot.KnownBlocks = append(fixture.snapshot.KnownBlocks, side)
		writeRestartSecuritySnapshot(t, fixture.dataDir, fixture.snapshot)

		requireRestartSecurityFailure(t, fixture.config, "invalid block signature")
	})
}

func TestRestartRejectsInvalidFinalityEvidence(t *testing.T) {
	t.Run("invalid signature", func(t *testing.T) {
		fixture := newRestartSecurityFixture(t)
		side, evidence := restartSecurityEvidence(t, fixture)
		evidence.FirstSignature = corruptRestartSecuritySignature(evidence.FirstSignature)
		fixture.snapshot.KnownBlocks = append(fixture.snapshot.KnownBlocks, side)
		fixture.snapshot.FinalityEvidence = []types.FinalityEquivocationEvidence{evidence}
		writeRestartSecuritySnapshot(t, fixture.dataDir, fixture.snapshot)

		requireRestartSecurityFailure(t, fixture.config, "finality evidence signature is invalid")
	})

	t.Run("duplicate identity", func(t *testing.T) {
		fixture := newRestartSecurityFixture(t)
		side, evidence := restartSecurityEvidence(t, fixture)
		fixture.snapshot.KnownBlocks = append(fixture.snapshot.KnownBlocks, side)
		fixture.snapshot.FinalityEvidence = []types.FinalityEquivocationEvidence{evidence, evidence}
		writeRestartSecuritySnapshot(t, fixture.dataDir, fixture.snapshot)

		requireRestartSecurityFailure(t, fixture.config, "duplicate identity")
	})
}

type restartSecurityFixture struct {
	config   Config
	key      chaincrypto.PrivateKey
	proposer string
	dataDir  string
	snapshot diskSnapshot
}

func newRestartSecurityFixture(t *testing.T) restartSecurityFixture {
	t.Helper()
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := chaincrypto.AddressFromPrivateKey(key)
	dataDir := t.TempDir()
	config := Config{
		Role:           RoleValidator,
		ChainID:        "chainlab-restart-security",
		ProposerKey:    key,
		Validators:     []string{proposer},
		GenesisBalance: map[string]uint64{proposer: 10_000_000},
		DataDir:        dataDir, GenesisTimeUnix: DeterministicDevGenesisTimeUnix,
	}
	n, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	tx := types.Transaction{
		ChainID:  config.ChainID,
		Type:     types.TxTransfer,
		From:     proposer,
		To:       "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	}
	tx.Signature, err = chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := n.SubmitTx(tx); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := loadDiskSnapshot(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot == nil || len(snapshot.Blocks) != 2 || len(snapshot.Blocks[1].Transactions) != 1 {
		t.Fatalf("unexpected restart fixture snapshot: %+v", snapshot)
	}
	closeTestNode(t, n)
	return restartSecurityFixture{
		config:   config,
		key:      key,
		proposer: proposer,
		dataDir:  dataDir,
		snapshot: *snapshot,
	}
}

func restartSecuritySideBlock(t *testing.T, fixture restartSecurityFixture) types.Block {
	t.Helper()
	genesis := fixture.snapshot.Blocks[0]
	canonical := fixture.snapshot.Blocks[1]
	return signedRestartSecurityEmptyBlock(t, fixture.key, genesis, canonical.Header.TimeUnix+1)
}

func signedRestartSecurityEmptyBlock(
	t *testing.T,
	key chaincrypto.PrivateKey,
	parent types.Block,
	timeUnix int64,
) types.Block {
	t.Helper()
	block := types.Block{Header: types.BlockHeader{
		ChainID:       parent.Header.ChainID,
		Height:        parent.Header.Height + 1,
		ParentHash:    parent.Hash(),
		TimeUnix:      timeUnix,
		Proposer:      chaincrypto.AddressFromPrivateKey(key),
		GasLimit:      parent.Header.GasLimit,
		BaseFeePerGas: NextBaseFee(parent, parent.Header.GasLimit),
		TxRoot:        types.TransactionRoot(nil),
		ReceiptRoot:   types.ReceiptRoot(nil),
		StateRoot:     parent.Header.StateRoot,
	}}
	if err := consensus.SignBlock(key, &block); err != nil {
		t.Fatal(err)
	}
	return block
}

func restartSecurityEvidence(
	t *testing.T,
	fixture restartSecurityFixture,
) (types.Block, types.FinalityEquivocationEvidence) {
	t.Helper()
	canonical := fixture.snapshot.Blocks[1]
	side := restartSecuritySideBlock(t, fixture)
	first, err := consensus.SignFinalityVote(fixture.key, canonical)
	if err != nil {
		t.Fatal(err)
	}
	second, err := consensus.SignFinalityVote(fixture.key, side)
	if err != nil {
		t.Fatal(err)
	}
	return side, types.FinalityEquivocationEvidence{
		Validator:       fixture.proposer,
		Height:          canonical.Header.Height,
		FirstBlockHash:  canonical.Hash(),
		FirstSignature:  first.Signature,
		SecondBlockHash: side.Hash(),
		SecondSignature: second.Signature,
	}
}

func corruptRestartSecuritySignature(signature string) string {
	replacement := byte('0')
	if signature[4] == replacement {
		replacement = '1'
	}
	return signature[:4] + string(replacement) + signature[5:]
}

func writeRestartSecuritySnapshot(t *testing.T, dataDir string, snapshot diskSnapshot) {
	t.Helper()
	writeRestartSecurityRaw(t, dataDir, restartSecuritySnapshotJSON(t, snapshot))
}

func restartSecuritySnapshotJSON(t *testing.T, snapshot diskSnapshot) []byte {
	t.Helper()
	snapshot.Checksum = ""
	checksum, err := diskSnapshotChecksum(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Checksum = checksum
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(raw, '\n')
}

func writeRestartSecurityJSONWithoutField(t *testing.T, dataDir string, raw []byte, field string) {
	t.Helper()
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, field)
	writeRestartSecurityRawFields(t, dataDir, fields)
}

func writeRestartSecurityRawFields(t *testing.T, dataDir string, fields map[string]json.RawMessage) {
	t.Helper()
	raw, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeRestartSecurityRaw(t, dataDir, append(raw, '\n'))
}

func writeRestartSecurityRaw(t *testing.T, dataDir string, raw []byte) {
	t.Helper()
	if err := os.WriteFile(chainPath(dataDir), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func requireRestartSecurityFailure(t *testing.T, config Config, want string) {
	t.Helper()
	if _, err := New(config); err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("restart error = %v, want substring %q", err, want)
	}
}
