package core_test

import (
	"encoding/hex"
	"errors"
	"maps"
	"strconv"
	"strings"
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

type countingContract struct {
	calls int
}

func (*countingContract) Deploy(contracts.Context, map[string]string) ([]types.Event, error) {
	return nil, nil
}

func (c *countingContract) Call(contracts.Context, string, map[string]string) ([]types.Event, error) {
	c.calls++
	return nil, nil
}

func (*countingContract) Read(contracts.Context, string, map[string]string) (string, error) {
	return "", nil
}

func TestFeeAdmissionPrecedesContractExecution(t *testing.T) {
	tests := []struct {
		name     string
		gasPrice uint64
		balance  uint64
		want     string
	}{
		{name: "fee cap below base fee", gasPrice: 0, balance: 1_000_000, want: "below block base fee"},
		{name: "insufficient maximum fee balance", gasPrice: 1, balance: 0, want: "insufficient funds"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key, err := chaincrypto.GenerateKey()
			if err != nil {
				t.Fatal(err)
			}
			alice := chaincrypto.AddressFromPrivateKey(key)
			store := state.NewStore()
			store.SetBalance(alice, test.balance)
			runtime := contracts.NewRuntime()
			contract := &countingContract{}
			runtime.Register("counting.v1", contract)
			address, _, err := runtime.Deploy(store, alice, "counting.v1", "seed", nil)
			if err != nil {
				t.Fatal(err)
			}
			executor := core.NewExecutor("chainlab-local", "", runtime)
			tx := signedTx(t, key, types.Transaction{
				ChainID:  "chainlab-local",
				Type:     types.TxCall,
				From:     alice,
				To:       address,
				Nonce:    0,
				GasLimit: 50_000,
				GasPrice: test.gasPrice,
				Payload:  map[string]string{"method": "count"},
			})
			_, err = executor.ExecuteWithContext(store, tx, core.ExecutionContext{BaseFeePerGas: 1})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("fee admission error = %v, want %q", err, test.want)
			}
			if contract.calls != 0 {
				t.Fatalf("contract executed %d times before fee admission", contract.calls)
			}
		})
	}
}

func TestBatchOperationLimitIsEnforcedBeforeExecution(t *testing.T) {
	store, executor, key, alice, bob := newExecutorFixture(t)
	operations := make([]types.BatchOperation, types.MaxBatchOperations+1)
	for index := range operations {
		operations[index] = types.BatchOperation{Type: types.TxTransfer, To: bob, Value: 1}
	}
	tx := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxBatch,
		From:     alice,
		Nonce:    0,
		GasLimit: 42_000,
		GasPrice: 0,
		Batch:    operations,
	})
	rootBefore := store.Root()
	if _, err := executor.Execute(store, tx); err == nil || !strings.Contains(err.Error(), "batch exceeds") {
		t.Fatalf("oversized batch error = %v", err)
	}
	if store.Root() != rootBefore {
		t.Fatal("oversized batch changed state")
	}
	if _, err := core.EstimateGasForBatch(operations); err == nil || !strings.Contains(err.Error(), "batch exceeds") {
		t.Fatalf("oversized batch estimate error = %v", err)
	}
}

func TestBatchValueAndMaximumFeeAreReservedBeforeContractExecution(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	store := state.NewStore()
	const gasLimit = 113_000
	const transferValue = 100
	store.SetBalance(alice, gasLimit+transferValue-1)
	runtime := contracts.NewRuntime()
	contract := &countingContract{}
	runtime.Register("counting.v1", contract)
	address, _, err := runtime.Deploy(store, alice, "counting.v1", "seed", nil)
	if err != nil {
		t.Fatal(err)
	}
	executor := core.NewExecutor("chainlab-local", "", runtime)
	tx := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxBatch,
		From:     alice,
		Nonce:    0,
		GasLimit: gasLimit,
		GasPrice: 1,
		Batch: []types.BatchOperation{
			{Type: types.TxTransfer, To: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Value: transferValue},
			{Type: types.TxCall, To: address, Payload: map[string]string{"method": "count"}},
		},
	})
	rootBefore := store.Root()
	if _, err := executor.ExecuteWithContext(store, tx, core.ExecutionContext{BaseFeePerGas: 1}); err == nil || err.Error() != "insufficient funds" {
		t.Fatalf("batch fee/value reservation error = %v", err)
	}
	if contract.calls != 0 || store.Root() != rootBefore {
		t.Fatalf("underfunded batch executed contract or changed state: calls=%d", contract.calls)
	}
}

func TestSponsoredBatchChecksSenderValueBeforeContractExecution(t *testing.T) {
	userKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	paymasterKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	user := chaincrypto.AddressFromPrivateKey(userKey)
	paymaster := chaincrypto.AddressFromPrivateKey(paymasterKey)
	store := state.NewStore()
	store.SetBalance(user, 99)
	store.SetBalance(paymaster, 200_000)
	runtime := contracts.NewRuntime()
	contract := &countingContract{}
	runtime.Register("counting.v1", contract)
	address, _, err := runtime.Deploy(store, user, "counting.v1", "seed", nil)
	if err != nil {
		t.Fatal(err)
	}
	executor := core.NewExecutor("chainlab-local", "", runtime)
	tx := sponsoredTx(t, userKey, paymasterKey, types.Transaction{
		ChainID:   "chainlab-local",
		Type:      types.TxBatch,
		From:      user,
		Nonce:     0,
		GasLimit:  113_000,
		GasPrice:  1,
		Paymaster: paymaster,
		Batch: []types.BatchOperation{
			{Type: types.TxCall, To: address, Payload: map[string]string{"method": "count"}},
			{Type: types.TxTransfer, To: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Value: 100},
		},
	})
	rootBefore := store.Root()
	if _, err := executor.ExecuteWithContext(store, tx, core.ExecutionContext{BaseFeePerGas: 1}); err == nil || err.Error() != "insufficient funds" {
		t.Fatalf("sponsored batch sender-value admission error = %v", err)
	}
	if contract.calls != 0 || store.Root() != rootBefore {
		t.Fatalf("underfunded sponsored batch executed contract or changed state: calls=%d", contract.calls)
	}
}

func TestFeeAdmissionReservesSignedMaxFeeCap(t *testing.T) {
	store, executor, key, alice, bob := newExecutorFixture(t)
	store.SetBalance(alice, 100_000)
	tx := signedTx(t, key, types.Transaction{
		ChainID:              "chainlab-local",
		Type:                 types.TxTransfer,
		From:                 alice,
		To:                   bob,
		Nonce:                0,
		Value:                1,
		GasLimit:             21_000,
		MaxFeePerGas:         10,
		MaxPriorityFeePerGas: 1,
	})
	if _, err := executor.ExecuteWithContext(store, tx, core.ExecutionContext{BaseFeePerGas: 1}); err == nil || err.Error() != "insufficient funds" {
		t.Fatalf("max-fee reservation error = %v", err)
	}
}

func TestFeeAdmissionRejectsUnstakeSettlementOverflow(t *testing.T) {
	store, executor, key, alice, _ := newExecutorFixture(t)
	const gasLimit = uint64(31_000)
	store.SetBalance(alice, gasLimit)
	if err := store.AddStake(alice, ^uint64(0)); err != nil {
		t.Fatal(err)
	}
	tx := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxUnstake,
		From:     alice,
		Nonce:    0,
		Value:    ^uint64(0),
		GasLimit: gasLimit,
		GasPrice: 1,
	})
	rootBefore := store.Root()
	_, err := executor.ExecuteWithContext(store, tx, core.ExecutionContext{BaseFeePerGas: 1})
	if err == nil || err.Error() != "recipient balance overflow" {
		t.Fatalf("unstake settlement overflow error = %v", err)
	}
	if core.IsFatalExecutionError(err) {
		t.Fatalf("unstake settlement overflow was classified fatal: %v", err)
	}
	if store.Root() != rootBefore {
		t.Fatal("rejected unstake settlement overflow changed state")
	}
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

func TestEIP1559FeeMarketBurnsBaseFeeAndPaysPriorityFee(t *testing.T) {
	store, executor, key, alice, bob := newExecutorFixture(t)
	tx := signedTx(t, key, types.Transaction{
		ChainID:              "chainlab-local",
		Type:                 types.TxTransfer,
		From:                 alice,
		To:                   bob,
		Nonce:                0,
		Value:                100,
		GasLimit:             21_000,
		MaxFeePerGas:         5,
		MaxPriorityFeePerGas: 2,
	})

	receipt, err := executor.ExecuteWithContext(store, tx, core.ExecutionContext{
		BlockHeight:   1,
		BaseFeePerGas: 3,
	})
	if err != nil {
		t.Fatal(err)
	}

	if receipt.EffectiveGasPrice != 5 {
		t.Fatalf("effective gas price = %d", receipt.EffectiveGasPrice)
	}
	if receipt.BaseFeeBurned != 63_000 {
		t.Fatalf("base fee burned = %d", receipt.BaseFeeBurned)
	}
	if receipt.PriorityFeePaid != 42_000 {
		t.Fatalf("priority fee paid = %d", receipt.PriorityFeePaid)
	}
	if got := store.GetAccount(alice).Balance; got != 894_900 {
		t.Fatalf("alice balance = %d", got)
	}
	if got := store.GetAccount(bob).Balance; got != 100 {
		t.Fatalf("bob balance = %d", got)
	}
	if got := store.GetAccount("0xfee0000000000000000000000000000000000000").Balance; got != 42_000 {
		t.Fatalf("fee collector balance = %d", got)
	}
}

func TestExecutorAcceptsEthereumType2TransferSignature(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store := state.NewStore()
	store.SetBalance(alice, 1_000_000)
	tx := types.Transaction{
		ChainID:              "chainlab-local",
		Type:                 types.TxTransfer,
		From:                 alice,
		To:                   bob,
		Nonce:                0,
		Value:                100,
		GasLimit:             21_000,
		MaxFeePerGas:         5,
		MaxPriorityFeePerGas: 1,
		SignatureKind:        types.SignatureKindEthereumType2,
		EthereumRawHash:      "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	digest, err := types.EthereumType2SigningDigest(tx)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := chaincrypto.SignDigest(key, digest)
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = signature
	tx.EthereumRawHash, err = types.EthereumType2TransactionHash(tx)
	if err != nil {
		t.Fatal(err)
	}

	executor := core.NewExecutor("chainlab-local", alice, nil)
	receipt, err := executor.ExecuteWithContext(store, tx, core.ExecutionContext{BlockHeight: 1, BaseFeePerGas: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Success || store.GetAccount(bob).Balance != 100 {
		t.Fatalf("receipt = %+v bob = %+v", receipt, store.GetAccount(bob))
	}
}

func TestSponsoredTransferChargesPaymasterAndNotSenderForGas(t *testing.T) {
	userKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	paymasterKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	user := chaincrypto.AddressFromPrivateKey(userKey)
	paymaster := chaincrypto.AddressFromPrivateKey(paymasterKey)
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	feeCollector := "0xfee0000000000000000000000000000000000000"
	store := state.NewStore()
	store.SetBalance(user, 100)
	store.SetBalance(paymaster, 100_000)
	executor := core.NewExecutor("chainlab-local", feeCollector, contracts.NewRuntimeWithDefaults())

	tx := sponsoredTx(t, userKey, paymasterKey, types.Transaction{
		ChainID:              "chainlab-local",
		Type:                 types.TxTransfer,
		From:                 user,
		To:                   receiver,
		Nonce:                0,
		Value:                100,
		GasLimit:             21_000,
		MaxFeePerGas:         3,
		MaxPriorityFeePerGas: 1,
		Paymaster:            paymaster,
	})

	receipt, err := executor.ExecuteWithContext(store, tx, core.ExecutionContext{
		BlockHeight:   1,
		BaseFeePerGas: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	if receipt.FeePayer != paymaster {
		t.Fatalf("fee payer = %q", receipt.FeePayer)
	}
	if got := store.GetAccount(user).Balance; got != 0 {
		t.Fatalf("user balance = %d", got)
	}
	if got := store.GetAccount(receiver).Balance; got != 100 {
		t.Fatalf("receiver balance = %d", got)
	}
	if got := store.GetAccount(paymaster).Balance; got != 37_000 {
		t.Fatalf("paymaster balance = %d", got)
	}
	if got := store.GetAccount(feeCollector).Balance; got != 21_000 {
		t.Fatalf("fee collector balance = %d", got)
	}
	if len(receipt.Events) != 2 || receipt.Events[1].Type != "paymaster.sponsored" {
		t.Fatalf("events = %#v", receipt.Events)
	}
}

func TestSponsoredTransferRequiresPaymasterSignature(t *testing.T) {
	userKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	paymasterKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	user := chaincrypto.AddressFromPrivateKey(userKey)
	paymaster := chaincrypto.AddressFromPrivateKey(paymasterKey)
	store := state.NewStore()
	store.SetBalance(user, 100)
	store.SetBalance(paymaster, 100_000)
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", contracts.NewRuntimeWithDefaults())

	tx := signedTx(t, userKey, types.Transaction{
		ChainID:   "chainlab-local",
		Type:      types.TxTransfer,
		From:      user,
		To:        "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Nonce:     0,
		Value:     100,
		GasLimit:  21_000,
		GasPrice:  2,
		Paymaster: paymaster,
	})

	if _, err := executor.Execute(store, tx); err == nil {
		t.Fatal("sponsored transaction without paymaster signature should fail")
	}
}

func TestBatchTransferExecutesAtomicallyWithSingleNonce(t *testing.T) {
	store, executor, key, alice, bob := newExecutorFixture(t)
	carol := "0xcccccccccccccccccccccccccccccccccccccccc"
	tx := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxBatch,
		From:     alice,
		Nonce:    0,
		GasLimit: 84_000,
		GasPrice: 1,
		Batch: []types.BatchOperation{
			{Type: types.TxTransfer, To: bob, Value: 10},
			{Type: types.TxTransfer, To: carol, Value: 20},
		},
	})

	receipt, err := executor.Execute(store, tx)
	if err != nil {
		t.Fatal(err)
	}

	if receipt.GasUsed != 84_000 {
		t.Fatalf("gas used = %d", receipt.GasUsed)
	}
	if len(receipt.Events) != 3 || receipt.Events[0].Type != "batch.executed" || receipt.Events[1].Type != "transfer" || receipt.Events[2].Type != "transfer" {
		t.Fatalf("events = %#v", receipt.Events)
	}
	if got := store.GetAccount(alice).Nonce; got != 1 {
		t.Fatalf("alice nonce = %d", got)
	}
	if got := store.GetAccount(alice).Balance; got != 915_970 {
		t.Fatalf("alice balance = %d", got)
	}
	if got := store.GetAccount(bob).Balance; got != 10 {
		t.Fatalf("bob balance = %d", got)
	}
	if got := store.GetAccount(carol).Balance; got != 20 {
		t.Fatalf("carol balance = %d", got)
	}
}

func TestBatchRollsBackAllOperationsWhenOneOperationFails(t *testing.T) {
	store, executor, key, alice, bob := newExecutorFixture(t)
	carol := "0xcccccccccccccccccccccccccccccccccccccccc"
	store.SetBalance(alice, 1_000)
	tx := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxBatch,
		From:     alice,
		Nonce:    0,
		GasLimit: 84_000,
		GasPrice: 1,
		Batch: []types.BatchOperation{
			{Type: types.TxTransfer, To: bob, Value: 100},
			{Type: types.TxTransfer, To: carol, Value: 2_000},
		},
	})

	if _, err := executor.Execute(store, tx); err == nil {
		t.Fatal("batch with a failing operation should fail")
	}
	if got := store.GetAccount(alice).Nonce; got != 0 {
		t.Fatalf("alice nonce = %d", got)
	}
	if got := store.GetAccount(alice).Balance; got != 1_000 {
		t.Fatalf("alice balance = %d", got)
	}
	if got := store.GetAccount(bob).Balance; got != 0 {
		t.Fatalf("bob balance = %d", got)
	}
	if got := store.GetAccount(carol).Balance; got != 0 {
		t.Fatalf("carol balance = %d", got)
	}
	if got := store.GetAccount("0xfee0000000000000000000000000000000000000").Balance; got != 0 {
		t.Fatalf("fee collector balance = %d", got)
	}
}

func TestSponsoredBatchChargesPaymasterAndNotSenderForGas(t *testing.T) {
	userKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	paymasterKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	user := chaincrypto.AddressFromPrivateKey(userKey)
	paymaster := chaincrypto.AddressFromPrivateKey(paymasterKey)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	carol := "0xcccccccccccccccccccccccccccccccccccccccc"
	feeCollector := "0xfee0000000000000000000000000000000000000"
	store := state.NewStore()
	store.SetBalance(user, 30)
	store.SetBalance(paymaster, 500_000)
	executor := core.NewExecutor("chainlab-local", feeCollector, contracts.NewRuntimeWithDefaults())

	tx := sponsoredTx(t, userKey, paymasterKey, types.Transaction{
		ChainID:              "chainlab-local",
		Type:                 types.TxBatch,
		From:                 user,
		Nonce:                0,
		GasLimit:             84_000,
		MaxFeePerGas:         3,
		MaxPriorityFeePerGas: 1,
		Paymaster:            paymaster,
		Batch: []types.BatchOperation{
			{Type: types.TxTransfer, To: bob, Value: 10},
			{Type: types.TxTransfer, To: carol, Value: 20},
		},
	})

	receipt, err := executor.ExecuteWithContext(store, tx, core.ExecutionContext{
		BlockHeight:   1,
		BaseFeePerGas: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	if receipt.FeePayer != paymaster {
		t.Fatalf("fee payer = %q", receipt.FeePayer)
	}
	if got := store.GetAccount(user).Balance; got != 0 {
		t.Fatalf("user balance = %d", got)
	}
	if got := store.GetAccount(bob).Balance; got != 10 {
		t.Fatalf("bob balance = %d", got)
	}
	if got := store.GetAccount(carol).Balance; got != 20 {
		t.Fatalf("carol balance = %d", got)
	}
	if got := store.GetAccount(paymaster).Balance; got != 248_000 {
		t.Fatalf("paymaster balance = %d", got)
	}
	if got := store.GetAccount(feeCollector).Balance; got != 84_000 {
		t.Fatalf("fee collector balance = %d", got)
	}
	if len(receipt.Events) != 4 || receipt.Events[3].Type != "paymaster.sponsored" {
		t.Fatalf("events = %#v", receipt.Events)
	}
}

func TestBatchCallUpdatesContractState(t *testing.T) {
	store, executor, key, alice, _ := newExecutorFixture(t)
	deploy := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     alice,
		Nonce:    0,
		GasLimit: 100_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": "counter.v1",
			"initial": "0",
		},
	})
	deployReceipt, err := executor.Execute(store, deploy)
	if err != nil {
		t.Fatal(err)
	}

	batch := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxBatch,
		From:     alice,
		Nonce:    1,
		GasLimit: 100_000,
		GasPrice: 1,
		Batch: []types.BatchOperation{
			{
				Type: types.TxCall,
				To:   deployReceipt.ContractAddress,
				Payload: map[string]string{
					"method": "increment",
					"amount": "4",
				},
			},
		},
	})
	receipt, err := executor.Execute(store, batch)
	if err != nil {
		t.Fatal(err)
	}

	if receipt.GasUsed != 93_453 {
		t.Fatalf("gas used = %d", receipt.GasUsed)
	}
	if got := store.GetAccount(deployReceipt.ContractAddress).Storage["count"]; got != "4" {
		t.Fatalf("counter value = %q", got)
	}
	if got := store.GetAccount(alice).Nonce; got != 2 {
		t.Fatalf("alice nonce = %d", got)
	}
	if len(receipt.Events) != 2 || receipt.Events[1].Type != "counter.incremented" {
		t.Fatalf("events = %#v", receipt.Events)
	}
}

