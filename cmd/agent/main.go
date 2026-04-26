package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent"
	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
)

// Version is overridable at link time: -ldflags "-X main.Version=0.1.0"
var Version = "0.1.0-dev"

func main() {
	configPath := flag.String("config", "/opt/etc/wg-monitor/config.yaml", "path to YAML config")
	allowHTTP := flag.Bool("allow-http", false, "allow http:// backend URL (dev only)")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	var loadOpts []agent.LoadOption
	if *allowHTTP {
		loadOpts = append(loadOpts, agent.WithAllowHTTP())
	}
	cfg, err := agent.LoadConfig(*configPath, loadOpts...)
	if err != nil {
		logger.Error("config load", "err", err, "path", *configPath)
		os.Exit(2)
	}
	logger.Info("starting", "nickname", cfg.Agent.Nickname, "backend", cfg.Backend.URL,
		"interval", cfg.Agent.Interval(), "version", Version)

	client := agent.NewClient(cfg.Backend.URL, cfg.Backend.Token, Version, 10*time.Second)
	httpc := &http.Client{
		Transport: &http.Transport{
			DialContext: checks.IfaceDialer(cfg.Checks.AWG.Interface).DialContext,
		},
		Timeout: 12 * time.Second,
	}
	chks := []checks.Check{
		checks.AwgHandshake{Iface: cfg.Checks.AWG.Interface, MaxAge: cfg.Checks.AWG.HandshakeMaxAge()},
		checks.AwgRouting{Iface: cfg.Checks.AWG.Interface, URL: cfg.Checks.AWG.RoutingURL(), Expected: cfg.Checks.AWG.ExpectedExitIP},
		checks.AwgMarker{Iface: cfg.Checks.AWG.Interface, URL: cfg.Checks.AWG.MarkerURL, MaxRetries: 3, BaseBackoff: 250 * time.Millisecond},
		dnsCheckFromCfg(cfg.Checks.DNS),
	}
	deps := checks.Deps{Runner: checks.OSExec{}, HTTPClient: httpc}
	rep := agent.NewReporter(client, Version, cfg.Agent.Interval(), chks, deps)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	rep.Run(ctx)
	logger.Info("stopped")
}

func dnsCheckFromCfg(c agent.DNSCheckConfig) checks.DNSDoH {
	provs := make([]checks.DNSProvider, len(c.Providers))
	for i, p := range c.Providers {
		provs[i] = checks.DNSProvider{Name: p.Name, Host: p.Host}
	}
	return checks.DNSDoH{Providers: provs, TestDomain: c.TestDomain, FailThreshold: c.FailThreshold}
}
