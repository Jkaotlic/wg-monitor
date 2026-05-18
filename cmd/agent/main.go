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
	"github.com/anex/wg-monitor/internal/agent/actions"
	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/internal/agent/checks"
	"github.com/anex/wg-monitor/internal/agent/cmdloop"
	"github.com/anex/wg-monitor/internal/agent/keenetic"
)

// Version is overridable at link time: -ldflags "-X main.Version=0.5.0"
var Version = "0.8.0-tunnel-import"

func main() {
	configPath := flag.String("config", "/opt/etc/wg-monitor/config.yaml", "path to YAML config")
	allowHTTP := flag.Bool("allow-http", false, "allow http:// backend URL (dev only)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		_, _ = os.Stdout.WriteString(Version + "\n")
		return
	}

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
	awgPassword, err := cfg.AwgManager.PasswordValue()
	if err != nil {
		logger.Error("config load", "err", err, "path", *configPath)
		os.Exit(2)
	}
	if cfg.AwgManager.Login != "" || awgPassword != "" {
		awgClient.SetCredentials(cfg.AwgManager.Login, awgPassword)
	}

	// Single-Check probes
	singleChecks := []checks.Check{
		checks.AwgManagerCheck{Client: awgClient},
		checks.HydraRouteCheck{Client: awgClient},
		buildDNSCheck(cfg, awgClient, logger),
	}
	if cfg.ExternalReach.Enabled {
		if er := buildExternalReachCheck(cfg, awgClient, logger); er != nil {
			singleChecks = append(singleChecks, er)
		}
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
		AwgClient:            awgClient,
		ForceRecheck:         rep.ForceResumed,
		Opkg:                 opkg,
		Exec:                 actions.DefaultExec,
		AllowRouterReboot:    cfg.Maintenance.AllowRouterReboot,
		AllowFirmwareInstall: cfg.Maintenance.AllowFirmwareInstall,
	}
	loop := cmdloop.New(client, runner, 30)
	go loop.Run(ctx)

	rep.Run(ctx)
	logger.Info("stopped")
}

func buildDNSCheck(cfg *agent.Config, awgClient *awgmgr.Client, logger *slog.Logger) checks.Check {
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
	ifaceMap, err := fetchIfaceMap(mapCtx, awgClient)
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

func fetchIfaceMap(ctx context.Context, awgClient *awgmgr.Client) (map[string]string, error) {
	ta, err := awgClient.TunnelsAll(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(ta.Tunnels))
	for _, t := range ta.Tunnels {
		if t.NDMSName != "" && t.InterfaceName != "" {
			m[t.NDMSName] = t.InterfaceName
		}
	}
	return m, nil
}

// pickDefaultRouteIface returns the linux iface name (e.g. "nwg1") of the
// tunnel marked defaultRoute=true. Empty when no such tunnel exists or
// awg-manager is unreachable — caller is expected to fall back to the
// system default route.
func pickDefaultRouteIface(ctx context.Context, c *awgmgr.Client, logger *slog.Logger) string {
	ta, err := c.TunnelsAll(ctx)
	if err != nil {
		logger.Warn("external_reach: cannot pick default-route iface", "err", err)
		return ""
	}
	for _, t := range ta.Tunnels {
		if t.DefaultRoute && t.Enabled && t.InterfaceName != "" {
			return t.InterfaceName
		}
	}
	return ""
}

func buildExternalReachCheck(cfg *agent.Config, awgClient *awgmgr.Client, logger *slog.Logger) checks.Check {
	pc := cfg.ExternalReach
	if len(pc.Targets) == 0 {
		logger.Warn("external_reach enabled but no targets — skipping")
		return nil
	}

	targets := make([]checks.ExternalReachTarget, 0, len(pc.Targets))
	for _, t := range pc.Targets {
		targets = append(targets, checks.ExternalReachTarget{Name: t.Name, URL: t.URL})
	}

	httpc := &http.Client{Timeout: 6 * time.Second}
	viaIface := ""
	if pc.BindToDefault {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		viaIface = pickDefaultRouteIface(ctx, awgClient, logger)
		cancel()
		if viaIface != "" {
			httpc = &http.Client{
				Timeout: 6 * time.Second,
				Transport: &http.Transport{
					DialContext: checks.IfaceDialer(viaIface).DialContext,
				},
			}
			logger.Info("external_reach iface-bound", "iface", viaIface, "targets", len(targets))
		} else {
			logger.Warn("external_reach: no defaultRoute tunnel found, using system route")
		}
	}

	return checks.ExternalReachCheck{
		Targets:         targets,
		FailThreshold:   pc.FailThreshold,
		HTTPClient:      httpc,
		PerProbeTimeout: 5 * time.Second,
		ViaInterface:    viaIface,
	}
}
