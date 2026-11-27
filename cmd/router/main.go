package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/budget"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/config"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/proxy"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/storage"
	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/telemetry"
)

func main() {
	cfgPath := flag.String("config", "router.config.yaml", "path to router YAML config")
	credentialStore := flag.String("credential-store", os.Getenv("LLM_ROUTER_CREDENTIAL_STORE"), "override the Windows DPAPI credential store path")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if err := cfg.LoadCredentials(*credentialStore); err != nil {
		log.Fatalf("load credentials: %v", err)
	}

	store, err := storage.Open(cfg.Telemetry.SQLitePath)
	if err != nil {
		log.Fatalf("open sqlite: %v", err)
	}
	defer store.Close()

	recorder, err := telemetry.NewRecorder(store, cfg.Telemetry.JSONLPath)
	if err != nil {
		log.Fatalf("init telemetry recorder: %v", err)
	}

	guard := budget.NewGuard(store, cfg.Budgets)
	srv := proxy.NewServer(cfg, store, recorder, guard)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	httpServer := &http.Server{
		Addr:    addr,
		Handler: srv.Handler(),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("Local LLM Router Proxy listening on http://%s", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down proxy...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
