package types

import (
	"bytes"
	"testing"

	"chainlab/internal/hash"
	chainproof "chainlab/pkg/proof"
)

func TestTransactionAndReceiptMerkleProofsBindCanonicalValues(t *testing.T) {
	txs := []Transaction{
		{ChainID: "proof-chain", Type: TxTransfer, From: "0x1111111111111111111111111111111111111111", To: "0x2222222222222222222222222222222222222222", Nonce: 0, Value: 1},
		{ChainID: "proof-chain", Type: TxTransfer, From: "0x1111111111111111111111111111111111111111", To: "0x3333333333333333333333333333333333333333", Nonce: 1, Value: 2},
	}
	receipts := []Receipt{
		{TxHash: txs[0].Hash(), Success: true, GasUsed: 21_000},
		{TxHash: txs[1].Hash(), Success: false, GasUsed: 30_000, FailureCode: ReceiptFailureExecutionReverted},
	}
	txRoot, err := TransactionMerkleRoot(txs)
	if err != nil {
		t.Fatal(err)
	}
	txProof, err := TransactionMerkleProof(txs, 1)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := chainproof.Verify(txRoot, txProof)
	if err != nil {
		t.Fatal(err)
	}
	want, err := hash.CanonicalBytes(txs[1])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(leaf, want) {
		t.Fatalf("transaction proof leaf = %s", leaf)
	}
	receiptRoot, err := ReceiptMerkleRoot(receipts)
	if err != nil {
		t.Fatal(err)
	}
	receiptProof, err := ReceiptMerkleProof(receipts, 0)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err = chainproof.Verify(receiptRoot, receiptProof)
	if err != nil {
		t.Fatal(err)
	}
	want, err = hash.CanonicalBytes(receipts[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(leaf, want) {
		t.Fatalf("receipt proof leaf = %s", leaf)
	}
	if txRoot == TransactionRoot(txs) || receiptRoot == ReceiptRoot(receipts) {
		t.Fatal("Merkle roots reused legacy list hashes")
	}
}
