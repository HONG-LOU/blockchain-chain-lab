package rpc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"chainlab/internal/core"
	"chainlab/internal/node"
	"chainlab/internal/types"
)

func NewServer(n *node.Node) http.Handler {
	return NewServerWithPeers(n, nil)
}

func NewServerWithPeers(n *node.Node, peers []string) http.Handler {
	server := &Server{
		node:   n,
		peers:  append([]string(nil), peers...),
		client: &http.Client{Timeout: 5 * time.Second},
	}
	return server.routes()
}

type Server struct {
	node   *node.Node
	peers  []string
	client *http.Client
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /chain/head", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.node.Head())
	})
	mux.HandleFunc("GET /chain/finality", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.node.Finality())
	})
	mux.HandleFunc("GET /chain/block/{height}", func(w http.ResponseWriter, r *http.Request) {
		height, err := strconv.ParseUint(r.PathValue("height"), 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid height"})
			return
		}
		block, ok := s.node.Block(height)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "block not found"})
			return
		}
		writeJSON(w, http.StatusOK, block)
	})
	mux.HandleFunc("POST /chain/produce", func(w http.ResponseWriter, r *http.Request) {
		block, err := s.node.ProduceBlock()
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		peerErrors := s.broadcastBlock(block)
		if len(peerErrors) > 0 {
			writeJSON(w, http.StatusOK, map[string]any{"block": block, "peer_errors": peerErrors})
			return
		}
		writeJSON(w, http.StatusOK, block)
	})
	mux.HandleFunc("GET /account/{address}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.node.Account(r.PathValue("address")))
	})
	mux.HandleFunc("GET /validators", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.node.Validators())
	})
	mux.HandleFunc("GET /tx/{hash}", func(w http.ResponseWriter, r *http.Request) {
		record, ok := s.node.Transaction(r.PathValue("hash"))
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "transaction not found"})
			return
		}
		writeJSON(w, http.StatusOK, record)
	})
	mux.HandleFunc("GET /txpool", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.node.TxPool())
	})
	mux.HandleFunc("POST /tx", func(w http.ResponseWriter, r *http.Request) {
		tx, err := decodeTransaction(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := s.node.SubmitTx(tx); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		peerErrors := s.broadcastTransaction(tx)
		writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted", "hash": tx.Hash(), "peer_errors": peerErrors})
	})
	mux.HandleFunc("POST /peer/tx", func(w http.ResponseWriter, r *http.Request) {
		tx, err := decodeTransaction(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := s.node.SubmitTx(tx); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "hash": tx.Hash()})
	})
	mux.HandleFunc("POST /peer/block", func(w http.ResponseWriter, r *http.Request) {
		var block types.Block
		if err := json.NewDecoder(r.Body).Decode(&block); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid block json"})
			return
		}
		if err := s.node.ImportBlock(block); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "hash": block.Hash()})
	})
	mux.HandleFunc("POST /rpc", func(w http.ResponseWriter, r *http.Request) {
		handleJSONRPC(w, r, s.node)
	})
	return mux
}

func decodeTransaction(r *http.Request) (types.Transaction, error) {
	var tx types.Transaction
	if err := json.NewDecoder(r.Body).Decode(&tx); err != nil {
		return types.Transaction{}, fmt.Errorf("invalid transaction json")
	}
	return tx, nil
}

func (s *Server) broadcastTransaction(tx types.Transaction) []string {
	return s.broadcast("/peer/tx", tx)
}

func (s *Server) broadcastBlock(block types.Block) []string {
	return s.broadcast("/peer/block", block)
}

