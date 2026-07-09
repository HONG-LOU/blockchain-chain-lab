package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"chainlab/examples"
	"chainlab/internal/consensus"
	"chainlab/internal/contracts"
	"chainlab/internal/core"
	"chainlab/internal/crypto"
	"chainlab/internal/node"
	chainrpc "chainlab/internal/rpc"
	"chainlab/internal/types"
)

type peerListFlag []string

func (p *peerListFlag) Set(value string) error {
	if value == "" {
		return fmt.Errorf("peer URL cannot be empty")
	}
	*p = append(*p, value)
	return nil
}

func (p *peerListFlag) String() string {
	return fmt.Sprint([]string(*p))
}

func (p *peerListFlag) Values() []string {
	return append([]string(nil), (*p)...)
}

type stringListFlag []string

func (s *stringListFlag) Set(value string) error {
	if value == "" {
		return fmt.Errorf("value cannot be empty")
	}
	*s = append(*s, value)
	return nil
}

func (s *stringListFlag) String() string {
	return fmt.Sprint([]string(*s))
}

func (s *stringListFlag) Values() []string {
	return append([]string(nil), (*s)...)
}

type GenesisFile struct {
	ChainID    string            `json:"chain_id"`
	Proposer   string            `json:"proposer"`
	PrivateKey string            `json:"private_key"`
	Validators []string          `json:"validators"`
	Balances   map[string]uint64 `json:"balances"`
}

type nodeOptions struct {
	Listen        string
	PrivateKeyHex string
	GenesisPath   string
	DataDir       string
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	switch os.Args[1] {
	case "keygen":
		key, err := crypto.GenerateKey()
		if err != nil {
			log.Fatal(err)
		}
		write(map[string]string{
			"address":     crypto.AddressFromPrivateKey(key),
			"private_key": crypto.PrivateKeyToHex(key),
		})
	case "demo":
		summary, err := examples.RunDemo()
		if err != nil {
			log.Fatal(err)
		}
		write(summary)
	case "init":
		initCommand(os.Args[2:])
	case "node":
		nodeCommand(os.Args[2:])
	case "faucet":
		faucetCommand(os.Args[2:], os.Stdout)
	case "tx":
		txCommand(os.Args[2:], os.Stdout)
	case "query":
		queryCommand(os.Args[2:], os.Stdout)
	case "chain":
		chainCommand(os.Args[2:], os.Stdout)
	default:
		usage()
		os.Exit(2)
	}
}

func initCommand(args []string) {
	flags := flag.NewFlagSet("init", flag.ExitOnError)
	out := flags.String("out", "", "optional genesis output file")
	if err := flags.Parse(args); err != nil {
		log.Fatal(err)
	}
	if *out == "" {
		key, err := crypto.GenerateKey()
		if err != nil {
			log.Fatal(err)
		}
		write(GenesisFile{
			ChainID:    "chainlab-local",
			Proposer:   crypto.AddressFromPrivateKey(key),
			PrivateKey: crypto.PrivateKeyToHex(key),
			Validators: []string{crypto.AddressFromPrivateKey(key)},
			Balances: map[string]uint64{
				crypto.AddressFromPrivateKey(key): 1_000_000_000,
			},
		})
		return
	}
	genesis, err := createGenesisFile(*out)
	if err != nil {
		log.Fatal(err)
	}
	write(genesis)
}

func nodeCommand(args []string) {
	flags := flag.NewFlagSet("node", flag.ExitOnError)
	listen := flags.String("listen", ":8547", "HTTP listen address")
	privateKeyHex := flags.String("private-key", "", "proposer private key")
	genesisPath := flags.String("genesis", "", "genesis file path")
	dataDir := flags.String("data-dir", "", "persistent chain data directory")
	var peers peerListFlag
	flags.Var(&peers, "peer", "peer HTTP base URL; can be repeated")
	if err := flags.Parse(args); err != nil {
		log.Fatal(err)
	}
	config, err := buildNodeConfig(nodeOptions{
		Listen:        *listen,
		PrivateKeyHex: *privateKeyHex,
		GenesisPath:   *genesisPath,
		DataDir:       *dataDir,
	})
	if err != nil {
		log.Fatal(err)
	}
	addr := crypto.AddressFromPrivateKey(config.ProposerKey)
	n, err := node.New(config)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("chainlab node listening on %s proposer=%s\n", *listen, addr)
	log.Fatal(http.ListenAndServe(*listen, chainrpc.NewServerWithPeers(n, peers.Values())))
}

func buildNodeConfig(options nodeOptions) (node.Config, error) {
	chainID := "chainlab-local"
	balances := make(map[string]uint64)
	privateKeyHex := options.PrivateKeyHex
	var validators []string
	if options.GenesisPath != "" {
		genesis, err := readGenesisFile(options.GenesisPath)
		if err != nil {
			return node.Config{}, err
		}
		chainID = genesis.ChainID
		if privateKeyHex == "" {
			privateKeyHex = genesis.PrivateKey
		}
		validators = genesis.Validators
		balances = genesis.Balances
	}
	var key crypto.PrivateKey
	var err error
	if privateKeyHex == "" {
		key, err = crypto.GenerateKey()
	} else {
		key, err = crypto.PrivateKeyFromHex(privateKeyHex)
	}
	if err != nil {
		return node.Config{}, err
	}
	addr := crypto.AddressFromPrivateKey(key)
	if len(balances) == 0 {
		balances[addr] = 1_000_000_000
	}
	return node.Config{
		ChainID:        chainID,
		ProposerKey:    key,
		Validators:     validators,
		GenesisBalance: balances,
		DataDir:        options.DataDir,
	}, nil
}

func usage() {
	fmt.Println("usage: chainlab <keygen|init|demo|node|faucet|tx|query|chain>")
}

func faucetCommand(args []string, out io.Writer) {
	if len(args) < 1 {
		log.Fatal("usage: chainlab faucet <request>")
	}
	switch args[0] {
	case "request":
		if err := faucetRequestCommand(args[1:], out); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unknown faucet command %q", args[0])
	}
}

func faucetRequestCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("faucet request", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	to := flags.String("to", "", "recipient address")
	amount := flags.Uint64("amount", 0, "faucet amount")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*to) == "" {
		return fmt.Errorf("recipient is required")
	}
	if *amount == 0 {
		return fmt.Errorf("amount must be positive")
	}
	raw, err := json.Marshal(map[string]any{
		"address": *to,
		"amount":  *amount,
	})
	if err != nil {
		return err
	}
	resp, err := http.Post(trimSlash(*rpcURL)+"/faucet", "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("faucet request failed: status %d: %s", resp.StatusCode, string(body))
	}
	var response map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return err
	}
	return writeTo(out, response)
}

func txCommand(args []string, out io.Writer) {
	if len(args) < 1 {
		log.Fatal("usage: chainlab tx <transfer|batch-transfer|deploy|call|wasm-upload|stake|proposal-submit|vote|proposal-execute|validator-join|validator-leave|validator-slash|raw-submit>")
	}
	switch args[0] {
	case "transfer":
		if err := transferCommand(args[1:], out); err != nil {
			log.Fatal(err)
		}
	case "batch-transfer":
		if err := batchTransferCommand(args[1:], out); err != nil {
			log.Fatal(err)
		}
	case "deploy":
		if err := deployCommand(args[1:], out); err != nil {
			log.Fatal(err)
		}
	case "call":
		if err := contractCallCommand(args[1:], out); err != nil {
			log.Fatal(err)
		}
	case "wasm-upload":
		if err := wasmUploadCommand(args[1:], out); err != nil {
			log.Fatal(err)
		}
	case "stake":
		if err := stakeCommand(args[1:], out); err != nil {
			log.Fatal(err)
		}
	case "proposal-submit":
		if err := proposalSubmitCommand(args[1:], out); err != nil {
			log.Fatal(err)
		}
	case "vote":
		if err := voteCommand(args[1:], out); err != nil {
			log.Fatal(err)
		}
	case "proposal-execute":
		if err := proposalExecuteCommand(args[1:], out); err != nil {
			log.Fatal(err)
		}
	case "validator-join":
		if err := validatorJoinCommand(args[1:], out); err != nil {
			log.Fatal(err)
		}
	case "validator-leave":
		if err := validatorLeaveCommand(args[1:], out); err != nil {
			log.Fatal(err)
		}
	case "validator-slash":
		if err := validatorSlashCommand(args[1:], out); err != nil {
			log.Fatal(err)
		}
	case "raw-submit":
		if err := rawSubmitCommand(args[1:], out); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatalf("unknown tx command %q", args[0])
	}
}

func queryCommand(args []string, out io.Writer) {
	if len(args) < 1 {
		log.Fatal("usage: chainlab query <account|tx|head|finality|finality-evidence|fees|mempool|logs|validators|proposal|param|call|estimate-gas>")
	}
	var err error
	switch args[0] {
	case "account":
		err = accountCommand(args[1:], out)
	case "tx":
		err = txQueryCommand(args[1:], out)
	case "head":
		err = headCommand(args[1:], out)
	case "finality":
		err = finalityCommand(args[1:], out)
	case "finality-evidence":
		err = finalityEvidenceCommand(args[1:], out)
	case "fees":
		err = feesCommand(args[1:], out)
	case "mempool":
		err = mempoolCommand(args[1:], out)
	case "logs":
		err = logsCommand(args[1:], out)
	case "validators":
		err = validatorsCommand(args[1:], out)
	case "proposal":
		err = proposalCommand(args[1:], out)
	case "param":
		err = paramCommand(args[1:], out)
	case "call":
		err = callCommand(args[1:], out)
	case "estimate-gas":
		err = estimateGasCommand(args[1:], out)
	default:
		log.Fatalf("unknown query command %q", args[0])
	}
	if err != nil {
		log.Fatal(err)
	}
}

func chainCommand(args []string, out io.Writer) {
	if len(args) < 1 {
		log.Fatal("usage: chainlab chain <produce|finality-vote>")
	}
	var err error
	switch args[0] {
	case "produce":
		err = produceCommand(args[1:], out)
	case "finality-vote":
		err = finalityVoteCommand(args[1:], out)
	default:
		log.Fatalf("unknown chain command %q", args[0])
	}
	if err != nil {
		log.Fatal(err)
	}
}

func transferCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("tx transfer", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	from := flags.String("from", "", "optional account address to send from; private key signs as owner")
	privateKeyHex := flags.String("private-key", "", "sender or owner private key")
	to := flags.String("to", "", "recipient address")
	value := flags.Uint64("value", 0, "transfer amount")
	gasLimit := flags.Uint64("gas-limit", 21_000, "gas limit")
	gasPrice := flags.Uint64("gas-price", 1, "gas price")
	maxFeePerGas := flags.Uint64("max-fee-per-gas", 0, "EIP-1559-style max fee per gas")
	maxPriorityFeePerGas := flags.Uint64("max-priority-fee-per-gas", 0, "EIP-1559-style max priority fee per gas")
	paymasterPrivateKeyHex := flags.String("paymaster-private-key", "", "optional paymaster private key for sponsored gas")
	rawOnly := flags.Bool("raw-only", false, "print signed raw transaction without submitting")
	var authPrivateKeys stringListFlag
	flags.Var(&authPrivateKeys, "auth-private-key", "additional multisig authorization private key; can be repeated")
	if err := flags.Parse(args); err != nil {
		return err
	}
	tx, err := buildSignedTransferFromWithFeeCapsAndPaymaster(*rpcURL, *privateKeyHex, *from, authPrivateKeys.Values(), *to, *value, *gasLimit, *gasPrice, *maxFeePerGas, *maxPriorityFeePerGas, *paymasterPrivateKeyHex)
	if err != nil {
		return err
	}
	if *rawOnly {
		raw, err := types.EncodeRawTransaction(tx)
		if err != nil {
			return err
		}
		return writeTo(out, map[string]any{"hash": tx.Hash(), "raw": raw, "transaction": tx})
	}
	response, err := submitTransaction(*rpcURL, tx)
	if err != nil {
		return err
	}
	return writeTo(out, response)
}

func batchTransferCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("tx batch-transfer", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	from := flags.String("from", "", "optional account address to send from; private key signs as owner")
	privateKeyHex := flags.String("private-key", "", "sender or owner private key")
	gasLimit := flags.Uint64("gas-limit", 0, "gas limit; defaults to estimated batch gas")
	gasPrice := flags.Uint64("gas-price", 1, "gas price")
	maxFeePerGas := flags.Uint64("max-fee-per-gas", 0, "EIP-1559-style max fee per gas")
	maxPriorityFeePerGas := flags.Uint64("max-priority-fee-per-gas", 0, "EIP-1559-style max priority fee per gas")
	paymasterPrivateKeyHex := flags.String("paymaster-private-key", "", "optional paymaster private key for sponsored gas")
	rawOnly := flags.Bool("raw-only", false, "print signed raw transaction without submitting")
	var transfers stringListFlag
	var authPrivateKeys stringListFlag
	flags.Var(&transfers, "to", "recipient:amount transfer; can be repeated")
	flags.Var(&authPrivateKeys, "auth-private-key", "additional multisig authorization private key; can be repeated")
	if err := flags.Parse(args); err != nil {
		return err
	}
	batch, err := parseBatchTransferArgs(transfers.Values())
	if err != nil {
		return err
	}
	limit := *gasLimit
	if limit == 0 {
		limit, err = core.EstimateGasForBatch(batch)
		if err != nil {
			return err
		}
	}
	tx, err := buildSignedBatchTransactionFromWithFeeCapsAndPaymaster(*rpcURL, *privateKeyHex, *from, authPrivateKeys.Values(), batch, limit, *gasPrice, *maxFeePerGas, *maxPriorityFeePerGas, *paymasterPrivateKeyHex)
	if err != nil {
		return err
	}
	if *rawOnly {
		raw, err := types.EncodeRawTransaction(tx)
		if err != nil {
			return err
		}
		return writeTo(out, map[string]any{"hash": tx.Hash(), "raw": raw, "transaction": tx})
	}
	response, err := submitTransaction(*rpcURL, tx)
	if err != nil {
		return err
	}
	return writeTo(out, response)
}

func rawSubmitCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("tx raw-submit", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	raw := flags.String("raw", "", "0x-prefixed raw ChainLab transaction")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*raw) == "" {
		return fmt.Errorf("raw transaction is required")
	}
	var hash string
	if err := rpcCall(*rpcURL, "eth_sendRawTransaction", []any{*raw}, &hash); err != nil {
		return err
	}
	return writeTo(out, map[string]string{"hash": hash})
}

func wasmUploadCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("tx wasm-upload", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	privateKeyHex := flags.String("private-key", "", "uploader private key")
	wasmFile := flags.String("wasm-file", "", "path to a wasm module")
	bytecodeHex := flags.String("bytecode", "", "0x-prefixed wasm bytecode")
	example := flags.String("example", "", "built-in example module to upload: echo")
	gasLimit := flags.Uint64("gas-limit", 0, "gas limit; defaults to metered upload gas")
	gasPrice := flags.Uint64("gas-price", 1, "gas price")
	if err := flags.Parse(args); err != nil {
		return err
	}
	bytecode, err := readWASMUploadBytecode(*wasmFile, *bytecodeHex, *example)
	if err != nil {
		return err
	}
	limit := *gasLimit
	if limit == 0 {
		limit = core.EstimateWASMUploadGas(bytecode)
	}
	payload := map[string]string{"bytecode": "0x" + hex.EncodeToString(bytecode)}
	tx, err := buildSignedTransaction(*rpcURL, *privateKeyHex, types.TxWASMUpload, "", 0, limit, *gasPrice, payload)
	if err != nil {
		return err
	}
	response, err := submitTransaction(*rpcURL, tx)
	if err != nil {
		return err
	}
	return writeTo(out, response)
}

func readWASMUploadBytecode(wasmFile string, bytecodeHex string, example string) ([]byte, error) {
	wasmFile = strings.TrimSpace(wasmFile)
	bytecodeHex = strings.TrimSpace(bytecodeHex)
	example = strings.TrimSpace(example)
	sources := 0
	for _, value := range []string{wasmFile, bytecodeHex, example} {
		if value != "" {
			sources++
		}
	}
	if sources == 0 {
		return nil, fmt.Errorf("wasm file, bytecode, or example is required")
	}
	if sources > 1 {
		return nil, fmt.Errorf("provide only one of wasm file, bytecode, or example")
	}
	if wasmFile != "" {
		bytecode, err := os.ReadFile(wasmFile)
		if err != nil {
			return nil, err
		}
		if len(bytecode) == 0 {
			return nil, fmt.Errorf("wasm bytecode is required")
		}
		return bytecode, nil
	}
	if example != "" {
		switch example {
		case "echo":
			return contracts.WasmEchoCode(), nil
		default:
			return nil, fmt.Errorf("unknown wasm example %q", example)
		}
	}
	bytecode, err := hex.DecodeString(strings.TrimPrefix(bytecodeHex, "0x"))
	if err != nil {
		return nil, fmt.Errorf("invalid wasm bytecode hex: %w", err)
	}
	if len(bytecode) == 0 {
		return nil, fmt.Errorf("wasm bytecode is required")
	}
	return bytecode, nil
}

func stakeCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("tx stake", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	privateKeyHex := flags.String("private-key", "", "sender private key")
	value := flags.Uint64("value", 0, "stake amount")
	gasLimit := flags.Uint64("gas-limit", 30_000, "gas limit")
	gasPrice := flags.Uint64("gas-price", 1, "gas price")
	if err := flags.Parse(args); err != nil {
		return err
	}
	tx, err := buildSignedTransaction(*rpcURL, *privateKeyHex, types.TxStake, "", *value, *gasLimit, *gasPrice, nil)
	if err != nil {
		return err
	}
	response, err := submitTransaction(*rpcURL, tx)
	if err != nil {
		return err
	}
	return writeTo(out, response)
}

func proposalSubmitCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("tx proposal-submit", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	privateKeyHex := flags.String("private-key", "", "proposer private key")
	title := flags.String("title", "", "proposal title")
	description := flags.String("description", "", "proposal description")
	kind := flags.String("kind", "param.change", "proposal kind")
	param := flags.String("param", "", "parameter key for param.change")
	value := flags.String("value", "", "parameter value for param.change")
	votingPeriod := flags.Uint64("voting-period", 2, "voting period in blocks")
	gasLimit := flags.Uint64("gas-limit", 35_000, "gas limit")
	gasPrice := flags.Uint64("gas-price", 1, "gas price")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*title) == "" {
		return fmt.Errorf("proposal title is required")
	}
	if strings.TrimSpace(*kind) == "" {
		return fmt.Errorf("proposal kind is required")
	}
	if *votingPeriod == 0 {
		return fmt.Errorf("voting period must be positive")
	}
	payload := map[string]string{
		"title":         *title,
		"description":   *description,
		"kind":          *kind,
		"voting_period": strconv.FormatUint(*votingPeriod, 10),
	}
	if *kind == "param.change" {
		if strings.TrimSpace(*param) == "" {
			return fmt.Errorf("param is required")
		}
		if strings.TrimSpace(*value) == "" {
			return fmt.Errorf("value is required")
		}
		payload["param"] = *param
		payload["value"] = *value
	}
	tx, err := buildSignedTransaction(*rpcURL, *privateKeyHex, types.TxProposalSubmit, "", 0, *gasLimit, *gasPrice, payload)
	if err != nil {
		return err
	}
	response, err := submitTransaction(*rpcURL, tx)
	if err != nil {
		return err
	}
	return writeTo(out, response)
}

func voteCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("tx vote", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	privateKeyHex := flags.String("private-key", "", "voter private key")
	proposal := flags.String("proposal", "", "proposal id")
	choice := flags.String("choice", "", "vote choice: yes, no, or abstain")
	gasLimit := flags.Uint64("gas-limit", 25_000, "gas limit")
	gasPrice := flags.Uint64("gas-price", 1, "gas price")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*proposal) == "" {
		return fmt.Errorf("proposal is required")
	}
	if strings.TrimSpace(*choice) == "" {
		return fmt.Errorf("choice is required")
	}
	payload := map[string]string{
		"proposal": *proposal,
		"choice":   *choice,
	}
	tx, err := buildSignedTransaction(*rpcURL, *privateKeyHex, types.TxVote, "", 0, *gasLimit, *gasPrice, payload)
	if err != nil {
		return err
	}
	response, err := submitTransaction(*rpcURL, tx)
	if err != nil {
		return err
	}
	return writeTo(out, response)
}

func proposalExecuteCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("tx proposal-execute", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	privateKeyHex := flags.String("private-key", "", "executor private key")
	proposal := flags.String("proposal", "", "proposal id")
	gasLimit := flags.Uint64("gas-limit", 35_000, "gas limit")
	gasPrice := flags.Uint64("gas-price", 1, "gas price")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*proposal) == "" {
		return fmt.Errorf("proposal is required")
	}
	payload := map[string]string{"proposal": *proposal}
	tx, err := buildSignedTransaction(*rpcURL, *privateKeyHex, types.TxProposalExecute, "", 0, *gasLimit, *gasPrice, payload)
	if err != nil {
		return err
	}
	response, err := submitTransaction(*rpcURL, tx)
	if err != nil {
		return err
	}
	return writeTo(out, response)
}

func validatorJoinCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("tx validator-join", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	privateKeyHex := flags.String("private-key", "", "validator private key")
	gasLimit := flags.Uint64("gas-limit", 40_000, "gas limit")
	gasPrice := flags.Uint64("gas-price", 1, "gas price")
	if err := flags.Parse(args); err != nil {
		return err
	}
	tx, err := buildSignedTransaction(*rpcURL, *privateKeyHex, types.TxValidatorJoin, "", 0, *gasLimit, *gasPrice, nil)
	if err != nil {
		return err
	}
	response, err := submitTransaction(*rpcURL, tx)
	if err != nil {
		return err
	}
	return writeTo(out, response)
}

func validatorLeaveCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("tx validator-leave", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	privateKeyHex := flags.String("private-key", "", "validator private key")
	gasLimit := flags.Uint64("gas-limit", 40_000, "gas limit")
	gasPrice := flags.Uint64("gas-price", 1, "gas price")
	if err := flags.Parse(args); err != nil {
		return err
	}
	tx, err := buildSignedTransaction(*rpcURL, *privateKeyHex, types.TxValidatorLeave, "", 0, *gasLimit, *gasPrice, nil)
	if err != nil {
		return err
	}
	response, err := submitTransaction(*rpcURL, tx)
	if err != nil {
		return err
	}
	return writeTo(out, response)
}

func validatorSlashCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("tx validator-slash", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	privateKeyHex := flags.String("private-key", "", "reporter validator private key")
	target := flags.String("target", "", "validator address to slash")
	amount := flags.Uint64("amount", 0, "stake amount to slash")
	evidence := flags.String("evidence", "", "evidence reference or summary")
	gasLimit := flags.Uint64("gas-limit", 45_000, "gas limit")
	gasPrice := flags.Uint64("gas-price", 1, "gas price")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *target == "" {
		return fmt.Errorf("target validator is required")
	}
	if *amount == 0 {
		return fmt.Errorf("slash amount must be positive")
	}
	if strings.TrimSpace(*evidence) == "" {
		return fmt.Errorf("evidence is required")
	}
	payload := map[string]string{
		"target":   *target,
		"amount":   strconv.FormatUint(*amount, 10),
		"evidence": *evidence,
	}
	tx, err := buildSignedTransaction(*rpcURL, *privateKeyHex, types.TxValidatorSlash, "", 0, *gasLimit, *gasPrice, payload)
	if err != nil {
		return err
	}
	response, err := submitTransaction(*rpcURL, tx)
	if err != nil {
		return err
	}
	return writeTo(out, response)
}

func deployCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("tx deploy", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	privateKeyHex := flags.String("private-key", "", "deployer private key")
	codeID := flags.String("code-id", "", "native contract code id")
	gasLimit := flags.Uint64("gas-limit", 90_000, "gas limit")
	gasPrice := flags.Uint64("gas-price", 1, "gas price")
	var deployArgs stringListFlag
	flags.Var(&deployArgs, "arg", "deployment argument key=value; can be repeated")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *codeID == "" {
		return fmt.Errorf("code id is required")
	}
	payload, err := parseKeyValueArgs(deployArgs.Values())
	if err != nil {
		return err
	}
	payload["code_id"] = *codeID
	tx, err := buildSignedTransaction(*rpcURL, *privateKeyHex, types.TxDeploy, "", 0, *gasLimit, *gasPrice, payload)
	if err != nil {
		return err
	}
	response, err := submitTransaction(*rpcURL, tx)
	if err != nil {
		return err
	}
	return writeTo(out, response)
}

func contractCallCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("tx call", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	privateKeyHex := flags.String("private-key", "", "caller private key")
	to := flags.String("to", "", "contract address")
	method := flags.String("method", "", "contract write method")
	gasLimit := flags.Uint64("gas-limit", 60_000, "gas limit")
	gasPrice := flags.Uint64("gas-price", 1, "gas price")
	var callArgs stringListFlag
	flags.Var(&callArgs, "arg", "method argument key=value; can be repeated")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *to == "" {
		return fmt.Errorf("contract address is required")
	}
	if *method == "" {
		return fmt.Errorf("method is required")
	}
	payload, err := parseKeyValueArgs(callArgs.Values())
	if err != nil {
		return err
	}
	payload["method"] = *method
	tx, err := buildSignedTransaction(*rpcURL, *privateKeyHex, types.TxCall, *to, 0, *gasLimit, *gasPrice, payload)
	if err != nil {
		return err
	}
	response, err := submitTransaction(*rpcURL, tx)
	if err != nil {
		return err
	}
	return writeTo(out, response)
}

func buildSignedTransfer(rpcURL string, privateKeyHex string, to string, value uint64, gasLimit uint64, gasPrice uint64) (types.Transaction, error) {
	return buildSignedTransferWithFeeCaps(rpcURL, privateKeyHex, to, value, gasLimit, gasPrice, 0, 0)
}

func buildSignedTransferWithFeeCaps(rpcURL string, privateKeyHex string, to string, value uint64, gasLimit uint64, gasPrice uint64, maxFeePerGas uint64, maxPriorityFeePerGas uint64) (types.Transaction, error) {
	return buildSignedTransferWithFeeCapsAndPaymaster(rpcURL, privateKeyHex, to, value, gasLimit, gasPrice, maxFeePerGas, maxPriorityFeePerGas, "")
}

func buildSignedTransferWithFeeCapsAndPaymaster(rpcURL string, privateKeyHex string, to string, value uint64, gasLimit uint64, gasPrice uint64, maxFeePerGas uint64, maxPriorityFeePerGas uint64, paymasterPrivateKeyHex string) (types.Transaction, error) {
	return buildSignedTransferFromWithFeeCapsAndPaymaster(rpcURL, privateKeyHex, "", nil, to, value, gasLimit, gasPrice, maxFeePerGas, maxPriorityFeePerGas, paymasterPrivateKeyHex)
}

func buildSignedTransferFromWithFeeCapsAndPaymaster(rpcURL string, privateKeyHex string, fromOverride string, authPrivateKeyHexes []string, to string, value uint64, gasLimit uint64, gasPrice uint64, maxFeePerGas uint64, maxPriorityFeePerGas uint64, paymasterPrivateKeyHex string) (types.Transaction, error) {
	if privateKeyHex == "" {
		return types.Transaction{}, fmt.Errorf("private key is required")
	}
	if to == "" {
		return types.Transaction{}, fmt.Errorf("recipient is required")
	}
	return buildSignedTransactionFromSpec(rpcURL, privateKeyHex, signedTransactionSpec{
		txType:                 types.TxTransfer,
		fromOverride:           fromOverride,
		authPrivateKeyHexes:    authPrivateKeyHexes,
		to:                     to,
		value:                  value,
		gasLimit:               gasLimit,
		gasPrice:               gasPrice,
		maxFeePerGas:           maxFeePerGas,
		maxPriorityFeePerGas:   maxPriorityFeePerGas,
		paymasterPrivateKeyHex: paymasterPrivateKeyHex,
	})
}

func buildSignedTransaction(rpcURL string, privateKeyHex string, txType types.TxType, to string, value uint64, gasLimit uint64, gasPrice uint64, payload map[string]string) (types.Transaction, error) {
	return buildSignedTransactionWithFeeCaps(rpcURL, privateKeyHex, txType, to, value, gasLimit, gasPrice, 0, 0, payload)
}

func buildSignedTransactionWithFeeCaps(rpcURL string, privateKeyHex string, txType types.TxType, to string, value uint64, gasLimit uint64, gasPrice uint64, maxFeePerGas uint64, maxPriorityFeePerGas uint64, payload map[string]string) (types.Transaction, error) {
	return buildSignedTransactionWithFeeCapsAndPaymaster(rpcURL, privateKeyHex, txType, to, value, gasLimit, gasPrice, maxFeePerGas, maxPriorityFeePerGas, payload, "")
}

func buildSignedTransactionWithFeeCapsAndPaymaster(rpcURL string, privateKeyHex string, txType types.TxType, to string, value uint64, gasLimit uint64, gasPrice uint64, maxFeePerGas uint64, maxPriorityFeePerGas uint64, payload map[string]string, paymasterPrivateKeyHex string) (types.Transaction, error) {
	return buildSignedTransactionFromSpec(rpcURL, privateKeyHex, signedTransactionSpec{
		txType:                 txType,
		to:                     to,
		value:                  value,
		gasLimit:               gasLimit,
		gasPrice:               gasPrice,
		maxFeePerGas:           maxFeePerGas,
		maxPriorityFeePerGas:   maxPriorityFeePerGas,
		payload:                payload,
		paymasterPrivateKeyHex: paymasterPrivateKeyHex,
	})
}

func buildSignedBatchTransactionWithFeeCapsAndPaymaster(rpcURL string, privateKeyHex string, batch []types.BatchOperation, gasLimit uint64, gasPrice uint64, maxFeePerGas uint64, maxPriorityFeePerGas uint64, paymasterPrivateKeyHex string) (types.Transaction, error) {
	return buildSignedBatchTransactionFromWithFeeCapsAndPaymaster(rpcURL, privateKeyHex, "", nil, batch, gasLimit, gasPrice, maxFeePerGas, maxPriorityFeePerGas, paymasterPrivateKeyHex)
}

