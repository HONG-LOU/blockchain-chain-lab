package rpc_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	chaincrypto "chainlab/internal/crypto"
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
