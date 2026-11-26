package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// EnsureProxyRunning checks /healthz and starts llm-router detached if it is not yet running.
func (s *Server) EnsureProxyRunning(ctx context.Context) error {
	if s.isHealthy(ctx) {
		return nil
	}

	s.startMu.Lock()
	defer s.startMu.Unlock()

	if s.isHealthy(ctx) {
		return nil
	}
	if !isLocalURL(s.routerURL) {
		return fmt.Errorf("refusing to auto-start non-local router URL %q", s.routerURL)
	}
	if s.startProxyFn == nil {
		return fmt.Errorf("no proxy starter configured")
	}
	if err := s.startProxyFn(); err != nil {
		return err
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if s.isHealthy(ctx) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
	return fmt.Errorf("timed out waiting for %s/healthz", s.routerURL)
}

func (s *Server) isHealthy(ctx context.Context) bool {
	hCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(hCtx, http.MethodGet, s.routerURL+"/healthz", nil)
	if err != nil {
		return false
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func isLocalURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

func (s *Server) defaultStartProxy() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable path: %w", err)
	}
	binDir := filepath.Dir(exePath)
	repoRoot := filepath.Dir(binDir)

	proxyName := "llm-router"
	if runtime.GOOS == "windows" {
		proxyName = "llm-router.exe"
	}
	proxyExe := filepath.Join(binDir, proxyName)
	cfgPath := filepath.Join(repoRoot, "router.config.yaml")

	if _, err := os.Stat(proxyExe); err != nil {
		return fmt.Errorf("proxy binary missing at %s: %w", proxyExe, err)
	}
	if _, err := os.Stat(cfgPath); err != nil {
		return fmt.Errorf("config missing at %s: %w", cfgPath, err)
	}

	cmd := exec.Command(proxyExe, "-config", cfgPath)
	cmd.Dir = repoRoot
	detachProcess(cmd)
	return cmd.Start()
}