func buildSignedBatchTransactionFromWithFeeCapsAndPaymaster(rpcURL string, privateKeyHex string, fromOverride string, authPrivateKeyHexes []string, batch []types.BatchOperation, gasLimit uint64, gasPrice uint64, maxFeePerGas uint64, maxPriorityFeePerGas uint64, paymasterPrivateKeyHex string) (types.Transaction, error) {
	if len(batch) == 0 {
		return types.Transaction{}, fmt.Errorf("batch requires at least one operation")
	}
	return buildSignedTransactionFromSpec(rpcURL, privateKeyHex, signedTransactionSpec{
		txType:                 types.TxBatch,
		fromOverride:           fromOverride,
		authPrivateKeyHexes:    authPrivateKeyHexes,
		gasLimit:               gasLimit,
		gasPrice:               gasPrice,
		maxFeePerGas:           maxFeePerGas,
		maxPriorityFeePerGas:   maxPriorityFeePerGas,
		batch:                  batch,
		paymasterPrivateKeyHex: paymasterPrivateKeyHex,
	})
}

type signedTransactionSpec struct {
	txType                 types.TxType
	fromOverride           string
	authPrivateKeyHexes    []string
	to                     string
	value                  uint64
	gasLimit               uint64
	gasPrice               uint64
	maxFeePerGas           uint64
	maxPriorityFeePerGas   uint64
	payload                map[string]string
	batch                  []types.BatchOperation
	paymasterPrivateKeyHex string
}

func buildSignedTransactionFromSpec(rpcURL string, privateKeyHex string, spec signedTransactionSpec) (types.Transaction, error) {
	if privateKeyHex == "" {
		return types.Transaction{}, fmt.Errorf("private key is required")
	}
	key, err := crypto.PrivateKeyFromHex(privateKeyHex)
	if err != nil {
		return types.Transaction{}, err
	}
	authKeys := []crypto.PrivateKey{key}
	for _, authPrivateKeyHex := range spec.authPrivateKeyHexes {
		authKey, err := crypto.PrivateKeyFromHex(authPrivateKeyHex)
		if err != nil {
			return types.Transaction{}, err
		}
		authKeys = append(authKeys, authKey)
	}
	var paymasterKey crypto.PrivateKey
	var paymaster string
	if strings.TrimSpace(spec.paymasterPrivateKeyHex) != "" {
		paymasterKey, err = crypto.PrivateKeyFromHex(spec.paymasterPrivateKeyHex)
		if err != nil {
			return types.Transaction{}, err
		}
		paymaster = crypto.AddressFromPrivateKey(paymasterKey)
	}
	signer := crypto.AddressFromPrivateKey(key)
	from := signer
	if override := strings.ToLower(strings.TrimSpace(spec.fromOverride)); override != "" {
		from = override
	}
	signerField := ""
	useAuthorizations := len(spec.authPrivateKeyHexes) > 0
	if !useAuthorizations && !strings.EqualFold(from, signer) {
		signerField = signer
	}
	var chainIDHex string
	if err := rpcCall(rpcURL, "eth_chainId", []any{}, &chainIDHex); err != nil {
		return types.Transaction{}, err
	}
	chainID := chainIDFromHex(chainIDHex)
	var nonceHex string
	if err := rpcCall(rpcURL, "eth_getTransactionCount", []any{from, "pending"}, &nonceHex); err != nil {
		return types.Transaction{}, err
	}
	nonce, err := parseQuantity(nonceHex)
	if err != nil {
		return types.Transaction{}, err
	}
	tx := types.Transaction{
		ChainID:              chainID,
		Type:                 spec.txType,
		From:                 from,
		Signer:               signerField,
		To:                   spec.to,
		Nonce:                nonce,
		Value:                spec.value,
		GasLimit:             spec.gasLimit,
		GasPrice:             spec.gasPrice,
		MaxFeePerGas:         spec.maxFeePerGas,
		MaxPriorityFeePerGas: spec.maxPriorityFeePerGas,
		Paymaster:            paymaster,
		Payload:              spec.payload,
		Batch:                spec.batch,
	}
	if useAuthorizations {
		if strings.TrimSpace(spec.fromOverride) == "" {
			return types.Transaction{}, fmt.Errorf("multisig authorizations require --from")
		}
		tx.Authorizations = make([]types.Authorization, len(authKeys))
		for i, authKey := range authKeys {
			tx.Authorizations[i].Signer = crypto.AddressFromPrivateKey(authKey)
		}
		for i, authKey := range authKeys {
			signature, err := crypto.Sign(authKey, tx.SigningBytes())
			if err != nil {
				return types.Transaction{}, err
			}
			tx.Authorizations[i].Signature = signature
		}
	} else {
		signature, err := crypto.Sign(key, tx.SigningBytes())
		if err != nil {
			return types.Transaction{}, err
		}
		tx.Signature = signature
	}
	if paymasterKey != nil {
		paymasterSignature, err := crypto.Sign(paymasterKey, tx.PaymasterSigningBytes())
		if err != nil {
			return types.Transaction{}, err
		}
		tx.PaymasterSignature = paymasterSignature
	}
	return tx, nil
}

func submitTransaction(rpcURL string, tx types.Transaction) (map[string]any, error) {
	raw, err := json.Marshal(tx)
	if err != nil {
		return nil, err
	}
	resp, err := http.Post(trimSlash(rpcURL)+"/tx", "application/json", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("submit transaction failed: status %d: %s", resp.StatusCode, string(body))
	}
	var response map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return nil, err
	}
	return response, nil
}

func accountCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("query account", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	address := flags.String("address", "", "account address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *address == "" {
		return fmt.Errorf("address is required")
	}
	resp, err := http.Get(trimSlash(*rpcURL) + "/account/" + *address)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("account query failed: status %d: %s", resp.StatusCode, string(body))
	}
	var account types.Account
	if err := json.NewDecoder(resp.Body).Decode(&account); err != nil {
		return err
	}
	return writeTo(out, account)
}

func txQueryCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("query tx", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	hash := flags.String("hash", "", "transaction hash")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *hash == "" {
		return fmt.Errorf("transaction hash is required")
	}
	resp, err := http.Get(trimSlash(*rpcURL) + "/tx/" + *hash)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("transaction query failed: status %d: %s", resp.StatusCode, string(body))
	}
	var record types.TransactionRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		return err
	}
	return writeTo(out, record)
}

func headCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("query head", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	resp, err := http.Get(trimSlash(*rpcURL) + "/chain/head")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("head query failed: status %d: %s", resp.StatusCode, string(body))
	}
	var block types.Block
	if err := json.NewDecoder(resp.Body).Decode(&block); err != nil {
		return err
	}
	return writeTo(out, block)
}

func finalityCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("query finality", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	resp, err := http.Get(trimSlash(*rpcURL) + "/chain/finality")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("finality query failed: status %d: %s", resp.StatusCode, string(body))
	}
	var finality node.FinalityCheckpoint
	if err := json.NewDecoder(resp.Body).Decode(&finality); err != nil {
		return err
	}
	return writeTo(out, finality)
}

func finalityEvidenceCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("query finality-evidence", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	resp, err := http.Get(trimSlash(*rpcURL) + "/chain/finality/evidence")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("finality evidence query failed: status %d: %s", resp.StatusCode, string(body))
	}
	var evidence []types.FinalityEquivocationEvidence
	if err := json.NewDecoder(resp.Body).Decode(&evidence); err != nil {
		return err
	}
	return writeTo(out, evidence)
}

func feesCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("query fees", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	var fees map[string]string
	if err := rpcCall(*rpcURL, "chain_feeMarket", []any{}, &fees); err != nil {
		return err
	}
	return writeTo(out, fees)
}

func mempoolCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("query mempool", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	resp, err := http.Get(trimSlash(*rpcURL) + "/txpool")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("mempool query failed: status %d: %s", resp.StatusCode, string(body))
	}
	var pool node.MempoolSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&pool); err != nil {
		return err
	}
	return writeTo(out, pool)
}

func logsCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("query logs", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	fromBlock := flags.String("from-block", "earliest", "start block: earliest, latest, or hex quantity")
	toBlock := flags.String("to-block", "latest", "end block: earliest, latest, or hex quantity")
	address := flags.String("address", "", "contract address filter")
	var topics stringListFlag
	flags.Var(&topics, "topic", "topic filter; can be repeated")
	if err := flags.Parse(args); err != nil {
		return err
	}
	filter := map[string]any{
		"fromBlock": *fromBlock,
		"toBlock":   *toBlock,
	}
	if *address != "" {
		filter["address"] = *address
	}
	if values := topics.Values(); len(values) > 0 {
		filter["topics"] = values
	}
	var logs []map[string]any
	if err := rpcCall(*rpcURL, "eth_getLogs", []any{filter}, &logs); err != nil {
		return err
	}
	return writeTo(out, logs)
}

func validatorsCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("query validators", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	resp, err := http.Get(trimSlash(*rpcURL) + "/validators")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("validators query failed: status %d: %s", resp.StatusCode, string(body))
	}
	var validators []string
	if err := json.NewDecoder(resp.Body).Decode(&validators); err != nil {
		return err
	}
	return writeTo(out, validators)
}

func proposalCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("query proposal", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	id := flags.String("id", "", "proposal id")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*id) == "" {
		return fmt.Errorf("proposal id is required")
	}
	resp, err := http.Get(trimSlash(*rpcURL) + "/proposal/" + *id)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("proposal query failed: status %d: %s", resp.StatusCode, string(body))
	}
	var proposal types.Proposal
	if err := json.NewDecoder(resp.Body).Decode(&proposal); err != nil {
		return err
	}
	return writeTo(out, proposal)
}

func paramCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("query param", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	key := flags.String("key", "", "parameter key")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*key) == "" {
		return fmt.Errorf("param key is required")
	}
	resp, err := http.Get(trimSlash(*rpcURL) + "/param/" + *key)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("param query failed: status %d: %s", resp.StatusCode, string(body))
	}
	var param map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&param); err != nil {
		return err
	}
	return writeTo(out, param)
}

func callCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("query call", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	from := flags.String("from", "", "caller address")
	to := flags.String("to", "", "contract address")
	method := flags.String("method", "", "read method")
	var callArgs stringListFlag
	flags.Var(&callArgs, "arg", "method argument key=value; can be repeated")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *to == "" {
		return fmt.Errorf("contract address is required")
	}
	if *method == "" {
		return fmt.Errorf("method is required")
	}
	payload, err := parseKeyValueArgs(callArgs.Values())
	if err != nil {
		return err
	}
	payload["method"] = *method
	request := map[string]any{
		"from":    *from,
		"to":      *to,
		"payload": payload,
	}
	var raw string
	if err := rpcCall(*rpcURL, "eth_call", []any{request, "latest"}, &raw); err != nil {
		return err
	}
	decoded, err := decodeDataHex(raw, *method)
	if err != nil {
		return err
	}
	return writeTo(out, map[string]string{"raw": raw, "result": decoded})
}

func estimateGasCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("query estimate-gas", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	txType := flags.String("type", "call", "transaction type")
	from := flags.String("from", "", "sender address")
	to := flags.String("to", "", "recipient or contract address")
	if err := flags.Parse(args); err != nil {
		return err
	}
	request := map[string]any{
		"type": *txType,
		"from": *from,
		"to":   *to,
	}
	var gas string
	if err := rpcCall(*rpcURL, "eth_estimateGas", []any{request}, &gas); err != nil {
		return err
	}
	return writeTo(out, map[string]string{"gas": gas})
}

func parseKeyValueArgs(values []string) (map[string]string, error) {
	output := make(map[string]string, len(values))
	for _, value := range values {
		key, raw, ok := strings.Cut(value, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("argument must be key=value")
		}
		output[key] = raw
	}
	return output, nil
}

