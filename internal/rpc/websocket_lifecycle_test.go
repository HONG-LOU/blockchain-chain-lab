package rpc

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"chainlab/internal/types"
)

func TestWebSocketIdleConnectionExpiresUnregistersAndReleasesSlot(t *testing.T) {
	server := newServer(newResourceLimitTestNode(t, 0), nil)
	server.wsConnectionSlots = make(chan struct{}, 1)
	server.wsTimeouts = webSocketTimeouts{
		pongTimeout:  500 * time.Millisecond,
		pingInterval: 50 * time.Millisecond,
		writeTimeout: 100 * time.Millisecond,
	}
	httpServer := httptest.NewServer(server.routes())
	t.Cleanup(httpServer.Close)

	conn, reader, status := dialLifecycleWebSocket(t, httpServer.URL)
	if status != http.StatusSwitchingProtocols {
		conn.Close()
		t.Fatalf("websocket status = %d", status)
	}
	writeLifecycleClientFrame(t, conn, websocketOpcodeText, []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["newHeads"]}`))
	responsePayload := readLifecycleTextFrame(t, conn, reader, false)
	var response rpcResponse
	if err := json.Unmarshal(responsePayload, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != "" || response.Result == nil {
		t.Fatalf("subscribe response = %+v", response)
	}
	waitForWebSocketLifecycle(t, time.Second, func() bool {
		connections, subscriptions := webSocketLifecycleCounts(server)
		return connections == 1 && subscriptions == 1
	})

	rejected, _, rejectedStatus := dialLifecycleWebSocket(t, httpServer.URL)
	_ = rejected.Close()
	if rejectedStatus != http.StatusServiceUnavailable {
		t.Fatalf("connection over limit status = %d", rejectedStatus)
	}

	for {
		opcode, _, err := readLifecycleServerFrame(conn, reader, time.Second)
		if err != nil {
			t.Fatalf("read heartbeat: %v", err)
		}
		if opcode == websocketOpcodePing {
			break
		}
	}
	waitForWebSocketLifecycle(t, 2*time.Second, func() bool {
		connections, subscriptions := webSocketLifecycleCounts(server)
		return connections == 0 && subscriptions == 0
	})
	_ = conn.Close()

	replacement, _, replacementStatus := dialLifecycleWebSocket(t, httpServer.URL)
	if replacementStatus != http.StatusSwitchingProtocols {
		replacement.Close()
		t.Fatalf("replacement websocket status = %d", replacementStatus)
	}
	_ = replacement.Close()
	waitForWebSocketLifecycle(t, time.Second, func() bool {
		connections, _ := webSocketLifecycleCounts(server)
		return connections == 0
	})
}

func TestWebSocketPongRefreshesReadDeadline(t *testing.T) {
	server := newServer(newResourceLimitTestNode(t, 0), nil)
	server.wsConnectionSlots = make(chan struct{}, 1)
	server.wsTimeouts = webSocketTimeouts{
		pongTimeout:  150 * time.Millisecond,
		pingInterval: 25 * time.Millisecond,
		writeTimeout: 75 * time.Millisecond,
	}
	httpServer := httptest.NewServer(server.routes())
	t.Cleanup(httpServer.Close)

	conn, reader, status := dialLifecycleWebSocket(t, httpServer.URL)
	if status != http.StatusSwitchingProtocols {
		conn.Close()
		t.Fatalf("websocket status = %d", status)
	}
	defer conn.Close()
	for range 8 {
		for {
			opcode, payload, err := readLifecycleServerFrame(conn, reader, time.Second)
			if err != nil {
				t.Fatalf("read ping: %v", err)
			}
			if opcode != websocketOpcodePing {
				continue
			}
			writeLifecycleClientFrame(t, conn, websocketOpcodePong, payload)
			break
		}
	}

	writeLifecycleClientFrame(t, conn, websocketOpcodeText, []byte(`{"jsonrpc":"2.0","id":7,"method":"unknown"}`))
	responsePayload := readLifecycleTextFrame(t, conn, reader, true)
	var response rpcResponse
	if err := json.Unmarshal(responsePayload, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error != "unknown method" {
		t.Fatalf("response after heartbeat = %+v", response)
	}
	connections, _ := webSocketLifecycleCounts(server)
	if connections != 1 {
		t.Fatalf("connections after pong heartbeat = %d", connections)
	}
}

func TestWebSocketWriteTimeoutClosesAndUnregisters(t *testing.T) {
	server := newServer(newResourceLimitTestNode(t, 0), nil)
	server.wsConnectionSlots = make(chan struct{}, 1)
	server.wsTimeouts = webSocketTimeouts{
		pongTimeout:  time.Second,
		pingInterval: 500 * time.Millisecond,
		writeTimeout: 40 * time.Millisecond,
	}
	serverConn, clientConn := net.Pipe()
	defer clientConn.Close()
	writer := &lifecycleHijackResponseWriter{
		header: make(http.Header),
		conn:   serverConn,
		rw: bufio.NewReadWriter(
			bufio.NewReader(serverConn),
			bufio.NewWriter(serverConn),
		),
	}
	request := httptest.NewRequest(http.MethodGet, "http://chain.test/rpc/ws", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", lifecycleWebSocketKey(t))

	done := make(chan struct{})
	go func() {
		server.handleWebSocketJSONRPC(writer, request)
		close(done)
	}()
	clientReader := bufio.NewReader(clientConn)
	if err := clientConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	handshake, err := http.ReadResponse(clientReader, request)
	if err != nil {
		t.Fatal(err)
	}
	if handshake.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("websocket status = %d", handshake.StatusCode)
	}
	writeLifecycleClientFrame(t, clientConn, websocketOpcodeText, []byte(`{"jsonrpc":"2.0","id":1,"method":"eth_subscribe","params":["newHeads"]}`))

	select {
	case <-done:
	case <-time.After(time.Second):
		_ = clientConn.Close()
		<-done
		t.Fatal("websocket handler did not stop after write deadline")
	}
	connections, subscriptions := webSocketLifecycleCounts(server)
	if connections != 0 || subscriptions != 0 {
		t.Fatalf("lifecycle after write timeout: connections=%d subscriptions=%d", connections, subscriptions)
	}
}

func TestWebSocketNotificationBackpressureClosesConnectionAndUnregisters(t *testing.T) {
	t.Run("new heads", func(t *testing.T) {
		server, connection, cleanup := newBackpressureTestServer(t)
		defer cleanup()
		if _, _, err := server.registerNewHeadSubscription(connection); err != nil {
			t.Fatal(err)
		}
		for range 9 {
			server.notifyNewHead(types.Block{})
		}
		assertBackpressureClosed(t, server, connection)
	})

	t.Run("logs", func(t *testing.T) {
		server, connection, cleanup := newBackpressureTestServer(t)
		defer cleanup()
		if _, _, err := server.registerLogSubscription(logFilter{}, connection); err != nil {
			t.Fatal(err)
		}
		block := websocketLogBudgetBlock(
			"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		)
		for range 17 {
			server.notifyLogs(block)
		}
		assertBackpressureClosed(t, server, connection)
	})

	t.Run("pending transactions", func(t *testing.T) {
		server, connection, cleanup := newBackpressureTestServer(t)
		defer cleanup()
		if _, _, err := server.registerPendingTransactionSubscription(connection); err != nil {
			t.Fatal(err)
		}
		tx := types.Transaction{Type: types.TxCall, From: "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", To: "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}
		for range 17 {
			server.notifyPendingTransaction(tx)
		}
		assertBackpressureClosed(t, server, connection)
	})
}

func TestWebSocketSubscriptionsExpireAndReleaseCapacity(t *testing.T) {
	server := newServer(newResourceLimitTestNode(t, 0), nil)
	server.wsSubscriptionTTL = 25 * time.Millisecond

	newHeadID, newHeads, err := server.registerNewHeadSubscription(nil)
	if err != nil {
		t.Fatal(err)
	}
	logID, logs, err := server.registerLogSubscription(logFilter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pendingID, pending, err := server.registerPendingTransactionSubscription(nil)
	if err != nil {
		t.Fatal(err)
	}
	local := map[string]struct{}{newHeadID: {}, logID: {}, pendingID: {}}

	waitForWebSocketLifecycle(t, time.Second, func() bool {
		connections, subscriptions := webSocketLifecycleCounts(server)
		server.wsMu.Lock()
		timers := len(server.wsSubscriptionTimers)
		server.wsMu.Unlock()
		return connections == 0 && subscriptions == 0 && timers == 0
	})
	if _, ok := <-newHeads; ok {
		t.Fatal("expired new-head subscription remained open")
	}
	if _, ok := <-logs; ok {
		t.Fatal("expired log subscription remained open")
	}
	if _, ok := <-pending; ok {
		t.Fatal("expired pending-transaction subscription remained open")
	}

	server.wsMu.Lock()
	server.wsSubscriptionTTL = time.Second
	server.wsMu.Unlock()
	activeID, _, err := server.registerNewHeadSubscription(nil)
	if err != nil {
		t.Fatal(err)
	}
	local[activeID] = struct{}{}
	server.pruneLocalWebSocketSubscriptions(local)
	if len(local) != 1 {
		t.Fatalf("local subscriptions after expiry = %v", local)
	}
	if _, ok := local[activeID]; !ok {
		t.Fatal("active local subscription was pruned")
	}
	if !server.unregisterNewHeadSubscription(activeID) {
		t.Fatal("active subscription was not unregistered")
	}
}

func newBackpressureTestServer(t *testing.T) (*Server, *webSocketConnection, func()) {
	t.Helper()
	serverConn, clientConn := net.Pipe()
	connection := newWebSocketConnection(serverConn, defaultWebSocketTimeouts())
	server := &Server{
		wsNewHeads:            make(map[string]chan types.Block),
		wsLogs:                make(map[string]*wsLogSubscription),
		wsPendingTransactions: make(map[string]chan string),
		wsConnections:         make(map[string]*webSocketConnection),
	}
	return server, connection, func() {
		connection.close()
		_ = clientConn.Close()
	}
}

func assertBackpressureClosed(t *testing.T, server *Server, connection *webSocketConnection) {
	t.Helper()
	select {
	case <-connection.closed:
	default:
		t.Fatal("slow websocket connection remained open")
	}
	server.wsMu.Lock()
	subscriptions := len(server.wsNewHeads) + len(server.wsLogs) + len(server.wsPendingTransactions)
	server.wsMu.Unlock()
	if subscriptions != 0 {
		t.Fatalf("slow websocket subscriptions remaining = %d", subscriptions)
	}
}

type lifecycleHijackResponseWriter struct {
	header http.Header
	conn   net.Conn
	rw     *bufio.ReadWriter
}

func (writer *lifecycleHijackResponseWriter) Header() http.Header {
	return writer.header
}

func (writer *lifecycleHijackResponseWriter) Write(data []byte) (int, error) {
	return writer.conn.Write(data)
}

func (writer *lifecycleHijackResponseWriter) WriteHeader(int) {}

func (writer *lifecycleHijackResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return writer.conn, writer.rw, nil
}

func dialLifecycleWebSocket(t *testing.T, serverURL string) (net.Conn, *bufio.Reader, int) {
	t.Helper()
	parsed, err := url.Parse(serverURL)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", parsed.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodGet, "http://"+parsed.Host+"/rpc/ws", nil)
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	request.Host = parsed.Host
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", lifecycleWebSocketKey(t))
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := request.Write(conn); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, request)
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	return conn, reader, response.StatusCode
}

func lifecycleWebSocketKey(t *testing.T) string {
	t.Helper()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(nonce[:])
}

func writeLifecycleClientFrame(t *testing.T, conn net.Conn, opcode byte, payload []byte) {
	t.Helper()
	if len(payload) > 65535 {
		t.Fatalf("test websocket payload too large: %d", len(payload))
	}
	header := []byte{0x80 | opcode}
	if len(payload) <= 125 {
		header = append(header, 0x80|byte(len(payload)))
	} else {
		header = append(header, 0x80|126, byte(len(payload)>>8), byte(len(payload)))
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		t.Fatal(err)
	}
	frame := append(header, mask[:]...)
	for index, value := range payload {
		frame = append(frame, value^mask[index%len(mask)])
	}
	if err := conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(frame); err != nil {
		t.Fatal(err)
	}
}

func readLifecycleTextFrame(t *testing.T, conn net.Conn, reader *bufio.Reader, respondToPing bool) []byte {
	t.Helper()
	for {
		opcode, payload, err := readLifecycleServerFrame(conn, reader, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if opcode == websocketOpcodePing && respondToPing {
			writeLifecycleClientFrame(t, conn, websocketOpcodePong, payload)
			continue
		}
		if opcode == websocketOpcodeText {
			return payload
		}
	}
}

func readLifecycleServerFrame(conn net.Conn, reader *bufio.Reader, timeout time.Duration) (byte, []byte, error) {
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return 0, nil, err
	}
	first, err := reader.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	second, err := reader.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	length := uint64(second & 0x7f)
	switch length {
	case 126:
		var extended [2]byte
		if _, err := io.ReadFull(reader, extended[:]); err != nil {
			return 0, nil, err
		}
		length = uint64(extended[0])<<8 | uint64(extended[1])
	case 127:
		return 0, nil, io.ErrShortBuffer
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	return first & 0x0f, payload, nil
}

func waitForWebSocketLifecycle(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !condition() {
		t.Fatal("timed out waiting for websocket lifecycle state")
	}
}

func webSocketLifecycleCounts(server *Server) (int, int) {
	server.wsMu.Lock()
	defer server.wsMu.Unlock()
	connections := len(server.wsConnectionSlots)
	subscriptions := len(server.wsNewHeads) + len(server.wsLogs) + len(server.wsPendingTransactions)
	return connections, subscriptions
}
