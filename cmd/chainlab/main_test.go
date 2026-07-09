package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"chainlab/internal/crypto"
	"chainlab/internal/hash"
	"chainlab/internal/node"
	chainrpc "chainlab/internal/rpc"
	"chainlab/internal/types"
)

func TestWriteAndReadGenesisFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "genesis.json")
	genesis, err := createGenesisFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if genesis.ChainID != "chainlab-local" {
		t.Fatalf("chain id = %q", genesis.ChainID)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fromDisk GenesisFile
	if err := json.Unmarshal(raw, &fromDisk); err != nil {
		t.Fatal(err)
	}
	if fromDisk.Proposer == "" || fromDisk.PrivateKey == "" {
		t.Fatalf("genesis missing proposer credentials: %+v", fromDisk)
	}
	var rawGenesis map[string]any
	if err := json.Unmarshal(raw, &rawGenesis); err != nil {
		t.Fatal(err)
	}
	validators, ok := rawGenesis["validators"].([]any)
	if !ok || len(validators) != 1 || validators[0] != fromDisk.Proposer {
		t.Fatalf("genesis validators = %#v", rawGenesis["validators"])
	}

	loaded, err := readGenesisFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Proposer != fromDisk.Proposer {
		t.Fatalf("loaded proposer = %q", loaded.Proposer)
	}
}

