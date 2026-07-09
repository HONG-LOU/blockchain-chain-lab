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

	"chainlab/internal/contracts"
	"chainlab/internal/core"
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

func TestTransferCommandSupportsEIP1559FeeCaps(t *testing.T) {
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
		"--max-fee-per-gas", "5",
		"--max-priority-fee-per-gas", "2",
	}, &out); err != nil {
		t.Fatal(err)
	}
	pool := n.TxPool()
	if pool.PendingCount != 1 {
		t.Fatalf("txpool = %+v", pool)
	}
	if pool.Pending[0].MaxFeePerGas != 5 || pool.Pending[0].MaxPriorityFeePerGas != 2 {
		t.Fatalf("fee caps = max %d priority %d", pool.Pending[0].MaxFeePerGas, pool.Pending[0].MaxPriorityFeePerGas)
	}
}

func TestTransferCommandUsesPendingNonceAndQueryMempool(t *testing.T) {
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

	for i := 0; i < 2; i++ {
		var out bytes.Buffer
		if err := transferCommand([]string{
			"--rpc", server.URL,
			"--private-key", crypto.PrivateKeyToHex(key),
			"--to", bob,
			"--value", "100",
		}, &out); err != nil {
			t.Fatal(err)
		}
	}

	var poolOut bytes.Buffer
	if err := mempoolCommand([]string{"--rpc", server.URL}, &poolOut); err != nil {
		t.Fatal(err)
	}
	var pool struct {
		PendingCount int                 `json:"pending_count"`
		QueuedCount  int                 `json:"queued_count"`
		Pending      []types.Transaction `json:"pending"`
	}
	if err := json.Unmarshal(poolOut.Bytes(), &pool); err != nil {
		t.Fatal(err)
	}
	if pool.PendingCount != 2 || pool.QueuedCount != 0 || len(pool.Pending) != 2 {
		t.Fatalf("mempool = %+v", pool)
	}

	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(block.Transactions) != 2 {
		t.Fatalf("block transactions = %d", len(block.Transactions))
	}
	if block.Transactions[0].Nonce != 0 || block.Transactions[1].Nonce != 1 {
		t.Fatalf("transaction nonces = %d, %d", block.Transactions[0].Nonce, block.Transactions[1].Nonce)
	}
}

func TestRawTransactionCommandsBuildAndSubmitRawTx(t *testing.T) {
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

	var rawOut bytes.Buffer
	if err := transferCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--to", bob,
		"--value", "100",
		"--raw-only",
	}, &rawOut); err != nil {
		t.Fatal(err)
	}
	var rawResult struct {
		Hash string `json:"hash"`
		Raw  string `json:"raw"`
	}
	if err := json.Unmarshal(rawOut.Bytes(), &rawResult); err != nil {
		t.Fatal(err)
	}
	if rawResult.Hash == "" || len(rawResult.Raw) <= 2 || rawResult.Raw[:2] != "0x" {
		t.Fatalf("raw result = %+v", rawResult)
	}
	if pool := n.TxPool(); pool.PendingCount != 0 {
		t.Fatalf("raw-only should not submit tx, pool = %+v", pool)
	}

	var submitOut bytes.Buffer
	if err := rawSubmitCommand([]string{"--rpc", server.URL, "--raw", rawResult.Raw}, &submitOut); err != nil {
		t.Fatal(err)
	}
	var submitResult struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(submitOut.Bytes(), &submitResult); err != nil {
		t.Fatal(err)
	}
	if submitResult.Hash != rawResult.Hash {
		t.Fatalf("submit hash = %q, want %q", submitResult.Hash, rawResult.Hash)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	if got := n.Account(bob).Balance; got != 100 {
		t.Fatalf("bob balance = %d", got)
	}
}

