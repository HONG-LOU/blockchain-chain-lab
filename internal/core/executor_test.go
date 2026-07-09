package core_test

import (
	"encoding/hex"
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
		GasLimit: 80_000,
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
		GasLimit: 92_000,
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

	if receipt.GasUsed != 92_000 {
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

	submit := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxProposalSubmit,
		From:     alice,
		Nonce:    1,
		GasLimit: 35_000,
		GasPrice: 1,
		Payload: map[string]string{
			"title":         "Upgrade one",
			"kind":          "param.change",
			"param":         "governance.mode",
			"value":         "demo",
			"voting_period": "2",
		},
	})
	submitReceipt, err := executor.ExecuteAtHeight(store, submit, 1)
	if err != nil {
		t.Fatal(err)
	}

	vote := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxVote,
		From:     alice,
		Nonce:    2,
		GasLimit: 25_000,
		GasPrice: 1,
		Payload: map[string]string{
			"proposal": submitReceipt.ProposalID,
			"choice":   "yes",
		},
	})
	if _, err := executor.ExecuteAtHeight(store, vote, 2); err != nil {
		t.Fatal(err)
	}
	if got := store.Proposal(submitReceipt.ProposalID).Votes["yes"]; got != 500 {
		t.Fatalf("yes vote power = %d", got)
	}

	unstake := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxUnstake,
		From:     alice,
		Nonce:    3,
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

func TestGovernanceProposalLifecycleExecutesParamChange(t *testing.T) {
	store, executor, key, alice, _ := newExecutorFixture(t)
	if err := store.AddStake(alice, 500); err != nil {
		t.Fatal(err)
	}

	submit := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxProposalSubmit,
		From:     alice,
		Nonce:    0,
		GasLimit: 35_000,
		GasPrice: 1,
		Payload: map[string]string{
			"title":         "Tune governance quorum",
			"description":   "Move quorum to majority for the local chain",
			"kind":          "param.change",
			"param":         "governance.quorum",
			"value":         "majority",
			"voting_period": "2",
		},
	})
	submitReceipt, err := executor.ExecuteAtHeight(store, submit, 1)
	if err != nil {
		t.Fatal(err)
	}
	if submitReceipt.ProposalID == "" {
		t.Fatalf("submit receipt = %+v", submitReceipt)
	}
	proposal := store.Proposal(submitReceipt.ProposalID)
	if proposal.Status != types.ProposalStatusOpen {
		t.Fatalf("proposal after submit = %+v", proposal)
	}
	if proposal.SubmitHeight != 1 || proposal.VotingEndHeight != 3 {
		t.Fatalf("proposal heights = submit %d end %d", proposal.SubmitHeight, proposal.VotingEndHeight)
	}

	vote := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxVote,
		From:     alice,
		Nonce:    1,
		GasLimit: 25_000,
		GasPrice: 1,
		Payload: map[string]string{
			"proposal": submitReceipt.ProposalID,
			"choice":   "yes",
		},
	})
	if _, err := executor.ExecuteAtHeight(store, vote, 2); err != nil {
		t.Fatal(err)
	}
	if got := store.Proposal(submitReceipt.ProposalID).Votes["yes"]; got != 500 {
		t.Fatalf("yes votes = %d", got)
	}

	earlyExecute := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxProposalExecute,
		From:     alice,
		Nonce:    2,
		GasLimit: 35_000,
		GasPrice: 1,
		Payload: map[string]string{
			"proposal": submitReceipt.ProposalID,
		},
	})
	if _, err := executor.ExecuteAtHeight(store, earlyExecute, 2); err == nil {
		t.Fatal("proposal should not execute before voting period ends")
	}

	execute := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxProposalExecute,
		From:     alice,
		Nonce:    2,
		GasLimit: 35_000,
		GasPrice: 1,
		Payload: map[string]string{
			"proposal": submitReceipt.ProposalID,
		},
	})
	executeReceipt, err := executor.ExecuteAtHeight(store, execute, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Param("governance.quorum"); got != "majority" {
		t.Fatalf("param value = %q", got)
	}
	proposal = store.Proposal(submitReceipt.ProposalID)
	if proposal.Status != types.ProposalStatusExecuted {
		t.Fatalf("proposal after execute = %+v", proposal)
	}
	if len(executeReceipt.Events) != 1 || executeReceipt.Events[0].Type != "governance.proposal.executed" {
		t.Fatalf("execute receipt events = %+v", executeReceipt.Events)
	}
}

func TestValidatorJoinRequiresStake(t *testing.T) {
	store, executor, key, alice, _ := newExecutorFixture(t)

	joinWithoutStake := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxValidatorJoin,
		From:     alice,
		Nonce:    0,
		GasLimit: 40_000,
		GasPrice: 1,
	})
	if _, err := executor.Execute(store, joinWithoutStake); err == nil {
		t.Fatal("validator join without stake should fail")
	}

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

	join := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxValidatorJoin,
		From:     alice,
		Nonce:    1,
		GasLimit: 40_000,
		GasPrice: 1,
	})
	receipt, err := executor.Execute(store, join)
	if err != nil {
		t.Fatal(err)
	}
	validators := store.Validators()
	if len(validators) != 1 || validators[0] != alice {
		t.Fatalf("validators = %#v", validators)
	}
	if len(receipt.Events) != 1 || receipt.Events[0].Type != "validator.joined" {
		t.Fatalf("events = %#v", receipt.Events)
	}
}

