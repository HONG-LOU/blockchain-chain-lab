package rpc_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"chainlab/internal/consensus"
	"chainlab/internal/contracts"
	"chainlab/internal/core"
	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/hash"
	"chainlab/internal/node"
	chainrpc "chainlab/internal/rpc"
	"chainlab/internal/types"
)

func TestRPCHealthHeadAccountAndTx(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", resp.StatusCode)
	}

	tx := types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       bob,
		Nonce:    0,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	}
	sig, err := chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = sig

	body, _ := json.Marshal(tx)
	resp, err = http.Post(server.URL+"/tx", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("tx status = %d", resp.StatusCode)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	resp, err = http.Get(server.URL + "/account/" + bob)
	if err != nil {
		t.Fatal(err)
	}
	var account types.Account
	if err := json.NewDecoder(resp.Body).Decode(&account); err != nil {
		t.Fatal(err)
	}
	if account.Balance != 100 {
		t.Fatalf("rpc account balance = %d", account.Balance)
	}

	rpcBody := bytes.NewBufferString(`{"id":1,"method":"chain_head"}`)
	resp, err = http.Post(server.URL+"/rpc", "application/json", rpcBody)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("json-rpc status = %d", resp.StatusCode)
	}
	var rpcResp struct {
		ID     int         `json:"id"`
		Result types.Block `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatal(err)
	}
	if rpcResp.Result.Header.Height != 1 {
		t.Fatalf("json-rpc head height = %d", rpcResp.Result.Header.Height)
	}
}

func TestEVMCompatibleJSONRPCSubset(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	tx := types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     alice,
		To:       bob,
		Nonce:    0,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	}
	sig, err := chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = sig
	if err := n.SubmitTx(tx); err != nil {
		t.Fatal(err)
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}

	assertRPCResult(t, server.URL, "eth_chainId", []any{}, "0x7a69")
	assertRPCResult(t, server.URL, "eth_blockNumber", []any{}, "0x1")
	assertRPCResult(t, server.URL, "eth_getBalance", []any{bob, "latest"}, "0x64")
	assertRPCResult(t, server.URL, "eth_getTransactionCount", []any{alice, "latest"}, "0x1")

	txResult := callRPC(t, server.URL, "eth_getTransactionByHash", []any{tx.Hash()})
	txMap, ok := txResult.(map[string]any)
	if !ok {
		t.Fatalf("transaction result type = %T", txResult)
	}
	if txMap["hash"] != tx.Hash() {
		t.Fatalf("transaction hash = %v", txMap["hash"])
	}
	if txMap["blockNumber"] != "0x1" {
		t.Fatalf("transaction block number = %v", txMap["blockNumber"])
	}

	receiptResult := callRPC(t, server.URL, "eth_getTransactionReceipt", []any{tx.Hash()})
	receiptMap, ok := receiptResult.(map[string]any)
	if !ok {
		t.Fatalf("receipt result type = %T", receiptResult)
	}
	if receiptMap["status"] != "0x1" {
		t.Fatalf("receipt status = %v", receiptMap["status"])
	}
	if receiptMap["blockHash"] != block.Hash() {
		t.Fatalf("receipt block hash = %v", receiptMap["blockHash"])
	}

	blockResult := callRPC(t, server.URL, "eth_getBlockByNumber", []any{"0x1", true})
	blockMap, ok := blockResult.(map[string]any)
	if !ok {
		t.Fatalf("block result type = %T", blockResult)
	}
	if blockMap["number"] != "0x1" {
		t.Fatalf("block number = %v", blockMap["number"])
	}
}

func TestRPCExposesFeeMarketFields(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	carol := "0xcccccccccccccccccccccccccccccccccccccccc"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
		BlockGasLimit:  42_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	for nonce, to := range []string{bob, carol} {
		tx := signedRPCTransaction(t, key, types.Transaction{
			ChainID:  "chainlab-local",
			Type:     types.TxTransfer,
			From:     alice,
			To:       to,
			Nonce:    uint64(nonce),
			Value:    1,
			GasLimit: 21_000,
			GasPrice: 2,
		})
		if err := n.SubmitTx(tx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	assertRPCResult(t, server.URL, "eth_gasPrice", []any{}, "0x3")
	assertRPCResult(t, server.URL, "eth_maxPriorityFeePerGas", []any{}, "0x1")
	feeMarket := callRPC(t, server.URL, "chain_feeMarket", []any{})
	feeMap, ok := feeMarket.(map[string]any)
	if !ok || feeMap["base_fee_per_gas"] != "0x2" || feeMap["gas_price"] != "0x3" {
		t.Fatalf("fee market = %#v", feeMarket)
	}

	blockResult := callRPC(t, server.URL, "eth_getBlockByNumber", []any{"latest", false})
	blockMap, ok := blockResult.(map[string]any)
	if !ok {
		t.Fatalf("block result type = %T", blockResult)
	}
	if blockMap["baseFeePerGas"] != "0x2" || blockMap["gasLimit"] != "0xa410" || blockMap["gasUsed"] != "0x0" {
		t.Fatalf("fee block fields = %#v", blockMap)
	}
}

func TestRPCSubmitsSponsoredUserOperation(t *testing.T) {
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
	n, err := node.New(node.Config{
		ChainID:     "chainlab-local",
		ProposerKey: paymasterKey,
		GenesisBalance: map[string]uint64{
			user:      100,
			paymaster: 100_000,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	tx := sponsoredRPCTransaction(t, userKey, paymasterKey, types.Transaction{
		ChainID:   "chainlab-local",
		Type:      types.TxTransfer,
		From:      user,
		To:        receiver,
		Nonce:     0,
		Value:     100,
		GasLimit:  21_000,
		GasPrice:  2,
		Paymaster: paymaster,
	})
	result := callRPC(t, server.URL, "chain_sendUserOperation", []any{tx})
	resultMap, ok := result.(map[string]any)
	if !ok || resultMap["hash"] != tx.Hash() {
		t.Fatalf("send user operation result = %#v", result)
	}
	pool := n.TxPool()
	if pool.PendingCount != 1 || pool.Pending[0].Paymaster != paymaster {
		t.Fatalf("txpool = %+v", pool)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	receiptResult := callRPC(t, server.URL, "eth_getTransactionReceipt", []any{tx.Hash()})
	receiptMap, ok := receiptResult.(map[string]any)
	if !ok || receiptMap["feePayer"] != paymaster {
		t.Fatalf("receipt = %#v", receiptResult)
	}
	if got := n.Account(user).Balance; got != 0 {
		t.Fatalf("user balance = %d", got)
	}
}

func TestRPCSubmitsSponsoredBatchUserOperationAndEstimatesGas(t *testing.T) {
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
	n, err := node.New(node.Config{
		ChainID:     "chainlab-local",
		ProposerKey: paymasterKey,
		GenesisBalance: map[string]uint64{
			user:      30,
			paymaster: 500_000,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	estimate := callRPC(t, server.URL, "eth_estimateGas", []any{map[string]any{
		"type": "batch",
		"batch": []any{
			map[string]any{"type": "transfer", "to": bob, "value": "0xa"},
			map[string]any{"type": "transfer", "to": carol, "value": "0x14"},
		},
	}})
	if estimate != "0x14820" {
		t.Fatalf("batch gas estimate = %v", estimate)
	}

	tx := sponsoredRPCTransaction(t, userKey, paymasterKey, types.Transaction{
		ChainID:   "chainlab-local",
		Type:      types.TxBatch,
		From:      user,
		Nonce:     0,
		GasLimit:  84_000,
		GasPrice:  2,
		Paymaster: paymaster,
		Batch: []types.BatchOperation{
			{Type: types.TxTransfer, To: bob, Value: 10},
			{Type: types.TxTransfer, To: carol, Value: 20},
		},
	})
	result := callRPC(t, server.URL, "chain_sendUserOperation", []any{tx})
	resultMap, ok := result.(map[string]any)
	if !ok || resultMap["hash"] != tx.Hash() {
		t.Fatalf("send batch user operation result = %#v", result)
	}
	pool := n.TxPool()
	if pool.PendingCount != 1 || pool.Pending[0].Type != types.TxBatch || len(pool.Pending[0].Batch) != 2 {
		t.Fatalf("txpool = %+v", pool)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	receiptResult := callRPC(t, server.URL, "eth_getTransactionReceipt", []any{tx.Hash()})
	receiptMap, ok := receiptResult.(map[string]any)
	if !ok || receiptMap["feePayer"] != paymaster || receiptMap["gasUsed"] != "0x14820" {
		t.Fatalf("receipt = %#v", receiptResult)
	}
	if got := n.Account(user).Balance; got != 0 {
		t.Fatalf("user balance = %d", got)
	}
	if got := n.Account(bob).Balance; got != 10 {
		t.Fatalf("bob balance = %d", got)
	}
	if got := n.Account(carol).Balance; got != 20 {
		t.Fatalf("carol balance = %d", got)
	}
}

func TestRPCExposesSmartAccountSigner(t *testing.T) {
	ownerKey, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	owner := chaincrypto.AddressFromPrivateKey(ownerKey)
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    ownerKey,
		GenesisBalance: map[string]uint64{owner: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	deploy := signedRPCTransaction(t, ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     owner,
		Nonce:    0,
		GasLimit: 80_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": contracts.AccountCodeID,
			"owner":   owner,
		},
	})
	if err := n.SubmitTx(deploy); err != nil {
		t.Fatal(err)
	}
	deployBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	smartAccount := deployBlock.Receipts[0].ContractAddress

	fund := signedRPCTransaction(t, ownerKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     owner,
		To:       smartAccount,
		Nonce:    1,
		Value:    50_000,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if err := n.SubmitTx(fund); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	tx := signedRPCTransaction(t, ownerKey, types.Transaction{
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
	if err := n.SubmitTx(tx); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	txResult := callRPC(t, server.URL, "eth_getTransactionByHash", []any{tx.Hash()})
	txMap, ok := txResult.(map[string]any)
	if !ok {
		t.Fatalf("transaction result type = %T", txResult)
	}
	if txMap["signer"] != strings.ToLower(owner) {
		t.Fatalf("transaction signer = %#v", txMap["signer"])
	}
}

func TestRPCExposesMultisigAuthorizationCount(t *testing.T) {
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
	receiver := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    ownerAKey,
		GenesisBalance: map[string]uint64{ownerA: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	deploy := signedRPCTransaction(t, ownerAKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     ownerA,
		Nonce:    0,
		GasLimit: 80_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id":   contracts.MultisigCodeID,
			"owners":    ownerA + "," + ownerB,
			"threshold": "2",
		},
	})
	if err := n.SubmitTx(deploy); err != nil {
		t.Fatal(err)
	}
	deployBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	multisig := deployBlock.Receipts[0].ContractAddress

	fund := signedRPCTransaction(t, ownerAKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     ownerA,
		To:       multisig,
		Nonce:    1,
		Value:    50_000,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if err := n.SubmitTx(fund); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	tx := authorizedRPCTransaction(t, []chaincrypto.PrivateKey{ownerAKey, ownerBKey}, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     multisig,
		To:       receiver,
		Nonce:    0,
		Value:    100,
		GasLimit: 21_000,
		GasPrice: 1,
	})
	if err := n.SubmitTx(tx); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	txResult := callRPC(t, server.URL, "eth_getTransactionByHash", []any{tx.Hash()})
	txMap, ok := txResult.(map[string]any)
	if !ok {
		t.Fatalf("transaction result type = %T", txResult)
	}
	if txMap["authorizationCount"] != "0x2" {
		t.Fatalf("authorization count = %#v", txMap["authorizationCount"])
	}
}

func TestRPCExposesValidatorSet(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validator := chaincrypto.AddressFromPrivateKey(key)
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{validator: 1_000_000},
		Validators:     []string{validator},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	resp, err := http.Get(server.URL + "/validators")
	if err != nil {
		t.Fatal(err)
	}
	var validators []string
	if err := json.NewDecoder(resp.Body).Decode(&validators); err != nil {
		t.Fatal(err)
	}
	if len(validators) != 1 || validators[0] != validator {
		t.Fatalf("validators = %#v", validators)
	}

	result := callRPC(t, server.URL, "chain_validators", []any{})
	rpcValidators, ok := result.([]any)
	if !ok || len(rpcValidators) != 1 || rpcValidators[0] != validator {
		t.Fatalf("rpc validators = %#v", result)
	}
}

func TestRPCExposesGovernanceProposalAndParam(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	stake := signedRPCTransaction(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxStake,
		From:     alice,
		Nonce:    0,
		Value:    500,
		GasLimit: 30_000,
		GasPrice: 1,
	})
	if err := n.SubmitTx(stake); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	submit := signedRPCTransaction(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxProposalSubmit,
		From:     alice,
		Nonce:    1,
		GasLimit: 35_000,
		GasPrice: 1,
		Payload: map[string]string{
			"title":         "Set governance quorum",
			"description":   "Use majority quorum for local governance",
			"kind":          "param.change",
			"param":         "governance.quorum",
			"value":         "majority",
			"voting_period": "2",
		},
	})
	if err := n.SubmitTx(submit); err != nil {
		t.Fatal(err)
	}
	submitBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	proposalID := submitBlock.Receipts[0].ProposalID

	vote := signedRPCTransaction(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxVote,
		From:     alice,
		Nonce:    2,
		GasLimit: 25_000,
		GasPrice: 1,
		Payload: map[string]string{
			"proposal": proposalID,
			"choice":   "yes",
		},
	})
	if err := n.SubmitTx(vote); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	execute := signedRPCTransaction(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxProposalExecute,
		From:     alice,
		Nonce:    3,
		GasLimit: 35_000,
		GasPrice: 1,
		Payload: map[string]string{
			"proposal": proposalID,
		},
	})
	if err := n.SubmitTx(execute); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	resp, err := http.Get(server.URL + "/proposal/" + proposalID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("proposal status = %d body = %s", resp.StatusCode, string(body))
	}
	var proposal types.Proposal
	if err := json.NewDecoder(resp.Body).Decode(&proposal); err != nil {
		t.Fatal(err)
	}
	if proposal.Status != types.ProposalStatusExecuted || proposal.Votes["yes"] != 500 {
		t.Fatalf("proposal = %+v", proposal)
	}

	resp, err = http.Get(server.URL + "/param/governance.quorum")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("param status = %d body = %s", resp.StatusCode, string(body))
	}
	var param map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&param); err != nil {
		t.Fatal(err)
	}
	if param["key"] != "governance.quorum" || param["value"] != "majority" {
		t.Fatalf("param = %+v", param)
	}

	proposalResult := callRPC(t, server.URL, "chain_proposal", []any{proposalID})
	proposalMap, ok := proposalResult.(map[string]any)
	if !ok || proposalMap["status"] != string(types.ProposalStatusExecuted) {
		t.Fatalf("rpc proposal = %#v", proposalResult)
	}
	assertRPCResult(t, server.URL, "chain_param", []any{"governance.quorum"}, "majority")
}

func TestRPCExposesSafeAndFinalizedHeads(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validator := chaincrypto.AddressFromPrivateKey(key)
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{validator: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	var blocks []types.Block
	for i := 0; i < 4; i++ {
		block, err := n.ProduceBlock()
		if err != nil {
			t.Fatal(err)
		}
		blocks = append(blocks, block)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	resp, err := http.Get(server.URL + "/chain/finality")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("finality status = %d", resp.StatusCode)
	}
	var finality struct {
		HeadHeight      uint64 `json:"head_height"`
		HeadHash        string `json:"head_hash"`
		SafeHeight      uint64 `json:"safe_height"`
		SafeHash        string `json:"safe_hash"`
		SafeDepth       uint64 `json:"safe_depth"`
		FinalizedHeight uint64 `json:"finalized_height"`
		FinalizedHash   string `json:"finalized_hash"`
		FinalizedDepth  uint64 `json:"finalized_depth"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&finality); err != nil {
		t.Fatal(err)
	}
	if finality.HeadHeight != 4 || finality.HeadHash != blocks[3].Hash() {
		t.Fatalf("head finality = %+v", finality)
	}
	if finality.SafeHeight != 3 || finality.SafeHash != blocks[2].Hash() || finality.SafeDepth != 1 {
		t.Fatalf("safe finality = %+v", finality)
	}
	if finality.FinalizedHeight != 2 || finality.FinalizedHash != blocks[1].Hash() || finality.FinalizedDepth != 2 {
		t.Fatalf("finalized finality = %+v", finality)
	}

	rpcFinality := callRPC(t, server.URL, "chain_finality", []any{})
	rpcMap, ok := rpcFinality.(map[string]any)
	if !ok {
		t.Fatalf("rpc finality type = %T", rpcFinality)
	}
	if rpcMap["finalized_height"] != float64(2) || rpcMap["safe_height"] != float64(3) {
		t.Fatalf("rpc finality = %#v", rpcMap)
	}

	finalizedBlock := callRPC(t, server.URL, "eth_getBlockByNumber", []any{"finalized", false})
	finalizedMap, ok := finalizedBlock.(map[string]any)
	if !ok {
		t.Fatalf("finalized block type = %T", finalizedBlock)
	}
	if finalizedMap["number"] != "0x2" || finalizedMap["hash"] != blocks[1].Hash() {
		t.Fatalf("finalized block = %#v", finalizedMap)
	}
	safeBlock := callRPC(t, server.URL, "eth_getBlockByNumber", []any{"safe", false})
	safeMap, ok := safeBlock.(map[string]any)
	if !ok {
		t.Fatalf("safe block type = %T", safeBlock)
	}
	if safeMap["number"] != "0x3" || safeMap["hash"] != blocks[2].Hash() {
		t.Fatalf("safe block = %#v", safeMap)
	}
}