func (s *Server) broadcast(path string, payload any) []string {
	if len(s.peers) == 0 {
		return nil
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return []string{err.Error()}
	}
	var peerErrors []string
	for _, peer := range s.peers {
		url := strings.TrimRight(peer, "/") + path
		resp, err := s.client.Post(url, "application/json", bytes.NewReader(raw))
		if err != nil {
			peerErrors = append(peerErrors, fmt.Sprintf("%s: %s", peer, err.Error()))
			continue
		}
		if resp.Body != nil {
			_ = resp.Body.Close()
		}
		if resp.StatusCode >= http.StatusBadRequest {
			peerErrors = append(peerErrors, fmt.Sprintf("%s: status %d", peer, resp.StatusCode))
		}
	}
	return peerErrors
}

type rpcRequest struct {
	ID     any             `json:"id,omitempty"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	ID     any    `json:"id,omitempty"`
	Result any    `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
}

func handleJSONRPC(w http.ResponseWriter, r *http.Request, n *node.Node) {
	var request rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{Error: "invalid json"})
		return
	}
	switch request.Method {
	case "eth_chainId":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: quantity(chainNumber(n.ChainID()))})
	case "eth_blockNumber":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: quantity(n.Head().Header.Height)})
	case "eth_getBalance":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 1 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "address is required"})
			return
		}
		address, ok := params[0].(string)
		if !ok || strings.TrimSpace(address) == "" {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "address is required"})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: quantity(n.Account(address).Balance)})
	case "eth_getTransactionCount":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 1 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "address is required"})
			return
		}
		address, ok := params[0].(string)
		if !ok || strings.TrimSpace(address) == "" {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "address is required"})
			return
		}
		if len(params) > 1 && params[1] == "pending" {
			account, err := n.PendingAccount(address)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: quantity(account.Nonce)})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: quantity(n.Account(address).Nonce)})
	case "eth_getTransactionByHash":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 1 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "transaction hash is required"})
			return
		}
		txHash, ok := params[0].(string)
		if !ok {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "transaction hash must be a string"})
			return
		}
		record, ok := n.Transaction(txHash)
		if !ok {
			writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: nil})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: evmTransaction(record)})
	case "eth_getTransactionReceipt":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 1 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "transaction hash is required"})
			return
		}
		txHash, ok := params[0].(string)
		if !ok {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "transaction hash must be a string"})
			return
		}
		record, ok := n.Transaction(txHash)
		if !ok {
			writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: nil})
			return
		}
		block, _ := n.Block(record.BlockHeight)
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: evmReceipt(record, block)})
	case "eth_getBlockByNumber":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 1 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "block number is required"})
			return
		}
		finality := n.Finality()
		height, err := parseBlockNumber(params[0], blockTags{
			latest:    finality.HeadHeight,
			safe:      finality.SafeHeight,
			finalized: finality.FinalizedHeight,
		})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		block, ok := n.Block(height)
		if !ok {
			writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: nil})
			return
		}
		fullTx := false
		if len(params) > 1 {
			if value, ok := params[1].(bool); ok {
				fullTx = value
			}
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: evmBlock(block, fullTx)})
	case "eth_getLogs":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 1 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "filter is required"})
			return
		}
		finality := n.Finality()
		filter, err := parseLogFilter(params[0], blockTags{
			latest:    finality.HeadHeight,
			safe:      finality.SafeHeight,
			finalized: finality.FinalizedHeight,
		})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		logs := evmLogs(n, filter)
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: logs})
	case "eth_call":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 1 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "call object is required"})
			return
		}
		call, err := parseCallObject(params[0])
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		result, err := n.ReadContract(call.from, call.to, call.method, call.payload)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: dataHex(result)})
	case "eth_estimateGas":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 1 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "call object is required"})
			return
		}
		call, err := parseCallObject(params[0])
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		gas, err := core.EstimateGas(call.txType)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: quantity(gas)})
	case "chain_head":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: n.Head()})
	case "chain_finality":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: n.Finality()})
	case "txpool_status":
		pool := n.TxPool()
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: map[string]string{
			"pending": quantity(uint64(pool.PendingCount)),
			"queued":  quantity(uint64(pool.QueuedCount)),
		}})
	case "txpool_content":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: evmTxPool(n.TxPool())})
	case "chain_getAccount":
		var params struct {
			Address string `json:"address"`
		}
		if err := json.Unmarshal(request.Params, &params); err != nil || strings.TrimSpace(params.Address) == "" {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "address is required"})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: n.Account(params.Address)})
	case "chain_validators":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: n.Validators()})
	case "chain_sendTx":
		var tx types.Transaction
		if err := json.Unmarshal(request.Params, &tx); err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "invalid transaction"})
			return
		}
		if err := n.SubmitTx(tx); err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: map[string]string{"hash": tx.Hash()}})
	default:
		writeJSON(w, http.StatusNotFound, rpcResponse{ID: request.ID, Error: "unknown method"})
	}
}

