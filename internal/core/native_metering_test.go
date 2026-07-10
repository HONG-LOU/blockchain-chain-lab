package core_test

import (
	"errors"
	"testing"

	"chainlab/internal/contracts"
	"chainlab/internal/core"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"
	"chainlab/internal/types"
)

func TestNativeV1TransactionGasVector(t *testing.T) {
	store, executor, key, sender := newNativeMeteringExecutor(t)
	deploy := signedTx(t, key, types.Transaction{
		ChainID: "chainlab-local", Type: types.TxDeploy, From: sender,
		Nonce: 0, GasLimit: 100_000, GasPrice: 1,
		Payload: map[string]string{"code_id": "counter.v1", "initial": "2"},
	})
	deployReceipt, err := executor.Execute(store, deploy)
	if err != nil {
		t.Fatal(err)
	}
	if !deployReceipt.Success || deployReceipt.GasUsed != 82_356 {
		t.Fatalf("deploy receipt = %+v", deployReceipt)
	}
	call := signedTx(t, key, types.Transaction{
		ChainID: "chainlab-local", Type: types.TxCall, From: sender, To: deployReceipt.ContractAddress,
		Nonce: 1, GasLimit: 60_000, GasPrice: 1,
		Payload: map[string]string{"method": "increment", "amount": "3"},
	})
	callReceipt, err := executor.Execute(store, call)
	if err != nil {
		t.Fatal(err)
	}
	if !callReceipt.Success || callReceipt.GasUsed != 51_453 || store.GetStorage(deployReceipt.ContractAddress, "count") != "5" {
		t.Fatalf("call receipt=%+v count=%q", callReceipt, store.GetStorage(deployReceipt.ContractAddress, "count"))
	}
}

func TestNativeV1TransactionOutOfGasChargesFullLimit(t *testing.T) {
	store, executor, key, sender := newNativeMeteringExecutor(t)
	address := deployNativeCounterTransaction(t, store, executor, key, sender)
	call := signedTx(t, key, types.Transaction{
		ChainID: "chainlab-local", Type: types.TxCall, From: sender, To: address,
		Nonce: 1, GasLimit: 50_600, GasPrice: 1,
		Payload: map[string]string{"method": "increment", "amount": "3"},
	})
	receipt, err := executor.Execute(store, call)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Success || receipt.FailureCode != types.ReceiptFailureOutOfGas || receipt.GasUsed != call.GasLimit {
		t.Fatalf("out-of-gas receipt = %+v", receipt)
	}
	if store.GetStorage(address, "count") != "2" || store.GetAccount(sender).Nonce != 2 {
		t.Fatalf("count=%q sender=%+v", store.GetStorage(address, "count"), store.GetAccount(sender))
	}
}

func TestNativeV1CorruptedStateIsFatalAndDoesNotCommitAnte(t *testing.T) {
	store, executor, key, sender := newNativeMeteringExecutor(t)
	address := deployNativeCounterTransaction(t, store, executor, key, sender)
	if err := store.SetStorage(address, "count", "bad"); err != nil {
		t.Fatal(err)
	}
	rootBefore := store.Root()
	accountBefore := store.GetAccount(sender)
	call := signedTx(t, key, types.Transaction{
		ChainID: "chainlab-local", Type: types.TxCall, From: sender, To: address,
		Nonce: 1, GasLimit: 60_000, GasPrice: 1,
		Payload: map[string]string{"method": "increment", "amount": "3"},
	})
	if _, err := executor.Execute(store, call); !errors.Is(err, contracts.ErrContractStateFault) {
		t.Fatalf("corrupted state error = %v", err)
	}
	accountAfter := store.GetAccount(sender)
	if store.Root() != rootBefore || accountAfter.Nonce != accountBefore.Nonce || accountAfter.Balance != accountBefore.Balance {
		t.Fatal("fatal native state fault committed nonce or fee")
	}
}

func newNativeMeteringExecutor(t *testing.T) (*state.Store, *core.Executor, chaincrypto.PrivateKey, string) {
	t.Helper()
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sender := chaincrypto.AddressFromPrivateKey(key)
	store := state.NewStore()
	store.SetBalance(sender, 2_000_000)
	return store, core.NewExecutor("chainlab-local", "", contracts.NewRuntimeWithDefaults()), key, sender
}

func deployNativeCounterTransaction(
	t *testing.T,
	store *state.Store,
	executor *core.Executor,
	key chaincrypto.PrivateKey,
	sender string,
) string {
	t.Helper()
	deploy := signedTx(t, key, types.Transaction{
		ChainID: "chainlab-local", Type: types.TxDeploy, From: sender,
		Nonce: 0, GasLimit: 100_000, GasPrice: 1,
		Payload: map[string]string{"code_id": "counter.v1", "initial": "2"},
	})
	receipt, err := executor.Execute(store, deploy)
	if err != nil || !receipt.Success {
		t.Fatalf("deploy receipt=%+v err=%v", receipt, err)
	}
	return receipt.ContractAddress
}
