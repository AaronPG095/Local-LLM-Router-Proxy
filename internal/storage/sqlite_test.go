package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreWALMigrationsBudgetsAndSummary(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "router.db")
	store, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() failed: %v", err)
	}
	defer store.Close()

	var mode string
	if err := store.DB().QueryRow("PRAGMA journal_mode;").Scan(&mode); err != nil {
		t.Fatalf("query journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Errorf("expected journal_mode=wal, got %q", mode)
	}

	ctx := context.Background()
	if err := store.AddBudgetUsage(ctx, "session", "s1", 0, 500, 0.05); err != nil {
		t.Fatalf("AddBudgetUsage 1: %v", err)
	}
	if err := store.AddBudgetUsage(ctx, "session", "s1", 0, 250, 0.025); err != nil {
		t.Fatalf("AddBudgetUsage 2: %v", err)
	}

	u, err := store.GetBudgetUsage(ctx, "session", "s1", 0)
	if err != nil {
		t.Fatalf("GetBudgetUsage: %v", err)
	}
	if u.TotalTokens != 750 || u.RequestCount != 2 {
		t.Errorf("unexpected budget usage: %+v", u)
	}

	now := time.Now().UTC().UnixMilli()
	logs := []RequestLog{
		{
			ID: "01JTEST1", TsUTC: now, ProjectID: "p1", SessionID: "s1",
			APIFormat: "gemini_native", RequestedModel: "gemini-2.5-flash",
			RoutedProvider: "gemini", RoutedModel: "gemini-2.5-flash",
			StatusCode: 200, CacheStatus: "miss",
			PromptTokens: 100, CompletionTokens: 50, EstimatedCostUSD: 0.001,
			TotalMs: 120, ProxyOverheadMs: 2,
		},
		{
			ID: "01JTEST2", TsUTC: now, ProjectID: "p1", SessionID: "s1",
			APIFormat: "openai_chat", RequestedModel: "gpt-4o",
			RoutedProvider: "ollama", RoutedModel: "qwen2.5-coder:7b",
			StatusCode: 200, CacheStatus: "l1_hit",
			PromptTokens: 200, CompletionTokens: 100, SavedTokens: 300,
			EstimatedCostUSD: 0.0, SavedCostUSD: 0.002,
			TotalMs: 5, ProxyOverheadMs: 1,
		},
	}
	for _, l := range logs {
		if err := store.InsertRequestLog(ctx, l); err != nil {
			t.Fatalf("InsertRequestLog(%s): %v", l.ID, err)
		}
	}

	sum, err := store.GetMetricsSummary(ctx, 0)
	if err != nil {
		t.Fatalf("GetMetricsSummary: %v", err)
	}
	if sum.TotalRequests != 2 || sum.CacheHits != 1 || sum.SavedTokens != 300 {
		t.Errorf("unexpected summary: %+v", sum)
	}
}
