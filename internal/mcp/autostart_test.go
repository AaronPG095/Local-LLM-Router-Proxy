package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestEnsureProxyRunningNoOpWhenHealthy(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer mock.Close()

	var started atomic.Int32
	srv := NewServer(mock.URL)
	srv.startProxyFn = func() error {
		started.Add(1)
		return nil
	}

	if err := srv.EnsureProxyRunning(context.Background()); err != nil {
		t.Fatalf("EnsureProxyRunning: %v", err)
	}
	if started.Load() != 0 {
		t.Fatalf("expected 0 starts when already healthy, got %d", started.Load())
	}
}

func TestEnsureProxyRunningStartsWhenDown(t *testing.T) {
	var healthy atomic.Bool
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			if !healthy.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}
	}))
	defer mock.Close()

	var started atomic.Int32
	srv := NewServer(mock.URL)
	srv.startProxyFn = func() error {
		started.Add(1)
		healthy.Store(true)
		return nil
	}

	if err := srv.EnsureProxyRunning(context.Background()); err != nil {
		t.Fatalf("EnsureProxyRunning: %v", err)
	}
	if started.Load() != 1 {
		t.Fatalf("expected 1 start when initially down, got %d", started.Load())
	}
}
