package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/anex/wg-monitor/internal/agent"
	"github.com/anex/wg-monitor/internal/agent/actions"
	"github.com/anex/wg-monitor/internal/agent/awgmgr"
	"github.com/anex/wg-monitor/internal/agent/checks"
	"github.com/anex/wg-monitor/internal/agent/cmdloop"
	"github.com/anex/wg-monitor/internal/agent/keenetic"
	"github.com/anex/wg-monitor/pkg/wire"
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
		ConfigPath:  *configPath,
		BackendURL:  cfg.Backend.URL,
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
		ConfigPath:           *configPath,
		BackendURL:           cfg.Backend.URL,
		Version:              Version,
	}
	loop := cmdloop.New(client, runner, 30)
	loop.SetResultCachePath(cfg.State.CommandResultPath())
	go loop.Run(ctx)

	rep.Run(ctx)
	logger.Info("stopped")
}

func buildDNSCheck(cfg *agent.Config, awgClient *awgmgr.Client, logger *slog.Logger) checks.Check {
	dc := cfg.Checks.DNS

	var endpoints []keenetic.DNSEndpoint
	var endpointProvider func(context.Context) ([]keenetic.DNSEndpoint, error)
	if dc.AutoDiscover {
		runner := checks.OSExec{}
		ndmc := keenetic.NDMC{Runner: runner}
		endpointProvider = func(ctx context.Context) ([]keenetic.DNSEndpoint, error) {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			rc, err := ndmc.Show(ctx, "running-config")
			if err != nil {
				if logger != nil {
					logger.Warn("dns auto-discover skipped", "err", err)
				}
				return nil, err
			}
			return keenetic.ParseDNSEndpoints(rc), nil
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

	return checks.DNS{
		Endpoints:        endpoints,
		EndpointProvider: endpointProvider,
		TestDomain:       dc.TestDomain,
		FailThreshold:    dc.FailThreshold,
		IfaceDialFn:      checks.IfaceDialer,
		HTTPClient:       &http.Client{Timeout: 5 * time.Second},
		PerProbeTimeout:  3 * time.Second,
		IfaceMapProvider: func(ctx context.Context) (map[string]string, error) {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			ifaceMap, err := fetchIfaceMap(ctx, awgClient)
			if err != nil {
				if logger != nil {
					logger.Warn("iface map unavailable; plain DNS will use default routing", "err", err)
				}
				return nil, err
			}
			return ifaceMap, nil
		},
		RKNTestDomains: dc.RKNTestDomains,
	}
}

func fetchIfaceMap(ctx context.Context, awgClient *awgmgr.Client) (map[string]string, error) {
	ta, err := awgClient.TunnelsAll(ctx)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(ta.Tunnels))
	for _, t := range ta.Tunnels {
		if t.NDMSName != "" && t.InterfaceName != "" && dnsIfaceMapTunnelUsable(t) {
			m[t.NDMSName] = t.InterfaceName
		}
	}
	return m, nil
}

func dnsIfaceMapTunnelUsable(t awgmgr.Tunnel) bool {
	if !t.Enabled {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(t.Status))
	if status == "" {
		status = strings.ToLower(strings.TrimSpace(t.State))
	}
	return status == "" || status == "running" || status == "connected"
}

// pickDefaultRouteIface returns the linux iface name (e.g. "nwg1") of the
// tunnel marked defaultRoute=true. Empty when no such tunnel exists or
// awg-manager is unreachable — caller is expected to fall back to the
// system default route.
func pickDefaultRouteIface(ctx context.Context, c *awgmgr.Client, logger *slog.Logger) string {
	if c == nil {
		return ""
	}
	ta, err := c.TunnelsAll(ctx)
	if err != nil {
		if logger != nil {
			logger.Warn("external_reach: cannot pick default-route iface", "err", err)
		}
		return ""
	}
	// Prefer the tunnel awg-manager actually routes default traffic through
	// (settings.download.routeTag). Several tunnels can each report
	// defaultRoute=true, but only one is the live egress; binding external_reach
	// to the first-listed default would probe the wrong tunnel and give a false
	// reachability signal. Best-effort: on Settings error keep the legacy
	// first-defaultRoute heuristic.
	activeID := ""
	if s, serr := c.Settings(ctx); serr == nil {
		activeID = s.ActiveDefaultTunnelID()
	}
	if activeID != "" {
		for _, t := range ta.Tunnels {
			if t.ID == activeID && t.Enabled && t.InterfaceName != "" {
				return t.InterfaceName
			}
		}
	}
	for _, t := range ta.Tunnels {
		if t.DefaultRoute && t.Enabled && t.InterfaceName != "" {
			return t.InterfaceName
		}
	}
	return ""
}

var externalReachHTTPClientForIface = func(iface string) *http.Client {
	return &http.Client{
		Timeout: 6 * time.Second,
		Transport: &http.Transport{
			DialContext: checks.IfaceDialer(iface).DialContext,
		},
	}
}

type defaultRouteExternalReachCheck struct {
	targets       []checks.ExternalReachTarget
	failThreshold int
	awgClient     *awgmgr.Client
	logger        *slog.Logger
}

func (c defaultRouteExternalReachCheck) Name() string { return "external_reach" }

func (c defaultRouteExternalReachCheck) Run(ctx context.Context, deps checks.Deps) wire.Check {
	start := time.Now()
	// When the sing-box router is the active routing method (e.g. snekhaev:
	// singboxRouter.enabled + deviceMode "all"), awg-manager routes per sing-box's
	// own policy using the tunnels as outbounds, and download.routeTag is "direct".
	// A single iface-bound (or WAN-direct) probe from the router cannot replicate
	// that per-destination routing, so it gives a misleading signal — which showed
	// up as a false "external services unreachable" HARD. Report OK with a note
	// that routing is sing-box-managed instead of probing.
	if c.awgClient != nil {
		sbCtx, sbCancel := context.WithTimeout(ctx, 5*time.Second)
		s, err := c.awgClient.Settings(sbCtx)
		sbCancel()
		if err == nil && s.SingboxRouterActive() {
			if c.logger != nil {
				c.logger.Info("external_reach: sing-box router active — skipping router-side probe", "device_mode", s.SingboxRouter.DeviceMode)
			}
			return checks.OK("external_reach", start, map[string]any{
				"skipped":     true,
				"reason":      "singbox_router_active",
				"routing_via": "singbox",
				"device_mode": s.SingboxRouter.DeviceMode,
			})
		}
	}
	pickCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	viaIface := pickDefaultRouteIface(pickCtx, c.awgClient, c.logger)
	cancel()
	if viaIface == "" {
		if c.logger != nil {
			c.logger.Warn("external_reach: no defaultRoute tunnel found")
		}
		return checks.ExternalReachCheck{
			Targets:         c.targets,
			FailThreshold:   c.failThreshold,
			PerProbeTimeout: 5 * time.Second,
			ConfigReason:    "no_default_route_tunnel",
			ConfigError:     "no enabled defaultRoute tunnel found in awg-manager; external_reach is configured to bind to a tunnel and will not probe through system WAN",
		}.Run(ctx, deps)
	}
	if c.logger != nil {
		c.logger.Info("external_reach iface-bound", "iface", viaIface, "targets", len(c.targets))
	}
	return checks.ExternalReachCheck{
		Targets:         c.targets,
		FailThreshold:   c.failThreshold,
		HTTPClient:      externalReachHTTPClientForIface(viaIface),
		PerProbeTimeout: 5 * time.Second,
		ViaInterface:    viaIface,
	}.Run(ctx, deps)
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
	if pc.BindToDefault {
		return defaultRouteExternalReachCheck{
			targets:       targets,
			failThreshold: pc.FailThreshold,
			awgClient:     awgClient,
			logger:        logger,
		}
	}

	return checks.ExternalReachCheck{
		Targets:         targets,
		FailThreshold:   pc.FailThreshold,
		HTTPClient:      httpc,
		PerProbeTimeout: 5 * time.Second,
	}
}
