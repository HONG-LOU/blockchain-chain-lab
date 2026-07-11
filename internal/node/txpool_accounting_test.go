package node

import (
	"strings"
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/hash"
	"chainlab/internal/types"
)

func TestTxPoolByteAccountingAcrossNodeLifecycle(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := chaincrypto.AddressFromPrivateKey(key)
	n, err := NewDevelopment(Config{
		ChainID:         "chainlab-local",
		ProposerKey:     key,
		GenesisBalance:  map[string]uint64{sender: 1_000_000},
		GenesisTimeUnix: DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}

	future := signRuntimeFaultTestTx(t, key, types.Transaction{
		ChainID: "chainlab-local", Type: types.TxTransfer, From: sender,
		To: "0x2222222222222222222222222222222222222222", Nonce: 1,
		Value: 1, GasLimit: 21_000, GasPrice: 1,
	})
	if err := n.SubmitTx(future); err != nil {
		t.Fatal(err)
	}
	assertTxPoolByteAccounting(t, n)

	first := signRuntimeFaultTestTx(t, key, types.Transaction{
		ChainID: "chainlab-local", Type: types.TxTransfer, From: sender,
		To: "0x3333333333333333333333333333333333333333", Nonce: 0,
		Value: 1, GasLimit: 21_000, GasPrice: 1,
	})
	if err := n.SubmitTx(first); err != nil {
		t.Fatal(err)
	}
	assertTxPoolByteAccounting(t, n)

	replacement := signRuntimeFaultTestTx(t, key, types.Transaction{
		ChainID: "chainlab-local", Type: types.TxTransfer, From: sender,
		To: "0x4444444444444444444444444444444444444444", Nonce: 0,
		Value: 2, GasLimit: 21_000, GasPrice: 2,
	})
	if err := n.SubmitTx(replacement); err != nil {
		t.Fatal(err)
	}
	assertTxPoolByteAccounting(t, n)

	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	assertTxPoolByteAccounting(t, n)
	if n.txPoolBytes != 0 {
		t.Fatalf("transaction pool bytes after block = %d, want 0", n.txPoolBytes)
	}
}

func TestIncrementalTxPoolByteAccounting(t *testing.T) {
	pending := types.Transaction{
		From:    "0x1111111111111111111111111111111111111111",
		Nonce:   0,
		Payload: map[string]string{"padding": strings.Repeat("p", 17)},
	}
	queued := types.Transaction{
		From:    pending.From,
		Nonce:   2,
		Payload: map[string]string{"padding": strings.Repeat("q", 31)},
	}
	n := &Node{}
	n.setTxPoolLocked(
		[]types.Transaction{pending},
		[]types.Transaction{queued},
		transactionPoolBytes([]types.Transaction{pending}, []types.Transaction{queued}),
	)
	assertTxPoolByteAccounting(t, n)

	appended := types.Transaction{
		From:    "0x2222222222222222222222222222222222222222",
		Nonce:   4,
		Payload: map[string]string{"padding": strings.Repeat("a", 47)},
	}
	n.appendQueuedLocked(appended, uint64(len(hash.MustCanonicalBytes(appended))))
	assertTxPoolByteAccounting(t, n)

	replacement := appended
	replacement.Payload = map[string]string{"padding": strings.Repeat("r", 83)}
	n.replaceQueuedLocked(1, replacement, uint64(len(hash.MustCanonicalBytes(replacement))))
	assertTxPoolByteAccounting(t, n)

	n.setTxPoolLocked(nil, nil, 0)
	assertTxPoolByteAccounting(t, n)
}

func TestTxPoolCapacityUsesIncrementalByteTotal(t *testing.T) {
	n := &Node{txPoolBytes: maxTxPoolBytes - 3}
	tx := types.Transaction{From: "0x1111111111111111111111111111111111111111"}
	err := n.validateNewTxPoolCapacityLocked(tx, 4)
	if err == nil || !strings.Contains(err.Error(), "transaction pool byte limit") {
		t.Fatalf("capacity error = %v", err)
	}
}

func assertTxPoolByteAccounting(t *testing.T, n *Node) {
	t.Helper()
	want := transactionPoolBytes(n.mempool, n.queued)
	if n.txPoolBytesLocked() != want {
		t.Fatalf("transaction pool bytes = %d, want %d", n.txPoolBytesLocked(), want)
	}
}