func TestSmartAccountTransferUsesOwnerSignatureAndContractNonce(t *testing.T) {
	ownerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	owner := chaincrypto.AddressFromPrivateKey(ownerKey)
	smartAccount := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store := state.NewStore()
	store.SetCodeID(smartAccount, contracts.AccountCodeID)
	store.SetStorage(smartAccount, "owner", owner)
	store.SetBalance(smartAccount, 50_000)
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", contracts.NewRuntimeWithDefaults())

	tx := signedTx(t, ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     smartAccount,
		Signer:   owner,
		To:       receiver,
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
	if got := store.GetAccount(smartAccount).Nonce; got != 1 {
		t.Fatalf("smart account nonce = %d", got)
	}
	if got := store.GetAccount(smartAccount).Balance; got != 28_900 {
		t.Fatalf("smart account balance = %d", got)
	}
	if got := store.GetAccount(receiver).Balance; got != 100 {
		t.Fatalf("receiver balance = %d", got)
	}
	if got := store.GetAccount(owner).Balance; got != 0 {
		t.Fatalf("owner balance = %d", got)
	}
}

func TestSetCodeDelegatesEOAToAccountOwnerAndOwnerCanTransfer(t *testing.T) {
	eoaKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	eoa := chaincrypto.AddressFromPrivateKey(eoaKey)
	owner := chaincrypto.AddressFromPrivateKey(ownerKey)
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store := state.NewStore()
	store.SetBalance(eoa, 100_000)
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", contracts.NewRuntimeWithDefaults())

	setCode := signedTx(t, eoaKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxSetCode,
		From:     eoa,
		Nonce:    0,
		GasLimit: 45_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": contracts.AccountCodeID,
			"owner":   owner,
		},
	})
	receipt, err := executor.Execute(store, setCode)
	if err != nil {
		t.Fatal(err)
	}
	if store.GetAccount(eoa).DelegatedCodeID != contracts.AccountCodeID {
		t.Fatalf("delegation = %+v", store.GetAccount(eoa))
	}
	if store.GetStorage(eoa, "owner") != owner {
		t.Fatalf("owner = %q", store.GetStorage(eoa, "owner"))
	}
	if len(receipt.Events) == 0 || receipt.Events[0].Type != "account.delegation_set" {
		t.Fatalf("set-code events = %#v", receipt.Events)
	}

	transfer := signedTx(t, ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     eoa,
		Signer:   owner,
		To:       receiver,
		Nonce:    1,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	transferReceipt, err := executor.Execute(store, transfer)
	if err != nil {
		t.Fatal(err)
	}
	if !transferReceipt.Success {
		t.Fatalf("transfer receipt = %+v", transferReceipt)
	}
	if got := store.GetAccount(eoa).Nonce; got != 2 {
		t.Fatalf("eoa nonce = %d", got)
	}
	if got := store.GetAccount(receiver).Balance; got != 100 {
		t.Fatalf("receiver balance = %d", got)
	}
	if got := store.GetAccount(owner).Nonce; got != 0 {
		t.Fatalf("owner nonce = %d", got)
	}
}

