package core_test

import (
	"encoding/hex"
	"math"
	"strconv"
	"testing"

	"chainlab/internal/contracts"
	"chainlab/internal/core"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/state"
	"chainlab/internal/types"
)

const chargedFailureFeeCollector = "0xfee0000000000000000000000000000000000000"

func TestIncludedTransferFailureChargesFeeAndCommitsNonce(t *testing.T) {
	store, executor, key, alice, bob := newExecutorFixture(t)
	store.SetBalance(alice, 100_000)
	store.SetBalance(bob, math.MaxUint64)
	transaction := signedTx(t, key, types.Transaction{
		ChainID:              "chainlab-local",
		Type:                 types.TxTransfer,
		From:                 alice,
		To:                   bob,
		Nonce:                0,
		Value:                1,
		GasLimit:             21_000,
		MaxFeePerGas:         3,
		MaxPriorityFeePerGas: 1,
	})

	payerBalanceBefore := store.GetAccount(alice).Balance
	collectorBalanceBefore := store.GetAccount(chargedFailureFeeCollector).Balance
	receipt, err := executor.ExecuteWithContext(store, transaction, core.ExecutionContext{BaseFeePerGas: 2})

	requireChargedFailureReceipt(t, receipt, err, transaction.Hash(), "execution_reverted")
	if receipt.GasUsed != 21_000 {
		t.Fatalf("failed transfer gas used = %d, want 21000", receipt.GasUsed)
	}
	requireChargedFailureFee(t, store, receipt, alice, payerBalanceBefore, collectorBalanceBefore)
	if got := store.GetAccount(alice).Nonce; got != 1 {
		t.Fatalf("sender nonce = %d, want 1", got)
	}
	if got := store.GetAccount(bob).Balance; got != math.MaxUint64 {
		t.Fatalf("receiver balance = %d, want %d", got, uint64(math.MaxUint64))
	}
}

func TestIncludedNativeContractFailureRollsBackContractState(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	store := state.NewStore()
	store.SetBalance(alice, 2_000_000)
	runtime := contracts.NewRuntimeWithDefaults()
	token := deployNativeToken(t, runtime, store, alice, "native-failure")
	store.SetStorage(token, "balance:"+alice, "10")
	store.SetStorage(token, "balance:"+bob, strconv.FormatUint(math.MaxUint64, 10))
	executor := core.NewExecutor("chainlab-local", chargedFailureFeeCollector, runtime)
	transaction := signedTx(t, key, types.Transaction{
		ChainID:              "chainlab-local",
		Type:                 types.TxCall,
		From:                 alice,
		To:                   token,
		Nonce:                0,
		GasLimit:             250_000,
		MaxFeePerGas:         4,
		MaxPriorityFeePerGas: 1,
		Payload: map[string]string{
			"method": "transfer",
			"to":     bob,
			"amount": "1",
		},
	})

	payerBalanceBefore := store.GetAccount(alice).Balance
	collectorBalanceBefore := store.GetAccount(chargedFailureFeeCollector).Balance
	receipt, err := executor.ExecuteWithContext(store, transaction, core.ExecutionContext{BaseFeePerGas: 2})

	requireChargedFailureReceipt(t, receipt, err, transaction.Hash(), "execution_reverted")
	requireChargedFailureFee(t, store, receipt, alice, payerBalanceBefore, collectorBalanceBefore)
	if got := store.GetAccount(alice).Nonce; got != 1 {
		t.Fatalf("sender nonce = %d, want 1", got)
	}
	if got := store.GetStorage(token, "balance:"+alice); got != "10" {
		t.Fatalf("sender token balance = %q, want 10", got)
	}
	if got := store.GetStorage(token, "balance:"+bob); got != strconv.FormatUint(math.MaxUint64, 10) {
		t.Fatalf("receiver token balance = %q, want %d", got, uint64(math.MaxUint64))
	}
}

