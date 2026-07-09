package rpc_test

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	if result != hexData("3") {
		t.Fatalf("eth_call result = %v", result)
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