func TestDelegatedEOARejectsUnauthorizedSignerAndClearDisablesDelegation(t *testing.T) {
	eoaKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	intruderKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	eoa := chaincrypto.AddressFromPrivateKey(eoaKey)
	owner := chaincrypto.AddressFromPrivateKey(ownerKey)
	intruder := chaincrypto.AddressFromPrivateKey(intruderKey)
	store := state.NewStore()
	store.SetBalance(eoa, 200_000)
	store.SetDelegatedCodeID(eoa, contracts.AccountCodeID)
	store.SetStorage(eoa, "owner", owner)
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", contracts.NewRuntimeWithDefaults())

	unauthorized := signedTx(t, intruderKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     eoa,
		Signer:   intruder,
		To:       "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Nonce:    0,
		Value:    10,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if _, err := executor.Execute(store, unauthorized); err == nil {
		t.Fatal("unauthorized delegated signer should fail")
	}
	if got := store.GetAccount(eoa).Nonce; got != 0 {
		t.Fatalf("nonce after unauthorized tx = %d", got)
	}

	clear := signedTx(t, eoaKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxSetCode,
		From:     eoa,
		Nonce:    0,
		GasLimit: 45_000,
		GasPrice: 1,
		Payload:  map[string]string{"code_id": ""},
	})
	if _, err := executor.Execute(store, clear); err != nil {
		t.Fatal(err)
	}
	if got := store.GetAccount(eoa).DelegatedCodeID; got != "" {
		t.Fatalf("delegation after clear = %q", got)
	}

	afterClear := signedTx(t, ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     eoa,
		Signer:   owner,
		To:       "0xcccccccccccccccccccccccccccccccccccccccc",
		Nonce:    1,
		Value:    10,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if _, err := executor.Execute(store, afterClear); err == nil {
		t.Fatal("owner signer should fail after delegation is cleared")
	}
}

func TestSetCodeRejectsInvalidOwnerAndClearsPreviousAuthorizationPolicy(t *testing.T) {
	eoaKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	eoa := chaincrypto.AddressFromPrivateKey(eoaKey)
	owner := chaincrypto.AddressFromPrivateKey(ownerKey)
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", contracts.NewRuntimeWithDefaults())

	invalidOwners := []struct {
		name      string
		owner     string
		wantError string
	}{
		{name: "malformed", owner: "0x1234", wantError: "set_code owner must be a 20-byte hex address"},
		{name: "zero", owner: "0x0000000000000000000000000000000000000000", wantError: "set_code owner must not be the zero address"},
		{name: "self", owner: eoa, wantError: "set_code owner must differ from delegated account"},
	}
	for _, test := range invalidOwners {
		t.Run(test.name, func(t *testing.T) {
			store := state.NewStore()
			store.SetBalance(eoa, 100_000)
			tx := signedTx(t, eoaKey, types.Transaction{
				ChainID:  "chainlab-local",
				Type:     types.TxSetCode,
				From:     eoa,
				Nonce:    0,
				GasLimit: 45_000,
				GasPrice: 1,
				Payload:  map[string]string{"code_id": contracts.AccountCodeID, "owner": test.owner},
			})
			rootBefore := store.Root()
			if _, err := executor.Execute(store, tx); err == nil || err.Error() != test.wantError {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
			if store.Root() != rootBefore || store.GetAccount(eoa).Nonce != 0 {
				t.Fatal("invalid delegated owner mutated state")
			}
		})
	}

	store := state.NewStore()
	store.SetBalance(eoa, 100_000)
	store.SetDelegatedCodeID(eoa, contracts.AccountCodeID)
	store.SetStorage(eoa, "owner", "0xdddddddddddddddddddddddddddddddddddddddd")
	store.SetStorage(eoa, "session:0xcccccccccccccccccccccccccccccccccccccccc:limit", "100")
	store.SetStorage(eoa, "recovery:guardians", "0xcccccccccccccccccccccccccccccccccccccccc")
	store.SetStorage(eoa, "recovery:votes", "0xcccccccccccccccccccccccccccccccccccccccc="+owner)
	store.SetStorage(eoa, "profile:name", "alice")
	reconfigure := signedTx(t, eoaKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxSetCode,
		From:     eoa,
		Nonce:    0,
		GasLimit: 45_000,
		GasPrice: 1,
		Payload:  map[string]string{"code_id": contracts.AccountCodeID, "owner": owner},
	})
	if _, err := executor.Execute(store, reconfigure); err != nil {
		t.Fatal(err)
	}
	if got := store.GetStorage(eoa, "owner"); got != owner {
		t.Fatalf("reconfigured owner = %q", got)
	}
	for key := range store.GetAccount(eoa).Storage {
		if strings.HasPrefix(key, "session:") || strings.HasPrefix(key, "recovery:") {
			t.Fatalf("stale delegated authorization %q remained", key)
		}
	}
	if got := store.GetStorage(eoa, "profile:name"); got != "alice" {
		t.Fatalf("unrelated delegated storage = %q", got)
	}
}

func TestAccountSessionKeyCanTransferWithinPolicy(t *testing.T) {
	ownerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sessionKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	owner := chaincrypto.AddressFromPrivateKey(ownerKey)
	session := chaincrypto.AddressFromPrivateKey(sessionKey)
	smartAccount := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store := state.NewStore()
	store.SetCodeID(smartAccount, contracts.AccountCodeID)
	store.SetStorage(smartAccount, "owner", owner)
	store.SetBalance(smartAccount, 200_000)
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", contracts.NewRuntimeWithDefaults())

	addSession := signedTx(t, ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxSessionKey,
		From:     smartAccount,
		Signer:   owner,
		Nonce:    0,
		GasLimit: 45_000,
		GasPrice: 1,
		Payload: map[string]string{
			"action":  "add",
			"key":     session,
			"limit":   "100",
			"expires": "10",
			"to":      receiver,
		},
	})
	addReceipt, err := executor.ExecuteWithContext(store, addSession, core.ExecutionContext{BlockHeight: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(addReceipt.Events) == 0 || addReceipt.Events[0].Type != "account.session_key_added" {
		t.Fatalf("session add events = %#v", addReceipt.Events)
	}
	if got := store.GetStorage(smartAccount, "session:"+session+":limit"); got != "100" {
		t.Fatalf("session limit = %q", got)
	}
	if got := store.GetStorage(smartAccount, "session:"+session+":spent"); got != "0" {
		t.Fatalf("session spent = %q", got)
	}

	transfer := signedTx(t, sessionKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     smartAccount,
		Signer:   session,
		To:       receiver,
		Nonce:    1,
		Value:    40,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	transferReceipt, err := executor.ExecuteWithContext(store, transfer, core.ExecutionContext{BlockHeight: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.GetAccount(smartAccount).Nonce; got != 2 {
		t.Fatalf("smart account nonce = %d", got)
	}
	if got := store.GetAccount(receiver).Balance; got != 40 {
		t.Fatalf("receiver balance = %d", got)
	}
	if got := store.GetAccount(session).Nonce; got != 0 {
		t.Fatalf("session nonce = %d", got)
	}
	if got := store.GetStorage(smartAccount, "session:"+session+":spent"); got != "40" {
		t.Fatalf("session spent after transfer = %q", got)
	}
	if len(transferReceipt.Events) < 2 || transferReceipt.Events[1].Type != "account.session_key_used" {
		t.Fatalf("session transfer events = %#v", transferReceipt.Events)
	}
}

func TestDelegatedEOASessionKeyCanTransferWithinPolicy(t *testing.T) {
	eoaKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sessionKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	eoa := chaincrypto.AddressFromPrivateKey(eoaKey)
	owner := chaincrypto.AddressFromPrivateKey(ownerKey)
	session := chaincrypto.AddressFromPrivateKey(sessionKey)
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store := state.NewStore()
	store.SetDelegatedCodeID(eoa, contracts.AccountCodeID)
	store.SetStorage(eoa, "owner", owner)
	store.SetBalance(eoa, 200_000)
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", contracts.NewRuntimeWithDefaults())

	addSession := signedTx(t, ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxSessionKey,
		From:     eoa,
		Signer:   owner,
		Nonce:    0,
		GasLimit: 45_000,
		GasPrice: 1,
		Payload: map[string]string{
			"action": "add",
			"key":    session,
			"limit":  "50",
			"to":     receiver,
		},
	})
	if _, err := executor.ExecuteWithContext(store, addSession, core.ExecutionContext{BlockHeight: 1}); err != nil {
		t.Fatal(err)
	}

	transfer := signedTx(t, sessionKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     eoa,
		Signer:   session,
		To:       receiver,
		Nonce:    1,
		Value:    25,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if _, err := executor.ExecuteWithContext(store, transfer, core.ExecutionContext{BlockHeight: 2}); err != nil {
		t.Fatal(err)
	}
	if got := store.GetAccount(receiver).Balance; got != 25 {
		t.Fatalf("receiver balance = %d", got)
	}
	if got := store.GetStorage(eoa, "session:"+session+":spent"); got != "25" {
		t.Fatalf("session spent = %q", got)
	}
	if got := store.GetAccount(session).Nonce; got != 0 {
		t.Fatalf("session nonce = %d", got)
	}
}

func TestAccountSessionKeyCanCallAllowedContractMethod(t *testing.T) {
	ownerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sessionKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	owner := chaincrypto.AddressFromPrivateKey(ownerKey)
	session := chaincrypto.AddressFromPrivateKey(sessionKey)
	smartAccount := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	store.SetCodeID(smartAccount, contracts.AccountCodeID)
	store.SetStorage(smartAccount, "owner", owner)
	store.SetBalance(smartAccount, 300_000)
	store.SetBalance(owner, 500_000)
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", contracts.NewRuntimeWithDefaults())

	deploy := signedTx(t, ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     owner,
		Nonce:    0,
		GasLimit: 100_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": "counter.v1",
			"initial": "0",
		},
	})
	deployReceipt, err := executor.ExecuteWithContext(store, deploy, core.ExecutionContext{BlockHeight: 1})
	if err != nil {
		t.Fatal(err)
	}
	counter := deployReceipt.ContractAddress

	addSession := signedTx(t, ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxSessionKey,
		From:     smartAccount,
		Signer:   owner,
		Nonce:    0,
		GasLimit: 45_000,
		GasPrice: 1,
		Payload: map[string]string{
			"action":      "add",
			"key":         session,
			"expires":     "10",
			"call_to":     counter,
			"call_method": "increment",
		},
	})
	addReceipt, err := executor.ExecuteWithContext(store, addSession, core.ExecutionContext{BlockHeight: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(addReceipt.Events) == 0 || addReceipt.Events[0].Attributes["call_to"] != counter || addReceipt.Events[0].Attributes["call_method"] != "increment" {
		t.Fatalf("session add events = %#v", addReceipt.Events)
	}
	if got := store.GetStorage(smartAccount, "session:"+session+":call_to"); got != counter {
		t.Fatalf("session call_to = %q", got)
	}

	call := signedTx(t, sessionKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     smartAccount,
		Signer:   session,
		To:       counter,
		Nonce:    1,
		GasLimit: 60_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method": "increment",
			"amount": "4",
		},
	})
	callReceipt, err := executor.ExecuteWithContext(store, call, core.ExecutionContext{BlockHeight: 3})
	if err != nil {
		t.Fatal(err)
	}
	if got := store.GetStorage(counter, "count"); got != "4" {
		t.Fatalf("counter value = %q", got)
	}
	if got := store.GetAccount(smartAccount).Nonce; got != 2 {
		t.Fatalf("smart account nonce = %d", got)
	}
	if got := store.GetAccount(session).Nonce; got != 0 {
		t.Fatalf("session nonce = %d", got)
	}
	if len(callReceipt.Events) < 2 || callReceipt.Events[len(callReceipt.Events)-1].Type != "account.session_key_used" || callReceipt.Events[len(callReceipt.Events)-1].Attributes["type"] != "call" {
		t.Fatalf("session call events = %#v", callReceipt.Events)
	}
}

func TestAccountSessionKeyRejectsInvalidContractCallPolicyUse(t *testing.T) {
	ownerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sessionKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	owner := chaincrypto.AddressFromPrivateKey(ownerKey)
	session := chaincrypto.AddressFromPrivateKey(sessionKey)
	smartAccount := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	counter := "0xcccccccccccccccccccccccccccccccccccccccc"
	otherCounter := "0xdddddddddddddddddddddddddddddddddddddddd"
	store := state.NewStore()
	store.SetCodeID(smartAccount, contracts.AccountCodeID)
	store.SetStorage(smartAccount, "owner", owner)
	store.SetStorage(smartAccount, "session:"+session+":call_to", counter)
	store.SetStorage(smartAccount, "session:"+session+":call_method", "increment")
	store.SetStorage(smartAccount, "session:"+session+":expires", "5")
	store.SetBalance(smartAccount, 300_000)
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", contracts.NewRuntimeWithDefaults())

	wrongTarget := signedTx(t, sessionKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     smartAccount,
		Signer:   session,
		To:       otherCounter,
		Nonce:    0,
		GasLimit: 60_000,
		GasPrice: 1,
		Payload:  map[string]string{"method": "increment"},
	})
	if _, err := executor.ExecuteWithContext(store, wrongTarget, core.ExecutionContext{BlockHeight: 1}); err == nil {
		t.Fatal("session key should not call a disallowed target")
	}

	wrongMethod := signedTx(t, sessionKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     smartAccount,
		Signer:   session,
		To:       counter,
		Nonce:    0,
		GasLimit: 60_000,
		GasPrice: 1,
		Payload:  map[string]string{"method": "reset"},
	})
	if _, err := executor.ExecuteWithContext(store, wrongMethod, core.ExecutionContext{BlockHeight: 1}); err == nil {
		t.Fatal("session key should not call a disallowed method")
	}

	expired := signedTx(t, sessionKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     smartAccount,
		Signer:   session,
		To:       counter,
		Nonce:    0,
		GasLimit: 60_000,
		GasPrice: 1,
		Payload:  map[string]string{"method": "increment"},
	})
	if _, err := executor.ExecuteWithContext(store, expired, core.ExecutionContext{BlockHeight: 6}); err == nil {
		t.Fatal("session key should expire for calls")
	}

	transfer := signedTx(t, sessionKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     smartAccount,
		Signer:   session,
		To:       otherCounter,
		Nonce:    0,
		Value:    1,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if _, err := executor.ExecuteWithContext(store, transfer, core.ExecutionContext{BlockHeight: 1}); err == nil {
		t.Fatal("call-only session key should not authorize transfer")
	}

	revoke := signedTx(t, ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxSessionKey,
		From:     smartAccount,
		Signer:   owner,
		Nonce:    0,
		GasLimit: 45_000,
		GasPrice: 1,
		Payload: map[string]string{
			"action": "revoke",
			"key":    session,
		},
	})
	if _, err := executor.ExecuteWithContext(store, revoke, core.ExecutionContext{BlockHeight: 1}); err != nil {
		t.Fatal(err)
	}
	if got := store.GetStorage(smartAccount, "session:"+session+":call_to"); got != "" {
		t.Fatalf("revoked call_to = %q", got)
	}
}