func TestRPCSendFinalityVoteCertifiesBlock(t *testing.T) {
	keyA, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyC, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorA := chaincrypto.AddressFromPrivateKey(keyA)
	validatorB := chaincrypto.AddressFromPrivateKey(keyB)
	validatorC := chaincrypto.AddressFromPrivateKey(keyC)
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyA,
		GenesisBalance: map[string]uint64{validatorA: 1_000_000},
		Validators:     []string{validatorA, validatorB, validatorC},
	})
	if err != nil {
		t.Fatal(err)
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var finality map[string]any
	for _, key := range []chaincrypto.PrivateKey{keyA, keyB, keyC} {
		vote, err := consensus.SignFinalityVote(key, block)
		if err != nil {
			t.Fatal(err)
		}
		result := callRPC(t, server.URL, "chain_sendFinalityVote", []any{vote})
		var ok bool
		finality, ok = result.(map[string]any)
		if !ok {
			t.Fatalf("finality vote result type = %T", result)
		}
	}
	if finality["finalized_source"] != "bft_certificate" || finality["certified_height"] != float64(1) || finality["certified_signers"] != float64(3) {
		t.Fatalf("finality after votes = %#v", finality)
	}

	blockResult := callRPC(t, server.URL, "eth_getBlockByNumber", []any{"finalized", false})
	blockMap, ok := blockResult.(map[string]any)
	if !ok {
		t.Fatalf("finalized block type = %T", blockResult)
	}
	certificate, ok := blockMap["finalityCertificate"].(map[string]any)
	if !ok {
		t.Fatalf("finality certificate = %#v", blockMap["finalityCertificate"])
	}
	if certificate["blockHash"] != block.Hash() || certificate["signerCount"] != "0x3" {
		t.Fatalf("evm block certificate = %#v", certificate)
	}
}

