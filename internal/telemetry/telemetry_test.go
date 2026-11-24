package telemetry

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/storage"
)

func TestRecorderDualWriteAndCost(t *testing.T) {
	dir := t.TempDir()
	store, err := storage.Open(filepath.Join(dir, "router.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	jsonlPath := filepath.Join(dir, "telemetry.jsonl")
	rec, err := NewRecorder(store, jsonlPath)
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}

	cost := CalculateCost(config.ModelPricing{
		Input:       2.0,
		Output:      10.0,
		CachedInput: 0.5,
	}, 1_000_000, 500_000, 400_000)
	// uncached = 600k * 2.0/1M = 1.20; cached = 400k * 0.5/1M = 0.20; output = 500k * 10.0/1M = 5.00 => 6.40
	if math.Abs(cost-6.40) > 1e-9 {
		t.Fatalf("CalculateCost = %v, want 6.40", cost)
	}

	err = rec.Record(context.Background(), storage.RequestLog{
		ProjectID:        "proj-a",
		SessionID:        "sess-1",
		APIFormat:        "gemini_native",
		RequestedModel:   "gemini-2.5-flash",
		RoutedProvider:   "gemini",
		RoutedModel:      "gemini-2.5-flash",
		StatusCode:       200,
		PromptTokens:     1000,
		CompletionTokens: 200,
		EstimatedCostUSD: 0.001,
	})
	if err != nil {
		t.Fatalf("Record() error: %v", err)
	}

	raw, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read jsonl: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 jsonl line, got %d", len(lines))
	}
	var parsed storage.RequestLog
	if err := json.Unmarshal([]byte(lines[0]), &parsed); err != nil {
		t.Fatalf("unmarshal jsonl line: %v", err)
	}
	if parsed.ProjectID != "proj-a" || parsed.PromptTokens != 1000 {
		t.Errorf("unexpected parsed jsonl record: %+v", parsed)
	}
}