func TestAccountSessionKeyRejectsInvalidPolicyUseAndRevocation(t *testing.T) {
	ownerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	sessionKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	intruderKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	owner := chaincrypto.AddressFromPrivateKey(ownerKey)
	session := chaincrypto.AddressFromPrivateKey(sessionKey)
	intruder := chaincrypto.AddressFromPrivateKey(intruderKey)
	smartAccount := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	otherReceiver := "0xcccccccccccccccccccccccccccccccccccccccc"
	store := state.NewStore()
	store.SetCodeID(smartAccount, contracts.AccountCodeID)
	store.SetStorage(smartAccount, "owner", owner)
	store.SetStorage(smartAccount, "session:"+session+":limit", "50")
	store.SetStorage(smartAccount, "session:"+session+":spent", "20")
	store.SetStorage(smartAccount, "session:"+session+":expires", "5")
	store.SetStorage(smartAccount, "session:"+session+":to", receiver)
	store.SetBalance(smartAccount, 300_000)
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", contracts.NewRuntimeWithDefaults())

	nonOwnerAdd := signedTx(t, intruderKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxSessionKey,
		From:     smartAccount,
		Signer:   intruder,
		Nonce:    0,
		GasLimit: 45_000,
		GasPrice: 1,
		Payload: map[string]string{
			"action": "add",
			"key":    intruder,
			"limit":  "10",
		},
	})
	if _, err := executor.ExecuteWithContext(store, nonOwnerAdd, core.ExecutionContext{BlockHeight: 1}); err == nil {
		t.Fatal("non-owner should not add session key")
	}

	overLimit := signedTx(t, sessionKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     smartAccount,
		Signer:   session,
		To:       receiver,
		Nonce:    0,
		Value:    31,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if _, err := executor.ExecuteWithContext(store, overLimit, core.ExecutionContext{BlockHeight: 1}); err == nil {
		t.Fatal("session key should not exceed remaining limit")
	}

	wrongRecipient := signedTx(t, sessionKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     smartAccount,
		Signer:   session,
		To:       otherReceiver,
		Nonce:    0,
		Value:    10,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if _, err := executor.ExecuteWithContext(store, wrongRecipient, core.ExecutionContext{BlockHeight: 1}); err == nil {
		t.Fatal("session key should not transfer to a disallowed recipient")
	}

	expired := signedTx(t, sessionKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     smartAccount,
		Signer:   session,
		To:       receiver,
		Nonce:    0,
		Value:    10,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if _, err := executor.ExecuteWithContext(store, expired, core.ExecutionContext{BlockHeight: 6}); err == nil {
		t.Fatal("session key should expire after configured block height")
	}
	if got := store.GetAccount(smartAccount).Nonce; got != 0 {
		t.Fatalf("nonce after rejected session attempts = %d", got)
	}

	revoke := signedTx(t, ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxSessionKey,
		From:     smartAccount,
		Signer:   owner,
		Nonce:    0,
		GasLimit: 45_000,
		GasPrice: 1,
		Payload: map[string]string{
			"action": "revoke",
			"key":    session,
		},
	})
	revokeReceipt, err := executor.ExecuteWithContext(store, revoke, core.ExecutionContext{BlockHeight: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(revokeReceipt.Events) == 0 || revokeReceipt.Events[0].Type != "account.session_key_revoked" {
		t.Fatalf("session revoke events = %#v", revokeReceipt.Events)
	}
	if got := store.GetStorage(smartAccount, "session:"+session+":limit"); got != "" {
		t.Fatalf("revoked session limit = %q", got)
	}

	afterRevoke := signedTx(t, sessionKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     smartAccount,
		Signer:   session,
		To:       receiver,
		Nonce:    1,
		Value:    10,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if _, err := executor.ExecuteWithContext(store, afterRevoke, core.ExecutionContext{BlockHeight: 1}); err == nil {
		t.Fatal("revoked session key should not authorize transfer")
	}
}

const recoveryFeeCollector = "0xfee0000000000000000000000000000000000000"

func requireRecoveryExecutionRevert(
	t *testing.T,
	fixture recoveryFixture,
	tx types.Transaction,
	context core.ExecutionContext,
) types.Receipt {
	t.Helper()
	before := fixture.store.Clone()
	receipt, err := fixture.executor.ExecuteWithContext(fixture.store, tx, context)
	if err != nil {
		t.Fatalf("included recovery failure returned error: %v", err)
	}
	if receipt.Success || receipt.FailureCode != types.ReceiptFailureExecutionReverted || receipt.Error != "" {
		t.Fatalf("recovery failure receipt = %+v", receipt)
	}
	if receipt.GasUsed == 0 {
		t.Fatal("reverted recovery did not consume gas")
	}

	expected := before.Clone()
	if err := expected.IncrementNonce(tx.From); err != nil {
		t.Fatal(err)
	}
	feePayer := strings.ToLower(strings.TrimSpace(tx.From))
	action := strings.ToLower(strings.TrimSpace(tx.Payload["action"]))
	if action == "approve" || action == "execute" {
		feePayer = strings.ToLower(strings.TrimSpace(tx.Signer))
	}
	wantReceiptFeePayer := ""
	if feePayer != strings.ToLower(strings.TrimSpace(tx.From)) {
		wantReceiptFeePayer = feePayer
	}
	if receipt.FeePayer != wantReceiptFeePayer {
		t.Fatalf("recovery failure fee payer = %q, want %q", receipt.FeePayer, wantReceiptFeePayer)
	}
	fee, err := core.CalculateFee(tx, receipt.GasUsed, context.BaseFeePerGas)
	if err != nil {
		t.Fatal(err)
	}
	if err := expected.SubBalance(feePayer, fee.TotalFee); err != nil {
		t.Fatal(err)
	}
	if err := expected.AddBalance(recoveryFeeCollector, fee.PriorityFee); err != nil {
		t.Fatal(err)
	}
	if fixture.store.Root() != expected.Root() {
		t.Fatal("reverted recovery changed state beyond nonce and fee settlement")
	}
	return receipt
}

func TestAccountRecoveryRotatesOwnerAfterThresholdAndDelay(t *testing.T) {
	fixture := newRecoveryFixture(t, false)
	store := fixture.store
	executor := fixture.executor

	store.SetStorage(fixture.account, "session:"+fixture.guardianA+":limit", "100")
	store.SetStorage(fixture.account, "session:"+fixture.guardianA+":spent", "0")
	store.SetStorage(fixture.account, "session:"+fixture.guardianA+":call_to", "0xcccccccccccccccccccccccccccccccccccccccc")
	store.SetStorage(fixture.account, "session:"+fixture.guardianA+":call_method", "increment")

	configure := recoveryTx(t, fixture.ownerKey, fixture.account, 0, map[string]string{
		"action":    "configure",
		"guardians": fixture.guardianA + "," + fixture.guardianB,
		"threshold": "2",
		"delay":     "3",
	})
	configured, err := executor.ExecuteWithContext(store, configure, core.ExecutionContext{BlockHeight: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(configured.Events) != 1 || configured.Events[0].Type != "account.recovery_configured" {
		t.Fatalf("configure events = %#v", configured.Events)
	}
	if got := store.GetStorage(fixture.account, "recovery:guardians"); got != fixture.guardianA+","+fixture.guardianB {
		t.Fatalf("guardians = %q", got)
	}

	approveA := recoveryTx(t, fixture.guardianAKey, fixture.account, 1, map[string]string{
		"action":    "approve",
		"new_owner": fixture.newOwner,
	})
	approvedA, err := executor.ExecuteWithContext(store, approveA, core.ExecutionContext{BlockHeight: 11})
	if err != nil {
		t.Fatal(err)
	}
	if approvedA.Events[0].Attributes["approvals"] != "1" {
		t.Fatalf("first approval event = %#v", approvedA.Events)
	}
	if got := store.GetStorage(fixture.account, "recovery:execute_after"); got != "" {
		t.Fatalf("execute_after before threshold = %q", got)
	}
	if got := store.GetAccount(fixture.guardianA).Nonce; got != 0 {
		t.Fatalf("guardian nonce = %d", got)
	}

	earlyThreshold := recoveryTx(t, fixture.guardianAKey, fixture.account, 2, map[string]string{
		"action":    "execute",
		"new_owner": fixture.newOwner,
	})
	requireRecoveryExecutionRevert(t, fixture, earlyThreshold, core.ExecutionContext{BlockHeight: 14})

	approveB := recoveryTx(t, fixture.guardianBKey, fixture.account, 3, map[string]string{
		"action":    "approve",
		"new_owner": fixture.newOwner,
	})
	approvedB, err := executor.ExecuteWithContext(store, approveB, core.ExecutionContext{BlockHeight: 12})
	if err != nil {
		t.Fatal(err)
	}
	if approvedB.FeePayer != fixture.guardianB || approvedB.Events[0].Attributes["pending"] != "true" {
		t.Fatalf("threshold approval receipt = %+v", approvedB)
	}
	if got := store.GetStorage(fixture.account, "recovery:execute_after"); got != "15" {
		t.Fatalf("execute_after after threshold = %q", got)
	}
	earlyDelay := recoveryTx(t, fixture.guardianAKey, fixture.account, 4, map[string]string{
		"action":    "execute",
		"new_owner": fixture.newOwner,
	})
	requireRecoveryExecutionRevert(t, fixture, earlyDelay, core.ExecutionContext{BlockHeight: 13})

	execute := recoveryTx(t, fixture.guardianBKey, fixture.account, 5, map[string]string{
		"action":    "execute",
		"new_owner": fixture.newOwner,
	})
	executed, err := executor.ExecuteWithContext(store, execute, core.ExecutionContext{BlockHeight: 15})
	if err != nil {
		t.Fatal(err)
	}
	if len(executed.Events) != 1 || executed.Events[0].Type != "account.recovery_executed" {
		t.Fatalf("execute events = %#v", executed.Events)
	}
	if got := store.GetStorage(fixture.account, "owner"); got != fixture.newOwner {
		t.Fatalf("rotated owner = %q", got)
	}
	for _, key := range []string{"recovery:pending_owner", "recovery:execute_after", "recovery:approvals"} {
		if got := store.GetStorage(fixture.account, key); got != "" {
			t.Fatalf("pending key %q = %q", key, got)
		}
	}
	if got := store.GetStorage(fixture.account, "recovery:guardians"); got == "" {
		t.Fatal("successful recovery should preserve guardian configuration")
	}
	if executed.FeePayer != fixture.guardianB || executed.Events[0].Attributes["account"] != fixture.account || executed.Events[0].Attributes["new_owner"] != fixture.newOwner {
		t.Fatalf("execute receipt = %+v", executed)
	}

	oldOwnerTransfer := signedTx(t, fixture.ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     fixture.account,
		Signer:   fixture.owner,
		To:       fixture.receiver,
		Nonce:    6,
		Value:    1,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if _, err := executor.Execute(store, oldOwnerTransfer); err == nil {
		t.Fatal("old owner should not authorize after recovery")
	}
	oldSessionTransfer := signedTx(t, fixture.guardianAKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     fixture.account,
		Signer:   fixture.guardianA,
		To:       fixture.receiver,
		Nonce:    6,
		Value:    1,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if _, err := executor.Execute(store, oldSessionTransfer); err == nil {
		t.Fatal("session policy installed before recovery should be invalidated")
	}
	oldSessionCall := signedTx(t, fixture.guardianAKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     fixture.account,
		Signer:   fixture.guardianA,
		To:       "0xcccccccccccccccccccccccccccccccccccccccc",
		Nonce:    6,
		GasLimit: 50_000,
		GasPrice: 1,
		Payload:  map[string]string{"method": "increment"},
	})
	if _, err := executor.Execute(store, oldSessionCall); err == nil {
		t.Fatal("call session policy installed before recovery should be invalidated")
	}
	newOwnerTransfer := signedTx(t, fixture.newOwnerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     fixture.account,
		Signer:   fixture.newOwner,
		To:       fixture.receiver,
		Nonce:    6,
		Value:    10,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if _, err := executor.Execute(store, newOwnerTransfer); err != nil {
		t.Fatal(err)
	}
	if got := store.GetAccount(fixture.receiver).Balance; got != 10 {
		t.Fatalf("receiver balance = %d", got)
	}
	if got := store.GetAccount(fixture.account).Balance; got != 1_923_990 {
		t.Fatalf("recovered account balance = %d", got)
	}
	if got := store.GetAccount(fixture.guardianA).Balance; got != 335_000 {
		t.Fatalf("guardian A balance = %d", got)
	}
	if got := store.GetAccount(fixture.guardianB).Balance; got != 390_000 {
		t.Fatalf("guardian B balance = %d", got)
	}
	if got := store.GetAccount(fixture.guardianB).Nonce; got != 0 {
		t.Fatalf("guardian B nonce = %d", got)
	}
	if got := store.GetAccount(recoveryFeeCollector).Balance; got != 351_000 {
		t.Fatalf("fee collector balance = %d", got)
	}
}

func TestDelegatedEOARecoveryCancelClearAndRoleIsolation(t *testing.T) {
	fixture := newRecoveryFixture(t, true)
	store := fixture.store
	executor := fixture.executor

	configure := recoveryTx(t, fixture.ownerKey, fixture.account, 0, map[string]string{
		"action":    "configure",
		"guardians": fixture.guardianA + "," + fixture.guardianB,
		"threshold": "1",
		"delay":     "0",
	})
	if _, err := executor.ExecuteWithContext(store, configure, core.ExecutionContext{BlockHeight: 1}); err != nil {
		t.Fatal(err)
	}
	approve := recoveryTx(t, fixture.guardianAKey, fixture.account, 1, map[string]string{
		"action":    "approve",
		"new_owner": fixture.newOwner,
	})
	if _, err := executor.ExecuteWithContext(store, approve, core.ExecutionContext{BlockHeight: 2}); err != nil {
		t.Fatal(err)
	}
	cancel := recoveryTx(t, fixture.ownerKey, fixture.account, 2, map[string]string{"action": "cancel"})
	cancelled, err := executor.ExecuteWithContext(store, cancel, core.ExecutionContext{BlockHeight: 2})
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Events[0].Type != "account.recovery_cancelled" || store.GetStorage(fixture.account, "recovery:pending_owner") != "" {
		t.Fatalf("cancel result = %#v", cancelled)
	}
	if store.GetStorage(fixture.account, "recovery:guardians") == "" {
		t.Fatal("cancel should preserve guardian configuration")
	}

	guardianTransfer := signedTx(t, fixture.guardianAKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     fixture.account,
		Signer:   fixture.guardianA,
		To:       fixture.receiver,
		Nonce:    3,
		Value:    1,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if _, err := executor.Execute(store, guardianTransfer); err == nil {
		t.Fatal("guardian recovery authority must not authorize transfers")
	}

	rejectedRoleRoot := store.Root()
	intruderConfigure := recoveryTx(t, fixture.intruderKey, fixture.account, 3, map[string]string{
		"action":    "configure",
		"guardians": fixture.intruder,
		"threshold": "1",
		"delay":     "0",
	})
	if _, err := executor.Execute(store, intruderConfigure); err == nil || err.Error() != "account recovery owner action requires current owner" {
		t.Fatalf("non-owner configure error = %v", err)
	}
	intruderApprove := recoveryTx(t, fixture.intruderKey, fixture.account, 3, map[string]string{
		"action":    "approve",
		"new_owner": fixture.newOwner,
	})
	if _, err := executor.Execute(store, intruderApprove); err == nil || err.Error() != "account recovery guardian action requires configured guardian" {
		t.Fatalf("non-guardian approve error = %v", err)
	}
	guardianClear := recoveryTx(t, fixture.guardianAKey, fixture.account, 3, map[string]string{"action": "clear"})
	if _, err := executor.Execute(store, guardianClear); err == nil || err.Error() != "account recovery owner action requires current owner" {
		t.Fatalf("guardian clear error = %v", err)
	}
	if store.Root() != rejectedRoleRoot || store.GetAccount(fixture.account).Nonce != 3 {
		t.Fatal("rejected recovery role actions mutated state")
	}

	clear := recoveryTx(t, fixture.ownerKey, fixture.account, 3, map[string]string{"action": "clear"})
	cleared, err := executor.Execute(store, clear)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Events[0].Type != "account.recovery_cleared" {
		t.Fatalf("clear events = %#v", cleared.Events)
	}
	for _, key := range []string{"recovery:guardians", "recovery:threshold", "recovery:delay", "recovery:pending_owner", "recovery:execute_after", "recovery:expires_at", "recovery:approvals", "recovery:votes"} {
		if got := store.GetStorage(fixture.account, key); got != "" {
			t.Fatalf("cleared key %q = %q", key, got)
		}
	}
}

func TestDelegatedEOARecoveryRotatesOwnerWithGuardianFundedGas(t *testing.T) {
	fixture := newRecoveryFixture(t, true)
	store := fixture.store
	store.SetBalance(fixture.account, 100_000)

	configure := recoveryTx(t, fixture.ownerKey, fixture.account, 0, map[string]string{
		"action":    "configure",
		"guardians": fixture.guardianA,
		"threshold": "1",
		"delay":     "0",
	})
	configured, err := fixture.executor.ExecuteWithContext(store, configure, core.ExecutionContext{BlockHeight: 1})
	if err != nil {
		t.Fatal(err)
	}
	if configured.FeePayer != "" || store.GetAccount(fixture.account).Balance != 45_000 {
		t.Fatalf("owner-funded configure = %+v, account = %+v", configured, store.GetAccount(fixture.account))
	}

	approve := recoveryTx(t, fixture.guardianAKey, fixture.account, 1, map[string]string{
		"action":    "approve",
		"new_owner": fixture.newOwner,
	})
	approved, err := fixture.executor.ExecuteWithContext(store, approve, core.ExecutionContext{BlockHeight: 2})
	if err != nil {
		t.Fatal(err)
	}
	if approved.FeePayer != fixture.guardianA || store.GetAccount(fixture.account).Balance != 45_000 {
		t.Fatalf("guardian-funded approval = %+v", approved)
	}

	execute := recoveryTx(t, fixture.guardianAKey, fixture.account, 2, map[string]string{
		"action":    "execute",
		"new_owner": fixture.newOwner,
	})
	executed, err := fixture.executor.ExecuteWithContext(store, execute, core.ExecutionContext{BlockHeight: 2})
	if err != nil {
		t.Fatal(err)
	}
	if executed.FeePayer != fixture.guardianA || store.GetStorage(fixture.account, "owner") != fixture.newOwner {
		t.Fatalf("delegated execute = %+v", executed)
	}
	if got := store.GetAccount(fixture.account).DelegatedCodeID; got != contracts.AccountCodeID {
		t.Fatalf("delegated code after recovery = %q", got)
	}
	if got := store.GetAccount(fixture.guardianA).Balance; got != 390_000 {
		t.Fatalf("guardian balance = %d", got)
	}
	if got := store.GetAccount(fixture.guardianA).Nonce; got != 0 {
		t.Fatalf("guardian nonce = %d", got)
	}

	transfer := signedTx(t, fixture.newOwnerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     fixture.account,
		Signer:   fixture.newOwner,
		To:       fixture.receiver,
		Nonce:    3,
		Value:    10,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if _, err := fixture.executor.Execute(store, transfer); err != nil {
		t.Fatal(err)
	}
	if got := store.GetAccount(fixture.receiver).Balance; got != 10 {
		t.Fatalf("receiver balance = %d", got)
	}
}

func TestAccountRecoveryGuardianInsufficientGasBalanceRollsBackApproval(t *testing.T) {
	fixture := newRecoveryFixture(t, false)
	fixture.store.SetStorage(fixture.account, "recovery:guardians", fixture.guardianA)
	fixture.store.SetStorage(fixture.account, "recovery:threshold", "1")
	fixture.store.SetStorage(fixture.account, "recovery:delay", "0")
	fixture.store.SetBalance(fixture.guardianA, 54_999)
	approve := recoveryTx(t, fixture.guardianAKey, fixture.account, 0, map[string]string{
		"action":    "approve",
		"new_owner": fixture.newOwner,
	})
	rootBefore := fixture.store.Root()
	if _, err := fixture.executor.ExecuteWithContext(fixture.store, approve, core.ExecutionContext{BlockHeight: 1}); err == nil || err.Error() != "insufficient funds" {
		t.Fatalf("insufficient guardian fee error = %v", err)
	}
	if fixture.store.Root() != rootBefore || fixture.store.GetAccount(fixture.account).Nonce != 0 {
		t.Fatal("failed guardian fee charge mutated recovery account")
	}
	if got := fixture.store.GetStorage(fixture.account, "recovery:pending_owner"); got != "" {
		t.Fatalf("pending owner after failed fee = %q", got)
	}
}

func TestAccountRecoveryVotesConvergeWithoutMinorityTargetBlocking(t *testing.T) {
	fixture := newRecoveryFixture(t, false)
	store := fixture.store
	store.SetStorage(fixture.account, "recovery:guardians", fixture.guardianA+","+fixture.guardianB)
	store.SetStorage(fixture.account, "recovery:threshold", "2")
	store.SetStorage(fixture.account, "recovery:delay", "3")

	first := recoveryTx(t, fixture.guardianAKey, fixture.account, 0, map[string]string{
		"action":    "approve",
		"new_owner": fixture.newOwner,
	})
	if _, err := fixture.executor.ExecuteWithContext(store, first, core.ExecutionContext{BlockHeight: 10}); err != nil {
		t.Fatal(err)
	}
	duplicate := recoveryTx(t, fixture.guardianAKey, fixture.account, 1, map[string]string{
		"action":    "approve",
		"new_owner": fixture.newOwner,
	})
	requireRecoveryExecutionRevert(t, fixture, duplicate, core.ExecutionContext{BlockHeight: 11})

	otherOwnerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	otherOwner := chaincrypto.AddressFromPrivateKey(otherOwnerKey)
	competing := recoveryTx(t, fixture.guardianBKey, fixture.account, 2, map[string]string{
		"action":    "approve",
		"new_owner": otherOwner,
	})
	if _, err := fixture.executor.ExecuteWithContext(store, competing, core.ExecutionContext{BlockHeight: 12}); err != nil {
		t.Fatal(err)
	}
	if got := store.GetStorage(fixture.account, "recovery:pending_owner"); got != "" {
		t.Fatalf("split vote pending owner = %q", got)
	}
	if got := store.GetStorage(fixture.account, "recovery:votes"); got == "" {
		t.Fatal("split guardian votes were not recorded")
	}
	if got := store.GetStorage(fixture.account, "recovery:execute_after"); got != "" {
		t.Fatalf("split vote execute_after = %q", got)
	}

	thirdOwnerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	thirdOwner := chaincrypto.AddressFromPrivateKey(thirdOwnerKey)
	nonConvergingChange := recoveryTx(t, fixture.guardianAKey, fixture.account, 3, map[string]string{
		"action":    "approve",
		"new_owner": thirdOwner,
	})
	requireRecoveryExecutionRevert(t, fixture, nonConvergingChange, core.ExecutionContext{BlockHeight: 13})

	converge := recoveryTx(t, fixture.guardianAKey, fixture.account, 4, map[string]string{
		"action":    "approve",
		"new_owner": otherOwner,
	})
	converged, err := fixture.executor.ExecuteWithContext(store, converge, core.ExecutionContext{BlockHeight: 13})
	if err != nil {
		t.Fatal(err)
	}
	if converged.Events[0].Attributes["updated"] != "true" || converged.Events[0].Attributes["pending"] != "true" {
		t.Fatalf("converged vote event = %#v", converged.Events)
	}
	if got := store.GetStorage(fixture.account, "recovery:pending_owner"); got != otherOwner {
		t.Fatalf("converged pending owner = %q", got)
	}
	if got := store.GetStorage(fixture.account, "recovery:approvals"); got != fixture.guardianA+","+fixture.guardianB {
		t.Fatalf("converged approvals = %q", got)
	}
	if got := store.GetStorage(fixture.account, "recovery:execute_after"); got != "16" {
		t.Fatalf("converged execute_after = %q", got)
	}

	mismatch := recoveryTx(t, fixture.guardianBKey, fixture.account, 5, map[string]string{
		"action":    "execute",
		"new_owner": fixture.newOwner,
	})
	requireRecoveryExecutionRevert(t, fixture, mismatch, core.ExecutionContext{BlockHeight: 16})
}

func TestAccountRecoveryVotingRoundExpiresAndConfigureClearsActiveState(t *testing.T) {
	fixture := newRecoveryFixture(t, false)
	store := fixture.store
	store.SetStorage(fixture.account, "recovery:guardians", fixture.guardianA+","+fixture.guardianB)
	store.SetStorage(fixture.account, "recovery:threshold", "2")
	store.SetStorage(fixture.account, "recovery:delay", "3")

	first := recoveryTx(t, fixture.guardianAKey, fixture.account, 0, map[string]string{
		"action":    "approve",
		"new_owner": fixture.newOwner,
	})
	if _, err := fixture.executor.ExecuteWithContext(store, first, core.ExecutionContext{BlockHeight: 10}); err != nil {
		t.Fatal(err)
	}
	if got := store.GetStorage(fixture.account, "recovery:expires_at"); got != "266" {
		t.Fatalf("voting round expiry = %q", got)
	}

	otherOwnerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	otherOwner := chaincrypto.AddressFromPrivateKey(otherOwnerKey)
	newRound := recoveryTx(t, fixture.guardianBKey, fixture.account, 1, map[string]string{
		"action":    "approve",
		"new_owner": otherOwner,
	})
	if _, err := fixture.executor.ExecuteWithContext(store, newRound, core.ExecutionContext{BlockHeight: 267}); err != nil {
		t.Fatal(err)
	}
	if got := store.GetStorage(fixture.account, "recovery:votes"); got != fixture.guardianB+"="+otherOwner {
		t.Fatalf("new round votes = %q", got)
	}
	if got := store.GetStorage(fixture.account, "recovery:expires_at"); got != "523" {
		t.Fatalf("new round expiry = %q", got)
	}

	store.SetStorage(fixture.account, "recovery:pending_owner", fixture.newOwner)
	store.SetStorage(fixture.account, "recovery:execute_after", "600")
	store.SetStorage(fixture.account, "recovery:approvals", fixture.guardianA+","+fixture.guardianB)
	reconfigure := recoveryTx(t, fixture.ownerKey, fixture.account, 2, map[string]string{
		"action":    "configure",
		"guardians": fixture.guardianA,
		"threshold": "1",
		"delay":     "5",
	})
	if _, err := fixture.executor.ExecuteWithContext(store, reconfigure, core.ExecutionContext{BlockHeight: 268}); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"recovery:pending_owner", "recovery:execute_after", "recovery:expires_at", "recovery:approvals", "recovery:votes"} {
		if got := store.GetStorage(fixture.account, key); got != "" {
			t.Fatalf("reconfigured active key %q = %q", key, got)
		}
	}
	if got := store.GetStorage(fixture.account, "recovery:delay"); got != "5" {
		t.Fatalf("reconfigured delay = %q", got)
	}
}

func TestAccountRecoveryExecutionWindowBoundaryAndRollover(t *testing.T) {
	t.Run("expiry block is executable", func(t *testing.T) {
		fixture := newRecoveryFixture(t, false)
		fixture.store.SetStorage(fixture.account, "recovery:guardians", fixture.guardianA)
		fixture.store.SetStorage(fixture.account, "recovery:threshold", "1")
		fixture.store.SetStorage(fixture.account, "recovery:delay", "0")
		approve := recoveryTx(t, fixture.guardianAKey, fixture.account, 0, map[string]string{"action": "approve", "new_owner": fixture.newOwner})
		if _, err := fixture.executor.ExecuteWithContext(fixture.store, approve, core.ExecutionContext{BlockHeight: 1}); err != nil {
			t.Fatal(err)
		}
		execute := recoveryTx(t, fixture.guardianAKey, fixture.account, 1, map[string]string{"action": "execute", "new_owner": fixture.newOwner})
		if _, err := fixture.executor.ExecuteWithContext(fixture.store, execute, core.ExecutionContext{BlockHeight: 257}); err != nil {
			t.Fatal(err)
		}
		if got := fixture.store.GetStorage(fixture.account, "owner"); got != fixture.newOwner {
			t.Fatalf("owner at expiry boundary = %q", got)
		}
	})

	t.Run("expired pending starts a new round", func(t *testing.T) {
		fixture := newRecoveryFixture(t, false)
		fixture.store.SetStorage(fixture.account, "recovery:guardians", fixture.guardianA)
		fixture.store.SetStorage(fixture.account, "recovery:threshold", "1")
		fixture.store.SetStorage(fixture.account, "recovery:delay", "0")
		approve := recoveryTx(t, fixture.guardianAKey, fixture.account, 0, map[string]string{"action": "approve", "new_owner": fixture.newOwner})
		if _, err := fixture.executor.ExecuteWithContext(fixture.store, approve, core.ExecutionContext{BlockHeight: 1}); err != nil {
			t.Fatal(err)
		}
		execute := recoveryTx(t, fixture.guardianAKey, fixture.account, 1, map[string]string{"action": "execute", "new_owner": fixture.newOwner})
		requireRecoveryExecutionRevert(t, fixture, execute, core.ExecutionContext{BlockHeight: 258})
		otherOwnerKey, err := chaincrypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		otherOwner := chaincrypto.AddressFromPrivateKey(otherOwnerKey)
		newRound := recoveryTx(t, fixture.guardianAKey, fixture.account, 2, map[string]string{"action": "approve", "new_owner": otherOwner})
		if _, err := fixture.executor.ExecuteWithContext(fixture.store, newRound, core.ExecutionContext{BlockHeight: 258}); err != nil {
			t.Fatal(err)
		}
		if got := fixture.store.GetStorage(fixture.account, "recovery:pending_owner"); got != otherOwner {
			t.Fatalf("rolled over pending owner = %q", got)
		}
		if got := fixture.store.GetStorage(fixture.account, "recovery:execute_after"); got != "258" {
			t.Fatalf("rolled over execute_after = %q", got)
		}
	})
}

func TestAccountRecoveryRejectsInvalidConfigurationAndEnvelope(t *testing.T) {
	validGuardianKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validGuardian := chaincrypto.AddressFromPrivateKey(validGuardianKey)
	tooManyGuardians := make([]string, 17)
	for i := range tooManyGuardians {
		key, err := chaincrypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		tooManyGuardians[i] = chaincrypto.AddressFromPrivateKey(key)
	}

	tests := []struct {
		name      string
		payload   map[string]string
		mutate    func(*types.Transaction, recoveryFixture)
		wantError string
	}{
		{name: "missing guardians", payload: map[string]string{"action": "configure", "threshold": "1", "delay": "0"}, wantError: "recovery guardians are required"},
		{name: "duplicate guardians", payload: map[string]string{"action": "configure", "guardians": validGuardian + "," + validGuardian, "threshold": "1", "delay": "0"}, wantError: "recovery guardians must be unique"},
		{name: "malformed guardian", payload: map[string]string{"action": "configure", "guardians": "0x1234", "threshold": "1", "delay": "0"}, wantError: "recovery guardian must be a 20-byte hex address"},
		{name: "zero guardian", payload: map[string]string{"action": "configure", "guardians": "0x0000000000000000000000000000000000000000", "threshold": "1", "delay": "0"}, wantError: "recovery guardian must not be the zero address"},
		{name: "zero threshold", payload: map[string]string{"action": "configure", "guardians": validGuardian, "threshold": "0", "delay": "0"}, wantError: "recovery threshold must be positive"},
		{name: "high threshold", payload: map[string]string{"action": "configure", "guardians": validGuardian, "threshold": "2", "delay": "0"}, wantError: "recovery threshold exceeds guardian count"},
		{name: "missing delay", payload: map[string]string{"action": "configure", "guardians": validGuardian, "threshold": "1"}, wantError: "recovery delay is required"},
		{name: "invalid delay", payload: map[string]string{"action": "configure", "guardians": validGuardian, "threshold": "1", "delay": "-1"}, wantError: "recovery delay must be an unsigned integer"},
		{name: "too many guardians", payload: map[string]string{"action": "configure", "guardians": strings.Join(tooManyGuardians, ","), "threshold": "1", "delay": "0"}, wantError: "recovery guardian count exceeds 16"},
		{name: "unknown action", payload: map[string]string{"action": "replace"}, wantError: "account recovery action must be configure, approve, execute, cancel, or clear"},
		{name: "unsupported payload", payload: map[string]string{"action": "clear", "new_owner": validGuardian}, wantError: "account recovery clear payload contains unsupported fields"},
		{name: "missing signer", payload: map[string]string{"action": "clear"}, mutate: func(tx *types.Transaction, _ recoveryFixture) { tx.Signer = "" }, wantError: "account recovery requires signer"},
		{name: "multisig envelope", payload: map[string]string{"action": "clear"}, mutate: func(tx *types.Transaction, fixture recoveryFixture) {
			tx.Authorizations = []types.Authorization{{Signer: fixture.owner, Signature: tx.Signature}}
		}, wantError: "account recovery does not support multisig authorizations"},
		{name: "ethereum envelope", payload: map[string]string{"action": "clear"}, mutate: func(tx *types.Transaction, _ recoveryFixture) { tx.SignatureKind = types.SignatureKindEthereumType2 }, wantError: "account recovery does not support ethereum signatures"},
		{name: "paymaster envelope", payload: map[string]string{"action": "clear"}, mutate: func(tx *types.Transaction, fixture recoveryFixture) { tx.Paymaster = fixture.guardianA }, wantError: "account recovery does not support paymasters"},
		{name: "orphan paymaster signature", payload: map[string]string{"action": "clear"}, mutate: func(tx *types.Transaction, _ recoveryFixture) { tx.PaymasterSignature = "0x1234" }, wantError: "account recovery does not support paymasters"},
		{name: "to field", payload: map[string]string{"action": "clear"}, mutate: func(tx *types.Transaction, fixture recoveryFixture) { tx.To = fixture.receiver }, wantError: "account recovery does not support to, value, or batch fields"},
		{name: "value field", payload: map[string]string{"action": "clear"}, mutate: func(tx *types.Transaction, _ recoveryFixture) { tx.Value = 1 }, wantError: "account recovery does not support to, value, or batch fields"},
		{name: "batch field", payload: map[string]string{"action": "clear"}, mutate: func(tx *types.Transaction, fixture recoveryFixture) {
			tx.Batch = []types.BatchOperation{{Type: types.TxTransfer, To: fixture.receiver, Value: 1}}
		}, wantError: "account recovery does not support to, value, or batch fields"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryFixture(t, false)
			tx := recoveryTx(t, fixture.ownerKey, fixture.account, 0, test.payload)
			if test.mutate != nil {
				test.mutate(&tx, fixture)
				tx = resignTx(t, fixture.ownerKey, tx)
			}
			rootBefore := fixture.store.Root()
			_, err := fixture.executor.ExecuteWithContext(fixture.store, tx, core.ExecutionContext{BlockHeight: 1})
			if err == nil || err.Error() != test.wantError {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
			if fixture.store.Root() != rootBefore || fixture.store.GetAccount(fixture.account).Nonce != 0 {
				t.Fatal("invalid recovery transaction mutated state")
			}
		})
	}
}

func TestAccountRecoveryAcceptsMaximumGuardianSet(t *testing.T) {
	fixture := newRecoveryFixture(t, false)
	guardians := make([]string, 16)
	for i := range guardians {
		key, err := chaincrypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		guardians[i] = chaincrypto.AddressFromPrivateKey(key)
	}
	tx := recoveryTx(t, fixture.ownerKey, fixture.account, 0, map[string]string{
		"action":    "configure",
		"guardians": strings.Join(guardians, ","),
		"threshold": "16",
		"delay":     "0",
	})
	if _, err := fixture.executor.Execute(fixture.store, tx); err != nil {
		t.Fatal(err)
	}
	if got := fixture.store.GetStorage(fixture.account, "recovery:guardians"); got != strings.Join(guardians, ",") {
		t.Fatalf("maximum guardian set = %q", got)
	}
}

func TestAccountRecoveryRejectsInvalidAuthorityTargetAndNonceOverflow(t *testing.T) {
	fixture := newRecoveryFixture(t, false)
	store := fixture.store
	store.SetStorage(fixture.account, "recovery:guardians", fixture.guardianA)
	store.SetStorage(fixture.account, "recovery:threshold", "1")
	store.SetStorage(fixture.account, "recovery:delay", "0")

	authorizationRoot := store.Root()
	badSignature := recoveryTx(t, fixture.ownerKey, fixture.account, 0, map[string]string{"action": "clear"})
	badSignature.Signature = "0x1234"
	if _, err := fixture.executor.Execute(store, badSignature); err == nil || err.Error() != "invalid transaction signature" {
		t.Fatalf("bad signature error = %v", err)
	}

	signerMismatch := recoveryTx(t, fixture.ownerKey, fixture.account, 0, map[string]string{"action": "clear"})
	signerMismatch.Signer = fixture.guardianA
	signerMismatch = resignTx(t, fixture.ownerKey, signerMismatch)
	if _, err := fixture.executor.Execute(store, signerMismatch); err == nil || err.Error() != "invalid transaction signature" {
		t.Fatalf("signer mismatch error = %v", err)
	}
	if store.Root() != authorizationRoot || store.GetAccount(fixture.account).Nonce != 0 {
		t.Fatal("invalid recovery authorization mutated state")
	}

	currentOwner := recoveryTx(t, fixture.guardianAKey, fixture.account, 0, map[string]string{
		"action":    "approve",
		"new_owner": fixture.owner,
	})
	requireRecoveryExecutionRevert(t, fixture, currentOwner, core.ExecutionContext{})
	accountTarget := recoveryTx(t, fixture.guardianAKey, fixture.account, 1, map[string]string{
		"action":    "approve",
		"new_owner": fixture.account,
	})
	requireRecoveryExecutionRevert(t, fixture, accountTarget, core.ExecutionContext{})
	noPending := recoveryTx(t, fixture.guardianAKey, fixture.account, 2, map[string]string{
		"action":    "execute",
		"new_owner": fixture.newOwner,
	})
	requireRecoveryExecutionRevert(t, fixture, noPending, core.ExecutionContext{})

	ownerGuardian := recoveryTx(t, fixture.ownerKey, fixture.account, 3, map[string]string{
		"action":    "configure",
		"guardians": fixture.owner,
		"threshold": "1",
		"delay":     "0",
	})
	requireRecoveryExecutionRevert(t, fixture, ownerGuardian, core.ExecutionContext{})
	accountGuardian := recoveryTx(t, fixture.ownerKey, fixture.account, 4, map[string]string{
		"action":    "configure",
		"guardians": fixture.account,
		"threshold": "1",
		"delay":     "0",
	})
	requireRecoveryExecutionRevert(t, fixture, accountGuardian, core.ExecutionContext{})
	contractGuardianFixture := newRecoveryFixture(t, false)
	contractGuardianFixture.store.SetCodeID(contractGuardianFixture.guardianA, contracts.AccountCodeID)
	contractGuardian := recoveryTx(t, contractGuardianFixture.ownerKey, contractGuardianFixture.account, 0, map[string]string{
		"action":    "configure",
		"guardians": contractGuardianFixture.guardianA,
		"threshold": "1",
		"delay":     "0",
	})
	requireRecoveryExecutionRevert(t, contractGuardianFixture, contractGuardian, core.ExecutionContext{})
	contractOwnerFixture := newRecoveryFixture(t, false)
	contractOwnerFixture.store.SetStorage(contractOwnerFixture.account, "recovery:guardians", contractOwnerFixture.guardianA)
	contractOwnerFixture.store.SetStorage(contractOwnerFixture.account, "recovery:threshold", "1")
	contractOwnerFixture.store.SetStorage(contractOwnerFixture.account, "recovery:delay", "0")
	contractOwnerFixture.store.SetCodeID(contractOwnerFixture.newOwner, contracts.AccountCodeID)
	contractOwner := recoveryTx(t, contractOwnerFixture.guardianAKey, contractOwnerFixture.account, 0, map[string]string{
		"action":    "approve",
		"new_owner": contractOwnerFixture.newOwner,
	})
	requireRecoveryExecutionRevert(t, contractOwnerFixture, contractOwner, core.ExecutionContext{})

	plainStore := state.NewStore()
	plainStore.SetStorage(fixture.account, "owner", fixture.owner)
	plainStore.SetBalance(fixture.account, 100_000)
	plainTx := recoveryTx(t, fixture.ownerKey, fixture.account, 0, map[string]string{"action": "clear"})
	plainRoot := plainStore.Root()
	if _, err := fixture.executor.Execute(plainStore, plainTx); err == nil || err.Error() != "account recovery requires account.v1 from account" {
		t.Fatalf("plain account error = %v", err)
	}
	if plainStore.Root() != plainRoot {
		t.Fatal("plain-account recovery rejection mutated state")
	}
	multisigStore := state.NewStore()
	multisigStore.SetCodeID(fixture.account, contracts.MultisigCodeID)
	multisigStore.SetStorage(fixture.account, "owner", fixture.owner)
	multisigStore.SetBalance(fixture.account, 100_000)
	multisigRoot := multisigStore.Root()
	if _, err := fixture.executor.Execute(multisigStore, plainTx); err == nil || err.Error() != "account recovery requires account.v1 from account" {
		t.Fatalf("multisig account error = %v", err)
	}
	if multisigStore.Root() != multisigRoot {
		t.Fatal("multisig recovery rejection mutated state")
	}

	store.SetNonce(fixture.account, ^uint64(0))
	overflow := recoveryTx(t, fixture.ownerKey, fixture.account, ^uint64(0), map[string]string{"action": "clear"})
	rootBefore := store.Root()
	if _, err := fixture.executor.Execute(store, overflow); err == nil || err.Error() != "nonce overflow" {
		t.Fatalf("nonce overflow error = %v", err)
	}
	if store.Root() != rootBefore || store.GetAccount(fixture.account).Nonce != ^uint64(0) {
		t.Fatal("nonce overflow mutated account state")
	}
}

func TestAccountRecoveryRejectsInvalidTargetAndExecuteHeightOverflow(t *testing.T) {
	fixture := newRecoveryFixture(t, false)
	store := fixture.store
	store.SetStorage(fixture.account, "recovery:guardians", fixture.guardianA)
	store.SetStorage(fixture.account, "recovery:threshold", "1")
	store.SetStorage(fixture.account, "recovery:delay", "1")

	invalidTargetRoot := store.Root()
	invalidTarget := recoveryTx(t, fixture.guardianAKey, fixture.account, 0, map[string]string{
		"action":    "approve",
		"new_owner": "0x1234",
	})
	if _, err := fixture.executor.ExecuteWithContext(store, invalidTarget, core.ExecutionContext{BlockHeight: 1}); err == nil || err.Error() != "recovery new owner must be a 20-byte hex address" {
		t.Fatalf("invalid target error = %v", err)
	}
	zeroTarget := recoveryTx(t, fixture.guardianAKey, fixture.account, 0, map[string]string{
		"action":    "approve",
		"new_owner": "0x0000000000000000000000000000000000000000",
	})
	if _, err := fixture.executor.ExecuteWithContext(store, zeroTarget, core.ExecutionContext{BlockHeight: 1}); err == nil || err.Error() != "recovery new owner must not be the zero address" {
		t.Fatalf("zero target error = %v", err)
	}
	missingExecuteTarget := recoveryTx(t, fixture.guardianAKey, fixture.account, 0, map[string]string{"action": "execute"})
	if _, err := fixture.executor.ExecuteWithContext(store, missingExecuteTarget, core.ExecutionContext{BlockHeight: 1}); err == nil || err.Error() != "recovery new owner is required" {
		t.Fatalf("missing execute target error = %v", err)
	}
	if store.Root() != invalidTargetRoot || store.GetAccount(fixture.account).Nonce != 0 {
		t.Fatal("invalid recovery target mutated state")
	}
	overflow := recoveryTx(t, fixture.guardianAKey, fixture.account, 0, map[string]string{
		"action":    "approve",
		"new_owner": fixture.newOwner,
	})
	requireRecoveryExecutionRevert(t, fixture, overflow, core.ExecutionContext{BlockHeight: ^uint64(0)})
}

func TestAccountRecoveryRejectsCorruptedStoredState(t *testing.T) {
	tests := []struct {
		name             string
		setup            func(recoveryFixture)
		action           string
		wantError        string
		executionFailure bool
	}{
		{name: "zero threshold", setup: func(f recoveryFixture) { f.store.SetStorage(f.account, "recovery:threshold", "0") }, action: "approve", wantError: "recovery threshold must be positive"},
		{name: "malformed guardian set", setup: func(f recoveryFixture) { f.store.SetStorage(f.account, "recovery:guardians", "0x1234") }, action: "approve", wantError: "recovery guardian must be a 20-byte hex address"},
		{name: "invalid delay", setup: func(f recoveryFixture) { f.store.SetStorage(f.account, "recovery:delay", "invalid") }, action: "approve", wantError: "recovery delay must be an unsigned integer"},
		{name: "malformed votes", setup: func(f recoveryFixture) { f.store.SetStorage(f.account, "recovery:votes", "invalid") }, action: "approve", wantError: "recovery vote is invalid"},
		{name: "duplicate vote guardians", setup: func(f recoveryFixture) {
			vote := f.guardianA + "=" + f.newOwner
			f.store.SetStorage(f.account, "recovery:votes", vote+","+vote)
		}, action: "approve", wantError: "recovery guardian votes must be unique"},
		{name: "unconfigured vote guardian", setup: func(f recoveryFixture) {
			f.store.SetStorage(f.account, "recovery:votes", f.intruder+"="+f.newOwner)
		}, action: "approve", wantError: "recovery vote is not from a configured guardian"},
		{name: "premature execute height", setup: func(f recoveryFixture) {
			f.store.SetStorage(f.account, "recovery:execute_after", "0")
		}, action: "approve", wantError: "recovery execute height is set before approval threshold"},
		{name: "pending without expiry", setup: func(f recoveryFixture) {
			f.store.SetStorage(f.account, "recovery:pending_owner", f.newOwner)
		}, action: "approve", wantError: "recovery proposal expiry is not set"},
		{name: "duplicate approvals", setup: func(f recoveryFixture) {
			seedPendingRecovery(f, "1", f.guardianA+","+f.guardianA, "5", "261")
		}, action: "execute", wantError: "recovery approvals must be unique"},
		{name: "unconfigured approval", setup: func(f recoveryFixture) {
			seedPendingRecovery(f, "1", f.intruder, "5", "261")
		}, action: "execute", wantError: "recovery approval is not from a configured guardian"},
		{name: "threshold not met despite execute height", setup: func(f recoveryFixture) {
			seedPendingRecovery(f, "2", f.guardianA, "0", "256")
		}, action: "execute", wantError: "recovery approval threshold not met"},
		{name: "invalid execute height", setup: func(f recoveryFixture) {
			seedPendingRecovery(f, "1", f.guardianA, "invalid", "256")
		}, action: "execute", wantError: "recovery execute height is invalid"},
		{name: "invalid expiry", setup: func(f recoveryFixture) {
			seedPendingRecovery(f, "1", f.guardianA, "0", "invalid")
		}, action: "execute", wantError: "recovery proposal expiry is invalid"},
		{name: "session epoch overflow", setup: func(f recoveryFixture) {
			seedPendingRecovery(f, "1", f.guardianA, "0", "256")
			f.store.SetStorage(f.account, "session:epoch", "18446744073709551615")
		}, action: "execute", wantError: "session epoch overflow", executionFailure: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newRecoveryFixture(t, false)
			fixture.store.SetStorage(fixture.account, "recovery:guardians", fixture.guardianA+","+fixture.guardianB)
			fixture.store.SetStorage(fixture.account, "recovery:threshold", "2")
			fixture.store.SetStorage(fixture.account, "recovery:delay", "3")
			test.setup(fixture)
			payload := map[string]string{"action": test.action, "new_owner": fixture.newOwner}
			tx := recoveryTx(t, fixture.guardianBKey, fixture.account, 0, payload)
			if test.executionFailure {
				requireRecoveryExecutionRevert(t, fixture, tx, core.ExecutionContext{BlockHeight: 5})
				return
			}
			rootBefore := fixture.store.Root()
			_, err := fixture.executor.ExecuteWithContext(fixture.store, tx, core.ExecutionContext{BlockHeight: 5})
			if !errors.Is(err, core.ErrConsensusExecutionFault) || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want consensus fault containing %q", err, test.wantError)
			}
			if fixture.store.Root() != rootBefore || fixture.store.GetAccount(fixture.account).Nonce != 0 {
				t.Fatal("corrupted recovery state attempt mutated state")
			}
		})
	}
}

func seedPendingRecovery(fixture recoveryFixture, threshold string, approvals string, executeAfter string, expiresAt string) {
	fixture.store.SetStorage(fixture.account, "recovery:pending_owner", fixture.newOwner)
	fixture.store.SetStorage(fixture.account, "recovery:threshold", threshold)
	fixture.store.SetStorage(fixture.account, "recovery:approvals", approvals)
	fixture.store.SetStorage(fixture.account, "recovery:execute_after", executeAfter)
	fixture.store.SetStorage(fixture.account, "recovery:expires_at", expiresAt)
}

type recoveryFixture struct {
	store        *state.Store
	executor     *core.Executor
	account      string
	ownerKey     chaincrypto.PrivateKey
	owner        string
	guardianAKey chaincrypto.PrivateKey
	guardianA    string
	guardianBKey chaincrypto.PrivateKey
	guardianB    string
	newOwnerKey  chaincrypto.PrivateKey
	newOwner     string
	intruderKey  chaincrypto.PrivateKey
	intruder     string
	receiver     string
}

func newRecoveryFixture(t *testing.T, delegated bool) recoveryFixture {
	t.Helper()
	keys := make([]chaincrypto.PrivateKey, 5)
	addresses := make([]string, len(keys))
	for i := range keys {
		key, err := chaincrypto.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		keys[i] = key
		addresses[i] = chaincrypto.AddressFromPrivateKey(key)
	}
	account := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	if delegated {
		account = addresses[4]
		store.SetDelegatedCodeID(account, contracts.AccountCodeID)
	} else {
		store.SetCodeID(account, contracts.AccountCodeID)
	}
	store.SetStorage(account, "owner", addresses[0])
	store.SetBalance(account, 2_000_000)
	store.SetBalance(addresses[1], 500_000)
	store.SetBalance(addresses[2], 500_000)
	return recoveryFixture{
		store:        store,
		executor:     core.NewExecutor("chainlab-local", recoveryFeeCollector, contracts.NewRuntimeWithDefaults()),
		account:      account,
		ownerKey:     keys[0],
		owner:        addresses[0],
		guardianAKey: keys[1],
		guardianA:    addresses[1],
		guardianBKey: keys[2],
		guardianB:    addresses[2],
		newOwnerKey:  keys[3],
		newOwner:     addresses[3],
		intruderKey:  keys[4],
		intruder:     addresses[4],
		receiver:     "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
}

func recoveryTx(t *testing.T, key chaincrypto.PrivateKey, account string, nonce uint64, payload map[string]string) types.Transaction {
	t.Helper()
	return signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxAccountRecovery,
		From:     account,
		Signer:   chaincrypto.AddressFromPrivateKey(key),
		Nonce:    nonce,
		GasLimit: 55_000,
		GasPrice: 1,
		Payload:  payload,
	})
}

func resignTx(t *testing.T, key chaincrypto.PrivateKey, tx types.Transaction) types.Transaction {
	t.Helper()
	tx.Signature = ""
	return signedTx(t, key, tx)
}

func TestSmartAccountRejectsUnauthorizedSigner(t *testing.T) {
	ownerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	intruderKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	owner := chaincrypto.AddressFromPrivateKey(ownerKey)
	intruder := chaincrypto.AddressFromPrivateKey(intruderKey)
	smartAccount := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store := state.NewStore()
	store.SetCodeID(smartAccount, contracts.AccountCodeID)
	store.SetStorage(smartAccount, "owner", owner)
	store.SetBalance(smartAccount, 50_000)
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", contracts.NewRuntimeWithDefaults())

	tx := signedTx(t, intruderKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     smartAccount,
		Signer:   intruder,
		To:       receiver,
		Nonce:    0,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	})

	if _, err := executor.Execute(store, tx); err == nil {
		t.Fatal("unauthorized smart account signer should fail")
	}
	if got := store.GetAccount(smartAccount).Nonce; got != 0 {
		t.Fatalf("smart account nonce = %d", got)
	}
	if got := store.GetAccount(receiver).Balance; got != 0 {
		t.Fatalf("receiver balance = %d", got)
	}
}

func TestSmartAccountBatchCanUsePaymaster(t *testing.T) {
	ownerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	paymasterKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	owner := chaincrypto.AddressFromPrivateKey(ownerKey)
	paymaster := chaincrypto.AddressFromPrivateKey(paymasterKey)
	smartAccount := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	carol := "0xcccccccccccccccccccccccccccccccccccccccc"
	feeCollector := "0xfee0000000000000000000000000000000000000"
	store := state.NewStore()
	store.SetCodeID(smartAccount, contracts.AccountCodeID)
	store.SetStorage(smartAccount, "owner", owner)
	store.SetBalance(smartAccount, 30)
	store.SetBalance(paymaster, 500_000)
	executor := core.NewExecutor("chainlab-local", feeCollector, contracts.NewRuntimeWithDefaults())

	tx := sponsoredTx(t, ownerKey, paymasterKey, types.Transaction{
		ChainID:   "chainlab-local",
		Type:      types.TxBatch,
		From:      smartAccount,
		Signer:    owner,
		Nonce:     0,
		GasLimit:  84_000,
		GasPrice:  1,
		Paymaster: paymaster,
		Batch: []types.BatchOperation{
			{Type: types.TxTransfer, To: bob, Value: 10},
			{Type: types.TxTransfer, To: carol, Value: 20},
		},
	})

	receipt, err := executor.Execute(store, tx)
	if err != nil {
		t.Fatal(err)
	}

	if receipt.FeePayer != paymaster {
		t.Fatalf("fee payer = %q", receipt.FeePayer)
	}
	if got := store.GetAccount(smartAccount).Nonce; got != 1 {
		t.Fatalf("smart account nonce = %d", got)
	}
	if got := store.GetAccount(smartAccount).Balance; got != 0 {
		t.Fatalf("smart account balance = %d", got)
	}
	if got := store.GetAccount(bob).Balance; got != 10 {
		t.Fatalf("bob balance = %d", got)
	}
	if got := store.GetAccount(carol).Balance; got != 20 {
		t.Fatalf("carol balance = %d", got)
	}
	if got := store.GetAccount(paymaster).Balance; got != 416_000 {
		t.Fatalf("paymaster balance = %d", got)
	}
	if got := store.GetAccount(feeCollector).Balance; got != 84_000 {
		t.Fatalf("fee collector balance = %d", got)
	}
}

func TestMultisigAccountRequiresThresholdAuthorizationsAndContractNonce(t *testing.T) {
	ownerAKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerBKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerA := chaincrypto.AddressFromPrivateKey(ownerAKey)
	ownerB := chaincrypto.AddressFromPrivateKey(ownerBKey)
	multisig := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store := state.NewStore()
	store.SetCodeID(multisig, contracts.MultisigCodeID)
	store.SetStorage(multisig, "owners", ownerA+","+ownerB)
	store.SetStorage(multisig, "threshold", "2")
	store.SetBalance(multisig, 50_000)
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", contracts.NewRuntimeWithDefaults())

	oneAuth := authorizedTx(t, []chaincrypto.PrivateKey{ownerAKey}, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     multisig,
		To:       receiver,
		Nonce:    0,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if _, err := executor.Execute(store, oneAuth); err == nil {
		t.Fatal("multisig transaction below threshold should fail")
	}
	if got := store.GetAccount(multisig).Nonce; got != 0 {
		t.Fatalf("multisig nonce after failed tx = %d", got)
	}

	tx := authorizedTx(t, []chaincrypto.PrivateKey{ownerAKey, ownerBKey}, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     multisig,
		To:       receiver,
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
	if got := store.GetAccount(multisig).Nonce; got != 1 {
		t.Fatalf("multisig nonce = %d", got)
	}
	if got := store.GetAccount(multisig).Balance; got != 28_900 {
		t.Fatalf("multisig balance = %d", got)
	}
	if got := store.GetAccount(receiver).Balance; got != 100 {
		t.Fatalf("receiver balance = %d", got)
	}
}

func TestMultisigAccountRejectsDuplicateAuthorizations(t *testing.T) {
	ownerAKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerBKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerA := chaincrypto.AddressFromPrivateKey(ownerAKey)
	ownerB := chaincrypto.AddressFromPrivateKey(ownerBKey)
	multisig := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := state.NewStore()
	store.SetCodeID(multisig, contracts.MultisigCodeID)
	store.SetStorage(multisig, "owners", ownerA+","+ownerB)
	store.SetStorage(multisig, "threshold", "2")
	store.SetBalance(multisig, 50_000)
	executor := core.NewExecutor("chainlab-local", "0xfee0000000000000000000000000000000000000", contracts.NewRuntimeWithDefaults())

	tx := authorizedTx(t, []chaincrypto.PrivateKey{ownerAKey, ownerAKey}, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     multisig,
		To:       "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		Nonce:    0,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	})

	if _, err := executor.Execute(store, tx); err == nil {
		t.Fatal("duplicate multisig authorizations should fail")
	}
	if got := store.GetAccount(multisig).Nonce; got != 0 {
		t.Fatalf("multisig nonce = %d", got)
	}
}

func TestMultisigAccountBatchCanUsePaymaster(t *testing.T) {
	ownerAKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerBKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	paymasterKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ownerA := chaincrypto.AddressFromPrivateKey(ownerAKey)
	ownerB := chaincrypto.AddressFromPrivateKey(ownerBKey)
	paymaster := chaincrypto.AddressFromPrivateKey(paymasterKey)
	multisig := "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	feeCollector := "0xfee0000000000000000000000000000000000000"
	store := state.NewStore()
	store.SetCodeID(multisig, contracts.MultisigCodeID)
	store.SetStorage(multisig, "owners", ownerA+","+ownerB)
	store.SetStorage(multisig, "threshold", "2")
	store.SetBalance(multisig, 30)
	store.SetBalance(paymaster, 500_000)
	executor := core.NewExecutor("chainlab-local", feeCollector, contracts.NewRuntimeWithDefaults())

	tx := authorizedTx(t, []chaincrypto.PrivateKey{ownerAKey, ownerBKey}, types.Transaction{
		ChainID:   "chainlab-local",
		Type:      types.TxBatch,
		From:      multisig,
		Nonce:     0,
		GasLimit:  84_000,
		GasPrice:  1,
		Paymaster: paymaster,
		Batch: []types.BatchOperation{
			{Type: types.TxTransfer, To: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Value: 10},
			{Type: types.TxTransfer, To: "0xcccccccccccccccccccccccccccccccccccccccc", Value: 20},
		},
	})
	paymasterSignature, err := chaincrypto.Sign(paymasterKey, tx.PaymasterSigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.PaymasterSignature = paymasterSignature

	receipt, err := executor.Execute(store, tx)
	if err != nil {
		t.Fatal(err)
	}

	if receipt.FeePayer != paymaster {
		t.Fatalf("fee payer = %q", receipt.FeePayer)
	}
	if got := store.GetAccount(multisig).Nonce; got != 1 {
		t.Fatalf("multisig nonce = %d", got)
	}
	if got := store.GetAccount(multisig).Balance; got != 0 {
		t.Fatalf("multisig balance = %d", got)
	}
	if got := store.GetAccount(paymaster).Balance; got != 416_000 {
		t.Fatalf("paymaster balance = %d", got)
	}
	if got := store.GetAccount(feeCollector).Balance; got != 84_000 {
		t.Fatalf("fee collector balance = %d", got)
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

func sponsoredTx(t *testing.T, userKey chaincrypto.PrivateKey, paymasterKey chaincrypto.PrivateKey, tx types.Transaction) types.Transaction {
	t.Helper()
	signature, err := chaincrypto.Sign(userKey, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = signature
	paymasterSignature, err := chaincrypto.Sign(paymasterKey, tx.PaymasterSigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.PaymasterSignature = paymasterSignature
	return tx
}

func authorizedTx(t *testing.T, keys []chaincrypto.PrivateKey, tx types.Transaction) types.Transaction {
	t.Helper()
	tx.Authorizations = make([]types.Authorization, len(keys))
	for i, key := range keys {
		tx.Authorizations[i].Signer = chaincrypto.AddressFromPrivateKey(key)
	}
	for i, key := range keys {
		signature, err := chaincrypto.Sign(key, tx.SigningBytes())
		if err != nil {
			t.Fatal(err)
		}
		tx.Authorizations[i].Signature = signature
	}
	return tx
}

func TestStakeAndUnstake(t *testing.T) {
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

	unstake := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxUnstake,
		From:     alice,
		Nonce:    1,
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

func TestGovernanceTransactionsAreRejectedAtAdmission(t *testing.T) {
	store, executor, key, alice, _ := newExecutorFixture(t)
	if err := store.AddStake(alice, 500); err != nil {
		t.Fatal(err)
	}

	for _, txType := range []types.TxType{types.TxProposalSubmit, types.TxVote, types.TxProposalExecute} {
		t.Run(string(txType), func(t *testing.T) {
			tx := signedTx(t, key, types.Transaction{
				ChainID:  "chainlab-local",
				Type:     txType,
				From:     alice,
				Nonce:    0,
				GasLimit: 35_000,
				GasPrice: 1,
			})
			rootBefore := store.Root()
			accountBefore := store.GetAccount(alice)
			if _, err := executor.ExecuteAtHeight(store, tx, 1); !errors.Is(err, core.ErrGovernanceDisabled) {
				t.Fatalf("governance admission error = %v", err)
			}
			accountAfter := store.GetAccount(alice)
			if store.Root() != rootBefore || accountAfter.Nonce != accountBefore.Nonce || accountAfter.Balance != accountBefore.Balance {
				t.Fatal("disabled governance transaction changed state")
			}
		})
	}
}

func TestValidatorJoinAndLeaveAreRejectedAtAdmission(t *testing.T) {
	store, executor, key, validator, _ := newExecutorFixture(t)
	store.SetValidators([]string{validator})
	if err := store.AddStake(validator, 500); err != nil {
		t.Fatal(err)
	}

	for _, txType := range []types.TxType{types.TxValidatorJoin, types.TxValidatorLeave} {
		t.Run(string(txType), func(t *testing.T) {
			tx := signedTx(t, key, types.Transaction{
				ChainID:  "chainlab-local",
				Type:     txType,
				From:     validator,
				Nonce:    0,
				GasLimit: 40_000,
				GasPrice: 1,
			})
			rootBefore := store.Root()
			accountBefore := store.GetAccount(validator)
			feeCollectorBefore := store.GetAccount("0xfee0000000000000000000000000000000000000")

			if _, err := executor.Execute(store, tx); err == nil || !strings.Contains(err.Error(), "certified epoch transition") {
				t.Fatalf("%s admission error = %v", txType, err)
			}
			if store.Root() != rootBefore {
				t.Fatalf("rejected %s changed state", txType)
			}
			accountAfter := store.GetAccount(validator)
			if accountAfter.Nonce != accountBefore.Nonce || accountAfter.Balance != accountBefore.Balance {
				t.Fatalf("rejected %s changed sender nonce/balance: before=%+v after=%+v", txType, accountBefore, accountAfter)
			}
			if feeCollectorAfter := store.GetAccount("0xfee0000000000000000000000000000000000000"); feeCollectorAfter.Balance != feeCollectorBefore.Balance {
				t.Fatalf("rejected %s charged a fee", txType)
			}
		})
	}
}

func signFinalityVote(t *testing.T, key chaincrypto.PrivateKey, chainID string, height uint64, blockHash string) string {
	t.Helper()
	signature, err := chaincrypto.Sign(key, types.FinalityVoteSigningBytes(chainID, height, blockHash))
	if err != nil {
		t.Fatal(err)
	}
	return signature
}

func slashEvidencePayload(
	t *testing.T,
	targetKey chaincrypto.PrivateKey,
	target string,
	chainID string,
	height uint64,
	firstBlockHash string,
	secondBlockHash string,
) map[string]string {
	t.Helper()
	return map[string]string{
		"target":            target,
		"height":            strconv.FormatUint(height, 10),
		"first_block_hash":  firstBlockHash,
		"first_signature":   signFinalityVote(t, targetKey, chainID, height, firstBlockHash),
		"second_block_hash": secondBlockHash,
		"second_signature":  signFinalityVote(t, targetKey, chainID, height, secondBlockHash),
	}
}

func validatorSlashTx(t *testing.T, reporterKey chaincrypto.PrivateKey, reporter string, nonce uint64, payload map[string]string) types.Transaction {
	t.Helper()
	return signedTx(t, reporterKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxValidatorSlash,
		From:     reporter,
		Nonce:    nonce,
		GasLimit: 45_000,
		Payload:  payload,
	})
}

func TestValidatorSlashConsumesCanonicalEquivocationEvidenceOnce(t *testing.T) {
	store, executor, reporterKey, reporter, _ := newExecutorFixture(t)
	targetKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	target := chaincrypto.AddressFromPrivateKey(targetKey)
	validators := []string{reporter, target}
	store.SetValidators(validators)
	if err := store.AddStake(target, 600); err != nil {
		t.Fatal(err)
	}

	const height = uint64(7)
	firstBlockHash := "0x" + strings.Repeat("11", 32)
	secondBlockHash := "0x" + strings.Repeat("22", 32)
	payload := slashEvidencePayload(t, targetKey, target, "chainlab-local", height, firstBlockHash, secondBlockHash)
	receipt, err := executor.Execute(store, validatorSlashTx(t, reporterKey, reporter, 0, payload))
	if err != nil {
		t.Fatal(err)
	}
	if !receipt.Success || len(receipt.Events) != 1 || receipt.Events[0].Type != "validator.slashed" {
		t.Fatalf("slash receipt = %+v", receipt)
	}
	event := receipt.Events[0]
	if event.Attributes["reporter"] != reporter || event.Attributes["target"] != target ||
		event.Attributes["amount"] != "600" || event.Attributes["removed"] != "false" {
		t.Fatalf("slash event = %+v", event)
	}
	if got := store.StakeOf(target); got != 0 {
		t.Fatalf("stake after slash = %d", got)
	}
	if got := strings.Join(store.Validators(), ","); got != strings.Join(validators, ",") {
		t.Fatalf("slash changed fixed validator set: %q", got)
	}
	evidenceID := event.Attributes["evidence"]
	offenceID := event.Attributes["offence"]
	if evidenceID == "" || offenceID == "" || store.Param("slash:offence:"+offenceID) != "consumed" {
		t.Fatalf("slash offence was not marked consumed: evidence=%q offence=%q", evidenceID, offenceID)
	}

	if err := store.AddStake(target, 100); err != nil {
		t.Fatal(err)
	}
	replay, err := executor.Execute(store, validatorSlashTx(t, reporterKey, reporter, 1, payload))
	if err != nil {
		t.Fatal(err)
	}
	if replay.Success || replay.FailureCode != types.ReceiptFailureExecutionReverted {
		t.Fatalf("replayed slash receipt = %+v", replay)
	}
	if got := store.StakeOf(target); got != 100 {
		t.Fatalf("replayed evidence slashed new stake: %d", got)
	}
	if got := strings.Join(store.Validators(), ","); got != strings.Join(validators, ",") {
		t.Fatalf("replayed slash changed fixed validator set: %q", got)
	}

	swapped := maps.Clone(payload)
	swapped["first_block_hash"], swapped["second_block_hash"] = swapped["second_block_hash"], swapped["first_block_hash"]
	swapped["first_signature"], swapped["second_signature"] = swapped["second_signature"], swapped["first_signature"]
	reordered, err := executor.Execute(store, validatorSlashTx(t, reporterKey, reporter, 2, swapped))
	if err != nil {
		t.Fatal(err)
	}
	if reordered.Success || reordered.FailureCode != types.ReceiptFailureExecutionReverted {
		t.Fatalf("reordered replay receipt = %+v", reordered)
	}
	if got := store.StakeOf(target); got != 100 {
		t.Fatalf("reordered evidence slashed new stake: %d", got)
	}

	thirdBlockHash := "0x" + strings.Repeat("33", 32)
	alternatePair := slashEvidencePayload(t, targetKey, target, "chainlab-local", height, firstBlockHash, thirdBlockHash)
	alternate, err := executor.Execute(store, validatorSlashTx(t, reporterKey, reporter, 3, alternatePair))
	if err != nil {
		t.Fatal(err)
	}
	if alternate.Success || alternate.FailureCode != types.ReceiptFailureExecutionReverted {
		t.Fatalf("alternate same-height evidence receipt = %+v", alternate)
	}
	if got := store.StakeOf(target); got != 100 {
		t.Fatalf("alternate same-height evidence slashed new stake: %d", got)
	}
}

func TestValidatorSlashRejectsInvalidEvidenceAndMembership(t *testing.T) {
	tests := []struct {
		name           string
		activeReporter bool
		activeTarget   bool
		included       bool
		wantError      string
		mutate         func(*testing.T, chaincrypto.PrivateKey, chaincrypto.PrivateKey, uint64, map[string]string)
	}{
		{
			name:           "forged signature",
			activeReporter: true,
			activeTarget:   true,
			wantError:      "slash evidence signatures are invalid",
			mutate: func(t *testing.T, _ chaincrypto.PrivateKey, forgedKey chaincrypto.PrivateKey, height uint64, payload map[string]string) {
				payload["first_signature"] = signFinalityVote(t, forgedKey, "chainlab-local", height, payload["first_block_hash"])
			},
		},
		{
			name:           "wrong chain signatures",
			activeReporter: true,
			activeTarget:   true,
			wantError:      "slash evidence signatures are invalid",
			mutate: func(t *testing.T, targetKey, _ chaincrypto.PrivateKey, height uint64, payload map[string]string) {
				payload["first_signature"] = signFinalityVote(t, targetKey, "chainlab-other", height, payload["first_block_hash"])
				payload["second_signature"] = signFinalityVote(t, targetKey, "chainlab-other", height, payload["second_block_hash"])
			},
		},
		{
			name:           "same block hash",
			activeReporter: true,
			activeTarget:   true,
			wantError:      "two distinct block hashes",
			mutate: func(t *testing.T, targetKey, _ chaincrypto.PrivateKey, height uint64, payload map[string]string) {
				payload["second_block_hash"] = payload["first_block_hash"]
				payload["second_signature"] = signFinalityVote(t, targetKey, "chainlab-local", height, payload["first_block_hash"])
			},
		},
		{
			name:           "non-canonical target",
			activeReporter: true,
			activeTarget:   true,
			wantError:      "target is not canonically encoded",
			mutate: func(_ *testing.T, _ chaincrypto.PrivateKey, _ chaincrypto.PrivateKey, _ uint64, payload map[string]string) {
				payload["target"] = strings.ToUpper(payload["target"])
			},
		},
		{
			name:           "non-canonical block hash",
			activeReporter: true,
			activeTarget:   true,
			wantError:      "not a canonical 32-byte hash",
			mutate: func(_ *testing.T, _ chaincrypto.PrivateKey, _ chaincrypto.PrivateKey, _ uint64, payload map[string]string) {
				payload["first_block_hash"] = strings.ToUpper(payload["first_block_hash"])
			},
		},
		{
			name:           "non-canonical signature",
			activeReporter: true,
			activeTarget:   true,
			wantError:      "not a canonical compact signature",
			mutate: func(_ *testing.T, _ chaincrypto.PrivateKey, _ chaincrypto.PrivateKey, _ uint64, payload map[string]string) {
				payload["first_signature"] = strings.ToUpper(payload["first_signature"])
			},
		},
		{name: "inactive reporter", activeTarget: true, included: true},
		{name: "inactive target", activeReporter: true, included: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reporterKey, err := chaincrypto.GenerateKey()
			if err != nil {
				t.Fatal(err)
			}
			targetKey, err := chaincrypto.GenerateKey()
			if err != nil {
				t.Fatal(err)
			}
			forgedKey, err := chaincrypto.GenerateKey()
			if err != nil {
				t.Fatal(err)
			}
			reporter := chaincrypto.AddressFromPrivateKey(reporterKey)
			target := chaincrypto.AddressFromPrivateKey(targetKey)
			store := state.NewStore()
			store.SetBalance(reporter, 1_000_000)
			if err := store.AddStake(target, 600); err != nil {
				t.Fatal(err)
			}
			validators := make([]string, 0, 2)
			if test.activeReporter {
				validators = append(validators, reporter)
			}
			if test.activeTarget {
				validators = append(validators, target)
			}
			store.SetValidators(validators)
			executor := core.NewExecutor("chainlab-local", "", contracts.NewRuntimeWithDefaults())

			const height = uint64(9)
			payload := slashEvidencePayload(
				t,
				targetKey,
				target,
				"chainlab-local",
				height,
				"0x"+strings.Repeat("33", 32),
				"0x"+strings.Repeat("44", 32),
			)
			if test.mutate != nil {
				test.mutate(t, targetKey, forgedKey, height, payload)
			}
			tx := validatorSlashTx(t, reporterKey, reporter, 0, payload)
			rootBefore := store.Root()
			receipt, err := executor.Execute(store, tx)

			if !test.included {
				if err == nil || !strings.Contains(err.Error(), test.wantError) {
					t.Fatalf("slash admission error = %v, want %q", err, test.wantError)
				}
				if store.Root() != rootBefore || store.GetAccount(reporter).Nonce != 0 {
					t.Fatal("invalid slash evidence changed state before admission")
				}
				return
			}

			if err != nil {
				t.Fatal(err)
			}
			if receipt.Success || receipt.FailureCode != types.ReceiptFailureExecutionReverted {
				t.Fatalf("inactive validator slash receipt = %+v", receipt)
			}
			if store.GetAccount(reporter).Nonce != 1 || store.StakeOf(target) != 600 {
				t.Fatal("inactive validator slash changed stake or failed to consume included nonce")
			}
			if got := strings.Join(store.Validators(), ","); got != strings.Join(validators, ",") {
				t.Fatalf("rejected slash changed fixed validator set: %q", got)
			}
			if len(store.Params()) != 0 {
				t.Fatalf("rejected slash consumed evidence: %#v", store.Params())
			}
		})
	}
}

func TestWASMUploadStoresCodeAndDeploysByUploadedCodeID(t *testing.T) {
	store, executor, key, alice, _ := newExecutorFixture(t)
	initialRoot := store.Root()
	bytecode := contracts.WasmEchoCode()

	upload := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxWASMUpload,
		From:     alice,
		Nonce:    0,
		GasLimit: core.EstimateWASMUploadGas(bytecode),
		GasPrice: 1,
		Payload: map[string]string{
			"bytecode": "0x" + hex.EncodeToString(bytecode),
		},
	})
	uploadReceipt, err := executor.Execute(store, upload)
	if err != nil {
		t.Fatal(err)
	}
	if uploadReceipt.CodeID == "" {
		t.Fatalf("upload receipt missing code id: %+v", uploadReceipt)
	}
	stored, ok := store.ContractCode(uploadReceipt.CodeID)
	if !ok {
		t.Fatalf("uploaded code %q not found", uploadReceipt.CodeID)
	}
	if stored.Runtime != "wasm" || stored.Creator != alice {
		t.Fatalf("stored code = %+v", stored)
	}
	if store.Root() == initialRoot {
		t.Fatal("uploaded code should affect state root")
	}

	deploy := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     alice,
		Nonce:    1,
		GasLimit: 90_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": uploadReceipt.CodeID,
			"message": "hello",
		},
	})
	deployReceipt, err := executor.Execute(store, deploy)
	if err != nil {
		t.Fatal(err)
	}
	if deployReceipt.ContractAddress == "" {
		t.Fatalf("deploy receipt = %+v", deployReceipt)
	}

	call := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     alice,
		To:       deployReceipt.ContractAddress,
		Nonce:    2,
		GasLimit: 60_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method":  "set",
			"message": "world",
		},
	})
	if _, err := executor.Execute(store, call); err != nil {
		t.Fatal(err)
	}

	value, err := contracts.NewRuntimeWithDefaults().Read(store, deployReceipt.ContractAddress, alice, "get", nil)
	if err != nil {
		t.Fatal(err)
	}
	if value != "world" {
		t.Fatalf("uploaded wasm read = %q", value)
	}
}

func TestWASMUploadGasScalesWithBytecodeSizeAndRequiresLimit(t *testing.T) {
	store, executor, key, alice, _ := newExecutorFixture(t)
	bytecode := contracts.WasmEchoCode()
	baseGas, err := core.EstimateGas(types.TxWASMUpload)
	if err != nil {
		t.Fatal(err)
	}
	meteredGas := core.EstimateWASMUploadGas(bytecode)
	if meteredGas <= baseGas {
		t.Fatalf("metered upload gas = %d, base = %d", meteredGas, baseGas)
	}

	tooLow := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxWASMUpload,
		From:     alice,
		Nonce:    0,
		GasLimit: baseGas,
		GasPrice: 1,
		Payload: map[string]string{
			"bytecode": "0x" + hex.EncodeToString(bytecode),
		},
	})
	if _, err := executor.Execute(store, tooLow); err == nil {
		t.Fatal("upload with only base gas should fail")
	}

	ok := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxWASMUpload,
		From:     alice,
		Nonce:    0,
		GasLimit: meteredGas,
		GasPrice: 1,
		Payload: map[string]string{
			"bytecode": "0x" + hex.EncodeToString(bytecode),
		},
	})
	receipt, err := executor.Execute(store, ok)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.GasUsed != meteredGas {
		t.Fatalf("upload gas used = %d, want %d", receipt.GasUsed, meteredGas)
	}
	if got := store.GetAccount("0xfee0000000000000000000000000000000000000").Balance; got != meteredGas {
		t.Fatalf("fee collector balance = %d, want %d", got, meteredGas)
	}
}

