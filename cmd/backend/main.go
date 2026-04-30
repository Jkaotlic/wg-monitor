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
	"github.com/anex/wg-monitor/internal/backend/alerts"
	"github.com/anex/wg-monitor/internal/backend/callbacks"
	"github.com/anex/wg-monitor/internal/backend/cmd"
	"github.com/anex/wg-monitor/internal/backend/db"
	"github.com/anex/wg-monitor/internal/backend/heartbeat"
	"github.com/anex/wg-monitor/internal/backend/realert"
	"github.com/anex/wg-monitor/internal/backend/state"
	"github.com/anex/wg-monitor/internal/backend/tg"
)

var Version = "0.5.0-awgmgr-pivot"

func main() {
	cfgPath := flag.String("config", "/etc/wg-monitor/backend.yaml", "path to backend config yaml")
	flag.Parse()

	cfg, err := backend.LoadConfig(*cfgPath)
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(2)
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: parseLevel(cfg.LogLevel)}))
	slog.SetDefault(logger)

	d, err := db.Open(cfg.DBPath)
	if err != nil {
		logger.Error("db open", "err", err)
		os.Exit(2)
	}
	defer d.Close()

	tgClient := &tg.Client{
		BaseURL:      tg.DefaultBaseURL,
		Token:        cfg.Telegram.BotToken,
		HTTP:         &http.Client{Timeout: 15 * time.Second},
		LongPollHTTP: &http.Client{Timeout: 90 * time.Second},
	}
	disp := alerts.NewDispatcher(d, tgClient, alerts.Config{
		ChatID:            cfg.Telegram.ChatID,
		FailThreshold:     cfg.State.FailThreshold,
		RecoveryThreshold: cfg.State.RecoveryThreshold,
	})

	watcher := heartbeat.NewWatcher(d, disp, heartbeat.Config{
		StaleAfter:       time.Duration(cfg.Heartbeat.StaleAfterSec) * time.Second,
		StaleAfterStatic: time.Duration(cfg.Heartbeat.StaleAfterStaticSec) * time.Second,
		StaleAfterMobile: time.Duration(cfg.Heartbeat.StaleAfterMobileSec) * time.Second,
		ResumeGrace:      time.Duration(cfg.Heartbeat.ResumeGraceSec) * time.Second,
		ScanEvery:        time.Duration(cfg.Heartbeat.ScanIntervalSec) * time.Second,
	})

	cmdQueue := cmd.New()
	mux := backend.NewMux(backend.Deps{
		Logger:      logger,
		DB:          d,
		Dispatcher:  disp,
		Resumer:     watcher,
		CommandSink: cmdQueue,
		Thresholds:  state.Thresholds{Fail: cfg.State.FailThreshold, Recovery: cfg.State.RecoveryThreshold},
	})
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go watcher.Run(ctx)

	cb := callbacks.NewRouterWithSink(d, tgClient, cmdQueue, callbacks.Config{
		ChatID:         cfg.Telegram.ChatID,
		AdminUserID:    cfg.Telegram.AdminUserID,
		MuteCutoffHour: cfg.State.MuteCutoffHour,
	})
	go func() {
		if err := cb.Run(ctx); err != nil {
			logger.Error("callbacks router exited", "err", err)
		}
	}()

	rp := realert.NewPoller(d, tgClient, realert.Config{
		ChatID:       cfg.Telegram.ChatID,
		RealertEvery: time.Duration(cfg.State.RealertEverySec) * time.Second,
		TickEvery:    time.Duration(cfg.State.RealertTickSec) * time.Second,
	})
	go func() {
		if err := rp.Run(ctx); err != nil {
			logger.Error("realert poller exited", "err", err)
		}
	}()

	go func() {
		logger.Info("backend listening", "addr", cfg.Listen, "version", Version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("listen", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutCancel()
	_ = srv.Shutdown(shutCtx)
	watcher.WaitForExit()
	rp.WaitForExit()
	logger.Info("backend stopped")
}

func parseLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}
