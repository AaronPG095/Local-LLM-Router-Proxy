package proxy

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
)

const (
	defaultCooldown = 60 * time.Second
	minCooldown     = 5 * time.Second
	maxCooldown     = 15 * time.Minute
)

// ProviderHealth summarizes a provider's live cooldown and rate-limit state.
type ProviderHealth struct {
	CoolingDown          bool `json:"cooling_down"`
	CooldownRemainingSec int  `json:"cooldown_remaining_sec,omitempty"`
	MinuteRequests       int  `json:"minute_requests"`
	DayRequests          int  `json:"day_requests"`
	MaxRPM               int  `json:"max_rpm,omitempty"`
	MaxRPD               int  `json:"max_rpd,omitempty"`
}

type providerState struct {
	cooldownUntil time.Time
	minuteHits    []time.Time
	dayKey        string
	dayHits       int
}

// ProviderTracker tracks per-provider 429/529 cooldowns and sliding RPM/RPD counters.
type ProviderTracker struct {
	mu     sync.Mutex
	states map[string]*providerState
}

// NewProviderTracker creates a thread-safe ProviderTracker.
func NewProviderTracker() *ProviderTracker {
	return &ProviderTracker{
		states: make(map[string]*providerState),
	}
}

func (t *ProviderTracker) stateLocked(provider string) *providerState {
	st, ok := t.states[provider]
	if !ok {
		st = &providerState{}
		t.states[provider] = st
	}
	return st
}

func pruneMinuteHits(hits []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-60 * time.Second)
	idx := 0
	for idx < len(hits) && !hits[idx].After(cutoff) {
		idx++
	}
	if idx == 0 {
		return hits
	}
	return append([]time.Time(nil), hits[idx:]...)
}

func utcDayString(now time.Time) string {
	return now.UTC().Format("2006-01-02")
}

// IsAvailable returns true if the provider is neither cooling down nor at its configured RPM/RPD cap.
func (t *ProviderTracker) IsAvailable(provider string, pCfg config.ProviderConfig, now time.Time) (bool, string) {
	if t == nil {
		return true, ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	st := t.stateLocked(provider)
	if now.Before(st.cooldownUntil) {
		return false, "cooldown"
	}

	st.minuteHits = pruneMinuteHits(st.minuteHits, now)
	if pCfg.MaxRPM > 0 && len(st.minuteHits) >= pCfg.MaxRPM {
		return false, "max_rpm"
	}

	day := utcDayString(now)
	if st.dayKey != day {
		st.dayKey = day
		st.dayHits = 0
	}
	if pCfg.MaxRPD > 0 && st.dayHits >= pCfg.MaxRPD {
		return false, "max_rpd"
	}

	return true, ""
}

// RecordAttempt increments the sliding 60s and UTC-day request counters for provider.
func (t *ProviderTracker) RecordAttempt(provider string, now time.Time) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	st := t.stateLocked(provider)
	st.minuteHits = pruneMinuteHits(st.minuteHits, now)
	st.minuteHits = append(st.minuteHits, now)

	day := utcDayString(now)
	if st.dayKey != day {
		st.dayKey = day
		st.dayHits = 0
	}
	st.dayHits++
}

// MarkCooldown puts provider into cooldown based on Retry-After (seconds or HTTP-date), defaulting to 60s.
func (t *ProviderTracker) MarkCooldown(provider string, retryAfterHeader string, now time.Time) {
	if t == nil {
		return
	}
	dur := parseRetryAfter(retryAfterHeader, now)
	t.mu.Lock()
	defer t.mu.Unlock()

	st := t.stateLocked(provider)
	until := now.Add(dur)
	if until.After(st.cooldownUntil) {
		st.cooldownUntil = until
	}
}

// StatusSnapshot returns live health and quota counters for all configured providers.
func (t *ProviderTracker) StatusSnapshot(providers map[string]config.ProviderConfig, now time.Time) map[string]ProviderHealth {
	out := make(map[string]ProviderHealth, len(providers))
	if t == nil {
		return out
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	day := utcDayString(now)
	for name, pCfg := range providers {
		st := t.stateLocked(name)
		st.minuteHits = pruneMinuteHits(st.minuteHits, now)
		if st.dayKey != day {
			st.dayKey = day
			st.dayHits = 0
		}
		cooling := now.Before(st.cooldownUntil)
		remSec := 0
		if cooling {
			remSec = int(st.cooldownUntil.Sub(now).Seconds()) + 1
		}
		out[name] = ProviderHealth{
			CoolingDown:          cooling,
			CooldownRemainingSec: remSec,
			MinuteRequests:       len(st.minuteHits),
			DayRequests:          st.dayHits,
			MaxRPM:               pCfg.MaxRPM,
			MaxRPD:               pCfg.MaxRPD,
		}
	}
	return out
}

func parseRetryAfter(header string, now time.Time) time.Duration {
	h := strings.TrimSpace(header)
	if h == "" {
		return defaultCooldown
	}
	var dur time.Duration
	if secs, err := strconv.Atoi(h); err == nil {
		dur = time.Duration(secs) * time.Second
	} else if t, err := http.ParseTime(h); err == nil {
		dur = t.Sub(now)
	} else {
		return defaultCooldown
	}
	if dur < minCooldown {
		return minCooldown
	}
	if dur > maxCooldown {
		return maxCooldown
	}
	return dur
}
