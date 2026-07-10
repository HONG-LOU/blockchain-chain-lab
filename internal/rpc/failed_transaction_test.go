package rpc_test

import (
	"math"
	"net/http/httptest"
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/node"
	chainrpc "chainlab/internal/rpc"
	"chainlab/internal/types"
)

func TestRPCExposesFailedStatusCodeAndCumulativeGas(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := chaincrypto.AddressFromPrivateKey(key)
	overflowed := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	receiver := "0xcccccccccccccccccccccccccccccccccccccccc"
	n, err := node.NewDevelopment(node.Config{
		ChainID:     "chainlab-local",
		ProposerKey: key,
		GenesisBalance: map[string]uint64{
			sender:     100_000,
			overflowed: math.MaxUint64,
		}, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	failed := signFailedRPCTransaction(t, key, types.Transaction{
		ChainID: "chainlab-local", Type: types.TxTransfer, From: sender, To: overflowed,
		Nonce: 0, Value: 1, GasLimit: 21_000, GasPrice: 1,
	})
	succeeded := signFailedRPCTransaction(t, key, types.Transaction{
		ChainID: "chainlab-local", Type: types.TxTransfer, From: sender, To: receiver,
		Nonce: 1, Value: 1, GasLimit: 21_000, GasPrice: 1,
	})
	if err := n.SubmitTx(failed); err != nil {
		t.Fatal(err)
	}
	if err := n.SubmitTx(succeeded); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()
	failedRPC := callRPC(t, server.URL, "eth_getTransactionReceipt", []any{failed.Hash()}).(map[string]any)
	if failedRPC["status"] != "0x0" || failedRPC["gasUsed"] != "0x5208" || failedRPC["cumulativeGasUsed"] != "0x5208" {
		t.Fatalf("failed RPC receipt = %#v", failedRPC)
	}
	successRPC := callRPC(t, server.URL, "eth_getTransactionReceipt", []any{succeeded.Hash()}).(map[string]any)
	if successRPC["status"] != "0x1" || successRPC["cumulativeGasUsed"] != "0xa410" {
		t.Fatalf("success RPC receipt = %#v", successRPC)
	}
	trace := callRPC(t, server.URL, "debug_traceTransaction", []any{failed.Hash()}).(map[string]any)
	chainLab := trace["chainLab"].(map[string]any)
	if trace["failed"] != true || chainLab["failureCode"] != types.ReceiptFailureExecutionReverted || chainLab["error"] != types.ReceiptFailureExecutionReverted {
		t.Fatalf("failed trace = %#v", trace)
	}
}

func signFailedRPCTransaction(t *testing.T, key chaincrypto.PrivateKey, tx types.Transaction) types.Transaction {
	t.Helper()
	signature, err := chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = signature
	return tx
}
