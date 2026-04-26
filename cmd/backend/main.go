package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anex/wg-monitor/internal/backend"
)

func main() {
	configPath := flag.String("config", "/etc/wg-monitor/backend.yaml", "path to YAML config")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := backend.LoadConfig(*configPath)
	if err != nil {
		logger.Error("config load", "err", err, "path", *configPath)
		os.Exit(2)
	}
	logger.Info("starting", "listen", cfg.Listen, "agents", len(cfg.Agents))

	tokenMap := make(map[string]string, len(cfg.Agents))
	for _, a := range cfg.Agents {
		tokenMap[a.Token] = a.Nickname
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           backend.NewMux(logger, tokenMap),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		logger.Info("shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("listen", "err", err)
		os.Exit(1)
	}
	logger.Info("stopped")
}
