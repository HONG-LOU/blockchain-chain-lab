package node

import (
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/types"
)

func TestOrphanReinsertionPreservesCurrentSenderNonceWinner(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := chaincrypto.AddressFromPrivateKey(key)
	n, err := NewDevelopment(Config{
		ChainID: "chainlab-orphan-priority", ProposerKey: key,
		GenesisBalance:  map[string]uint64{sender: 1_000_000},
		GenesisTimeUnix: DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	current := reorgPoolSignedTransfer(t, key, "chainlab-orphan-priority", sender, 0, 2, "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	orphaned := reorgPoolSignedTransfer(t, key, "chainlab-orphan-priority", sender, 0, 1, "0xcccccccccccccccccccccccccccccccccccccccc")
	pending, queued, err := n.reinsertOrphanedTransactionsForState(
		n.state,
		[]types.Transaction{current},
		nil,
		[]types.Transaction{orphaned},
		map[string]struct{}{},
		1,
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Hash() != current.Hash() || len(queued) != 0 {
		t.Fatalf("reinserted pool = pending %+v queued %+v", pending, queued)
	}
}

func TestOrphanReinsertionRespectsPerSenderCapacity(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := chaincrypto.AddressFromPrivateKey(key)
	n, err := NewDevelopment(Config{
		ChainID: "chainlab-orphan-capacity", ProposerKey: key,
		GenesisBalance:  map[string]uint64{sender: 10_000_000},
		GenesisTimeUnix: DeterministicDevGenesisTimeUnix,
	})
	if err != nil {
		t.Fatal(err)
	}
	pending := make([]types.Transaction, maxTransactionsPerSender)
	for index := range pending {
		pending[index] = reorgPoolSignedTransfer(
			t,
			key,
			"chainlab-orphan-capacity",
			sender,
			uint64(index),
			1,
			"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		)
	}
	orphaned := reorgPoolSignedTransfer(
		t,
		key,
		"chainlab-orphan-capacity",
		sender,
		maxTransactionsPerSender,
		1,
		"0xcccccccccccccccccccccccccccccccccccccccc",
	)
	reinserted, queued, err := n.reinsertOrphanedTransactionsForState(
		n.state,
		pending,
		nil,
		[]types.Transaction{orphaned},
		map[string]struct{}{},
		1,
		1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(reinserted) != maxTransactionsPerSender || len(queued) != 0 {
		t.Fatalf("capacity pool sizes = pending %d queued %d", len(reinserted), len(queued))
	}
	for _, tx := range reinserted {
		if tx.Hash() == orphaned.Hash() {
			t.Fatal("orphaned transaction exceeded the per-sender capacity")
		}
	}
}

func reorgPoolSignedTransfer(
	t *testing.T,
	key chaincrypto.PrivateKey,
	chainID string,
	from string,
	nonce uint64,
	gasPrice uint64,
	to string,
) types.Transaction {
	t.Helper()
	return signRuntimeFaultTestTx(t, key, types.Transaction{
		ChainID: chainID, Type: types.TxTransfer, From: from, To: to,
		Nonce: nonce, GasLimit: 21_000, GasPrice: gasPrice,
	})
}
