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
	"github.com/Jkaotlic/wg-monitor/internal/agent/actions"
	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/agent/checks"
	"github.com/Jkaotlic/wg-monitor/internal/agent/cmdloop"
	"github.com/Jkaotlic/wg-monitor/internal/agent/keenetic"
)

// Version is overridable at link time: -ldflags "-X main.Version=0.5.0"
var Version = "0.5.0-awgmgr-pivot-cmdchan-dev"

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
		"interval", cfg.Agent.Interval(), "version", Version,
		"awg_manager", cfg.AwgManager.URL())

	client := agent.NewClient(cfg.Backend.URL, cfg.Backend.Token, Version, 10*time.Second)
	awgClient := awgmgr.New(cfg.AwgManager.URL())

	// Single-Check probes
	singleChecks := []checks.Check{
		checks.AwgManagerCheck{Client: awgClient},
		checks.HydraRouteCheck{Client: awgClient},
		buildDNSCheck(cfg, logger),
	}
	// MultiChecks emit per-tunnel results
	multiChecks := []checks.MultiCheck{
		checks.TunnelsCheck{
			Client:          awgClient,
			HandshakeMaxAge: cfg.Checks.AWG.HandshakeMaxAge(),
		},
	}

	deps := checks.Deps{Runner: checks.OSExec{}}
	rep := agent.NewReporter(agent.ReporterConfig{
		Sender:      client,
		Version:     Version,
		Interval:    cfg.Agent.Interval(),
		Checks:      singleChecks,
		MultiChecks: multiChecks,
		Deps:        deps,
		AwgClient:   awgClient,
		StatePath:   cfg.State.ResolvedPath(),
	})

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Command channel: long-poll /v1/cmd, dispatch to actions.Runner.
	// force_recheck wires straight into reporter.ForceResumed so an admin
	// taps "🔁 Force recheck" in TG and the agent emits a fresh report
	// with Resumed=true within seconds.
	opkg := &actions.OpkgRunner{
		LockPath: "/opt/var/wg-monitor/opkg.lock",
		LockTTL:  8 * time.Minute,
		Exec:     actions.DefaultExec,
	}
	runner := &actions.Runner{
		AwgClient:    awgClient,
		ForceRecheck: rep.ForceResumed,
		Opkg:         opkg,
	}
	loop := cmdloop.New(client, runner, 30)
	go loop.Run(ctx)

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

	mapCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ifaceMap, err := keenetic.FetchIfaceMap(mapCtx, keenetic.IfaceMapOptions{AwgManagerURL: cfg.AwgManager.URL()})
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
		RKNTestDomains:  dc.RKNTestDomains,
	}
}