func TestRPCExposesFinalityEquivocationEvidence(t *testing.T) {
	keyA, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyC, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorA := chaincrypto.AddressFromPrivateKey(keyA)
	validatorB := chaincrypto.AddressFromPrivateKey(keyB)
	validatorC := chaincrypto.AddressFromPrivateKey(keyC)
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyA,
		GenesisBalance: map[string]uint64{validatorA: 1_000_000},
		Validators:     []string{validatorA, validatorB, validatorC},
	})
	if err != nil {
		t.Fatal(err)
	}
	expected := seedFinalityEquivocationEvidence(t, n, keyA, keyB, validatorA, validatorB)
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	resp, err := http.Get(server.URL + "/chain/finality/evidence")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("evidence status = %d", resp.StatusCode)
	}
	var evidence []types.FinalityEquivocationEvidence
	if err := json.NewDecoder(resp.Body).Decode(&evidence); err != nil {
		t.Fatal(err)
	}
	if len(evidence) != 1 || evidence[0].SecondBlockHash != expected.SecondBlockHash {
		t.Fatalf("rest evidence = %+v, want %+v", evidence, expected)
	}

	rpcEvidence := callRPC(t, server.URL, "chain_finalityEvidence", []any{})
	items, ok := rpcEvidence.([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("rpc evidence = %#v", rpcEvidence)
	}
	item, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("rpc evidence item = %#v", items[0])
	}
	if item["validator"] != expected.Validator || item["second_block_hash"] != expected.SecondBlockHash {
		t.Fatalf("rpc evidence item = %#v, want %+v", item, expected)
	}
}