func rpcParams(raw json.RawMessage) ([]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var params []any
	if err := json.Unmarshal(raw, &params); err != nil {
		return nil, err
	}
	return params, nil
}

func quantity(value uint64) string {
	return fmt.Sprintf("0x%x", value)
}

func chainNumber(chainID string) uint64 {
	if chainID == "chainlab-local" {
		return 31337
	}
	var value uint64
	for _, b := range []byte(chainID) {
		value = value*33 + uint64(b)
	}
	if value == 0 {
		return 1
	}
	return value
}

type blockTags struct {
	latest    uint64
	safe      uint64
	finalized uint64
}

func parseBlockNumber(value any, tags blockTags) (uint64, error) {
	raw, ok := value.(string)
	if !ok {
		return 0, fmt.Errorf("block number must be a string")
	}
	switch raw {
	case "latest":
		return tags.latest, nil
	case "safe":
		return tags.safe, nil
	case "finalized":
		return tags.finalized, nil
	case "earliest":
		return 0, nil
	default:
		if !strings.HasPrefix(raw, "0x") {
			return 0, fmt.Errorf("block number must be hex quantity")
		}
		parsed, err := strconv.ParseUint(strings.TrimPrefix(raw, "0x"), 16, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid block number")
		}
		return parsed, nil
	}
}

type callObject struct {
	from    string
	to      string
	txType  types.TxType
	method  string
	payload map[string]string
}

func parseCallObject(value any) (callObject, error) {
	raw, ok := value.(map[string]any)
	if !ok {
		return callObject{}, fmt.Errorf("call object must be an object")
	}
	call := callObject{txType: types.TxCall, payload: make(map[string]string)}
	if from, ok := raw["from"].(string); ok {
		call.from = from
	}
	if to, ok := raw["to"].(string); ok {
		call.to = to
	}
	if txType, ok := raw["type"].(string); ok && txType != "" {
		call.txType = types.TxType(txType)
	} else if call.to == "" {
		call.txType = types.TxTransfer
	}
	if payloadRaw, ok := raw["payload"]; ok {
		payload, err := stringMap(payloadRaw)
		if err != nil {
			return callObject{}, err
		}
		call.payload = payload
	}
	call.method = call.payload["method"]
	if method, ok := raw["method"].(string); ok && method != "" {
		call.method = method
		call.payload["method"] = method
	}
	return call, nil
}

func stringMap(value any) (map[string]string, error) {
	raw, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("payload must be an object")
	}
	output := make(map[string]string, len(raw))
	for key, value := range raw {
		switch typed := value.(type) {
		case string:
			output[key] = typed
		case float64:
			output[key] = strconv.FormatUint(uint64(typed), 10)
		default:
			return nil, fmt.Errorf("payload values must be strings or numbers")
		}
	}
	return output, nil
}

func dataHex(value string) string {
	return "0x" + hex.EncodeToString([]byte(value))
}

