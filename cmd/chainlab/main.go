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
	fmt.Println("usage: chainlab <keygen|init|demo|node|tx|query|chain>")
}

func txCommand(args []string, out io.Writer) {
	if len(args) < 1 {
		log.Fatal("usage: chainlab tx <transfer|deploy|call|stake|validator-join|validator-leave>")
	}
	switch args[0] {
	case "transfer":
		if err := transferCommand(args[1:], out); err != nil {
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
	case "stake":
		if err := stakeCommand(args[1:], out); err != nil {
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
	default:
		log.Fatalf("unknown tx command %q", args[0])
	}
}

func queryCommand(args []string, out io.Writer) {
	if len(args) < 1 {
		log.Fatal("usage: chainlab query <account|tx|head|logs|validators|call|estimate-gas>")
	}
	var err error
	switch args[0] {
	case "account":
		err = accountCommand(args[1:], out)
	case "tx":
		err = txQueryCommand(args[1:], out)
	case "head":
		err = headCommand(args[1:], out)
	case "logs":
		err = logsCommand(args[1:], out)
	case "validators":
		err = validatorsCommand(args[1:], out)
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
		log.Fatal("usage: chainlab chain <produce>")
	}
	if args[0] != "produce" {
		log.Fatalf("unknown chain command %q", args[0])
	}
	if err := produceCommand(args[1:], out); err != nil {
		log.Fatal(err)
	}
}

func transferCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("tx transfer", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	privateKeyHex := flags.String("private-key", "", "sender private key")
	to := flags.String("to", "", "recipient address")
	value := flags.Uint64("value", 0, "transfer amount")
	gasLimit := flags.Uint64("gas-limit", 21_000, "gas limit")
	gasPrice := flags.Uint64("gas-price", 1, "gas price")
	if err := flags.Parse(args); err != nil {
		return err
	}
	tx, err := buildSignedTransfer(*rpcURL, *privateKeyHex, *to, *value, *gasLimit, *gasPrice)
	if err != nil {
		return err
	}
	response, err := submitTransaction(*rpcURL, tx)
	if err != nil {
		return err
	}
	return writeTo(out, response)
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

func deployCommand(args []string, out io.Writer) error {
	flags := flag.NewFlagSet("tx deploy", flag.ContinueOnError)
	rpcURL := flags.String("rpc", "http://127.0.0.1:8547", "RPC base URL")
	privateKeyHex := flags.String("private-key", "", "deployer private key")
	codeID := flags.String("code-id", "", "native contract code id")
	gasLimit := flags.Uint64("gas-limit", 80_000, "gas limit")
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
	gasLimit := flags.Uint64("gas-limit", 50_000, "gas limit")
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
	if privateKeyHex == "" {
		return types.Transaction{}, fmt.Errorf("private key is required")
	}
	if to == "" {
		return types.Transaction{}, fmt.Errorf("recipient is required")
	}
	return buildSignedTransaction(rpcURL, privateKeyHex, types.TxTransfer, to, value, gasLimit, gasPrice, nil)
}

func buildSignedTransaction(rpcURL string, privateKeyHex string, txType types.TxType, to string, value uint64, gasLimit uint64, gasPrice uint64, payload map[string]string) (types.Transaction, error) {
	if privateKeyHex == "" {
		return types.Transaction{}, fmt.Errorf("private key is required")
	}
	key, err := crypto.PrivateKeyFromHex(privateKeyHex)
	if err != nil {
		return types.Transaction{}, err
	}
	from := crypto.AddressFromPrivateKey(key)
	var chainIDHex string
	if err := rpcCall(rpcURL, "eth_chainId", []any{}, &chainIDHex); err != nil {
		return types.Transaction{}, err
	}
	chainID := chainIDFromHex(chainIDHex)
	var nonceHex string
	if err := rpcCall(rpcURL, "eth_getTransactionCount", []any{from, "latest"}, &nonceHex); err != nil {
		return types.Transaction{}, err
	}
	nonce, err := parseQuantity(nonceHex)
	if err != nil {
		return types.Transaction{}, err
	}
	tx := types.Transaction{
		ChainID:  chainID,
		Type:     txType,
		From:     from,
		To:       to,
		Nonce:    nonce,
		Value:    value,
		GasLimit: gasLimit,
		GasPrice: gasPrice,
		Payload:  payload,
	}
	signature, err := crypto.Sign(key, tx.SigningBytes())
	if err != nil {
		return types.Transaction{}, err
	}
	tx.Signature = signature
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
	decoded, err := decodeDataHex(raw)
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

func decodeDataHex(value string) (string, error) {
	raw, err := hex.DecodeString(strings.TrimPrefix(value, "0x"))
	if err != nil {
		return "", err
	}
	return string(raw), nil
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
