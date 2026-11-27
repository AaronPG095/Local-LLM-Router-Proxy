package proxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/router"
)

func TestProviderTrackerCooldownAndCaps(t *testing.T) {
	tr := NewProviderTracker()
	now := time.Now().UTC()
	pCfg := config.ProviderConfig{MaxRPM: 2, MaxRPD: 3}

	if ok, _ := tr.IsAvailable("groq", pCfg, now); !ok {
		t.Fatalf("expected groq initially available")
	}

	tr.RecordAttempt("groq", now)
	tr.RecordAttempt("groq", now.Add(1*time.Second))
	if ok, reason := tr.IsAvailable("groq", pCfg, now.Add(2*time.Second)); ok || reason != "max_rpm" {
		t.Fatalf("expected max_rpm block, got ok=%v reason=%q", ok, reason)
	}

	// After 61s, RPM resets, 3rd attempt hits max_rpd
	later := now.Add(61 * time.Second)
	if ok, _ := tr.IsAvailable("groq", pCfg, later); !ok {
		t.Fatalf("expected groq available after minute window")
	}
	tr.RecordAttempt("groq", later)
	if ok, reason := tr.IsAvailable("groq", pCfg, later.Add(1*time.Second)); ok || reason != "max_rpd" {
		t.Fatalf("expected max_rpd block, got ok=%v reason=%q", ok, reason)
	}

	// MarkCooldown with Retry-After: 10
	tr.MarkCooldown("gemini", "10", now)
	if ok, reason := tr.IsAvailable("gemini", config.ProviderConfig{}, now.Add(5*time.Second)); ok || reason != "cooldown" {
		t.Fatalf("expected cooldown block, got ok=%v reason=%q", ok, reason)
	}
	if ok, _ := tr.IsAvailable("gemini", config.ProviderConfig{}, now.Add(11*time.Second)); !ok {
		t.Fatalf("expected gemini available after cooldown expires")
	}
}

func TestExecuteWithFallbackSkipsCoolingDownProvider(t *testing.T) {
	var primaryCalls, secondaryCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCalls.Add(1)
		w.Header().Set("Retry-After", "30")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer primary.Close()

	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondaryCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer secondary.Close()

	tr := NewProviderTracker()
	providers := map[string]config.ProviderConfig{
		"groq":   {Type: "openai_compatible", BaseURL: primary.URL},
		"gemini": {Type: "gemini", BaseURL: secondary.URL},
	}
	candidates := []router.RouteTarget{
		{Provider: "groq", Model: "m1"},
		{Provider: "gemini", Model: "m2"},
	}

	build := func(target router.RouteTarget) (*http.Request, error) {
		return http.NewRequest(http.MethodGet, providers[target.Provider].BaseURL, nil)
	}

	// Call 1: primary returns 429 -> marks cooldown -> secondary succeeds
	resp1, win1, _, err := executeWithFallbackTracked(context.Background(), candidates, tr, providers, build, http.DefaultClient)
	if err != nil || resp1.StatusCode != 200 || win1.Provider != "gemini" {
		t.Fatalf("call 1 failed: win=%+v err=%v", win1, err)
	}
	resp1.Body.Close()

	// Call 2: primary should be skipped completely without even hitting primary server
	resp2, win2, _, err := executeWithFallbackTracked(context.Background(), candidates, tr, providers, build, http.DefaultClient)
	if err != nil || resp2.StatusCode != 200 || win2.Provider != "gemini" {
		t.Fatalf("call 2 failed: win=%+v err=%v", win2, err)
	}
	resp2.Body.Close()

	if primaryCalls.Load() != 1 || secondaryCalls.Load() != 2 {
		t.Fatalf("expected primaryCalls=1, secondaryCalls=2, got %d / %d", primaryCalls.Load(), secondaryCalls.Load())
	}
}
