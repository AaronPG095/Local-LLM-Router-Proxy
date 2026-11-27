package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
)

func TestDesktopClientStreamingContracts(t *testing.T) {
	cases := []struct {
		name, provider, endpoint, request, authHeader, authValue, event string
	}{
		{
			"Codex Responses", "openai", "/v1/responses",
			`{"model":"gpt-4o","input":"mock","stream":true}`,
			"Authorization", "Bearer fixture-openai", "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n",
		},
		{
			"Claude Messages", "anthropic", "/v1/messages",
			`{"model":"claude-fixture","messages":[{"role":"user","content":"mock"}],"max_tokens":16,"stream":true}`,
			"x-api-key", "fixture-anthropic", "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		},
		{
			"Gemini Native", "gemini", "/v1beta/models/gemini-fixture:streamGenerateContent?alt=sse",
			`{"contents":[{"role":"user","parts":[{"text":"mock"}]}]}`,
			"x-goog-api-key", "fixture-gemini", "data: {\"candidates\":[{\"finishReason\":\"STOP\"}]}\n\n",
		},
		{
			"Chat Completions", "openai", "/v1/chat/completions",
			`{"model":"gpt-4o","messages":[{"role":"user","content":"mock"}],"stream":true}`,
			"Authorization", "Bearer fixture-openai", "data: [DONE]\n\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keyName := "TEST_ROUTER_CLIENT_KEY"
			t.Setenv(keyName, strings.TrimPrefix(tc.authValue, "Bearer "))
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.RequestURI() != tc.endpoint || r.Header.Get(tc.authHeader) != tc.authValue {
					t.Error("mock upstream received an unexpected method, URI, or authentication header")
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.event)
			}))
			defer upstream.Close()
			proxy, _, _ := newTestServer(t, upstream.URL, upstream.URL+"/v1", 10000)
			baseURL := upstream.URL
			if tc.provider != "gemini" {
				baseURL += "/v1"
			}
			proxy.cfg.Providers[tc.provider] = config.ProviderConfig{Type: tc.provider, BaseURL: baseURL, APIKeyEnv: keyName}
			request := httptest.NewRequest(http.MethodPost, tc.endpoint, strings.NewReader(tc.request))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			proxy.Handler().ServeHTTP(response, request)
			if response.Code != http.StatusOK ||
				response.Header().Get("X-Router-Provider") != tc.provider ||
				!strings.HasPrefix(response.Header().Get("Content-Type"), "text/event-stream") ||
				!strings.Contains(response.Body.String(), strings.TrimSpace(tc.event)) {
				t.Fatalf("unexpected streaming response: status=%d provider=%q body=%q", response.Code, response.Header().Get("X-Router-Provider"), response.Body.String())
			}
		})
	}
}

func TestDesktopClientLocalPrivacyUsesOnlyOllama(t *testing.T) {
	cloud := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("local-only request reached a cloud provider")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer cloud.Close()
	ollama := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.URL.Path != "/v1/chat/completions" || !strings.Contains(string(body), `"model":"local-fixture"`) {
			t.Error("expected a translated local-model request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"local-fixture","choices":[{"message":{"role":"assistant","content":"local"}}]}`)
	}))
	defer ollama.Close()
	proxy, _, _ := newTestServer(t, cloud.URL, cloud.URL+"/v1", 10000)
	proxy.cfg.Providers["ollama"] = config.ProviderConfig{
		Type: "openai_compatible", BaseURL: ollama.URL + "/v1",
		PricingPer1M: map[string]config.ModelPricing{"local-fixture": {}},
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4o","input":"mock"}`))
	request.Header.Set("X-Router-Privacy", "local")
	response := httptest.NewRecorder()
	proxy.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("X-Router-Provider") != "ollama" {
		t.Fatalf("expected Ollama response, got status=%d provider=%q", response.Code, response.Header().Get("X-Router-Provider"))
	}
}