func TestIncludedSponsoredFailureChargesPaymaster(t *testing.T) {
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
	store := state.NewStore()
	store.SetBalance(user, 1_234)
	store.SetBalance(paymaster, 1_000_000)
	runtime := contracts.NewRuntimeWithDefaults()
	token := deployNativeToken(t, runtime, store, user, "sponsored-failure")
	store.SetStorage(token, "balance:"+user, "1")
	store.SetStorage(token, "balance:"+receiver, strconv.FormatUint(math.MaxUint64, 10))
	executor := core.NewExecutor("chainlab-local", chargedFailureFeeCollector, runtime)
	transaction := sponsoredTx(t, userKey, paymasterKey, types.Transaction{
		ChainID:              "chainlab-local",
		Type:                 types.TxCall,
		From:                 user,
		To:                   token,
		Nonce:                0,
		GasLimit:             200_000,
		MaxFeePerGas:         4,
		MaxPriorityFeePerGas: 1,
		Paymaster:            paymaster,
		Payload: map[string]string{
			"method": "transfer",
			"to":     receiver,
			"amount": "1",
		},
	})

	userBalanceBefore := store.GetAccount(user).Balance
	paymasterBalanceBefore := store.GetAccount(paymaster).Balance
	collectorBalanceBefore := store.GetAccount(chargedFailureFeeCollector).Balance
	receipt, err := executor.ExecuteWithContext(store, transaction, core.ExecutionContext{BaseFeePerGas: 2})

	requireChargedFailureReceipt(t, receipt, err, transaction.Hash(), "execution_reverted")
	if receipt.FeePayer != paymaster {
		t.Fatalf("failed transaction fee payer = %q, want %q", receipt.FeePayer, paymaster)
	}
	requireChargedFailureFee(t, store, receipt, paymaster, paymasterBalanceBefore, collectorBalanceBefore)
	if got := store.GetAccount(user).Balance; got != userBalanceBefore {
		t.Fatalf("sponsored user balance = %d, want %d", got, userBalanceBefore)
	}
	if got := store.GetAccount(user).Nonce; got != 1 {
		t.Fatalf("sponsored user nonce = %d, want 1", got)
	}
	if got := store.GetStorage(token, "balance:"+user); got != "1" {
		t.Fatalf("user token balance = %q, want 1", got)
	}
	if got := store.GetStorage(token, "balance:"+receiver); got != strconv.FormatUint(math.MaxUint64, 10) {
		t.Fatalf("receiver token balance = %q, want %d", got, uint64(math.MaxUint64))
	}
}

func TestIncludedBatchFailureRollsBackAllBusinessState(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	carol := "0xcccccccccccccccccccccccccccccccccccccccc"
	store := state.NewStore()
	store.SetBalance(alice, 2_000_000)
	runtime := contracts.NewRuntimeWithDefaults()
	token := deployNativeToken(t, runtime, store, alice, "batch-failure")
	executor := core.NewExecutor("chainlab-local", chargedFailureFeeCollector, runtime)
	transaction := signedTx(t, key, types.Transaction{
		ChainID:              "chainlab-local",
		Type:                 types.TxBatch,
		From:                 alice,
		Nonce:                0,
		GasLimit:             250_000,
		MaxFeePerGas:         3,
		MaxPriorityFeePerGas: 1,
		Batch: []types.BatchOperation{
			{Type: types.TxTransfer, To: bob, Value: 100},
			{
				Type: types.TxCall,
				To:   token,
				Payload: map[string]string{
					"method": "transfer",
					"to":     carol,
					"amount": "1",
				},
			},
		},
	})

	payerBalanceBefore := store.GetAccount(alice).Balance
	collectorBalanceBefore := store.GetAccount(chargedFailureFeeCollector).Balance
	receipt, err := executor.ExecuteWithContext(store, transaction, core.ExecutionContext{BaseFeePerGas: 1})

	requireChargedFailureReceipt(t, receipt, err, transaction.Hash(), "execution_reverted")
	requireChargedFailureFee(t, store, receipt, alice, payerBalanceBefore, collectorBalanceBefore)
	minimumGas := mustEstimateGas(t, types.TxBatch) + mustEstimateGas(t, types.TxTransfer) + mustEstimateGas(t, types.TxCall)
	if receipt.GasUsed < minimumGas {
		t.Fatalf("failed batch gas used = %d, want at least %d", receipt.GasUsed, minimumGas)
	}
	if got := store.GetAccount(alice).Nonce; got != 1 {
		t.Fatalf("batch sender nonce = %d, want 1", got)
	}
	if got := store.GetAccount(bob).Balance; got != 0 {
		t.Fatalf("first operation recipient balance = %d, want 0", got)
	}
	if got := store.GetStorage(token, "balance:"+alice); got != "" {
		t.Fatalf("batch sender token balance = %q, want empty", got)
	}
	if got := store.GetStorage(token, "balance:"+carol); got != "" {
		t.Fatalf("batch token recipient balance = %q, want empty", got)
	}
}

