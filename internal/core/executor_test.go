package core_test

import (
	"testing"

	"chainlab/internal/contracts"
	"chainlab/internal/core"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"
	"chainlab/internal/types"
)

func signedTx(t *testing.T, key any, tx types.Transaction) types.Transaction {
	t.Helper()
	privateKey, ok := key.(interface{})
	if !ok || privateKey == nil {
		t.Fatal("private key is required")
	}
	signer, ok := privateKey.(chaincrypto.PrivateKey)
	if !ok {
		t.Fatal("private key has unexpected type")
	}
	sig, err := chaincrypto.Sign(signer, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = sig
	return tx
}

func newExecutorFixture(t *testing.T) (*state.Store, *core.Executor, chaincrypto.PrivateKey, string, string) {
	t.Helper()
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store := state.NewStore()
	store.SetBalance(alice, 1_000_000)
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", contracts.NewRuntimeWithDefaults())
	return store, executor, key, alice, bob
}

func TestExecuteTransfer(t *testing.T) {
	store, executor, key, alice, bob := newExecutorFixture(t)
	tx := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       bob,
		Nonce:    0,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	})

	receipt, err := executor.Execute(store, tx)
	if err != nil {
		t.Fatal(err)
	}

	if !receipt.Success {
		t.Fatalf("receipt should succeed: %+v", receipt)
	}
	if got := store.GetAccount(alice).Balance; got != 978_900 {
		t.Fatalf("alice balance = %d", got)
	}
	if got := store.GetAccount(bob).Balance; got != 100 {
		t.Fatalf("bob balance = %d", got)
	}
	if got := store.GetAccount(alice).Nonce; got != 1 {
		t.Fatalf("alice nonce = %d", got)
	}
}

func TestRejectsBadSignatureAndBadNonce(t *testing.T) {
	store, executor, key, alice, bob := newExecutorFixture(t)
	tx := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       bob,
		Nonce:    1,
		Value:    1,
		GasLimit: 21_000,
		GasPrice: 1,
	})

	if _, err := executor.Execute(store, tx); err == nil {
		t.Fatal("bad nonce should fail")
	}

	tx.Nonce = 0
	tx.Signature = "0x1234"
	if _, err := executor.Execute(store, tx); err == nil {
		t.Fatal("bad signature should fail")
	}
}

func TestStakeUnstakeAndVote(t *testing.T) {
	store, executor, key, alice, _ := newExecutorFixture(t)

	stake := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxStake,
		From:     alice,
		Nonce:    0,
		Value:    500,
		GasLimit: 30_000,
		GasPrice: 1,
	})
	if _, err := executor.Execute(store, stake); err != nil {
		t.Fatal(err)
	}
	if got := store.StakeOf(alice); got != 500 {
		t.Fatalf("stake = %d", got)
	}

	vote := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxVote,
		From:     alice,
		Nonce:    1,
		GasLimit: 25_000,
		GasPrice: 1,
		Payload: map[string]string{
			"proposal": "upgrade-1",
			"choice":   "yes",
		},
	})
	if _, err := executor.Execute(store, vote); err != nil {
		t.Fatal(err)
	}
	if got := store.Proposal("upgrade-1").Votes["yes"]; got != 500 {
		t.Fatalf("yes vote power = %d", got)
	}

	unstake := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxUnstake,
		From:     alice,
		Nonce:    2,
		Value:    200,
		GasLimit: 30_000,
		GasPrice: 1,
	})
	if _, err := executor.Execute(store, unstake); err != nil {
		t.Fatal(err)
	}
	if got := store.StakeOf(alice); got != 300 {
		t.Fatalf("stake after unstake = %d", got)
	}
}
