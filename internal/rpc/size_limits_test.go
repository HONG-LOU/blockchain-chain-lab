package rpc_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	chaincrypto "chainlab/internal/crypto"
	"chainlab/internal/node"
	chainrpc "chainlab/internal/rpc"
)

func TestJSONPostRoutesRejectDeclaredOversizedBodies(t *testing.T) {
	handler := newSizeLimitRPCHandler(t)
	paths := []string{
		"/tx",
		"/tx/raw",
		"/faucet",
		"/peer/tx",
		"/peer/block",
		"/peer/finality-vote",
		"/rpc",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
			request.ContentLength = chainrpc.MaxRequestBodyBytes + 1
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), "request body exceeds") {
				t.Fatalf("body = %s", response.Body.String())
			}
		})
	}
}

func TestJSONRPCLimitsChunkedBodyWhileReading(t *testing.T) {
	handler := newSizeLimitRPCHandler(t)
	request := httptest.NewRequest(http.MethodPost, "/rpc", nil)
	request.ContentLength = -1
	request.Body = io.NopCloser(&repeatedByteReader{
		remaining: chainrpc.MaxRequestBodyBytes + 1,
		value:     ' ',
	})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "request body exceeds") {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestTransactionEndpointRejectsChunkedPaddingAfterJSONValue(t *testing.T) {
	handler := newSizeLimitRPCHandler(t)
	padding := &repeatedByteReader{
		remaining: chainrpc.MaxRequestBodyBytes,
		value:     ' ',
	}
	request := httptest.NewRequest(http.MethodPost, "/tx", nil)
	request.ContentLength = -1
	request.Body = io.NopCloser(io.MultiReader(strings.NewReader("{}"), padding))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

type repeatedByteReader struct {
	remaining int64
	value     byte
}

func (r *repeatedByteReader) Read(output []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	count := len(output)
	if int64(count) > r.remaining {
		count = int(r.remaining)
	}
	for index := 0; index < count; index++ {
		output[index] = r.value
	}
	r.remaining -= int64(count)
	return count, nil
}

func newSizeLimitRPCHandler(t *testing.T) http.Handler {
	t.Helper()
	key, err := chaincrypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	n, err := node.NewDevelopment(node.Config{ChainID: "chainlab-local", ProposerKey: key, GenesisTimeUnix: node.DeterministicDevGenesisTimeUnix})
	if err != nil {
		t.Fatal(err)
	}
	return chainrpc.NewServer(n)
}