func evmTransaction(record types.TransactionRecord) map[string]any {
	tx := record.Transaction
	return map[string]any{
		"hash":             tx.Hash(),
		"blockHash":        record.BlockHash,
		"blockNumber":      quantity(record.BlockHeight),
		"transactionIndex": quantity(uint64(record.Index)),
		"from":             strings.ToLower(tx.From),
		"to":               nullableAddress(tx.To),
		"nonce":            quantity(tx.Nonce),
		"value":            quantity(tx.Value),
		"gas":              quantity(tx.GasLimit),
		"gasPrice":         quantity(tx.GasPrice),
		"input":            "0x",
		"type":             "0x0",
	}
}

func evmPendingTransaction(tx types.Transaction) map[string]any {
	return map[string]any{
		"hash":             tx.Hash(),
		"blockHash":        nil,
		"blockNumber":      nil,
		"transactionIndex": nil,
		"from":             strings.ToLower(tx.From),
		"to":               nullableAddress(tx.To),
		"nonce":            quantity(tx.Nonce),
		"value":            quantity(tx.Value),
		"gas":              quantity(tx.GasLimit),
		"gasPrice":         quantity(tx.GasPrice),
		"input":            "0x",
		"type":             "0x0",
	}
}

func evmTxPool(pool node.MempoolSnapshot) map[string]any {
	pending := make(map[string]map[string]any)
	for _, tx := range pool.Pending {
		from := strings.ToLower(tx.From)
		byNonce, ok := pending[from]
		if !ok {
			byNonce = make(map[string]any)
			pending[from] = byNonce
		}
		byNonce[quantity(tx.Nonce)] = evmPendingTransaction(tx)
	}
	return map[string]any{
		"pending": pending,
		"queued":  map[string]any{},
	}
}

func evmReceipt(record types.TransactionRecord, block types.Block) map[string]any {
	status := uint64(0)
	if record.Receipt.Success {
		status = 1
	}
	return map[string]any{
		"transactionHash":   record.Transaction.Hash(),
		"transactionIndex":  quantity(uint64(record.Index)),
		"blockHash":         record.BlockHash,
		"blockNumber":       quantity(record.BlockHeight),
		"from":              strings.ToLower(record.Transaction.From),
		"to":                nullableAddress(record.Transaction.To),
		"contractAddress":   nullableAddress(record.Receipt.ContractAddress),
		"cumulativeGasUsed": quantity(record.Receipt.GasUsed),
		"gasUsed":           quantity(record.Receipt.GasUsed),
		"status":            quantity(status),
		"logs":              evmTransactionLogs(block, record.Transaction.Hash()),
	}
}

func evmBlock(block types.Block, fullTransactions bool) map[string]any {
	transactions := make([]any, len(block.Transactions))
	for i, tx := range block.Transactions {
		if fullTransactions {
			transactions[i] = map[string]any{
				"hash":             tx.Hash(),
				"blockHash":        block.Hash(),
				"blockNumber":      quantity(block.Header.Height),
				"transactionIndex": quantity(uint64(i)),
				"from":             strings.ToLower(tx.From),
				"to":               nullableAddress(tx.To),
				"nonce":            quantity(tx.Nonce),
				"value":            quantity(tx.Value),
				"gas":              quantity(tx.GasLimit),
				"gasPrice":         quantity(tx.GasPrice),
				"input":            "0x",
				"type":             "0x0",
			}
		} else {
			transactions[i] = tx.Hash()
		}
	}
	return map[string]any{
		"number":           quantity(block.Header.Height),
		"hash":             block.Hash(),
		"parentHash":       block.Header.ParentHash,
		"timestamp":        quantity(uint64(block.Header.TimeUnix)),
		"transactionsRoot": block.Header.TxRoot,
		"receiptsRoot":     block.Header.ReceiptRoot,
		"stateRoot":        block.Header.StateRoot,
		"miner":            strings.ToLower(block.Header.Proposer),
		"transactions":     transactions,
	}
}

func nullableAddress(address string) any {
	if address == "" {
		return nil
	}
	return strings.ToLower(address)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
