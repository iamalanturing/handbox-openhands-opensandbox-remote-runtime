// Command handbox translates OpenHands Remote Runtime API calls into
// OpenSandbox API calls, applying network-level policy on every sandbox
// creation.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/iamalanturing/handbox-openhands-opensandbox-remote-runtime/internal/config"
	"github.com/iamalanturing/handbox-openhands-opensandbox-remote-runtime/internal/levels"
	"github.com/iamalanturing/handbox-openhands-opensandbox-remote-runtime/internal/opensandbox"
	"github.com/iamalanturing/handbox-openhands-opensandbox-remote-runtime/internal/server"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	state := levels.New(cfg.StateFilePath, cfg.SingleUseLevel)
	sandboxClient := opensandbox.NewClient(cfg.OpenSandboxURL, cfg.OpenSandboxAPIKey, nil)
	srv := server.New(server.Config{
		SandboxAPIKey:     cfg.SandboxAPIKey,
		ResearchAllowlist: cfg.ResearchAllowlist,
		OllamaHost:        cfg.OllamaHost,
	}, state, sandboxClient)

	httpServer := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: srv.Handler(),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		log.Printf("handbox listening on %s", cfg.ListenAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
}
