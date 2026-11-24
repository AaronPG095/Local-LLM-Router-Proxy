package budget

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/storage"
)

func TestBudgetGuardThresholdsAndNeverDowngrade(t *testing.T) {
	store, err := storage.Open(filepath.Join(t.TempDir(), "router.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	guard := NewGuard(store, config.BudgetsConfig{
		Session: config.BudgetScopeConfig{
			MaxTokens:       1000,
			MaxCostUSD:      1.00,
			SoftWarnPct:     0.80,
			HardAction:      "downgrade",
			DowngradeTarget: "ollama:qwen2.5-coder:7b",
		},
		ProjectDaily: config.BudgetScopeConfig{
			MaxTokens:   5000,
			MaxCostUSD:  5.00,
			SoftWarnPct: 0.80,
			HardAction:  "block",
		},
	})

	ctx := context.Background()

	// Under soft warn
	d, err := guard.Check(ctx, "p1", "s1", false)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed || d.SoftWarn || d.Action != "allow" {
		t.Fatalf("unexpected initial decision: %+v", d)
	}

	// Hit soft warn (>= 80%)
	if err := guard.Record(ctx, "p1", "s1", 850, 0.10); err != nil {
		t.Fatal(err)
	}
	d, err = guard.Check(ctx, "p1", "s1", false)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed || !d.SoftWarn || d.WarningMsg == "" {
		t.Fatalf("expected soft warn decision, got %+v", d)
	}

	// Exceed session cap (>= 100%) with neverDowngrade=false -> downgrade
	if err := guard.Record(ctx, "p1", "s1", 200, 0.05); err != nil {
		t.Fatal(err)
	}
	d, err = guard.Check(ctx, "p1", "s1", false)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Allowed || d.Action != "downgrade" || d.DowngradeTarget != "ollama:qwen2.5-coder:7b" {
		t.Fatalf("expected downgrade decision, got %+v", d)
	}

	// Exceed session cap with neverDowngrade=true -> MUST hard block, never silently downgrade
	d, err = guard.Check(ctx, "p1", "s1", true)
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed || d.Action != "block" {
		t.Fatalf("expected hard block when neverDowngrade=true, got %+v", d)
	}
}