func TestRPCExposesTxPoolAndPendingNonce(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	tx := signedTransfer(t, key, alice, bob, 0, 100)
	if err := n.SubmitTx(tx); err != nil {
		t.Fatal(err)
	}

	assertRPCResult(t, server.URL, "eth_getTransactionCount", []any{alice, "latest"}, "0x0")
	assertRPCResult(t, server.URL, "eth_getTransactionCount", []any{alice, "pending"}, "0x1")
	status := callRPC(t, server.URL, "txpool_status", []any{})
	statusMap, ok := status.(map[string]any)
	if !ok {
		t.Fatalf("txpool_status type = %T", status)
	}
	if statusMap["pending"] != "0x1" || statusMap["queued"] != "0x0" {
		t.Fatalf("txpool_status = %#v", statusMap)
	}

	content := callRPC(t, server.URL, "txpool_content", []any{})
	contentMap, ok := content.(map[string]any)
	if !ok {
		t.Fatalf("txpool_content type = %T", content)
	}
	pending, ok := contentMap["pending"].(map[string]any)
	if !ok {
		t.Fatalf("pending content = %#v", contentMap["pending"])
	}
	byNonce, ok := pending[strings.ToLower(alice)].(map[string]any)
	if !ok {
		t.Fatalf("pending sender content = %#v", pending)
	}
	txMap, ok := byNonce["0x0"].(map[string]any)
	if !ok {
		t.Fatalf("pending nonce content = %#v", byNonce)
	}
	if txMap["hash"] != tx.Hash() || txMap["blockHash"] != nil || txMap["blockNumber"] != nil {
		t.Fatalf("pending tx = %#v", txMap)
	}
}

func TestRPCSendRawTransactionSubmitsSignedTx(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	tx := signedTransfer(t, key, alice, bob, 0, 100)
	raw, err := types.EncodeRawTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	result := callRPC(t, server.URL, "eth_sendRawTransaction", []any{raw})
	if result != tx.Hash() {
		t.Fatalf("raw tx hash = %v, want %s", result, tx.Hash())
	}
	pool := n.TxPool()
	if pool.PendingCount != 1 || pool.Pending[0].Hash() != tx.Hash() {
		t.Fatalf("txpool = %+v", pool)
	}

	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	if got := n.Account(bob).Balance; got != 100 {
		t.Fatalf("bob balance = %d", got)
	}
}

func TestRESTRawTransactionSubmitsSignedTx(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	tx := signedTransfer(t, key, alice, bob, 0, 100)
	raw, err := types.EncodeRawTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]string{"raw": raw})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(server.URL+"/tx/raw", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("raw tx status = %d", resp.StatusCode)
	}
	var result struct {
		Hash string `json:"hash"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Hash != tx.Hash() {
		t.Fatalf("raw tx hash = %q, want %q", result.Hash, tx.Hash())
	}
}

func TestRESTFaucetRequestsFundRecipientThroughMempool(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := chaincrypto.AddressFromPrivateKey(key)
	recipient := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{proposer: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	raw, err := json.Marshal(map[string]any{"address": recipient, "amount": 125})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(server.URL+"/faucet", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("faucet status = %d", resp.StatusCode)
	}
	var result struct {
		Status string `json:"status"`
		Hash   string `json:"hash"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if result.Status != "accepted" || result.Hash == "" {
		t.Fatalf("faucet response = %+v", result)
	}
	pool := n.TxPool()
	if pool.PendingCount != 1 || pool.Pending[0].Hash() != result.Hash {
		t.Fatalf("txpool = %+v, hash = %s", pool, result.Hash)
	}

	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	if got := n.Account(recipient).Balance; got != 125 {
		t.Fatalf("recipient balance = %d", got)
	}
}