func TestFaucetRequestCommandRequestsFunds(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := crypto.AddressFromPrivateKey(key)
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

	var out bytes.Buffer
	if err := faucetRequestCommand([]string{
		"--rpc", server.URL,
		"--to", recipient,
		"--amount", "60",
	}, &out); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Hash == "" {
		t.Fatalf("faucet result = %+v", result)
	}
	pool := n.TxPool()
	if pool.PendingCount != 1 || pool.Pending[0].Hash() != result.Hash {
		t.Fatalf("txpool = %+v, hash = %s", pool, result.Hash)
	}

	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}
	if got := n.Account(recipient).Balance; got != 60 {
		t.Fatalf("recipient balance = %d", got)
	}
}

func TestStakeAndValidatorJoinCommandsSendSignedTx(t *testing.T) {
	proposerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := crypto.AddressFromPrivateKey(proposerKey)
	validator := crypto.AddressFromPrivateKey(validatorKey)
	n, err := node.New(node.Config{
		ChainID:     "chainlab-local",
		ProposerKey: proposerKey,
		GenesisBalance: map[string]uint64{
			proposer:  1_000_000,
			validator: 1_000_000,
		},
		Validators: []string{proposer},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var stakeOut bytes.Buffer
	if err := stakeCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(validatorKey),
		"--value", "500",
	}, &stakeOut); err != nil {
		t.Fatal(err)
	}
	stakeBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(stakeBlock.Transactions) != 1 || stakeBlock.Transactions[0].Type != types.TxStake {
		t.Fatalf("stake block transactions = %#v", stakeBlock.Transactions)
	}

	var joinOut bytes.Buffer
	if err := validatorJoinCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(validatorKey),
	}, &joinOut); err != nil {
		t.Fatal(err)
	}
	joinBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(joinBlock.Transactions) != 1 || joinBlock.Transactions[0].Type != types.TxValidatorJoin {
		t.Fatalf("join block transactions = %#v", joinBlock.Transactions)
	}
	if len(joinBlock.Receipts) != 1 || len(joinBlock.Receipts[0].Events) != 1 || joinBlock.Receipts[0].Events[0].Type != "validator.joined" {
		t.Fatalf("join receipt = %#v", joinBlock.Receipts)
	}
}

func TestGovernanceProposalCommandsSubmitVoteExecuteAndQuery(t *testing.T) {
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

	var stakeOut bytes.Buffer
	if err := stakeCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--value", "500",
	}, &stakeOut); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	var submitOut bytes.Buffer
	if err := proposalSubmitCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--title", "Set local quorum",
		"--description", "Use majority quorum for ChainLab governance",
		"--kind", "param.change",
		"--param", "governance.quorum",
		"--value", "majority",
		"--voting-period", "2",
	}, &submitOut); err != nil {
		t.Fatal(err)
	}
	var submitResult struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(submitOut.Bytes(), &submitResult); err != nil {
		t.Fatal(err)
	}
	if submitResult.Hash == "" {
		t.Fatalf("submit result = %+v", submitResult)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	var txOut bytes.Buffer
	if err := txQueryCommand([]string{"--rpc", server.URL, "--hash", submitResult.Hash}, &txOut); err != nil {
		t.Fatal(err)
	}
	var txResult types.TransactionRecord
	if err := json.Unmarshal(txOut.Bytes(), &txResult); err != nil {
		t.Fatal(err)
	}
	proposalID := txResult.Receipt.ProposalID
	if proposalID == "" {
		t.Fatalf("tx result = %+v", txResult)
	}

	var voteOut bytes.Buffer
	if err := voteCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--proposal", proposalID,
		"--choice", "yes",
	}, &voteOut); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	var executeOut bytes.Buffer
	if err := proposalExecuteCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--proposal", proposalID,
	}, &executeOut); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	var proposalOut bytes.Buffer
	if err := proposalCommand([]string{"--rpc", server.URL, "--id", proposalID}, &proposalOut); err != nil {
		t.Fatal(err)
	}
	var proposal types.Proposal
	if err := json.Unmarshal(proposalOut.Bytes(), &proposal); err != nil {
		t.Fatal(err)
	}
	if proposal.Status != types.ProposalStatusExecuted || proposal.Votes["yes"] != 500 {
		t.Fatalf("proposal = %+v", proposal)
	}

	var paramOut bytes.Buffer
	if err := paramCommand([]string{"--rpc", server.URL, "--key", "governance.quorum"}, &paramOut); err != nil {
		t.Fatal(err)
	}
	var param map[string]string
	if err := json.Unmarshal(paramOut.Bytes(), &param); err != nil {
		t.Fatal(err)
	}
	if param["value"] != "majority" {
		t.Fatalf("param = %+v", param)
	}
}

