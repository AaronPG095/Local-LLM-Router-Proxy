package proxy

import (
	"context"
	"fmt"
	"net/http"

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

// executeWithFallback attempts candidates in order until one succeeds or returns a non-retryable status.
// It executes strictly before any bytes are written to the client ResponseWriter.
func executeWithFallback(
	ctx context.Context,
	candidates []router.RouteTarget,
	buildReq func(target router.RouteTarget) (*http.Request, error),
	client *http.Client,
) (*http.Response, router.RouteTarget, int, error) {
	if len(candidates) == 0 {
		return nil, router.RouteTarget{}, 0, fmt.Errorf("no upstream candidates available")
	}

	var lastErr error
	for i, target := range candidates {
		req, err := buildReq(target)
		if err != nil {
			lastErr = err
			continue
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if ctx.Err() != nil {
				return nil, target, i, err
			}
			continue
		}

		if isRetryableStatus(resp.StatusCode) && i < len(candidates)-1 {
			_ = resp.Body.Close()
			continue
		}

		return resp, target, i, nil
	}

	return nil, candidates[len(candidates)-1], len(candidates) - 1, lastErr
}
