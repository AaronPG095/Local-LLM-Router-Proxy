package proxy

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/router"
)

func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, // 429
		529, // Anthropic Overloaded
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	}
	return false
}

func executeWithFallback(
	ctx context.Context,
	candidates []router.RouteTarget,
	buildReq func(target router.RouteTarget) (*http.Request, error),
	client *http.Client,
) (*http.Response, router.RouteTarget, int, error) {
	return executeWithFallbackTracked(ctx, candidates, nil, nil, buildReq, client)
}

// executeWithFallbackTracked attempts candidates in order, skipping providers in cooldown or at RPM/RPD caps
// (falling back to the last candidate if all are cooling down) and recording 429/529 cooldowns.
func executeWithFallbackTracked(
	ctx context.Context,
	candidates []router.RouteTarget,
	tracker *ProviderTracker,
	providers map[string]config.ProviderConfig,
	buildReq func(target router.RouteTarget) (*http.Request, error),
	client *http.Client,
) (*http.Response, router.RouteTarget, int, error) {
	if len(candidates) == 0 {
		return nil, router.RouteTarget{}, 0, fmt.Errorf("no upstream candidates available")
	}

	now := time.Now().UTC()
	var active []router.RouteTarget
	if tracker != nil {
		for _, c := range candidates {
			if ok, _ := tracker.IsAvailable(c.Provider, providers[c.Provider], now); ok {
				active = append(active, c)
			}
		}
	}
	if len(active) == 0 {
		if tracker != nil {
			// All candidates cooling down/capped: try the last fallback as a last resort.
			active = []router.RouteTarget{candidates[len(candidates)-1]}
		} else {
			active = candidates
		}
	}

	var lastErr error
	for i, target := range active {
		req, err := buildReq(target)
		if err != nil {
			lastErr = err
			continue
		}

		tracker.RecordAttempt(target.Provider, time.Now().UTC())
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, target, i, err
			}
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == 529 {
			tracker.MarkCooldown(target.Provider, resp.Header.Get("Retry-After"), time.Now().UTC())
		}

		if isRetryableStatus(resp.StatusCode) && i < len(active)-1 {
			_ = resp.Body.Close()
			continue
		}

		return resp, target, i, nil
	}

	return nil, active[len(active)-1], len(active) - 1, lastErr
}