func TestValidatorLeaveCommandSendsSignedTx(t *testing.T) {
	proposerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := crypto.AddressFromPrivateKey(proposerKey)
	validator := crypto.AddressFromPrivateKey(validatorKey)
	n, err := node.New(node.Config{
		ChainID:     "chainlab-local",
		ProposerKey: proposerKey,
		GenesisBalance: map[string]uint64{
			proposer:  1_000_000,
			validator: 1_000_000,
		},
		Validators: []string{proposer, validator},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var leaveOut bytes.Buffer
	if err := validatorLeaveCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(validatorKey),
	}, &leaveOut); err != nil {
		t.Fatal(err)
	}
	leaveBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(leaveBlock.Transactions) != 1 || leaveBlock.Transactions[0].Type != types.TxValidatorLeave {
		t.Fatalf("leave block transactions = %#v", leaveBlock.Transactions)
	}
	if len(leaveBlock.Receipts) != 1 || len(leaveBlock.Receipts[0].Events) != 1 || leaveBlock.Receipts[0].Events[0].Type != "validator.left" {
		t.Fatalf("leave receipt = %#v", leaveBlock.Receipts)
	}
	validators := n.Validators()
	if len(validators) != 1 || validators[0] != proposer {
		t.Fatalf("validators = %#v", validators)
	}
}

func TestValidatorSlashCommandSendsSignedTx(t *testing.T) {
	proposerKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	validatorKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	proposer := crypto.AddressFromPrivateKey(proposerKey)
	validator := crypto.AddressFromPrivateKey(validatorKey)
	n, err := node.New(node.Config{
		ChainID:     "chainlab-local",
		ProposerKey: proposerKey,
		GenesisBalance: map[string]uint64{
			proposer:  1_000_000,
			validator: 1_000_000,
		},
		Validators: []string{proposer, validator},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	stake := signedCLITx(t, validatorKey, types.Transaction{
		ChainID:  "chainlab-local",
		Type:     types.TxStake,
		From:     validator,
		Nonce:    0,
		Value:    300,
		GasLimit: 30_000,
		GasPrice: 1,
	})
	if err := n.SubmitTx(stake); err != nil {
		t.Fatal(err)
	}

	var slashOut bytes.Buffer
	if err := validatorSlashCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(proposerKey),
		"--target", validator,
		"--amount", "300",
		"--evidence", "double-sign-height-3",
	}, &slashOut); err != nil {
		t.Fatal(err)
	}
	slashBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(slashBlock.Transactions) != 2 || slashBlock.Transactions[1].Type != types.TxValidatorSlash {
		t.Fatalf("slash block transactions = %#v", slashBlock.Transactions)
	}
	if len(slashBlock.Receipts) != 2 || len(slashBlock.Receipts[1].Events) != 1 || slashBlock.Receipts[1].Events[0].Type != "validator.slashed" {
		t.Fatalf("slash receipt = %#v", slashBlock.Receipts)
	}
	if got := n.StakeOf(validator); got != 0 {
		t.Fatalf("validator stake = %d", got)
	}
	validators := n.Validators()
	if len(validators) != 1 || validators[0] != proposer {
		t.Fatalf("validators = %#v", validators)
	}
}

