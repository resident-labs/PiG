package ai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func stallServer(t *testing.T, behavior func(int, http.ResponseWriter, *http.Request)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		behavior(int(attempts.Add(1)), w, r)
	}))
	t.Cleanup(server.Close)
	return server, &attempts
}