func TestValidatorLeaveRemovesActiveValidator(t *testing.T) {
	store, executor, key, alice, _ := newExecutorFixture(t)
	keyB, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorB := chaincrypto.AddressFromPrivateKey(keyB)
	store.SetBalance(validatorB, 1_000_000)
	store.SetValidators([]string{alice, validatorB})

	leave := signedTx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxValidatorLeave,
		From:     alice,
		Nonce:    0,
		GasLimit: 40_000,
		GasPrice: 1,
	})
	receipt, err := executor.Execute(store, leave)
	if err != nil {
		t.Fatal(err)
	}
	validators := store.Validators()
	if len(validators) != 1 || validators[0] != validatorB {
		t.Fatalf("validators = %#v", validators)
	}
	if len(receipt.Events) != 1 || receipt.Events[0].Type != "validator.left" {
		t.Fatalf("events = %#v", receipt.Events)
	}

	leaveLast := signedTx(t, keyB, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxValidatorLeave,
		From:     validatorB,
		Nonce:    0,
		GasLimit: 40_000,
		GasPrice: 1,
	})
	if _, err := executor.Execute(store, leaveLast); err == nil {
		t.Fatal("last validator should not be able to leave")
	}
}

func TestValidatorSlashReducesStakeAndRemovesDepletedValidator(t *testing.T) {
	store, executor, keyA, validatorA, _ := newExecutorFixture(t)
	keyB, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorB := chaincrypto.AddressFromPrivateKey(keyB)
	store.SetBalance(validatorB, 1_000_000)
	store.SetValidators([]string{validatorA, validatorB})
	if err := store.AddStake(validatorB, 600); err != nil {
		t.Fatal(err)
	}

	partial := signedTx(t, keyA, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxValidatorSlash,
		From:     validatorA,
		Nonce:    0,
		GasLimit: 45_000,
		GasPrice: 1,
		Payload: map[string]string{
			"target":   validatorB,
			"amount":   "200",
			"evidence": "double-sign-height-7",
		},
	})
	partialReceipt, err := executor.Execute(store, partial)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.StakeOf(validatorB); got != 400 {
		t.Fatalf("stake after partial slash = %d", got)
	}
	if len(partialReceipt.Events) != 1 || partialReceipt.Events[0].Type != "validator.slashed" {
		t.Fatalf("partial slash events = %#v", partialReceipt.Events)
	}

	deplete := signedTx(t, keyA, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxValidatorSlash,
		From:     validatorA,
		Nonce:    1,
		GasLimit: 45_000,
		GasPrice: 1,
		Payload: map[string]string{
			"target":   validatorB,
			"amount":   "500",
			"evidence": "downtime-window-9",
		},
	})
	receipt, err := executor.Execute(store, deplete)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.StakeOf(validatorB); got != 0 {
		t.Fatalf("stake after depleted slash = %d", got)
	}
	validators := store.Validators()
	if len(validators) != 1 || validators[0] != validatorA {
		t.Fatalf("validators = %#v", validators)
	}
	if len(receipt.Events) != 1 || receipt.Events[0].Attributes["removed"] != "true" {
		t.Fatalf("depleted slash event = %#v", receipt.Events)
	}
}

func TestValidatorSlashRequiresActiveReporterAndEvidence(t *testing.T) {
	store, executor, keyA, validatorA, _ := newExecutorFixture(t)
	keyReporter, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	reporter := chaincrypto.AddressFromPrivateKey(keyReporter)
	store.SetBalance(reporter, 1_000_000)
	store.SetValidators([]string{validatorA})
	if err := store.AddStake(validatorA, 600); err != nil {
		t.Fatal(err)
	}

	notValidator := signedTx(t, keyReporter, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxValidatorSlash,
		From:     reporter,
		Nonce:    0,
		GasLimit: 45_000,
		GasPrice: 1,
		Payload: map[string]string{
			"target":   validatorA,
			"amount":   "100",
			"evidence": "bad-signature",
		},
	})
	if _, err := executor.Execute(store, notValidator); err == nil {
		t.Fatal("inactive reporter should not slash validators")
	}

	missingEvidence := signedTx(t, keyA, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxValidatorSlash,
		From:     validatorA,
		Nonce:    0,
		GasLimit: 45_000,
		GasPrice: 1,
		Payload: map[string]string{
			"target": validatorA,
			"amount": "100",
		},
	})
	if _, err := executor.Execute(store, missingEvidence); err == nil {
		t.Fatal("slash without evidence should fail")
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
	if _, err := executor.Execute(store, lowCall); err == nil {
		t.Fatal("wasm call with only base gas should fail")
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
		Nonce:    2,
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