func TestWASMCallGasIncludesHostResourcesAndEnforcesLimit(t *testing.T) {
	store, executor, key, alice, _ := newExecutorFixture(t)
	bytecode := contracts.WasmEchoCode()
	upload := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxWASMUpload,
		From:     alice,
		Nonce:    0,
		GasLimit: core.EstimateWASMUploadGas(bytecode),
		GasPrice: 1,
		Payload: map[string]string{
			"bytecode": "0x" + hex.EncodeToString(bytecode),
		},
	})
	uploadReceipt, err := executor.Execute(store, upload)
	if err != nil {
		t.Fatal(err)
	}
	deploy := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     alice,
		Nonce:    1,
		GasLimit: 90_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": uploadReceipt.CodeID,
			"message": "hello",
		},
	})
	deployReceipt, err := executor.Execute(store, deploy)
	if err != nil {
		t.Fatal(err)
	}

	lowCall := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     alice,
		To:       deployReceipt.ContractAddress,
		Nonce:    2,
		GasLimit: 50_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method":  "set",
			"message": "world",
		},
	})
	lowReceipt, err := executor.Execute(store, lowCall)
	if err != nil {
		t.Fatal(err)
	}
	if lowReceipt.Success || lowReceipt.FailureCode != types.ReceiptFailureOutOfGas || lowReceipt.GasUsed != lowCall.GasLimit {
		t.Fatalf("low-gas call receipt = %+v", lowReceipt)
	}
	value, err := contracts.NewRuntimeWithDefaults().Read(store, deployReceipt.ContractAddress, alice, "get", nil)
	if err != nil {
		t.Fatal(err)
	}
	if value != "hello" {
		t.Fatalf("failed low-gas call should not mutate storage, got %q", value)
	}

	okCall := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     alice,
		To:       deployReceipt.ContractAddress,
		Nonce:    3,
		GasLimit: 60_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method":  "set",
			"message": "world",
		},
	})
	receipt, err := executor.Execute(store, okCall)
	if err != nil {
		t.Fatal(err)
	}
	baseGas, err := core.EstimateGas(types.TxCall)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.GasUsed <= baseGas {
		t.Fatalf("wasm call gas used = %d, base = %d", receipt.GasUsed, baseGas)
	}
}