func TestRPCFaucetRequestsFundRecipientThroughMempool(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := chaincrypto.AddressFromPrivateKey(key)
	recipient := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{proposer: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	result := callRPC(t, server.URL, "chain_faucet", []any{map[string]any{
		"address": recipient,
		"amount":  75,
	}})
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("faucet result type = %T", result)
	}
	hash, ok := resultMap["hash"].(string)
	if !ok || hash == "" {
		t.Fatalf("faucet result = %#v", resultMap)
	}
	pool := n.TxPool()
	if pool.PendingCount != 1 || pool.Pending[0].Hash() != hash {
		t.Fatalf("txpool = %+v, hash = %s", pool, hash)
	}

	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	if got := n.Account(recipient).Balance; got != 75 {
		t.Fatalf("recipient balance = %d", got)
	}
}

func TestExplorerRendersChainOverview(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	committed := signedTransfer(t, key, alice, bob, 0, 100)
	if err := n.SubmitTx(committed); err != nil {
		t.Fatal(err)
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	pending, err := n.RequestFaucet("0xcccccccccccccccccccccccccccccccccccccccc", 25)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	resp, err := http.Get(server.URL + "/explorer")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("explorer status = %d", resp.StatusCode)
	}
	if contentType := resp.Header.Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Fatalf("content type = %q", contentType)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, expected := range []string{
		"ChainLab Explorer",
		"chainlab-local",
		"Head Height",
		"1",
		block.Hash(),
		committed.Hash(),
		pending.Hash(),
		alice,
		"Pending Transactions",
		"Finalized Height",
		"Validators",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("explorer body missing %q:\n%s", expected, body)
		}
	}
}

func TestExplorerRendersDetailPages(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	tx := signedTransfer(t, key, alice, bob, 0, 100)
	if err := n.SubmitTx(tx); err != nil {
		t.Fatal(err)
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	overview := getHTML(t, server.URL+"/explorer", http.StatusOK)
	for _, href := range []string{
		`href="/explorer/block/1"`,
		`href="/explorer/tx/` + tx.Hash() + `"`,
		`href="/explorer/account/` + alice + `"`,
		`href="/explorer/account/` + bob + `"`,
	} {
		if !strings.Contains(overview, href) {
			t.Fatalf("overview missing link %s:\n%s", href, overview)
		}
	}

	blockPage := getHTML(t, server.URL+"/explorer/block/1", http.StatusOK)
	for _, expected := range []string{"Block Details", block.Hash(), tx.Hash(), alice, bob, "State Root"} {
		if !strings.Contains(blockPage, expected) {
			t.Fatalf("block page missing %q:\n%s", expected, blockPage)
		}
	}

	txPage := getHTML(t, server.URL+"/explorer/tx/"+tx.Hash(), http.StatusOK)
	for _, expected := range []string{"Transaction Details", tx.Hash(), "Receipt", "success", alice, bob, "100"} {
		if !strings.Contains(txPage, expected) {
			t.Fatalf("tx page missing %q:\n%s", expected, txPage)
		}
	}

	accountPage := getHTML(t, server.URL+"/explorer/account/"+bob, http.StatusOK)
	for _, expected := range []string{"Account Details", bob, "Balance", "100", "Nonce"} {
		if !strings.Contains(accountPage, expected) {
			t.Fatalf("account page missing %q:\n%s", expected, accountPage)
		}
	}

	getHTML(t, server.URL+"/explorer/block/99", http.StatusNotFound)
	getHTML(t, server.URL+"/explorer/tx/0xmissing", http.StatusNotFound)
}

func TestExplorerRendersEventPages(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	deploy := signedRPCTransaction(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     alice,
		Nonce:    0,
		GasLimit: 80_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": "counter.v1",
			"initial": "1",
		},
	})
	if err := n.SubmitTx(deploy); err != nil {
		t.Fatal(err)
	}
	deployBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	counter := deployBlock.Receipts[0].ContractAddress

	call := signedRPCTransaction(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     alice,
		To:       counter,
		Nonce:    1,
		GasLimit: 50_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method": "increment",
			"amount": "2",
		},
	})
	if err := n.SubmitTx(call); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	overview := getHTML(t, server.URL+"/explorer", http.StatusOK)
	if !strings.Contains(overview, `href="/explorer/events"`) {
		t.Fatalf("overview missing events link:\n%s", overview)
	}

	eventsPage := getHTML(t, server.URL+"/explorer/events", http.StatusOK)
	for _, expected := range []string{
		"Contract Events",
		"counter.initialized",
		"counter.incremented",
		hash.KeccakHex([]byte("counter.incremented")),
		counter,
		`href="/explorer/block/2"`,
		`href="/explorer/tx/` + call.Hash() + `"`,
		`href="/explorer/account/` + counter + `"`,
		"count",
		"3",
	} {
		if !strings.Contains(eventsPage, expected) {
			t.Fatalf("events page missing %q:\n%s", expected, eventsPage)
		}
	}
}

func TestEthGetLogsFiltersContractEvents(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	deploy := signedRPCTransaction(t, key, types.Transaction{
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
	if err := n.SubmitTx(deploy); err != nil {
		t.Fatal(err)
	}
	deployBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	counter := deployBlock.Receipts[0].ContractAddress

	call := signedRPCTransaction(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     alice,
		To:       counter,
		Nonce:    1,
		GasLimit: 50_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method": "increment",
			"amount": "2",
		},
	})
	if err := n.SubmitTx(call); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	topic := hash.KeccakHex([]byte("counter.incremented"))
	result := callRPC(t, server.URL, "eth_getLogs", []any{map[string]any{
		"fromBlock": "0x1",
		"toBlock":   "latest",
		"address":   counter,
		"topics":    []any{topic},
	}})
	logs, ok := result.([]any)
	if !ok {
		t.Fatalf("logs result type = %T", result)
	}
	if len(logs) != 1 {
		t.Fatalf("log count = %d", len(logs))
	}
	logMap, ok := logs[0].(map[string]any)
	if !ok {
		t.Fatalf("log type = %T", logs[0])
	}
	if logMap["address"] != counter {
		t.Fatalf("log address = %v", logMap["address"])
	}
	if logMap["blockNumber"] != "0x2" {
		t.Fatalf("log block number = %v", logMap["blockNumber"])
	}
	if logMap["transactionHash"] != call.Hash() {
		t.Fatalf("log tx hash = %v", logMap["transactionHash"])
	}
	if logMap["transactionIndex"] != "0x0" || logMap["logIndex"] != "0x0" {
		t.Fatalf("log indexes = tx %v log %v", logMap["transactionIndex"], logMap["logIndex"])
	}
	topics, ok := logMap["topics"].([]any)
	if !ok || len(topics) != 1 || topics[0] != topic {
		t.Fatalf("log topics = %#v", logMap["topics"])
	}
	data, ok := logMap["data"].(string)
	if !ok || !strings.HasPrefix(data, "0x") || len(data) <= 2 {
		t.Fatalf("log data = %#v", logMap["data"])
	}

	receiptResult := callRPC(t, server.URL, "eth_getTransactionReceipt", []any{call.Hash()})
	receipt, ok := receiptResult.(map[string]any)
	if !ok {
		t.Fatalf("receipt type = %T", receiptResult)
	}
	receiptLogs, ok := receipt["logs"].([]any)
	if !ok || len(receiptLogs) != 1 {
		t.Fatalf("receipt logs = %#v", receipt["logs"])
	}
	receiptLog, ok := receiptLogs[0].(map[string]any)
	if !ok {
		t.Fatalf("receipt log type = %T", receiptLogs[0])
	}
	if receiptLog["address"] != counter || receiptLog["transactionHash"] != call.Hash() || receiptLog["blockNumber"] != "0x2" {
		t.Fatalf("receipt log = %#v", receiptLog)
	}

	empty := callRPC(t, server.URL, "eth_getLogs", []any{map[string]any{
		"fromBlock": "0x1",
		"toBlock":   "latest",
		"address":   "0xcccccccccccccccccccccccccccccccccccccccc",
		"topics":    []any{topic},
	}})
	if logs, ok := empty.([]any); !ok || len(logs) != 0 {
		t.Fatalf("filtered logs = %#v", empty)
	}
}

