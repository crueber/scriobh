package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// newStallServer is a TLS receiver that accepts a delivery and never answers (until stall is closed).
func newStallServer(t *testing.T, stall chan struct{}) *httptest.Server {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-stall }))
	t.Cleanup(srv.Close)
	return srv
}