func TestDeployAndContractCallCommandsSendSignedTx(t *testing.T) {
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

	var deployOut bytes.Buffer
	if err := deployCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--code-id", "counter.v1",
		"--arg", "initial=2",
	}, &deployOut); err != nil {
		t.Fatal(err)
	}
	var deployResponse struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(deployOut.Bytes(), &deployResponse); err != nil {
		t.Fatal(err)
	}
	if deployResponse.Hash == "" {
		t.Fatal("deploy command should print transaction hash")
	}
	deployBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(deployBlock.Transactions) != 1 || deployBlock.Transactions[0].Type != types.TxDeploy {
		t.Fatalf("deploy block transactions = %#v", deployBlock.Transactions)
	}
	counter := deployBlock.Receipts[0].ContractAddress
	if counter == "" {
		t.Fatal("deploy receipt should include contract address")
	}

	var callOut bytes.Buffer
	if err := contractCallCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--to", counter,
		"--method", "increment",
		"--arg", "amount=5",
	}, &callOut); err != nil {
		t.Fatal(err)
	}
	var callResponse struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(callOut.Bytes(), &callResponse); err != nil {
		t.Fatal(err)
	}
	if callResponse.Hash == "" {
		t.Fatal("call command should print transaction hash")
	}
	callBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(callBlock.Transactions) != 1 || callBlock.Transactions[0].Type != types.TxCall {
		t.Fatalf("call block transactions = %#v", callBlock.Transactions)
	}

	var readOut bytes.Buffer
	if err := callCommand([]string{
		"--rpc", server.URL,
		"--from", alice,
		"--to", counter,
		"--method", "get",
	}, &readOut); err != nil {
		t.Fatal(err)
	}
	var readResult struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(readOut.Bytes(), &readResult); err != nil {
		t.Fatal(err)
	}
	if readResult.Result != "7" {
		t.Fatalf("counter value = %q", readResult.Result)
	}
}

func TestWASMUploadCommandDeploysUploadedCode(t *testing.T) {
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

	wasmPath := filepath.Join(t.TempDir(), "echo.wasm")
	if err := os.WriteFile(wasmPath, contracts.WasmEchoCode(), 0o600); err != nil {
		t.Fatal(err)
	}

	var uploadOut bytes.Buffer
	if err := wasmUploadCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--wasm-file", wasmPath,
	}, &uploadOut); err != nil {
		t.Fatal(err)
	}
	var uploadResponse struct {
		Hash string `json:"hash"`
	}
	if err := json.Unmarshal(uploadOut.Bytes(), &uploadResponse); err != nil {
		t.Fatal(err)
	}
	if uploadResponse.Hash == "" {
		t.Fatal("wasm upload command should print transaction hash")
	}
	uploadBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	codeID := uploadBlock.Receipts[0].CodeID
	if codeID == "" {
		t.Fatalf("upload receipt = %+v", uploadBlock.Receipts[0])
	}

	var deployOut bytes.Buffer
	if err := deployCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--code-id", codeID,
		"--arg", "message=hello",
	}, &deployOut); err != nil {
		t.Fatal(err)
	}
	deployBlock, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	contract := deployBlock.Receipts[0].ContractAddress

	var callOut bytes.Buffer
	if err := contractCallCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--to", contract,
		"--method", "set",
		"--arg", "message=world",
	}, &callOut); err != nil {
		t.Fatal(err)
	}
	if _, err := n.ProduceBlock(); err != nil {
		t.Fatal(err)
	}

	var readOut bytes.Buffer
	if err := callCommand([]string{"--rpc", server.URL, "--from", alice, "--to", contract, "--method", "get"}, &readOut); err != nil {
		t.Fatal(err)
	}
	var readResult struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal(readOut.Bytes(), &readResult); err != nil {
		t.Fatal(err)
	}
	if readResult.Result != "world" {
		t.Fatalf("uploaded wasm result = %q", readResult.Result)
	}
}

func TestReadWASMUploadBytecodeLoadsExample(t *testing.T) {
	bytecode, err := readWASMUploadBytecode("", "", "echo")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytecode, contracts.WasmEchoCode()) {
		t.Fatal("echo example bytecode should match built-in wasm echo module")
	}
}

