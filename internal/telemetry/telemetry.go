package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/storage"
)

// Recorder writes request telemetry to both SQLite and an append-only JSONL file.
type Recorder struct {
	store     *storage.Store
	jsonlPath string
	mu        sync.Mutex
}

// NewRecorder creates a Recorder and ensures the JSONL parent directory exists.
func NewRecorder(store *storage.Store, jsonlPath string) (*Recorder, error) {
	if jsonlPath != "" {
		if dir := filepath.Dir(jsonlPath); dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, fmt.Errorf("create jsonl dir: %w", err)
			}
		}
	}
	return &Recorder{
		store:     store,
		jsonlPath: jsonlPath,
	}, nil
}

// Record persists a RequestLog to SQLite and appends a JSON line to telemetry.jsonl.
func (r *Recorder) Record(ctx context.Context, rec storage.RequestLog) error {
	if rec.ID == "" {
		rec.ID = NewID()
	}
	if rec.TsUTC == 0 {
		rec.TsUTC = time.Now().UTC().UnixMilli()
	}

	if r.store != nil {
		if err := r.store.InsertRequestLog(ctx, rec); err != nil {
			return fmt.Errorf("insert request log: %w", err)
		}
	}

	if r.jsonlPath != "" {
		line, err := json.Marshal(rec)
		if err != nil {
			return fmt.Errorf("marshal telemetry jsonl: %w", err)
		}
		line = append(line, '\n')

		r.mu.Lock()
		defer r.mu.Unlock()
		f, err := os.OpenFile(r.jsonlPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return fmt.Errorf("open jsonl %q: %w", r.jsonlPath, err)
		}
		_, writeErr := f.Write(line)
		closeErr := f.Close()
		if writeErr != nil {
			return fmt.Errorf("write jsonl: %w", writeErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close jsonl: %w", closeErr)
		}
	}
	return nil
}

// CalculateCost computes USD cost given per-1M token pricing.
func CalculateCost(p config.ModelPricing, promptTokens, completionTokens, cachedTokens int64) float64 {
	uncachedPrompt := promptTokens - cachedTokens
	if uncachedPrompt < 0 {
		uncachedPrompt = 0
	}
	cachedRate := p.CachedInput
	if cachedRate == 0 && p.Input > 0 {
		cachedRate = p.Input
	}
	cost := (float64(uncachedPrompt)/1_000_000.0)*p.Input +
		(float64(cachedTokens)/1_000_000.0)*cachedRate +
		(float64(completionTokens)/1_000_000.0)*p.Output
	return cost
}

// NewID generates a time-ordered unique identifier.
func NewID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%d-%s", time.Now().UTC().UnixMilli(), hex.EncodeToString(b[:]))
}
