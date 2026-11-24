package budget

import (
	"context"
	"fmt"
	"time"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/storage"
)

// Decision describes the outcome of a pre-flight budget check.
type Decision struct {
	Allowed         bool
	SoftWarn        bool
	WarningMsg      string
	Action          string // "allow" | "block" | "downgrade"
	DowngradeTarget string
	BlockReason     string
}

// Guard enforces per-session and per-project-daily token and cost budgets.
type Guard struct {
	store *storage.Store
	cfg   config.BudgetsConfig
}

// NewGuard creates a budget Guard backed by SQLite storage.
func NewGuard(store *storage.Store, cfg config.BudgetsConfig) *Guard {
	return &Guard{store: store, cfg: cfg}
}

// Check evaluates session and project_daily budgets before forwarding a request.
// Invariant: if neverDowngrade is true, an exceeded budget with hard_action="downgrade"
// MUST still return Allowed=false, Action="block".
func (g *Guard) Check(ctx context.Context, projectID, sessionID string, neverDowngrade bool) (Decision, error) {
	if projectID == "" {
		projectID = "default"
	}
	if sessionID == "" {
		sessionID = "default"
	}

	scopes := []struct {
		scopeType string
		scopeKey  string
		window    int64
		cfg       config.BudgetScopeConfig
	}{
		{"session", sessionID, 0, g.cfg.Session},
		{"project_daily", projectID, utcDayStartMs(time.Now().UTC()), g.cfg.ProjectDaily},
	}

	decision := Decision{Allowed: true, Action: "allow"}

	for _, sc := range scopes {
		if sc.cfg.MaxTokens <= 0 && sc.cfg.MaxCostUSD <= 0 {
			continue
		}

		usage, err := g.store.GetBudgetUsage(ctx, sc.scopeType, sc.scopeKey, sc.window)
		if err != nil {
			return Decision{}, fmt.Errorf("get %s budget usage: %w", sc.scopeType, err)
		}

		tokenRatio := 0.0
		if sc.cfg.MaxTokens > 0 {
			tokenRatio = float64(usage.TotalTokens) / float64(sc.cfg.MaxTokens)
		}
		costRatio := 0.0
		if sc.cfg.MaxCostUSD > 0 {
			costRatio = usage.TotalCostUSD / sc.cfg.MaxCostUSD
		}
		maxRatio := tokenRatio
		if costRatio > maxRatio {
			maxRatio = costRatio
		}

		if maxRatio >= 1.0 {
			if sc.cfg.HardAction == "downgrade" && !neverDowngrade {
				return Decision{
					Allowed:         true,
					SoftWarn:        true,
					WarningMsg:      fmt.Sprintf("%s budget exceeded (%.0f%%); downgraded to %s", sc.scopeType, maxRatio*100, sc.cfg.DowngradeTarget),
					Action:          "downgrade",
					DowngradeTarget: sc.cfg.DowngradeTarget,
				}, nil
			}
			return Decision{
				Allowed:     false,
				Action:      "block",
				BlockReason: fmt.Sprintf("%s budget exceeded (tokens=%d/%d, cost=$%.4f/$%.4f)", sc.scopeType, usage.TotalTokens, sc.cfg.MaxTokens, usage.TotalCostUSD, sc.cfg.MaxCostUSD),
			}, nil
		}

		if sc.cfg.SoftWarnPct > 0 && maxRatio >= sc.cfg.SoftWarnPct {
			decision.SoftWarn = true
			decision.WarningMsg = fmt.Sprintf("%s budget at %.0f%%", sc.scopeType, maxRatio*100)
		}
	}

	return decision, nil
}

// Record increments both session and project_daily budget counters after a request completes.
func (g *Guard) Record(ctx context.Context, projectID, sessionID string, tokens int64, costUSD float64) error {
	if projectID == "" {
		projectID = "default"
	}
	if sessionID == "" {
		sessionID = "default"
	}
	if err := g.store.AddBudgetUsage(ctx, "session", sessionID, 0, tokens, costUSD); err != nil {
		return err
	}
	return g.store.AddBudgetUsage(ctx, "project_daily", projectID, utcDayStartMs(time.Now().UTC()), tokens, costUSD)
}

func utcDayStartMs(t time.Time) int64 {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC).UnixMilli()
}