func TestWASMUploadCommandUsesMeteredDefaultGasLimit(t *testing.T) {
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

	var out bytes.Buffer
	if err := wasmUploadCommand([]string{
		"--rpc", server.URL,
		"--private-key", crypto.PrivateKeyToHex(key),
		"--example", "echo",
	}, &out); err != nil {
		t.Fatal(err)
	}
	pool := n.TxPool()
	if pool.PendingCount != 1 {
		t.Fatalf("txpool = %+v", pool)
	}
	wantGas := core.EstimateWASMUploadGas(contracts.WasmEchoCode())
	if pool.Pending[0].GasLimit != wantGas {
		t.Fatalf("upload gas limit = %d, want %d", pool.Pending[0].GasLimit, wantGas)
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

	var validatorsOut bytes.Buffer
	if err := validatorsCommand([]string{"--rpc", server.URL}, &validatorsOut); err != nil {
		t.Fatal(err)
	}
	var validators []string
	if err := json.Unmarshal(validatorsOut.Bytes(), &validators); err != nil {
		t.Fatal(err)
	}
	if len(validators) != 1 || validators[0] != alice {
		t.Fatalf("validators = %#v", validators)
	}
}

func TestQueryFinalityCommand(t *testing.T) {
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
	for i := 0; i < 3; i++ {
		if _, err := n.ProduceBlock(); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(chainrpc.NewServer(n))
	defer server.Close()

	var out bytes.Buffer
	if err := finalityCommand([]string{"--rpc", server.URL}, &out); err != nil {
		t.Fatal(err)
	}
	var finality struct {
		HeadHeight      uint64 `json:"head_height"`
		SafeHeight      uint64 `json:"safe_height"`
		FinalizedHeight uint64 `json:"finalized_height"`
	}
	if err := json.Unmarshal(out.Bytes(), &finality); err != nil {
		t.Fatal(err)
	}
	if finality.HeadHeight != 3 || finality.SafeHeight != 2 || finality.FinalizedHeight != 1 {
		t.Fatalf("finality = %+v", finality)
	}
}

func TestQueryFeesCommand(t *testing.T) {
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

	var out bytes.Buffer
	if err := feesCommand([]string{"--rpc", server.URL}, &out); err != nil {
		t.Fatal(err)
	}
	var fees struct {
		BaseFeePerGas        string `json:"base_fee_per_gas"`
		MaxPriorityFeePerGas string `json:"max_priority_fee_per_gas"`
		GasPrice             string `json:"gas_price"`
	}
	if err := json.Unmarshal(out.Bytes(), &fees); err != nil {
		t.Fatal(err)
	}
	if fees.BaseFeePerGas != "0x1" || fees.MaxPriorityFeePerGas != "0x1" || fees.GasPrice != "0x2" {
		t.Fatalf("fees = %+v", fees)
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

func TestQueryCallAndEstimateGasCommands(t *testing.T) {
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
			"initial": "7",
		},
	})
	if err := n.SubmitTx(deploy); err != nil {
		t.Fatal(err)
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	counter := block.Receipts[0].ContractAddress

	var callOut bytes.Buffer
	if err := callCommand([]string{
		"--rpc", server.URL,
		"--from", alice,
		"--to", counter,
		"--method", "get",
	}, &callOut); err != nil {
		t.Fatal(err)
	}
	var callResult struct {
		Raw    string `json:"raw"`
		Result string `json:"result"`
	}
	if err := json.Unmarshal(callOut.Bytes(), &callResult); err != nil {
		t.Fatal(err)
	}
	if callResult.Result != "7" || callResult.Raw == "" {
		t.Fatalf("call result = %+v", callResult)
	}

	var gasOut bytes.Buffer
	if err := estimateGasCommand([]string{
		"--rpc", server.URL,
		"--type", "call",
		"--to", counter,
	}, &gasOut); err != nil {
		t.Fatal(err)
	}
	var gasResult struct {
		Gas string `json:"gas"`
	}
	if err := json.Unmarshal(gasOut.Bytes(), &gasResult); err != nil {
		t.Fatal(err)
	}
	if gasResult.Gas != "0xc350" {
		t.Fatalf("gas result = %+v", gasResult)
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