func TestIncludedWASMOutOfGasConsumesGasLimit(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	store := state.NewStore()
	store.SetBalance(alice, 5_000_000)
	runtime := contracts.NewRuntimeWithDefaults()
	executor := core.NewExecutor("chainlab-local", chargedFailureFeeCollector, runtime)
	bytecode := contracts.WasmEchoCode()
	upload := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxWASMUpload,
		From:     alice,
		Nonce:    0,
		GasLimit: core.EstimateWASMUploadGas(bytecode),
		GasPrice: 1,
		Payload:  map[string]string{"bytecode": "0x" + hex.EncodeToString(bytecode)},
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
		GasLimit: 100_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": uploadReceipt.CodeID,
			"message": "before-oog",
		},
	})
	deployReceipt, err := executor.Execute(store, deploy)
	if err != nil {
		t.Fatal(err)
	}
	transaction := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     alice,
		To:       deployReceipt.ContractAddress,
		Nonce:    2,
		GasLimit: 50_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method":  "set",
			"message": "after-oog",
		},
	})

	payerBalanceBefore := store.GetAccount(alice).Balance
	collectorBalanceBefore := store.GetAccount(chargedFailureFeeCollector).Balance
	receipt, err := executor.ExecuteWithContext(store, transaction, core.ExecutionContext{BaseFeePerGas: 1})

	requireChargedFailureReceipt(t, receipt, err, transaction.Hash(), "out_of_gas")
	if receipt.GasUsed != transaction.GasLimit {
		t.Fatalf("out-of-gas receipt gas used = %d, want gas limit %d", receipt.GasUsed, transaction.GasLimit)
	}
	requireChargedFailureFee(t, store, receipt, alice, payerBalanceBefore, collectorBalanceBefore)
	if got := store.GetAccount(alice).Nonce; got != 3 {
		t.Fatalf("WASM caller nonce = %d, want 3", got)
	}
	value, err := runtime.Read(store, deployReceipt.ContractAddress, alice, "get", nil)
	if err != nil {
		t.Fatal(err)
	}
	if value != "before-oog" {
		t.Fatalf("WASM value after failed call = %q, want before-oog", value)
	}
}

func requireChargedFailureReceipt(t *testing.T, receipt types.Receipt, err error, txHash string, failureCode string) {
	t.Helper()
	if err != nil {
		t.Fatalf("included deterministic failure returned executor error: %v", err)
	}
	if receipt.Success {
		t.Fatalf("failed transaction receipt reports success: %+v", receipt)
	}
	if receipt.FailureCode != failureCode {
		t.Fatalf("failure code = %q, want %q", receipt.FailureCode, failureCode)
	}
	if receipt.Error != "" {
		t.Fatalf("failed receipt exposed non-consensus error text %q", receipt.Error)
	}
	if receipt.TxHash != txHash {
		t.Fatalf("failed receipt transaction hash = %q, want %q", receipt.TxHash, txHash)
	}
	if receipt.GasUsed == 0 {
		t.Fatal("failed receipt gas used must be positive")
	}
}

func requireChargedFailureFee(
	t *testing.T,
	store *state.Store,
	receipt types.Receipt,
	feePayer string,
	payerBalanceBefore uint64,
	collectorBalanceBefore uint64,
) {
	t.Helper()
	payerBalanceAfter := store.GetAccount(feePayer).Balance
	if payerBalanceAfter > payerBalanceBefore {
		t.Fatalf("fee payer balance increased from %d to %d", payerBalanceBefore, payerBalanceAfter)
	}
	totalFee := receipt.GasUsed * receipt.EffectiveGasPrice
	if charged := payerBalanceBefore - payerBalanceAfter; charged != totalFee {
		t.Fatalf("charged failed fee = %d, want %d", charged, totalFee)
	}
	if receipt.BaseFeeBurned+receipt.PriorityFeePaid != totalFee {
		t.Fatalf("failed fee breakdown = burned %d + priority %d, want %d", receipt.BaseFeeBurned, receipt.PriorityFeePaid, totalFee)
	}
	collectorBalanceAfter := store.GetAccount(chargedFailureFeeCollector).Balance
	if collectorBalanceAfter < collectorBalanceBefore {
		t.Fatalf("fee collector balance decreased from %d to %d", collectorBalanceBefore, collectorBalanceAfter)
	}
	if paid := collectorBalanceAfter - collectorBalanceBefore; paid != receipt.PriorityFeePaid {
		t.Fatalf("collector priority fee = %d, want %d", paid, receipt.PriorityFeePaid)
	}
}

func deployNativeToken(t *testing.T, runtime *contracts.Runtime, store *state.Store, owner string, seed string) string {
	t.Helper()
	address, _, err := runtime.Deploy(store, owner, "token.v1", seed, map[string]string{"symbol": "FAIL"})
	if err != nil {
		t.Fatal(err)
	}
	return address
}

func mustEstimateGas(t *testing.T, transactionType types.TxType) uint64 {
	t.Helper()
	gas, err := core.EstimateGas(transactionType)
	if err != nil {
		t.Fatal(err)
	}
	return gas
}
