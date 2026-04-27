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

	"github.com/anex/wg-monitor/internal/agent"
	"github.com/anex/wg-monitor/internal/agent/checks"
	"github.com/anex/wg-monitor/internal/agent/checks/wgreader"
	"github.com/anex/wg-monitor/internal/agent/keenetic"
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
		checks.AwgMarker{Iface: cfg.Checks.AWG.Interface, URL: cfg.Checks.AWG.ResolvedMarkerURL(), MaxRetries: 3, BaseBackoff: 250 * time.Millisecond},
		buildDNSCheck(cfg, logger),
	}
	wgr := wgreader.Detect(checks.OSExec{})
	logger.Info("wg reader strategies", "strategies", wgr.Strategies())

	deps := checks.Deps{Runner: checks.OSExec{}, HTTPClient: httpc, WGReader: wgr}
	rep := agent.NewReporter(client, Version, cfg.Agent.Interval(), chks, deps)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	rep.Run(ctx)
	logger.Info("stopped")
}

func buildDNSCheck(cfg *agent.Config, logger *slog.Logger) checks.Check {
	dc := cfg.Checks.DNS

	var endpoints []keenetic.DNSEndpoint
	if dc.AutoDiscover {
		runner := checks.OSExec{}
		ndmc := keenetic.NDMC{Runner: runner}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		rc, err := ndmc.Show(ctx, "running-config")
		cancel()
		if err != nil {
			logger.Warn("dns auto-discover skipped", "err", err)
		} else {
			endpoints = append(endpoints, keenetic.ParseDNSEndpoints(rc)...)
			logger.Info("dns auto-discovered", "count", len(endpoints))
		}
	}

	// Merge with manual endpoints from config.
	for _, ec := range dc.Endpoints {
		ep := keenetic.DNSEndpoint{
			Type:     ec.Type,
			Host:     ec.Host,
			Port:     ec.Port,
			URL:      ec.URL,
			NDMSName: ec.NDMSName,
		}
		if ep.Port == 0 {
			switch ep.Type {
			case "plain":
				ep.Port = 53
			case "dot":
				ep.Port = 853
			}
		}
		endpoints = append(endpoints, ep)
	}

	// Iface map for NDMSName resolution.
	mapCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ifaceMap, err := keenetic.FetchIfaceMap(mapCtx, keenetic.IfaceMapOptions{})
	if err != nil {
		logger.Warn("iface map unavailable; plain DNS will use default routing", "err", err)
		ifaceMap = nil
	}

	return checks.DNS{
		Endpoints:       endpoints,
		TestDomain:      dc.TestDomain,
		FailThreshold:   dc.FailThreshold,
		IfaceDialFn:     checks.IfaceDialer,
		HTTPClient:      &http.Client{Timeout: 5 * time.Second},
		PerProbeTimeout: 3 * time.Second,
		IfaceMap:        ifaceMap,
	}
}
