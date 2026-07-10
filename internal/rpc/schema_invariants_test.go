package rpc_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJSONIngressRejectsUnknownSchemaFields(t *testing.T) {
	handler := newSizeLimitRPCHandler(t)
	tests := []struct {
		path string
		body string
	}{
		{path: "/tx", body: `{"chain_id":"chainlab-local","unknown":true}`},
		{path: "/peer/finality-vote", body: `{"validator":"0x1111111111111111111111111111111111111111","unknown":true}`},
		{path: "/peer/block", body: `{"header":{"chain_id":"chainlab-local","unknown":true}}`},
		{path: "/rpc", body: `{"jsonrpc":"2.0","id":1,"method":"chain_head","unknown":true}`},
	}
	for _, test := range tests {
		request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, body = %s", test.path, response.Code, response.Body.String())
		}
	}
}

func TestJSONRPCAcceptsVersionFieldAndRejectsWrongVersion(t *testing.T) {
	handler := newSizeLimitRPCHandler(t)
	for _, test := range []struct {
		version string
		status  int
	}{
		{version: "2.0", status: http.StatusOK},
		{version: "1.0", status: http.StatusBadRequest},
	} {
		request := httptest.NewRequest(http.MethodPost, "/rpc", strings.NewReader(`{"jsonrpc":"`+test.version+`","id":1,"method":"chain_head"}`))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.status {
			t.Fatalf("version %s status = %d, body = %s", test.version, response.Code, response.Body.String())
		}
	}
}