func parseBatchTransferArgs(values []string) ([]types.BatchOperation, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("at least one --to recipient:amount is required")
	}
	batch := make([]types.BatchOperation, 0, len(values))
	for _, value := range values {
		to, amountRaw, ok := strings.Cut(value, ":")
		to = strings.TrimSpace(to)
		amountRaw = strings.TrimSpace(amountRaw)
		if !ok || to == "" || amountRaw == "" {
			return nil, fmt.Errorf("batch transfer must be recipient:amount")
		}
		amount, err := strconv.ParseUint(amountRaw, 10, 64)
		if err != nil || amount == 0 {
			return nil, fmt.Errorf("batch transfer amount must be positive")
		}
		batch = append(batch, types.BatchOperation{
			Type:  types.TxTransfer,
			To:    to,
			Value: amount,
		})
	}
	return batch, nil
}

func decodeDataHex(value string, method string) (string, error) {
	raw, err := hex.DecodeString(strings.TrimPrefix(value, "0x"))
	if err != nil {
		return "", err
	}
	if decoded, ok, err := decodeABIString(raw); err != nil {
		return "", err
	} else if ok {
		return decoded, nil
	}
	if len(raw) == 32 {
		if method == "owner" {
			return "0x" + hex.EncodeToString(raw[12:]), nil
		}
		if method == "get" || method == "balanceOf" || method == "threshold" {
			if value, ok := abiWordUint64(raw); ok {
				return strconv.FormatUint(value, 10), nil
			}
		}
	}
	return string(raw), nil
}

func decodeABIString(raw []byte) (string, bool, error) {
	if len(raw) < 64 || len(raw)%32 != 0 {
		return "", false, nil
	}
	offset, ok := abiWordUint64(raw[:32])
	if !ok || offset != 32 {
		return "", false, nil
	}
	length, ok := abiWordUint64(raw[32:64])
	if !ok {
		return "", false, nil
	}
	if length > uint64(len(raw)-64) {
		return "", false, fmt.Errorf("ABI string length exceeds return data")
	}
	return string(raw[64 : 64+length]), true, nil
}

func abiWordUint64(word []byte) (uint64, bool) {
	if len(word) != 32 {
		return 0, false
	}
	for _, b := range word[:24] {
		if b != 0 {
			return 0, false
		}
	}
	var value uint64
	for _, b := range word[24:] {
		value = value<<8 | uint64(b)
	}
	return value, true
}

func produceCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("chain produce", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	resp, err := http.Post(trimSlash(*rpcURL)+"/chain/produce", "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("produce failed: status %d: %s", resp.StatusCode, string(body))
	}
	var block types.Block
	if err := json.NewDecoder(resp.Body).Decode(&block); err != nil {
		return err
	}
	return writeTo(out, block)
}

func finalityVoteCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("chain finality-vote", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	privateKeyHex := flags.String("private-key", "", "validator private key")
	height := flags.Uint64("height", 0, "block height to certify")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*privateKeyHex) == "" {
		return fmt.Errorf("private key is required")
	}
	if *height == 0 {
		return fmt.Errorf("height is required")
	}
	key, err := crypto.PrivateKeyFromHex(*privateKeyHex)
	if err != nil {
		return err
	}
	block, err := fetchBlockByHeight(*rpcURL, *height)
	if err != nil {
		return err
	}
	vote, err := consensus.SignFinalityVote(key, block)
	if err != nil {
		return err
	}
	var finality node.FinalityCheckpoint
	if err := rpcCall(*rpcURL, "chain_sendFinalityVote", []any{vote}, &finality); err != nil {
		return err
	}
	return writeTo(out, map[string]any{"vote": vote, "finality": finality})
}

func fetchBlockByHeight(rpcURL string, height uint64) (types.Block, error) {
	resp, err := http.Get(fmt.Sprintf("%s/chain/block/%d", trimSlash(rpcURL), height))
	if err != nil {
		return types.Block{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return types.Block{}, fmt.Errorf("block query failed: status %d: %s", resp.StatusCode, string(body))
	}
	var block types.Block
	if err := json.NewDecoder(resp.Body).Decode(&block); err != nil {
		return types.Block{}, err
	}
	return block, nil
}

type rpcResponseEnvelope struct {
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
}

func rpcCall(rpcURL string, method string, params []any, result any) error {
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	}
	raw, err := json.Marshal(request)
	if err != nil {
		return err
	}
	resp, err := http.Post(trimSlash(rpcURL)+"/rpc", "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("rpc %s failed: status %d: %s", method, resp.StatusCode, string(body))
	}
	var envelope rpcResponseEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return err
	}
	if envelope.Error != "" {
		return fmt.Errorf("rpc %s failed: %s", method, envelope.Error)
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(envelope.Result, result)
}

func chainIDFromHex(chainIDHex string) string {
	if chainIDHex == "0x7a69" {
		return "chainlab-local"
	}
	return chainIDHex
}

func parseQuantity(value string) (uint64, error) {
	if len(value) >= 2 && value[:2] == "0x" {
		return strconv.ParseUint(value[2:], 16, 64)
	}
	return strconv.ParseUint(value, 10, 64)
}

func trimSlash(value string) string {
	for len(value) > 0 && value[len(value)-1] == '/' {
		value = value[:len(value)-1]
	}
	return value
}

func writeTo(out io.Writer, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, string(encoded))
	return err
}

func createGenesisFile(path string) (GenesisFile, error) {
	key, err := crypto.GenerateKey()
	if err != nil {
		return GenesisFile{}, err
	}
	proposer := crypto.AddressFromPrivateKey(key)
	genesis := GenesisFile{
		ChainID:    "chainlab-local",
		Proposer:   proposer,
		PrivateKey: crypto.PrivateKeyToHex(key),
		Validators: []string{proposer},
		Balances:   map[string]uint64{proposer: 1_000_000_000},
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return GenesisFile{}, err
	}
	raw, err := json.MarshalIndent(genesis, "", "  ")
	if err != nil {
		return GenesisFile{}, err
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return GenesisFile{}, err
	}
	return genesis, nil
}

func readGenesisFile(path string) (GenesisFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return GenesisFile{}, err
	}
	var genesis GenesisFile
	if err := json.Unmarshal(raw, &genesis); err != nil {
		return GenesisFile{}, err
	}
	if genesis.ChainID == "" || genesis.PrivateKey == "" {
		return GenesisFile{}, fmt.Errorf("genesis requires chain_id and private_key")
	}
	if genesis.Balances == nil {
		genesis.Balances = make(map[string]uint64)
	}
	return genesis, nil
}

func write(value any) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(encoded))
}
