package main

import (
	"log"
	"os"

	"github.com/AaronPG095/Local-LLM-Router-Proxy/internal/mcp"
)

func main() {
	routerURL := os.Getenv("LLM_ROUTER_URL")
	if routerURL == "" {
		routerURL = "http://127.0.0.1:8787"
	}
	srv := mcp.NewServer(routerURL)
	if err := srv.Serve(os.Stdin, os.Stdout); err != nil {
		log.Fatalf("mcp serve error: %v", err)
	}
}