func TestEthLogFilterTracksIncrementalChanges(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	deploy := signedRPCTransaction(t, key, types.Transaction{
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
	if err := n.SubmitTx(deploy); err != nil {
		t.Fatal(err)
	}
	deployBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	counter := deployBlock.Receipts[0].ContractAddress
	topic := hash.KeccakHex([]byte("counter.incremented"))

	filterID, ok := callRPC(t, server.URL, "eth_newFilter", []any{map[string]any{
		"fromBlock": "0x2",
		"address":   counter,
		"topics":    []any{topic},
	}}).(string)
	if !ok || !strings.HasPrefix(filterID, "0x") {
		t.Fatalf("filter id = %#v", filterID)
	}

	firstCall := signedRPCTransaction(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     alice,
		To:       counter,
		Nonce:    1,
		GasLimit: 50_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method": "increment",
			"amount": "2",
		},
	})
	if err := n.SubmitTx(firstCall); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	changes := callRPC(t, server.URL, "eth_getFilterChanges", []any{filterID})
	changeLogs, ok := changes.([]any)
	if !ok || len(changeLogs) != 1 {
		t.Fatalf("first filter changes = %#v", changes)
	}
	firstLog, ok := changeLogs[0].(map[string]any)
	if !ok || firstLog["transactionHash"] != firstCall.Hash() || firstLog["blockNumber"] != "0x2" {
		t.Fatalf("first filter log = %#v", changeLogs[0])
	}

	if empty := callRPC(t, server.URL, "eth_getFilterChanges", []any{filterID}); len(empty.([]any)) != 0 {
		t.Fatalf("second filter changes = %#v", empty)
	}

	secondCall := signedRPCTransaction(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     alice,
		To:       counter,
		Nonce:    2,
		GasLimit: 50_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method": "increment",
			"amount": "1",
		},
	})
	if err := n.SubmitTx(secondCall); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	allLogs := callRPC(t, server.URL, "eth_getFilterLogs", []any{filterID})
	all, ok := allLogs.([]any)
	if !ok || len(all) != 2 {
		t.Fatalf("filter logs = %#v", allLogs)
	}
	secondChanges := callRPC(t, server.URL, "eth_getFilterChanges", []any{filterID})
	secondChangeLogs, ok := secondChanges.([]any)
	if !ok || len(secondChangeLogs) != 1 {
		t.Fatalf("third filter changes = %#v", secondChanges)
	}
	secondLog, ok := secondChangeLogs[0].(map[string]any)
	if !ok || secondLog["transactionHash"] != secondCall.Hash() || secondLog["blockNumber"] != "0x3" {
		t.Fatalf("second filter log = %#v", secondChangeLogs[0])
	}

	if uninstalled := callRPC(t, server.URL, "eth_uninstallFilter", []any{filterID}); uninstalled != true {
		t.Fatalf("uninstall filter = %#v", uninstalled)
	}
	if uninstalled := callRPC(t, server.URL, "eth_uninstallFilter", []any{filterID}); uninstalled != false {
		t.Fatalf("second uninstall filter = %#v", uninstalled)
	}
}

