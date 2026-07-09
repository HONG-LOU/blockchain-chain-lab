package rpc

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"chainlab/internal/core"
	"chainlab/internal/hash"
	"chainlab/internal/node"
	"chainlab/internal/types"
)

func NewServer(n *node.Node) http.Handler {
	return NewServerWithPeers(n, nil)
}

func NewServerWithPeers(n *node.Node, peers []string) http.Handler {
	server := &Server{
		node:       n,
		peers:      append([]string(nil), peers...),
		client:     &http.Client{Timeout: 5 * time.Second},
		filters:    newLogFilterStore(),
		wsNewHeads: make(map[string]chan types.Block),
		wsLogs:     make(map[string]*wsLogSubscription),
	}
	return server.routes()
}

type Server struct {
	node       *node.Node
	peers      []string
	client     *http.Client
	filters    *logFilterStore
	wsMu       sync.Mutex
	nextWSID   uint64
	wsNewHeads map[string]chan types.Block
	wsLogs     map[string]*wsLogSubscription
}

type wsLogSubscription struct {
	filter logFilter
	events chan map[string]any
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /explorer", s.handleExplorer)
	mux.HandleFunc("GET /explorer/events", s.handleExplorerEvents)
	mux.HandleFunc("GET /explorer/block/{height}", s.handleExplorerBlock)
	mux.HandleFunc("GET /explorer/tx/{hash}", s.handleExplorerTransaction)
	mux.HandleFunc("GET /explorer/account/{address}", s.handleExplorerAccount)
	mux.HandleFunc("GET /chain/head", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.node.Head())
	})
	mux.HandleFunc("GET /chain/finality", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.node.Finality())
	})
	mux.HandleFunc("GET /chain/finality/evidence", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.node.FinalityEvidence())
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
		s.notifyNewHead(block)
		s.notifyLogs(block)
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
	mux.HandleFunc("GET /proposal/{id}", func(w http.ResponseWriter, r *http.Request) {
		proposal := s.node.Proposal(r.PathValue("id"))
		if proposal.Status == "" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "proposal not found"})
			return
		}
		writeJSON(w, http.StatusOK, proposal)
	})
	mux.HandleFunc("GET /param/{key}", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(r.PathValue("key"))
		value := s.node.Param(key)
		if value == "" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "param not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"key": key, "value": value})
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
	mux.HandleFunc("POST /tx/raw", func(w http.ResponseWriter, r *http.Request) {
		tx, err := decodeRawTransaction(r)
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
	mux.HandleFunc("POST /faucet", func(w http.ResponseWriter, r *http.Request) {
		request, err := decodeFaucetRequest(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		tx, err := s.node.RequestFaucet(request.recipient(), request.Amount)
		if err != nil {
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
		if head := s.node.Head(); head.Hash() == block.Hash() {
			s.notifyNewHead(block)
			s.notifyLogs(block)
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "hash": block.Hash()})
	})
	mux.HandleFunc("POST /peer/finality-vote", func(w http.ResponseWriter, r *http.Request) {
		vote, err := decodeFinalityVote(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := s.node.SubmitFinalityVote(vote); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted", "validator": vote.Validator})
	})
	mux.HandleFunc("POST /rpc", s.handleJSONRPC)
	mux.HandleFunc("GET /rpc/ws", s.handleWebSocketJSONRPC)
	return mux
}

func decodeTransaction(r *http.Request) (types.Transaction, error) {
	var tx types.Transaction
	if err := json.NewDecoder(r.Body).Decode(&tx); err != nil {
		return types.Transaction{}, fmt.Errorf("invalid transaction json")
	}
	return tx, nil
}

func decodeRawTransaction(r *http.Request) (types.Transaction, error) {
	var request struct {
		Raw string `json:"raw"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		return types.Transaction{}, fmt.Errorf("invalid raw transaction json")
	}
	if strings.TrimSpace(request.Raw) == "" {
		return types.Transaction{}, fmt.Errorf("raw transaction is required")
	}
	tx, err := types.DecodeRawTransaction(request.Raw)
	if err != nil {
		return types.Transaction{}, err
	}
	return tx, nil
}

func decodeFinalityVote(r *http.Request) (types.FinalitySignature, error) {
	var vote types.FinalitySignature
	if err := json.NewDecoder(r.Body).Decode(&vote); err != nil {
		return types.FinalitySignature{}, fmt.Errorf("invalid finality vote json")
	}
	return vote, nil
}

type faucetRequest struct {
	Address string `json:"address"`
	To      string `json:"to"`
	Amount  uint64 `json:"amount"`
}

func (f faucetRequest) recipient() string {
	if strings.TrimSpace(f.Address) != "" {
		return f.Address
	}
	return f.To
}

func decodeFaucetRequest(r *http.Request) (faucetRequest, error) {
	var request faucetRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		return faucetRequest{}, fmt.Errorf("invalid faucet json")
	}
	return request, nil
}

func (s *Server) broadcastTransaction(tx types.Transaction) []string {
	return s.broadcast("/peer/tx", tx)
}

func (s *Server) broadcastBlock(block types.Block) []string {
	return s.broadcast("/peer/block", block)
}

func (s *Server) broadcastFinalityVote(vote types.FinalitySignature) []string {
	return s.broadcast("/peer/finality-vote", vote)
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

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

func (s *Server) handleWebSocketJSONRPC(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get("Sec-WebSocket-Key"))
	if !websocketUpgradeRequested(r) || key == "" || r.Header.Get("Sec-WebSocket-Version") != "13" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "websocket upgrade is required"})
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "websocket hijack is not supported"})
		return
	}
	conn, rw, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	if err := writeWebSocketHandshake(rw, key); err != nil {
		return
	}
	localSubscriptions := make(map[string]struct{})
	defer func() {
		for id := range localSubscriptions {
			s.unregisterWebSocketSubscription(id)
		}
	}()
	var writeMu sync.Mutex
	for {
		payload, err := readWebSocketTextFrame(rw.Reader)
		if err != nil {
			return
		}
		var request rpcRequest
		if err := json.Unmarshal([]byte(payload), &request); err != nil {
			writeWebSocketRPCResponse(conn, &writeMu, rpcResponse{Error: "invalid json"})
			continue
		}
		switch request.Method {
		case "eth_subscribe":
			params, err := rpcParams(request.Params)
			if err != nil || len(params) < 1 {
				writeWebSocketRPCResponse(conn, &writeMu, rpcResponse{ID: request.ID, Error: "subscription type is required"})
				continue
			}
			subscriptionType, ok := params[0].(string)
			if !ok {
				writeWebSocketRPCResponse(conn, &writeMu, rpcResponse{ID: request.ID, Error: "unsupported subscription"})
				continue
			}
			switch subscriptionType {
			case "newHeads":
				id, events := s.registerNewHeadSubscription()
				localSubscriptions[id] = struct{}{}
				go s.writeNewHeadNotifications(conn, &writeMu, id, events)
				writeWebSocketRPCResponse(conn, &writeMu, rpcResponse{ID: request.ID, Result: id})
			case "logs":
				filter, err := s.parseWebSocketLogSubscription(params)
				if err != nil {
					writeWebSocketRPCResponse(conn, &writeMu, rpcResponse{ID: request.ID, Error: err.Error()})
					continue
				}
				id, events := s.registerLogSubscription(filter)
				localSubscriptions[id] = struct{}{}
				go s.writeLogNotifications(conn, &writeMu, id, events)
				writeWebSocketRPCResponse(conn, &writeMu, rpcResponse{ID: request.ID, Result: id})
			default:
				writeWebSocketRPCResponse(conn, &writeMu, rpcResponse{ID: request.ID, Error: "unsupported subscription"})
			}
		case "eth_unsubscribe":
			params, err := rpcParams(request.Params)
			if err != nil || len(params) < 1 {
				writeWebSocketRPCResponse(conn, &writeMu, rpcResponse{ID: request.ID, Error: "subscription id is required"})
				continue
			}
			id, ok := params[0].(string)
			if !ok {
				writeWebSocketRPCResponse(conn, &writeMu, rpcResponse{ID: request.ID, Error: "subscription id must be a string"})
				continue
			}
			_, local := localSubscriptions[id]
			if local {
				delete(localSubscriptions, id)
			}
			writeWebSocketRPCResponse(conn, &writeMu, rpcResponse{ID: request.ID, Result: local && s.unregisterWebSocketSubscription(id)})
		default:
			writeWebSocketRPCResponse(conn, &writeMu, rpcResponse{ID: request.ID, Error: "unknown method"})
		}
	}
}

func websocketUpgradeRequested(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") && strings.Contains(strings.ToLower(r.Header.Get("Connection")), "upgrade")
}

func writeWebSocketHandshake(writer *bufio.ReadWriter, key string) error {
	accept := websocketAcceptKey(key)
	_, err := fmt.Fprintf(writer, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", accept)
	if err != nil {
		return err
	}
	return writer.Flush()
}

func websocketAcceptKey(key string) string {
	sum := sha1.Sum([]byte(key + websocketGUID))
	return base64.StdEncoding.EncodeToString(sum[:])
}

func readWebSocketTextFrame(reader *bufio.Reader) (string, error) {
	first, err := reader.ReadByte()
	if err != nil {
		return "", err
	}
	opcode := first & 0x0f
	if opcode == 0x8 {
		return "", io.EOF
	}
	if opcode != 0x1 {
		return "", fmt.Errorf("unsupported websocket opcode %d", opcode)
	}
	second, err := reader.ReadByte()
	if err != nil {
		return "", err
	}
	masked := second&0x80 != 0
	if !masked {
		return "", errors.New("client websocket frames must be masked")
	}
	length := uint64(second & 0x7f)
	switch length {
	case 126:
		extended := make([]byte, 2)
		if _, err := io.ReadFull(reader, extended); err != nil {
			return "", err
		}
		length = uint64(extended[0])<<8 | uint64(extended[1])
	case 127:
		return "", errors.New("websocket payload is too large")
	}
	var mask [4]byte
	if _, err := io.ReadFull(reader, mask[:]); err != nil {
		return "", err
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return "", err
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return string(payload), nil
}

func writeWebSocketRPCResponse(conn net.Conn, writeMu *sync.Mutex, response rpcResponse) {
	writeWebSocketJSON(conn, writeMu, response)
}

func writeWebSocketJSON(conn net.Conn, writeMu *sync.Mutex, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		return
	}
	writeMu.Lock()
	defer writeMu.Unlock()
	_ = writeWebSocketTextFrame(conn, payload)
}

func writeWebSocketTextFrame(conn net.Conn, payload []byte) error {
	header := []byte{0x81}
	switch {
	case len(payload) <= 125:
		header = append(header, byte(len(payload)))
	case len(payload) <= 65535:
		header = append(header, 126, byte(len(payload)>>8), byte(len(payload)))
	default:
		return errors.New("websocket payload is too large")
	}
	if _, err := conn.Write(header); err != nil {
		return err
	}
	_, err := conn.Write(payload)
	return err
}

func (s *Server) registerNewHeadSubscription() (string, <-chan types.Block) {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	s.nextWSID++
	id := quantity(s.nextWSID)
	events := make(chan types.Block, 8)
	s.wsNewHeads[id] = events
	return id, events
}

func (s *Server) unregisterNewHeadSubscription(id string) bool {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	events, ok := s.wsNewHeads[id]
	if !ok {
		return false
	}
	delete(s.wsNewHeads, id)
	close(events)
	return true
}

func (s *Server) registerLogSubscription(filter logFilter) (string, <-chan map[string]any) {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	s.nextWSID++
	id := quantity(s.nextWSID)
	events := make(chan map[string]any, 16)
	s.wsLogs[id] = &wsLogSubscription{filter: filter, events: events}
	return id, events
}

func (s *Server) unregisterLogSubscription(id string) bool {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	subscription, ok := s.wsLogs[id]
	if !ok {
		return false
	}
	delete(s.wsLogs, id)
	close(subscription.events)
	return true
}

func (s *Server) unregisterWebSocketSubscription(id string) bool {
	if s.unregisterNewHeadSubscription(id) {
		return true
	}
	return s.unregisterLogSubscription(id)
}

func (s *Server) notifyNewHead(block types.Block) {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	for _, events := range s.wsNewHeads {
		select {
		case events <- block:
		default:
		}
	}
}

func (s *Server) notifyLogs(block types.Block) {
	s.wsMu.Lock()
	defer s.wsMu.Unlock()
	for _, subscription := range s.wsLogs {
		for _, log := range evmBlockLogs(block, subscription.filter) {
			select {
			case subscription.events <- log:
			default:
			}
		}
	}
}

func (s *Server) writeNewHeadNotifications(conn net.Conn, writeMu *sync.Mutex, id string, events <-chan types.Block) {
	for block := range events {
		writeWebSocketJSON(conn, writeMu, map[string]any{
			"jsonrpc": "2.0",
			"method":  "eth_subscription",
			"params": map[string]any{
				"subscription": id,
				"result":       evmBlockHeader(block),
			},
		})
	}
}

func (s *Server) writeLogNotifications(conn net.Conn, writeMu *sync.Mutex, id string, events <-chan map[string]any) {
	for log := range events {
		writeWebSocketJSON(conn, writeMu, map[string]any{
			"jsonrpc": "2.0",
			"method":  "eth_subscription",
			"params": map[string]any{
				"subscription": id,
				"result":       log,
			},
		})
	}
}

func (s *Server) parseWebSocketLogSubscription(params []any) (logFilter, error) {
	if len(params) < 2 || params[1] == nil {
		return logFilter{}, nil
	}
	finality := s.node.Finality()
	return parseLogFilter(params[1], blockTags{
		latest:    finality.HeadHeight,
		safe:      finality.SafeHeight,
		finalized: finality.FinalizedHeight,
	})
}

func (s *Server) handleJSONRPC(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{Error: "invalid json"})
		return
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		writeJSON(w, http.StatusBadRequest, rpcResponse{Error: "invalid json"})
		return
	}
	if raw[0] == '[' {
		var requests []rpcRequest
		if err := json.Unmarshal(raw, &requests); err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{Error: "invalid json"})
			return
		}
		if len(requests) == 0 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{Error: "batch must include at least one request"})
			return
		}
		responses := make([]rpcResponse, 0, len(requests))
		for _, request := range requests {
			responses = append(responses, s.responseForJSONRPCBatchRequest(request))
		}
		writeJSON(w, http.StatusOK, responses)
		return
	}

	var request rpcRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{Error: "invalid json"})
		return
	}
	s.handleSingleJSONRPC(w, request)
}

func (s *Server) responseForJSONRPCBatchRequest(request rpcRequest) rpcResponse {
	recorder := newRPCResponseRecorder()
	s.handleSingleJSONRPC(recorder, request)
	var response rpcResponse
	if err := json.Unmarshal(recorder.body.Bytes(), &response); err != nil {
		return rpcResponse{ID: request.ID, Error: "invalid response"}
	}
	return response
}

type rpcResponseRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newRPCResponseRecorder() *rpcResponseRecorder {
	return &rpcResponseRecorder{header: make(http.Header)}
}

func (r *rpcResponseRecorder) Header() http.Header {
	return r.header
}

func (r *rpcResponseRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *rpcResponseRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(data)
}

func (s *Server) handleSingleJSONRPC(w http.ResponseWriter, request rpcRequest) {
	n := s.node
	switch request.Method {
	case "web3_clientVersion":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: "ChainLab/dev"})
	case "net_version":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: strconv.FormatUint(chainNumber(n.ChainID()), 10)})
	case "net_listening":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: true})
	case "eth_accounts":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: []string{n.Proposer()}})
	case "eth_coinbase":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: n.Proposer()})
	case "eth_mining":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: localProposerIsValidator(n)})
	case "eth_hashrate":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: quantity(0)})
	case "eth_chainId":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: quantity(chainNumber(n.ChainID()))})
	case "eth_syncing":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: false})
	case "eth_blockNumber":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: quantity(n.Head().Header.Height)})
	case "eth_gasPrice":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: quantity(n.FeeMarket().GasPrice)})
	case "eth_maxPriorityFeePerGas":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: quantity(n.FeeMarket().MaxPriorityFeePerGas)})
	case "eth_feeHistory":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 2 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "block count and newest block are required"})
			return
		}
		history, err := feeHistory(n, params)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: history})
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
	case "eth_getCode":
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
		if err := validateOptionalBlockTag(n, params, 1); err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: codeIDHex(n.Account(address).CodeID)})
	case "eth_getStorageAt":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 2 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "address and storage slot are required"})
			return
		}
		address, ok := params[0].(string)
		if !ok || strings.TrimSpace(address) == "" {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "address is required"})
			return
		}
		key, err := parseStorageKey(params[1])
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		if err := validateOptionalBlockTag(n, params, 2); err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		value := n.Account(address).Storage[key]
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: abiStorageWordHex(value)})
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
			if tx, ok := pendingTransactionByHash(n.TxPool(), txHash); ok {
				writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: evmPendingTransaction(tx)})
				return
			}
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
	case "debug_traceTransaction":
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
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: debugTransactionTrace(record)})
	case "eth_getBlockReceipts":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 1 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "block number or hash is required"})
			return
		}
		block, ok, err := blockByNumberOrHash(n, params[0])
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		if !ok {
			writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: nil})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: evmBlockReceipts(block)})
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
	case "eth_getBlockByHash":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 1 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "block hash is required"})
			return
		}
		blockHash, ok := params[0].(string)
		if !ok || strings.TrimSpace(blockHash) == "" {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "block hash is required"})
			return
		}
		block, ok := n.BlockByHash(blockHash)
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
	case "eth_getBlockTransactionCountByHash":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 1 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "block hash is required"})
			return
		}
		blockHash, ok := params[0].(string)
		if !ok || strings.TrimSpace(blockHash) == "" {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "block hash is required"})
			return
		}
		block, ok := n.BlockByHash(blockHash)
		if !ok {
			writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: nil})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: quantity(uint64(len(block.Transactions)))})
	case "eth_getBlockTransactionCountByNumber":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 1 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "block number is required"})
			return
		}
		height, err := parseRPCBlockNumber(n, params[0])
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		block, ok := n.Block(height)
		if !ok {
			writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: nil})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: quantity(uint64(len(block.Transactions)))})
	case "eth_getTransactionByBlockHashAndIndex":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 2 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "block hash and transaction index are required"})
			return
		}
		blockHash, ok := params[0].(string)
		if !ok || strings.TrimSpace(blockHash) == "" {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "block hash is required"})
			return
		}
		index, err := uint64RPCValue(params[1], "transaction index")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		block, ok := n.BlockByHash(blockHash)
		if !ok || index >= uint64(len(block.Transactions)) {
			writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: nil})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: evmBlockTransaction(block, int(index))})
	case "eth_getTransactionByBlockNumberAndIndex":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 2 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "block number and transaction index are required"})
			return
		}
		height, err := parseRPCBlockNumber(n, params[0])
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		index, err := uint64RPCValue(params[1], "transaction index")
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		block, ok := n.Block(height)
		if !ok || index >= uint64(len(block.Transactions)) {
			writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: nil})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: evmBlockTransaction(block, int(index))})
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
	case "eth_newFilter":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 1 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "filter is required"})
			return
		}
		finality := n.Finality()
		filter, nextBlock, err := parseNewLogFilter(params[0], blockTags{
			latest:    finality.HeadHeight,
			safe:      finality.SafeHeight,
			finalized: finality.FinalizedHeight,
		})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: s.registerLogFilter(filter, nextBlock)})
	case "eth_newBlockFilter":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: s.registerBlockFilter(n.Finality().HeadHeight + 1)})
	case "eth_newPendingTransactionFilter":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: s.registerPendingTransactionFilter()})
	case "eth_getFilterLogs":
		params, err := rpcParams(request.Params)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "filter id is required"})
			return
		}
		filterID, err := filterIDParam(params)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		logs, ok := s.logFilterLogs(filterID)
		if !ok {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "filter not found"})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: logs})
	case "eth_getFilterChanges":
		params, err := rpcParams(request.Params)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "filter id is required"})
			return
		}
		filterID, err := filterIDParam(params)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		changes, ok := s.filterChanges(filterID)
		if !ok {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "filter not found"})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: changes})
	case "eth_uninstallFilter":
		params, err := rpcParams(request.Params)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "filter id is required"})
			return
		}
		filterID, err := filterIDParam(params)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: s.uninstallFilter(filterID)})
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
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: abiEncodeCallResult(call.method, result)})
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
		var gas uint64
		if call.txType == types.TxBatch {
			gas, err = core.EstimateGasForBatch(call.batch)
		} else {
			gas, err = core.EstimateGasForPayload(call.txType, call.payload)
		}
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: quantity(gas)})
	case "eth_sendRawTransaction":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 1 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "raw transaction is required"})
			return
		}
		raw, ok := params[0].(string)
		if !ok || strings.TrimSpace(raw) == "" {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "raw transaction must be a string"})
			return
		}
		tx, err := types.DecodeRawTransaction(raw)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		if err := n.SubmitTx(tx); err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: tx.Hash()})
	case "chain_head":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: n.Head()})
	case "chain_finality":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: n.Finality()})
	case "chain_finalityEvidence":
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: n.FinalityEvidence()})
	case "chain_sendFinalityVote":
		vote, err := parseRPCFinalityVoteParam(request.Params)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		if err := n.SubmitFinalityVote(vote); err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		_ = s.broadcastFinalityVote(vote)
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: n.Finality()})
	case "chain_feeMarket":
		feeMarket := n.FeeMarket()
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: map[string]string{
			"base_fee_per_gas":         quantity(feeMarket.BaseFeePerGas),
			"next_base_fee_per_gas":    quantity(feeMarket.NextBaseFeePerGas),
			"max_priority_fee_per_gas": quantity(feeMarket.MaxPriorityFeePerGas),
			"gas_price":                quantity(feeMarket.GasPrice),
			"block_gas_limit":          quantity(feeMarket.BlockGasLimit),
			"last_block_gas_used":      quantity(feeMarket.LastBlockGasUsed),
		}})
	case "chain_faucet":
		faucet, err := parseRPCFaucetRequest(request.Params)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		tx, err := n.RequestFaucet(faucet.recipient(), faucet.Amount)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: map[string]string{"hash": tx.Hash()}})
	case "chain_sendUserOperation":
		tx, err := parseRPCTransactionParam(request.Params)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		if strings.TrimSpace(tx.Paymaster) == "" {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "paymaster is required"})
			return
		}
		if err := n.SubmitTx(tx); err != nil {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: map[string]string{"hash": tx.Hash()}})
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
	case "chain_proposal":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 1 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "proposal id is required"})
			return
		}
		proposalID, ok := params[0].(string)
		if !ok || strings.TrimSpace(proposalID) == "" {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "proposal id is required"})
			return
		}
		proposal := n.Proposal(proposalID)
		if proposal.Status == "" {
			writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: nil})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: proposal})
	case "chain_param":
		params, err := rpcParams(request.Params)
		if err != nil || len(params) < 1 {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "param key is required"})
			return
		}
		key, ok := params[0].(string)
		if !ok || strings.TrimSpace(key) == "" {
			writeJSON(w, http.StatusBadRequest, rpcResponse{ID: request.ID, Error: "param key is required"})
			return
		}
		writeJSON(w, http.StatusOK, rpcResponse{ID: request.ID, Result: n.Param(key)})
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

func parseRPCFinalityVoteParam(raw json.RawMessage) (types.FinalitySignature, error) {
	if len(raw) == 0 {
		return types.FinalitySignature{}, fmt.Errorf("finality vote is required")
	}
	var params []types.FinalitySignature
	if err := json.Unmarshal(raw, &params); err == nil && len(params) > 0 {
		return params[0], nil
	}
	var vote types.FinalitySignature
	if err := json.Unmarshal(raw, &vote); err != nil {
		return types.FinalitySignature{}, fmt.Errorf("invalid finality vote")
	}
	return vote, nil
}

func parseRPCTransactionParam(raw json.RawMessage) (types.Transaction, error) {
	if len(raw) == 0 {
		return types.Transaction{}, fmt.Errorf("transaction is required")
	}
	var params []types.Transaction
	if err := json.Unmarshal(raw, &params); err == nil && len(params) > 0 {
		return params[0], nil
	}
	var tx types.Transaction
	if err := json.Unmarshal(raw, &tx); err != nil {
		return types.Transaction{}, fmt.Errorf("invalid transaction")
	}
	if tx.ChainID == "" {
		return types.Transaction{}, fmt.Errorf("transaction is required")
	}
	return tx, nil
}

func parseRPCFaucetRequest(raw json.RawMessage) (faucetRequest, error) {
	if len(raw) == 0 {
		return faucetRequest{}, fmt.Errorf("faucet request is required")
	}
	var direct faucetRequest
	if err := json.Unmarshal(raw, &direct); err == nil {
		return direct, nil
	}
	var params []faucetRequest
	if err := json.Unmarshal(raw, &params); err != nil || len(params) == 0 {
		return faucetRequest{}, fmt.Errorf("faucet request is required")
	}
	return params[0], nil
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

func validateOptionalBlockTag(n *node.Node, params []any, index int) error {
	if len(params) <= index {
		return nil
	}
	_, err := parseRPCBlockNumber(n, params[index])
	return err
}

func localProposerIsValidator(n *node.Node) bool {
	proposer := strings.ToLower(n.Proposer())
	for _, validator := range n.Validators() {
		if strings.ToLower(validator) == proposer {
			return true
		}
	}
	return false
}

func parseRPCBlockNumber(n *node.Node, value any) (uint64, error) {
	finality := n.Finality()
	return parseBlockNumber(value, blockTags{
		latest:    finality.HeadHeight,
		safe:      finality.SafeHeight,
		finalized: finality.FinalizedHeight,
	})
}

func blockByNumberOrHash(n *node.Node, value any) (types.Block, bool, error) {
	raw, ok := value.(string)
	if !ok {
		return types.Block{}, false, fmt.Errorf("block number or hash must be a string")
	}
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" {
		return types.Block{}, false, fmt.Errorf("block number or hash is required")
	}
	if looksLikeBlockHash(raw) {
		block, ok := n.BlockByHash(raw)
		return block, ok, nil
	}
	height, err := parseRPCBlockNumber(n, raw)
	if err != nil {
		return types.Block{}, false, err
	}
	block, ok := n.Block(height)
	return block, ok, nil
}

func looksLikeBlockHash(value string) bool {
	if len(value) != 66 || !strings.HasPrefix(value, "0x") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "0x"))
	return err == nil
}

func feeHistory(n *node.Node, params []any) (map[string]any, error) {
	blockCount, err := uint64RPCValue(params[0], "block count")
	if err != nil {
		return nil, err
	}
	if blockCount == 0 {
		return nil, fmt.Errorf("block count must be positive")
	}
	newest, err := parseRPCBlockNumber(n, params[1])
	if err != nil {
		return nil, err
	}
	if _, ok := n.Block(newest); !ok {
		return nil, fmt.Errorf("newest block not found")
	}
	percentiles, err := rewardPercentiles(params)
	if err != nil {
		return nil, err
	}

	oldest := uint64(0)
	if blockCount <= newest+1 {
		oldest = newest - blockCount + 1
	}
	actualCount := newest - oldest + 1
	baseFees := make([]string, 0, actualCount+1)
	gasUsedRatios := make([]float64, 0, actualCount)
	rewards := make([][]string, 0, actualCount)
	for height := oldest; height <= newest; height++ {
		block, ok := n.Block(height)
		if !ok {
			return nil, fmt.Errorf("block %s not found", quantity(height))
		}
		baseFees = append(baseFees, quantity(blockBaseFee(block)))
		gasUsedRatios = append(gasUsedRatios, blockGasUsedRatio(block))
		if len(percentiles) > 0 {
			rewards = append(rewards, blockRewardPercentiles(block, percentiles))
		}
	}
	nextBaseFee, err := feeHistoryNextBaseFee(n, newest)
	if err != nil {
		return nil, err
	}
	baseFees = append(baseFees, quantity(nextBaseFee))

	result := map[string]any{
		"oldestBlock":   quantity(oldest),
		"baseFeePerGas": baseFees,
		"gasUsedRatio":  gasUsedRatios,
	}
	if len(percentiles) > 0 {
		result["reward"] = rewards
	}
	return result, nil
}

func feeHistoryNextBaseFee(n *node.Node, newest uint64) (uint64, error) {
	if next, ok := n.Block(newest + 1); ok {
		return blockBaseFee(next), nil
	}
	block, ok := n.Block(newest)
	if !ok {
		return 0, fmt.Errorf("newest block not found")
	}
	return node.NextBaseFee(block, node.DefaultBlockGasLimit), nil
}

func blockBaseFee(block types.Block) uint64 {
	if block.Header.BaseFeePerGas == 0 {
		return node.InitialBaseFeePerGas
	}
	return block.Header.BaseFeePerGas
}

func blockGasUsedRatio(block types.Block) float64 {
	gasLimit := block.Header.GasLimit
	if gasLimit == 0 {
		gasLimit = node.DefaultBlockGasLimit
	}
	return float64(block.Header.GasUsed) / float64(gasLimit)
}

func rewardPercentiles(params []any) ([]float64, error) {
	if len(params) < 3 || params[2] == nil {
		return nil, nil
	}
	raw, ok := params[2].([]any)
	if !ok {
		return nil, fmt.Errorf("reward percentiles must be an array")
	}
	percentiles := make([]float64, 0, len(raw))
	for _, value := range raw {
		percentile, err := rewardPercentile(value)
		if err != nil {
			return nil, err
		}
		percentiles = append(percentiles, percentile)
	}
	return percentiles, nil
}

func rewardPercentile(value any) (float64, error) {
	switch typed := value.(type) {
	case float64:
		if typed < 0 || typed > 100 {
			return 0, fmt.Errorf("reward percentile must be between 0 and 100")
		}
		return typed, nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		if err != nil || parsed < 0 || parsed > 100 {
			return 0, fmt.Errorf("reward percentile must be between 0 and 100")
		}
		return parsed, nil
	default:
		return 0, fmt.Errorf("reward percentile must be a number")
	}
}

type rewardSample struct {
	rewardPerGas uint64
	gasUsed      uint64
}

func blockRewardPercentiles(block types.Block, percentiles []float64) []string {
	samples := blockRewardSamples(block)
	output := make([]string, len(percentiles))
	if len(samples) == 0 {
		for i := range output {
			output[i] = quantity(0)
		}
		return output
	}
	sort.Slice(samples, func(i int, j int) bool {
		return samples[i].rewardPerGas < samples[j].rewardPerGas
	})
	var totalGas uint64
	for _, sample := range samples {
		totalGas += sample.gasUsed
	}
	for i, percentile := range percentiles {
		output[i] = quantity(weightedRewardPercentile(samples, totalGas, percentile))
	}
	return output
}

func blockRewardSamples(block types.Block) []rewardSample {
	samples := make([]rewardSample, 0, len(block.Receipts))
	for _, receipt := range block.Receipts {
		if receipt.GasUsed == 0 {
			continue
		}
		samples = append(samples, rewardSample{
			rewardPerGas: receipt.PriorityFeePaid / receipt.GasUsed,
			gasUsed:      receipt.GasUsed,
		})
	}
	return samples
}

func weightedRewardPercentile(samples []rewardSample, totalGas uint64, percentile float64) uint64 {
	if totalGas == 0 {
		return 0
	}
	threshold := uint64(float64(totalGas) * percentile / 100)
	if percentile > 0 && threshold == 0 {
		threshold = 1
	}
	var cumulative uint64
	for _, sample := range samples {
		cumulative += sample.gasUsed
		if cumulative >= threshold {
			return sample.rewardPerGas
		}
	}
	return samples[len(samples)-1].rewardPerGas
}

func codeIDHex(codeID string) string {
	codeID = strings.TrimSpace(codeID)
	if codeID == "" {
		return "0x"
	}
	return "0x" + hex.EncodeToString([]byte(codeID))
}

func parseStorageKey(value any) (string, error) {
	raw, ok := value.(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("storage slot must be a string")
	}
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "0x") {
		return raw, nil
	}
	encoded := strings.TrimPrefix(raw, "0x")
	if len(encoded)%2 != 0 {
		encoded = "0" + encoded
	}
	decoded, err := hex.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("storage slot hex is invalid")
	}
	decoded = bytes.TrimLeft(decoded, "\x00")
	return string(decoded), nil
}

func abiStorageWordHex(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return abiUint256Hex(0)
	}
	if parsed, err := strconv.ParseUint(trimmed, 10, 64); err == nil {
		return abiUint256Hex(parsed)
	}
	if isHexAddress(trimmed) {
		return abiAddressHex(trimmed)
	}
	word := make([]byte, 32)
	copy(word, []byte(value))
	return "0x" + hex.EncodeToString(word)
}

type callObject struct {
	from    string
	to      string
	txType  types.TxType
	method  string
	payload map[string]string
	batch   []types.BatchOperation
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
	if batchRaw, ok := raw["batch"]; ok {
		batch, err := batchOperations(batchRaw)
		if err != nil {
			return callObject{}, err
		}
		call.batch = batch
		call.txType = types.TxBatch
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
	if data, ok := callData(raw); ok {
		payload, err := parseCallData(data)
		if err != nil {
			return callObject{}, err
		}
		call.payload = payload
		call.method = payload["method"]
		call.txType = types.TxCall
	}
	return call, nil
}

func callData(raw map[string]any) (string, bool) {
	for _, field := range []string{"data", "input"} {
		value, ok := raw[field].(string)
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" || value == "0x" {
			continue
		}
		return value, true
	}
	return "", false
}

func parseCallData(value string) (map[string]string, error) {
	raw := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(value)), "0x")
	if len(raw) < 8 || len(raw)%2 != 0 {
		return nil, fmt.Errorf("call data must include a 4-byte selector")
	}
	selector := raw[:8]
	args := raw[8:]
	switch selector {
	case abiSelector("get()"):
		if args != "" {
			return nil, fmt.Errorf("get() call data must not include arguments")
		}
		return map[string]string{"method": "get"}, nil
	case abiSelector("symbol()"):
		if args != "" {
			return nil, fmt.Errorf("symbol() call data must not include arguments")
		}
		return map[string]string{"method": "symbol"}, nil
	case abiSelector("owner()"):
		if args != "" {
			return nil, fmt.Errorf("owner() call data must not include arguments")
		}
		return map[string]string{"method": "owner"}, nil
	case abiSelector("balanceOf(address)"):
		address, err := parseABIAddressArgument(args)
		if err != nil {
			return nil, err
		}
		return map[string]string{"method": "balanceOf", "address": address}, nil
	default:
		return nil, fmt.Errorf("unsupported call data selector 0x%s", selector)
	}
}

func abiSelector(signature string) string {
	return hex.EncodeToString(hash.Keccak([]byte(signature))[:4])
}

func parseABIAddressArgument(args string) (string, error) {
	if len(args) != 64 {
		return "", fmt.Errorf("balanceOf(address) call data must include one address argument")
	}
	if _, err := hex.DecodeString(args); err != nil {
		return "", fmt.Errorf("invalid ABI address argument")
	}
	if strings.Trim(args[:24], "0") != "" {
		return "", fmt.Errorf("ABI address argument must be left padded")
	}
	return "0x" + args[24:], nil
}

func batchOperations(value any) ([]types.BatchOperation, error) {
	raw, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("batch must be an array")
	}
	operations := make([]types.BatchOperation, 0, len(raw))
	for index, item := range raw {
		object, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("batch operation %d must be an object", index)
		}
		operation := types.BatchOperation{}
		if txType, ok := object["type"].(string); ok && strings.TrimSpace(txType) != "" {
			operation.Type = types.TxType(txType)
		} else {
			return nil, fmt.Errorf("batch operation %d type is required", index)
		}
		if to, ok := object["to"].(string); ok {
			operation.To = to
		}
		if value, ok := object["value"]; ok {
			parsed, err := uint64RPCValue(value, "value")
			if err != nil {
				return nil, fmt.Errorf("batch operation %d: %w", index, err)
			}
			operation.Value = parsed
		}
		if payloadRaw, ok := object["payload"]; ok {
			payload, err := stringMap(payloadRaw)
			if err != nil {
				return nil, fmt.Errorf("batch operation %d: %w", index, err)
			}
			operation.Payload = payload
		}
		operations = append(operations, operation)
	}
	return operations, nil
}

func uint64RPCValue(value any, field string) (uint64, error) {
	switch typed := value.(type) {
	case string:
		raw := strings.TrimSpace(typed)
		if strings.HasPrefix(raw, "0x") {
			parsed, err := strconv.ParseUint(strings.TrimPrefix(raw, "0x"), 16, 64)
			if err != nil {
				return 0, fmt.Errorf("%s must be a uint64 quantity", field)
			}
			return parsed, nil
		}
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("%s must be a uint64 quantity", field)
		}
		return parsed, nil
	case float64:
		if typed < 0 || typed != float64(uint64(typed)) {
			return 0, fmt.Errorf("%s must be a uint64 quantity", field)
		}
		return uint64(typed), nil
	default:
		return 0, fmt.Errorf("%s must be a uint64 quantity", field)
	}
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

func abiEncodeCallResult(method string, value string) string {
	if isAddressReturn(method, value) {
		return abiAddressHex(value)
	}
	if isUintReturn(method, value) {
		parsed, _ := strconv.ParseUint(value, 10, 64)
		return abiUint256Hex(parsed)
	}
	return abiStringHex(value)
}

func isUintReturn(method string, value string) bool {
	switch method {
	case "get", "balanceOf", "threshold":
		_, err := strconv.ParseUint(value, 10, 64)
		return err == nil
	default:
		return false
	}
}

func isAddressReturn(method string, value string) bool {
	return method == "owner" && isHexAddress(value)
}

func isHexAddress(value string) bool {
	if len(value) != 42 || !strings.HasPrefix(value, "0x") {
		return false
	}
	_, err := hex.DecodeString(value[2:])
	return err == nil
}

func abiUint256Hex(value uint64) string {
	return fmt.Sprintf("0x%064x", value)
}

func abiAddressHex(value string) string {
	return "0x" + strings.Repeat("0", 24) + strings.ToLower(strings.TrimPrefix(value, "0x"))
}

func abiStringHex(value string) string {
	raw := hex.EncodeToString([]byte(value))
	padding := strings.Repeat("0", (64-len(raw)%64)%64)
	return "0x" + fmt.Sprintf("%064x%064x", 32, len([]byte(value))) + raw + padding
}

func evmTransaction(record types.TransactionRecord) map[string]any {
	return evmTransactionObject(record.Transaction, record.BlockHash, record.BlockHeight, record.Index)
}

func evmBlockTransaction(block types.Block, index int) map[string]any {
	return evmTransactionObject(block.Transactions[index], block.Hash(), block.Header.Height, index)
}

func evmTransactionObject(tx types.Transaction, blockHash string, blockHeight uint64, index int) map[string]any {
	return map[string]any{
		"hash":                 tx.Hash(),
		"blockHash":            blockHash,
		"blockNumber":          quantity(blockHeight),
		"transactionIndex":     quantity(uint64(index)),
		"from":                 strings.ToLower(tx.From),
		"signer":               nullableAddress(tx.Signer),
		"to":                   nullableAddress(tx.To),
		"nonce":                quantity(tx.Nonce),
		"value":                quantity(tx.Value),
		"gas":                  quantity(tx.GasLimit),
		"gasPrice":             quantity(tx.GasPrice),
		"maxFeePerGas":         quantity(tx.MaxFeePerGas),
		"maxPriorityFeePerGas": quantity(tx.MaxPriorityFeePerGas),
		"chainType":            string(tx.Type),
		"batchOperationCount":  quantity(uint64(len(tx.Batch))),
		"authorizationCount":   quantity(uint64(len(tx.Authorizations))),
		"paymaster":            nullableAddress(tx.Paymaster),
		"input":                "0x",
		"type":                 "0x0",
	}
}

func evmPendingTransaction(tx types.Transaction) map[string]any {
	return map[string]any{
		"hash":                 tx.Hash(),
		"blockHash":            nil,
		"blockNumber":          nil,
		"transactionIndex":     nil,
		"from":                 strings.ToLower(tx.From),
		"signer":               nullableAddress(tx.Signer),
		"to":                   nullableAddress(tx.To),
		"nonce":                quantity(tx.Nonce),
		"value":                quantity(tx.Value),
		"gas":                  quantity(tx.GasLimit),
		"gasPrice":             quantity(tx.GasPrice),
		"maxFeePerGas":         quantity(tx.MaxFeePerGas),
		"maxPriorityFeePerGas": quantity(tx.MaxPriorityFeePerGas),
		"chainType":            string(tx.Type),
		"batchOperationCount":  quantity(uint64(len(tx.Batch))),
		"authorizationCount":   quantity(uint64(len(tx.Authorizations))),
		"paymaster":            nullableAddress(tx.Paymaster),
		"input":                "0x",
		"type":                 "0x0",
	}
}

func pendingTransactionByHash(pool node.MempoolSnapshot, txHash string) (types.Transaction, bool) {
	txHash = strings.ToLower(strings.TrimSpace(txHash))
	if txHash == "" {
		return types.Transaction{}, false
	}
	for _, tx := range pool.Pending {
		if strings.ToLower(tx.Hash()) == txHash {
			return tx, true
		}
	}
	return types.Transaction{}, false
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
		"effectiveGasPrice": quantity(record.Receipt.EffectiveGasPrice),
		"feePayer":          nullableAddress(record.Receipt.FeePayer),
		"status":            quantity(status),
		"logs":              evmTransactionLogs(block, record.Transaction.Hash()),
	}
}

func evmBlockReceipts(block types.Block) []map[string]any {
	receipts := make([]map[string]any, 0, len(block.Transactions))
	blockHash := block.Hash()
	for i, tx := range block.Transactions {
		receipt := types.Receipt{TxHash: tx.Hash()}
		if i < len(block.Receipts) {
			receipt = block.Receipts[i]
		}
		receipts = append(receipts, evmReceipt(types.TransactionRecord{
			Transaction: tx,
			Receipt:     receipt,
			BlockHeight: block.Header.Height,
			BlockHash:   blockHash,
			Index:       i,
		}, block))
	}
	return receipts
}

func debugTransactionTrace(record types.TransactionRecord) map[string]any {
	return map[string]any{
		"gas":         quantity(record.Receipt.GasUsed),
		"failed":      !record.Receipt.Success,
		"returnValue": "0x",
		"structLogs":  []any{},
		"chainLab":    chainLabTraceSummary(record),
	}
}

func chainLabTraceSummary(record types.TransactionRecord) map[string]any {
	return map[string]any{
		"transactionHash":   record.Transaction.Hash(),
		"blockHash":         record.BlockHash,
		"blockNumber":       quantity(record.BlockHeight),
		"transactionIndex":  quantity(uint64(record.Index)),
		"type":              string(record.Transaction.Type),
		"from":              strings.ToLower(record.Transaction.From),
		"signer":            nullableAddress(record.Transaction.Signer),
		"to":                nullableAddress(record.Transaction.To),
		"value":             quantity(record.Transaction.Value),
		"gasLimit":          quantity(record.Transaction.GasLimit),
		"gasUsed":           quantity(record.Receipt.GasUsed),
		"effectiveGasPrice": quantity(record.Receipt.EffectiveGasPrice),
		"feePayer":          strings.ToLower(traceFeePayer(record)),
		"contractAddress":   nullableAddress(record.Receipt.ContractAddress),
		"error":             record.Receipt.Error,
		"events":            chainLabTraceEvents(record.Receipt.Events),
	}
}

func traceFeePayer(record types.TransactionRecord) string {
	if strings.TrimSpace(record.Receipt.FeePayer) != "" {
		return record.Receipt.FeePayer
	}
	return record.Transaction.From
}

func chainLabTraceEvents(events []types.Event) []map[string]any {
	output := make([]map[string]any, 0, len(events))
	for _, event := range events {
		output = append(output, map[string]any{
			"type":       event.Type,
			"attributes": event.Attributes,
		})
	}
	return output
}

func evmBlockHeader(block types.Block) map[string]any {
	response := map[string]any{
		"number":           quantity(block.Header.Height),
		"hash":             block.Hash(),
		"parentHash":       block.Header.ParentHash,
		"timestamp":        quantity(uint64(block.Header.TimeUnix)),
		"transactionsRoot": block.Header.TxRoot,
		"receiptsRoot":     block.Header.ReceiptRoot,
		"stateRoot":        block.Header.StateRoot,
		"gasLimit":         quantity(block.Header.GasLimit),
		"gasUsed":          quantity(block.Header.GasUsed),
		"baseFeePerGas":    quantity(block.Header.BaseFeePerGas),
		"miner":            strings.ToLower(block.Header.Proposer),
	}
	if block.FinalityCertificate != nil {
		response["finalityCertificate"] = evmFinalityCertificate(*block.FinalityCertificate)
	}
	return response
}

func evmBlock(block types.Block, fullTransactions bool) map[string]any {
	transactions := make([]any, len(block.Transactions))
	for i, tx := range block.Transactions {
		if fullTransactions {
			transactions[i] = map[string]any{
				"hash":                 tx.Hash(),
				"blockHash":            block.Hash(),
				"blockNumber":          quantity(block.Header.Height),
				"transactionIndex":     quantity(uint64(i)),
				"from":                 strings.ToLower(tx.From),
				"signer":               nullableAddress(tx.Signer),
				"to":                   nullableAddress(tx.To),
				"nonce":                quantity(tx.Nonce),
				"value":                quantity(tx.Value),
				"gas":                  quantity(tx.GasLimit),
				"gasPrice":             quantity(tx.GasPrice),
				"maxFeePerGas":         quantity(tx.MaxFeePerGas),
				"maxPriorityFeePerGas": quantity(tx.MaxPriorityFeePerGas),
				"chainType":            string(tx.Type),
				"batchOperationCount":  quantity(uint64(len(tx.Batch))),
				"authorizationCount":   quantity(uint64(len(tx.Authorizations))),
				"paymaster":            nullableAddress(tx.Paymaster),
				"input":                "0x",
				"type":                 "0x0",
			}
		} else {
			transactions[i] = tx.Hash()
		}
	}
	response := evmBlockHeader(block)
	response["transactions"] = transactions
	return response
}

func evmFinalityCertificate(certificate types.FinalityCertificate) map[string]any {
	return map[string]any{
		"chainId":     certificate.ChainID,
		"height":      quantity(certificate.Height),
		"blockHash":   certificate.BlockHash,
		"signerCount": quantity(uint64(len(certificate.Signatures))),
		"signatures":  certificate.Signatures,
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
