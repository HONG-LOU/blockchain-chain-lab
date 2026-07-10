package core_test

import (
	"strings"
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/types"
)

func TestTransactionSchemaRejectsRedundantAndUnconsumedRepresentations(t *testing.T) {
	store, executor, key, alice, bob := newExecutorFixture(t)
	base := func(txType types.TxType) types.Transaction {
		return types.Transaction{
			ChainID:  "chainlab-local",
			Type:     txType,
			From:     alice,
			To:       bob,
			GasLimit: 100_000,
			GasPrice: 1,
		}
	}
	tests := []struct {
		name      string
		tx        types.Transaction
		wantError string
	}{
		{
			name: "mixed fee models",
			tx: func() types.Transaction {
				tx := base(types.TxTransfer)
				tx.MaxFeePerGas = 2
				return tx
			}(),
			wantError: "legacy gas price cannot be combined",
		},
		{
			name: "native ethereum hash metadata",
			tx: func() types.Transaction {
				tx := base(types.TxTransfer)
				tx.EthereumRawHash = "0x" + strings.Repeat("a", 64)
				return tx
			}(),
			wantError: "ethereum raw hash requires",
		},
		{
			name: "transfer payload",
			tx: func() types.Transaction {
				tx := base(types.TxTransfer)
				tx.Payload = map[string]string{"ignored": "value"}
				return tx
			}(),
			wantError: "transfer does not support payload",
		},
		{
			name: "vote unknown field",
			tx: func() types.Transaction {
				tx := base(types.TxVote)
				tx.To = ""
				tx.Payload = map[string]string{"proposal": "proposal:0x1", "choice": "yes", "ignored": "value"}
				return tx
			}(),
			wantError: "voting-power snapshots",
		},
		{
			name: "vote non-canonical choice",
			tx: func() types.Transaction {
				tx := base(types.TxVote)
				tx.To = ""
				tx.Payload = map[string]string{"proposal": "proposal:0x1", "choice": " YES "}
				return tx
			}(),
			wantError: "voting-power snapshots",
		},
		{
			name: "proposal non-canonical period",
			tx: func() types.Transaction {
				tx := base(types.TxProposalSubmit)
				tx.To = ""
				tx.Payload = map[string]string{"title": "upgrade", "kind": "param.change", "voting_period": "01", "param": "x", "value": "y"}
				return tx
			}(),
			wantError: "voting-power snapshots",
		},
		{
			name: "proposal execute value",
			tx: func() types.Transaction {
				tx := base(types.TxProposalExecute)
				tx.To = ""
				tx.Value = 1
				tx.Payload = map[string]string{"proposal": "proposal:0x1"}
				return tx
			}(),
			wantError: "voting-power snapshots",
		},
		{
			name: "slash unknown field",
			tx: func() types.Transaction {
				tx := base(types.TxValidatorSlash)
				tx.To = ""
				tx.Payload = map[string]string{"target": bob, "amount": "1", "evidence": "proof", "ignored": "value"}
				return tx
			}(),
			wantError: "unsupported field",
		},
		{
			name: "wasm alias",
			tx: func() types.Transaction {
				tx := base(types.TxWASMUpload)
				tx.To = ""
				tx.Payload = map[string]string{"wasm": "0x0061736d"}
				return tx
			}(),
			wantError: "only bytecode",
		},
		{
			name: "deploy code id whitespace",
			tx: func() types.Transaction {
				tx := base(types.TxDeploy)
				tx.To = ""
				tx.Payload = map[string]string{"code_id": " counter.v1 "}
				return tx
			}(),
			wantError: "code_id is not canonically encoded",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := signedTx(t, key, test.tx)
			if _, err := executor.Execute(store.Clone(), tx); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("schema error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestMultisigSchemaRejectsRedundantTransactionSignature(t *testing.T) {
	store, executor, key, alice, bob := newExecutorFixture(t)
	tx := types.Transaction{
		ChainID:        "chainlab-local",
		Type:           types.TxTransfer,
		From:           alice,
		To:             bob,
		GasLimit:       21_000,
		GasPrice:       1,
		Authorizations: []types.Authorization{{Signer: alice}},
	}
	signature, err := chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Authorizations[0].Signature = signature
	tx.Signature = signature
	if _, err := executor.Execute(store, tx); err == nil || !strings.Contains(err.Error(), "cannot include transaction signature") {
		t.Fatalf("redundant signature error = %v", err)
	}
}

func TestEthereumType2SchemaBindsExecutionFieldsAndRawHash(t *testing.T) {
	store, executor, key, alice, bob := newExecutorFixture(t)
	tx := types.Transaction{
		ChainID:              "chainlab-local",
		Type:                 types.TxTransfer,
		From:                 alice,
		To:                   bob,
		GasLimit:             21_000,
		MaxFeePerGas:         5,
		MaxPriorityFeePerGas: 1,
		SignatureKind:        types.SignatureKindEthereumType2,
	}
	digest, err := types.EthereumType2SigningDigest(tx)
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature, err = chaincrypto.SignDigest(key, digest)
	if err != nil {
		t.Fatal(err)
	}
	tx.EthereumRawHash, err = types.EthereumType2TransactionHash(tx)
	if err != nil {
		t.Fatal(err)
	}

	mutated := tx
	mutated.Payload = map[string]string{"method": "ignored"}
	if _, err := executor.Execute(store.Clone(), mutated); err == nil || !strings.Contains(err.Error(), "unsupported ChainLab fields") {
		t.Fatalf("mutated envelope error = %v", err)
	}
	mutated = tx
	mutated.EthereumRawHash = "0x" + strings.Repeat("f", 64)
	if _, err := executor.Execute(store.Clone(), mutated); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("raw hash mismatch error = %v", err)
	}
}