func TestEthCallAndEstimateGas(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	n, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: map[string]uint64{alice: 1_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	deploy := signedRPCTransaction(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     alice,
		Nonce:    0,
		GasLimit: 80_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": "counter.v1",
			"initial": "1",
		},
	})
	if err := n.SubmitTx(deploy); err != nil {
		t.Fatal(err)
	}
	deployBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	counter := deployBlock.Receipts[0].ContractAddress

	increment := signedRPCTransaction(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     alice,
		To:       counter,
		Nonce:    1,
		GasLimit: 50_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method": "increment",
			"amount": "2",
		},
	})
	if err := n.SubmitTx(increment); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	result := callRPC(t, server.URL, "eth_call", []any{map[string]any{
		"from": alice,
		"to":   counter,
		"payload": map[string]any{
			"method": "get",
		},
	}, "latest"})
	if result != abiUint256Hex(3) {
		t.Fatalf("eth_call result = %v", result)
	}
	resultByCalldata := callRPC(t, server.URL, "eth_call", []any{map[string]any{
		"from": alice,
		"to":   counter,
		"data": abiCallData("get()"),
	}, "latest"})
	if resultByCalldata != abiUint256Hex(3) {
		t.Fatalf("eth_call calldata result = %v", resultByCalldata)
	}

	tokenDeploy := signedRPCTransaction(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxDeploy,
		From:     alice,
		Nonce:    2,
		GasLimit: 80_000,
		GasPrice: 1,
		Payload: map[string]string{
			"code_id": "token.v1",
			"symbol":  "LAB",
		},
	})
	if err := n.SubmitTx(tokenDeploy); err != nil {
		t.Fatal(err)
	}
	tokenBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	token := tokenBlock.Receipts[0].ContractAddress
	symbol := callRPC(t, server.URL, "eth_call", []any{map[string]any{
		"from": alice,
		"to":   token,
		"payload": map[string]any{
			"method": "symbol",
		},
	}, "latest"})
	if symbol != abiStringHex("LAB") {
		t.Fatalf("eth_call symbol result = %v", symbol)
	}
	symbolByCalldata := callRPC(t, server.URL, "eth_call", []any{map[string]any{
		"from": alice,
		"to":   token,
		"data": abiCallData("symbol()"),
	}, "latest"})
	if symbolByCalldata != abiStringHex("LAB") {
		t.Fatalf("eth_call calldata symbol result = %v", symbolByCalldata)
	}
	owner := callRPC(t, server.URL, "eth_call", []any{map[string]any{
		"from": alice,
		"to":   token,
		"payload": map[string]any{
			"method": "owner",
		},
	}, "latest"})
	if owner != abiAddressHex(alice) {
		t.Fatalf("eth_call owner result = %v", owner)
	}
	ownerByCalldata := callRPC(t, server.URL, "eth_call", []any{map[string]any{
		"from": alice,
		"to":   token,
		"data": abiCallData("owner()"),
	}, "latest"})
	if ownerByCalldata != abiAddressHex(alice) {
		t.Fatalf("eth_call calldata owner result = %v", ownerByCalldata)
	}
	balanceByCalldata := callRPC(t, server.URL, "eth_call", []any{map[string]any{
		"from": alice,
		"to":   token,
		"data": abiCallData("balanceOf(address)", abiAddressArgument(alice)),
	}, "latest"})
	if balanceByCalldata != abiUint256Hex(0) {
		t.Fatalf("eth_call calldata balance result = %v", balanceByCalldata)
	}

	estimateCall := callRPC(t, server.URL, "eth_estimateGas", []any{map[string]any{
		"from": alice,
		"to":   counter,
		"payload": map[string]any{
			"method": "get",
		},
	}})
	if estimateCall != "0xc350" {
		t.Fatalf("call gas estimate = %v", estimateCall)
	}

	estimateTransfer := callRPC(t, server.URL, "eth_estimateGas", []any{map[string]any{
		"type":  "transfer",
		"from":  alice,
		"to":    bob,
		"value": "0x64",
	}})
	if estimateTransfer != "0x5208" {
		t.Fatalf("transfer gas estimate = %v", estimateTransfer)
	}

	wasmBytecode := contracts.WasmEchoCode()
	estimateUpload := callRPC(t, server.URL, "eth_estimateGas", []any{map[string]any{
		"type": string(types.TxWASMUpload),
		"from": alice,
		"payload": map[string]any{
			"bytecode": "0x" + hex.EncodeToString(wasmBytecode),
		},
	}})
	wantUploadGas := fmt.Sprintf("0x%x", core.EstimateWASMUploadGas(wasmBytecode))
	if estimateUpload != wantUploadGas {
		t.Fatalf("wasm upload gas estimate = %v, want %s", estimateUpload, wantUploadGas)
	}
}

func TestRPCBroadcastsTransactionsToPeers(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	genesisBalances := map[string]uint64{alice: 1_000_000}
	peerNode, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: genesisBalances,
		Validators:     []string{alice},
	})
	if err != nil {
		t.Fatal(err)
	}
	peerServer := httptest.NewServer(chainrpc.NewServer(peerNode))
	defer peerServer.Close()
	localNode, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: genesisBalances,
	})
	if err != nil {
		t.Fatal(err)
	}
	localServer := httptest.NewServer(chainrpc.NewServerWithPeers(localNode, []string{peerServer.URL}))
	defer localServer.Close()

	tx := signedTransfer(t, key, alice, bob, 0, 100)
	body, _ := json.Marshal(tx)
	resp, err := http.Post(localServer.URL+"/tx", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("tx status = %d", resp.StatusCode)
	}

	block, err := peerNode.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("peer block should include broadcast transaction, got %d txs", len(block.Transactions))
	}
	if got := peerNode.Account(bob).Balance; got != 100 {
		t.Fatalf("peer bob balance = %d", got)
	}
}

func hexData(value string) string {
	return "0x" + hex.EncodeToString([]byte(value))
}

func abiUint256Hex(value uint64) string {
	return fmt.Sprintf("0x%064x", value)
}

func abiStringHex(value string) string {
	raw := hex.EncodeToString([]byte(value))
	padding := strings.Repeat("0", (64-len(raw)%64)%64)
	return "0x" + fmt.Sprintf("%064x%064x", 32, len([]byte(value))) + raw + padding
}

func abiAddressHex(value string) string {
	return "0x" + strings.Repeat("0", 24) + strings.ToLower(strings.TrimPrefix(value, "0x"))
}

func abiCallData(signature string, args ...string) string {
	selector := hash.Keccak([]byte(signature))[:4]
	return "0x" + hex.EncodeToString(selector) + strings.Join(args, "")
}

func abiAddressArgument(value string) string {
	return strings.Repeat("0", 24) + strings.ToLower(strings.TrimPrefix(value, "0x"))
}

func getHTML(t *testing.T, url string, expectedStatus int) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != expectedStatus {
		t.Fatalf("%s status = %d, want %d:\n%s", url, resp.StatusCode, expectedStatus, string(raw))
	}
	if expectedStatus == http.StatusOK && !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("%s content type = %q", url, resp.Header.Get("Content-Type"))
	}
	return string(raw)
}

func TestRPCBroadcastsProducedBlocksToPeers(t *testing.T) {
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := chaincrypto.AddressFromPrivateKey(key)
	bob := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	genesisBalances := map[string]uint64{alice: 1_000_000}
	peerNode, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: genesisBalances,
		Validators:     []string{alice},
	})
	if err != nil {
		t.Fatal(err)
	}
	peerServer := httptest.NewServer(chainrpc.NewServer(peerNode))
	defer peerServer.Close()
	localNode, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    key,
		GenesisBalance: genesisBalances,
	})
	if err != nil {
		t.Fatal(err)
	}
	localServer := httptest.NewServer(chainrpc.NewServerWithPeers(localNode, []string{peerServer.URL}))
	defer localServer.Close()

	tx := signedTransfer(t, key, alice, bob, 0, 100)
	if err := localNode.SubmitTx(tx); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(localServer.URL+"/chain/produce", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("produce status = %d", resp.StatusCode)
	}

	if got := peerNode.Account(bob).Balance; got != 100 {
		t.Fatalf("peer bob balance after block broadcast = %d", got)
	}
	if peerNode.Head().Header.Height != 1 {
		t.Fatalf("peer height = %d", peerNode.Head().Header.Height)
	}
}

