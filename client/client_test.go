package client

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// An older server answers unknown API paths with the web app's HTML.
func TestOlderServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html></html>"))
	}))
	defer srv.Close()
	_, err := New(srv.URL, "lgk_x").Telegram(t.Context())
	if err == nil || !strings.Contains(err.Error(), "older Lagrange") {
		t.Fatalf("err = %v", err)
	}
}