func TestBuildNodeConfigUsesDataDir(t *testing.T) {
	key, err := createGenesisFile(filepath.Join(t.TempDir(), "genesis.json"))
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	config, err := buildNodeConfig(nodeOptions{
		GenesisPath: keyPath(t, key),
		DataDir:     dataDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	if config.DataDir != dataDir {
		t.Fatalf("data dir = %q", config.DataDir)
	}
	if config.ChainID != "chainlab-local" {
		t.Fatalf("chain id = %q", config.ChainID)
	}
}

func TestBuildNodeConfigUsesGenesisValidators(t *testing.T) {
	keyA, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorA := crypto.AddressFromPrivateKey(keyA)
	validatorB := crypto.AddressFromPrivateKey(keyB)
	path := filepath.Join(t.TempDir(), "genesis.json")
	raw := []byte(`{
		"chain_id": "chainlab-local",
		"private_key": "` + crypto.PrivateKeyToHex(keyA) + `",
		"proposer": "` + validatorA + `",
		"validators": ["` + validatorA + `", "` + validatorB + `"],
		"balances": {"` + validatorA + `": 1000000}
	}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	config, err := buildNodeConfig(nodeOptions{GenesisPath: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(config.Validators) != 2 {
		t.Fatalf("validator count = %d", len(config.Validators))
	}
	if config.Validators[0] != validatorA || config.Validators[1] != validatorB {
		t.Fatalf("validators = %#v", config.Validators)
	}
}

func TestBuildNodeConfigPrivateKeyFlagOverridesGenesisKey(t *testing.T) {
	keyA, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	keyB, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorA := crypto.AddressFromPrivateKey(keyA)
	validatorB := crypto.AddressFromPrivateKey(keyB)
	path := filepath.Join(t.TempDir(), "genesis.json")
	raw := []byte(`{
		"chain_id": "chainlab-local",
		"private_key": "` + crypto.PrivateKeyToHex(keyA) + `",
		"proposer": "` + validatorA + `",
		"validators": ["` + validatorA + `", "` + validatorB + `"],
		"balances": {"` + validatorA + `": 1000000}
	}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	config, err := buildNodeConfig(nodeOptions{
		GenesisPath:   path,
		PrivateKeyHex: crypto.PrivateKeyToHex(keyB),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := crypto.AddressFromPrivateKey(config.ProposerKey); got != validatorB {
		t.Fatalf("proposer = %q", got)
	}
	if len(config.Validators) != 2 {
		t.Fatalf("validator count = %d", len(config.Validators))
	}
}

func keyPath(t *testing.T, genesis GenesisFile) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "genesis.json")
	raw, err := json.Marshal(genesis)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPeerListFlagAcceptsRepeatedPeers(t *testing.T) {
	flags := flag.NewFlagSet("node", flag.ContinueOnError)
	var peers peerListFlag
	flags.Var(&peers, "peer", "peer URL")

	if err := flags.Parse([]string{"--peer", "http://127.0.0.1:18547", "--peer", "http://127.0.0.1:18548"}); err != nil {
		t.Fatal(err)
	}
	got := peers.Values()
	if len(got) != 2 {
		t.Fatalf("peer count = %d", len(got))
	}
	if got[0] != "http://127.0.0.1:18547" || got[1] != "http://127.0.0.1:18548" {
		t.Fatalf("peers = %#v", got)
	}
}

func TestBuildSignedTransferFetchesNonceFromRPC(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
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

	tx, err := buildSignedTransfer(server.URL, crypto.PrivateKeyToHex(key), bob, 100, 21_000, 1)
	if err != nil {
		t.Fatal(err)
	}
	if tx.From != alice {
		t.Fatalf("from = %q", tx.From)
	}
	if tx.Nonce != 0 {
		t.Fatalf("nonce = %d", tx.Nonce)
	}
	if tx.Signature == "" {
		t.Fatal("transaction should be signed")
	}
	if !crypto.Verify(alice, tx.SigningBytes(), tx.Signature) {
		t.Fatal("signature should verify")
	}
}

func TestSubmitTransferCommandSendsSignedTx(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
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

	var out bytes.Buffer
	if err := transferCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--to", bob,
		"--value", "100",
	}, &out); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(out.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Hash == "" {
		t.Fatal("transfer command should print transaction hash")
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 1 {
		t.Fatalf("block transactions = %d", len(block.Transactions))
	}
	if got := n.Account(bob).Balance; got != 100 {
		t.Fatalf("bob balance = %d", got)
	}
}

func TestQueryAccountAndProduceCommands(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
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

	var accountOut bytes.Buffer
	if err := accountCommand([]string{"--rpc", server.URL, "--address", alice}, &accountOut); err != nil {
		t.Fatal(err)
	}
	var account types.Account
	if err := json.Unmarshal(accountOut.Bytes(), &account); err != nil {
		t.Fatal(err)
	}
	if account.Balance != 1_000_000 {
		t.Fatalf("balance = %d", account.Balance)
	}

	var blockOut bytes.Buffer
	if err := produceCommand([]string{"--rpc", server.URL}, &blockOut); err != nil {
		t.Fatal(err)
	}
	var block types.Block
	if err := json.Unmarshal(blockOut.Bytes(), &block); err != nil {
		t.Fatal(err)
	}
	if block.Header.Height != 1 {
		t.Fatalf("produced height = %d", block.Header.Height)
	}
}

func TestQueryLogsCommand(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	alice := crypto.AddressFromPrivateKey(key)
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

	deploy := signedCLITx(t, key, types.Transaction{
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

	call := signedCLITx(t, key, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxCall,
		From:     alice,
		To:       counter,
		Nonce:    1,
		GasLimit: 50_000,
		GasPrice: 1,
		Payload: map[string]string{
			"method": "increment",
			"amount": "4",
		},
	})
	if err := n.SubmitTx(call); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	topic := hash.KeccakHex([]byte("counter.incremented"))
	if err := logsCommand([]string{
		"--rpc", server.URL,
		"--from-block", "0x1",
		"--to-block", "latest",
		"--address", counter,
		"--topic", topic,
	}, &out); err != nil {
		t.Fatal(err)
	}
	var logs []map[string]any
	if err := json.Unmarshal(out.Bytes(), &logs); err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("log count = %d", len(logs))
	}
	if logs[0]["address"] != counter || logs[0]["transactionHash"] != call.Hash() {
		t.Fatalf("log = %#v", logs[0])
	}
}

func TestRPCJSONCall(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request["method"] != "eth_blockNumber" {
			t.Fatalf("method = %v", request["method"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": request["id"], "result": "0x2"})
	})
	server := httptest.NewServer(handler)
	defer server.Close()

	var result string
	if err := rpcCall(server.URL, "eth_blockNumber", []any{}, &result); err != nil {
		t.Fatal(err)
	}
	if result != "0x2" {
		t.Fatalf("result = %q", result)
	}
}

func signedCLITx(t *testing.T, key crypto.PrivateKey, tx types.Transaction) types.Transaction {
	t.Helper()
	signature, err := crypto.Sign(key, tx.SigningBytes())
	if err != nil {
		t.Fatal(err)
	}
	tx.Signature = signature
	return tx
}
