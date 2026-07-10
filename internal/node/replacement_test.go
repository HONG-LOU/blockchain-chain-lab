package node

import (
	"errors"
	"testing"

	"chainlab/internal/types"
)

func TestReplacementRequiresSameAuthorizationPrincipal(t *testing.T) {
	account := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	signerA := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	signerB := "0xcccccccccccccccccccccccccccccccccccccccc"
	oldTx := types.Transaction{From: account, Signer: signerA, Nonce: 7, GasPrice: 10}

	differentSigner := oldTx
	differentSigner.Signer = signerB
	differentSigner.GasPrice = 11
	if err := canReplacePendingTransaction(oldTx, differentSigner); !errors.Is(err, errReplacementAuthorizationMismatch) {
		t.Fatalf("different signer replacement error = %v", err)
	}

	sameSigner := oldTx
	sameSigner.Signer = "0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	sameSigner.GasPrice = 11
	if err := canReplacePendingTransaction(oldTx, sameSigner); err != nil {
		t.Fatalf("same signer replacement error = %v", err)
	}
}

func TestReplacementMultisigIdentityUsesSignerSet(t *testing.T) {
	oldTx := types.Transaction{
		From:     "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Nonce:    4,
		GasPrice: 10,
		Authorizations: []types.Authorization{
			{Signer: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			{Signer: "0xcccccccccccccccccccccccccccccccccccccccc"},
		},
	}
	reordered := oldTx
	reordered.GasPrice = 11
	reordered.Authorizations = []types.Authorization{
		{Signer: "0xCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC"},
		{Signer: "0xBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"},
	}
	if err := canReplacePendingTransaction(oldTx, reordered); err != nil {
		t.Fatalf("same multisig signer set replacement error = %v", err)
	}

	differentSet := reordered
	differentSet.Authorizations[1].Signer = "0xdddddddddddddddddddddddddddddddddddddddd"
	if err := canReplacePendingTransaction(oldTx, differentSet); !errors.Is(err, errReplacementAuthorizationMismatch) {
		t.Fatalf("different multisig signer set replacement error = %v", err)
	}
}
