package rpc

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPeerBlockReplayOfCurrentHeadDoesNotRepeatWebSocketNotifications(t *testing.T) {
	n := newResourceLimitTestNode(t, 0)
	if _, err := n.RequestFaucet("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 1); err != nil {
		t.Fatal(err)
	}
	block, err := n.ProduceBlock()
	if err != nil {
		t.Fatal(err)
	}
	server := newServer(n, nil)
	defer server.filters.stop()
	_, headEvents, err := server.registerNewHeadSubscription(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, logEvents, err := server.registerLogSubscription(logFilter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/peer/block", bytes.NewReader(raw))
	response := httptest.NewRecorder()
	server.routes().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("replay status = %d, body = %s", response.Code, response.Body.String())
	}
	assertNoPeerReplayNotification(t, headEvents, "newHeads")
	assertNoPeerReplayNotification(t, logEvents, "logs")
}

func assertNoPeerReplayNotification[T any](t *testing.T, events <-chan T, kind string) {
	t.Helper()
	select {
	case event, ok := <-events:
		t.Fatalf("current-head replay emitted %s notification: %#v (open=%v)", kind, event, ok)
	default:
	}
}