func TestRPCBroadcastsFinalityVotesToPeers(t *testing.T) {
	keyA, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyC, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorA := chaincrypto.AddressFromPrivateKey(keyA)
	validatorB := chaincrypto.AddressFromPrivateKey(keyB)
	validatorC := chaincrypto.AddressFromPrivateKey(keyC)
	validators := []string{validatorA, validatorB, validatorC}
	genesisBalances := map[string]uint64{validatorA: 1_000_000}
	peerNode, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyB,
		GenesisBalance: genesisBalances,
		Validators:     validators,
	})
	if err != nil {
		t.Fatal(err)
	}
	peerServer := httptest.NewServer(chainrpc.NewServer(peerNode))
	defer peerServer.Close()
	localNode, err := node.New(node.Config{
		ChainID:        "chainlab-local",
		ProposerKey:    keyA,
		GenesisBalance: genesisBalances,
		Validators:     validators,
	})
	if err != nil {
		t.Fatal(err)
	}
	localServer := httptest.NewServer(chainrpc.NewServerWithPeers(localNode, []string{peerServer.URL}))
	defer localServer.Close()

	resp, err := http.Post(localServer.URL+"/chain/produce", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("produce status = %d", resp.StatusCode)
	}
	block, ok := peerNode.Block(1)
	if !ok {
		t.Fatal("peer should import produced block before votes")
	}

	for _, key := range []chaincrypto.PrivateKey{keyA, keyB, keyC} {
		vote, err := consensus.SignFinalityVote(key, block)
		if err != nil {
			t.Fatal(err)
		}
		callRPC(t, localServer.URL, "chain_sendFinalityVote", []any{vote})
	}

	peerFinality := peerNode.Finality()
	if peerFinality.FinalizedSource != "bft_certificate" || peerFinality.CertifiedSigners != 3 {
		t.Fatalf("peer finality after gossiped votes = %+v", peerFinality)
	}
	certifiedBlock, ok := peerNode.Block(1)
	if !ok || certifiedBlock.FinalityCertificate == nil {
		t.Fatalf("peer certified block = %+v ok=%v", certifiedBlock, ok)
	}
}

func signedTransfer(t *testing.T, key chaincrypto.PrivateKey, from string, to string, nonce uint64, value uint64) types.Transaction {
	t.Helper()
	tx := types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxTransfer,
		From:     from,
		To:       to,
		Nonce:    nonce,
		Value:    value,
		GasLimit: 21_000,
		GasPrice: 1,
	}
	sig, err := chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = sig
	return tx
}

func signedRPCTransaction(t *testing.T, key chaincrypto.PrivateKey, tx types.Transaction) types.Transaction {
	t.Helper()
	sig, err := chaincrypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = sig
	return tx
}

func sponsoredRPCTransaction(t *testing.T, userKey chaincrypto.PrivateKey, paymasterKey chaincrypto.PrivateKey, tx types.Transaction) types.Transaction {
	t.Helper()
	tx = signedRPCTransaction(t, userKey, tx)
	signature, err := chaincrypto.Sign(paymasterKey, tx.PaymasterSigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.PaymasterSignature = signature
	return tx
}

func authorizedRPCTransaction(t *testing.T, keys []chaincrypto.PrivateKey, tx types.Transaction) types.Transaction {
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

func seedFinalityEquivocationEvidence(t *testing.T, n *node.Node, keyA chaincrypto.PrivateKey, keyB chaincrypto.PrivateKey, validatorA string, validatorB string) types.FinalityEquivocationEvidence {
	t.Helper()
	blockA1, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	voteA1, err := consensus.SignFinalityVote(keyA, blockA1)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.SubmitFinalityVote(voteA1); err != nil {
		t.Fatal(err)
	}
	genesis, ok := n.Block(0)
	if !ok {
		t.Fatal("genesis should exist")
	}
	blockB1 := signedEmptyRPCBlock(t, keyA, genesis, validatorA, 1, blockA1.Header.TimeUnix+10)
	blockB2 := signedEmptyRPCBlock(t, keyB, blockB1, validatorB, 2, blockA1.Header.TimeUnix+20)
	if err := n.ImportBlock(blockB1); err != nil {
		t.Fatal(err)
	}
	if err := n.ImportBlock(blockB2); err != nil {
		t.Fatal(err)
	}
	voteB1, err := consensus.SignFinalityVote(keyA, blockB1)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.SubmitFinalityVote(voteB1); err == nil {
		t.Fatal("conflicting finality vote should be rejected")
	}
	evidence := n.FinalityEvidence()
	if len(evidence) != 1 {
		t.Fatalf("evidence = %+v", evidence)
	}
	return evidence[0]
}

func signedEmptyRPCBlock(t *testing.T, key chaincrypto.PrivateKey, parent types.Block, proposer string, height uint64, timeUnix int64) types.Block {
	t.Helper()
	block := types.Block{
		Header: types.BlockHeader{
			ChainID:       parent.Header.ChainID,
			Height:        height,
			ParentHash:    parent.Hash(),
			TimeUnix:      timeUnix,
			Proposer:      proposer,
			GasLimit:      node.DefaultBlockGasLimit,
			GasUsed:       0,
			BaseFeePerGas: node.NextBaseFee(parent, node.DefaultBlockGasLimit),
			TxRoot:        types.TransactionRoot(nil),
			ReceiptRoot:   types.ReceiptRoot(nil),
			StateRoot:     parent.Header.StateRoot,
		},
	}
	if err := consensus.SignBlock(key, &block); err != nil {
		t.Fatal(err)
	}
	return block
}

func assertRPCResult(t *testing.T, url string, method string, params []any, expected any) {
	t.Helper()
	if got := callRPC(t, url, method, params); got != expected {
		t.Fatalf("%s result = %v, want %v", method, got, expected)
	}
}

func callRPC(t *testing.T, url string, method string, params []any) any {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(url+"/rpc", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s status = %d", method, resp.StatusCode)
	}
	var rpcResp struct {
		Result any    `json:"result"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		t.Fatal(err)
	}
	if rpcResp.Error != "" {
		t.Fatalf("%s error = %s", method, rpcResp.Error)
	}
	return rpcResp.Result
}
