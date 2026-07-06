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

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/callbacks"
	"github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/digest"
	"github.com/Jkaotlic/wg-monitor/internal/backend/heartbeat"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/realert"
	"github.com/Jkaotlic/wg-monitor/internal/backend/retention"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/internal/backend/upstream"
)

var Version = "0.8.0-tunnel-import"

func telegramCommandMenu() []tg.BotCommand {
	return telegramOperatorCommandMenu()
}

func telegramOperatorCommandMenu() []tg.BotCommand {
	return tg.OperatorBotCommands()
}

func telegramAdminCommandMenu() []tg.BotCommand {
	return tg.AdminBotCommands()
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "backup" {
		if err := runBackupCommand(os.Args[2:]); err != nil {
			slog.Error("backup", "err", err)
			os.Exit(2)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "backend-update-runner" {
		if err := runBackendUpdateRunnerCommand(os.Args[2:]); err != nil {
			slog.Error("backend-update-runner", "err", err)
			os.Exit(2)
		}
		return
	}

	cfgPath := flag.String("config", "/etc/wg-monitor/backend.yaml", "path to backend config yaml")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		_, _ = os.Stdout.WriteString(Version + "\n")
		return
	}

	cfg, err := backend.LoadConfig(*cfgPath)
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(2)
	}
	backend.SetVersion(Version)
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
		Logger:       logger.With("component", "tg"),
	}
	// Register scoped slash-command menus so operators see router commands by
	// default while the admin gets fleet/topic-management commands.
	smcCtx, smcCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := tgClient.SetMyCommands(smcCtx, telegramOperatorCommandMenu()); err != nil {
		logger.Warn("setMyCommands operator failed (non-fatal)", "err", err)
	}
	if err := tgClient.SetCommandsMenuButton(smcCtx); err != nil {
		logger.Warn("setChatMenuButton commands failed (non-fatal)", "err", err)
	}
	adminCommands := telegramAdminCommandMenu()
	if cfg.Telegram.AdminUserID != 0 {
		if err := tgClient.SetMyCommandsWithScope(smcCtx, adminCommands, tg.BotCommandScope{
			Type:   "chat_member",
			ChatID: cfg.Telegram.ChatID,
			UserID: cfg.Telegram.AdminUserID,
		}); err != nil {
			logger.Warn("setMyCommands admin group scope failed (non-fatal)", "err", err)
		}
		if err := tgClient.SetMyCommandsWithScope(smcCtx, adminCommands, tg.BotCommandScope{
			Type:   "chat",
			ChatID: cfg.Telegram.AdminUserID,
		}); err != nil {
			logger.Warn("setMyCommands admin private scope failed (non-fatal)", "err", err)
		}
	}
	smcCancel()
	disp := alerts.NewDispatcher(d, tgClient, alerts.Config{
		ChatID:            cfg.Telegram.ChatID,
		FailThreshold:     cfg.State.FailThreshold,
		RecoveryThreshold: cfg.State.RecoveryThreshold,
	})

	mobileLifecycle := cfg.Heartbeat.MobileLifecycle == nil || *cfg.Heartbeat.MobileLifecycle
	watcher := heartbeat.NewWatcher(d, disp, heartbeat.Config{
		StaleAfter:       time.Duration(cfg.Heartbeat.StaleAfterSec) * time.Second,
		StaleAfterStatic: time.Duration(cfg.Heartbeat.StaleAfterStaticSec) * time.Second,
		StaleAfterMobile: time.Duration(cfg.Heartbeat.StaleAfterMobileSec) * time.Second,
		MobileSleepAfter: time.Duration(cfg.Heartbeat.MobileSleepAfterSec) * time.Second,
		MobileLifecycle:  mobileLifecycle,
		ResumeGrace:      time.Duration(cfg.Heartbeat.ResumeGraceSec) * time.Second,
		ScanEvery:        time.Duration(cfg.Heartbeat.ScanIntervalSec) * time.Second,
		RenotifyEvery:    time.Duration(cfg.State.RealertEverySec) * time.Second,
	})

	// Mobile-lifecycle notifiers: wake-card on Resumed=true, one-shot sleep-info
	// after MobileSleepAfter silence. Both no-op for static users / when
	// telegram_thread_id is NULL.
	wakeNotifier := alerts.NewWakeNotifier(d, tgClient, cfg.Telegram.ChatID)
	sleepNotifier := alerts.NewSleepNotifier(d, tgClient, cfg.Telegram.ChatID)
	deployNotifier := alerts.NewDeployNotifier(d, tgClient, cfg.Telegram.ChatID)
	watcher.SetSleepNotifier(sleepNotifier)

	cmdQueue := cmd.New()
	cmdQueue.SetLogger(logger.With("component", "cmd_queue"))
	backend.AttachDeployExpiryHandler(cmdQueue, d, logger)
	uiSnap := callbacks.UIConfigSnapshot{
		DeleteUserCommandMessages: cfg.UI.DeleteUserCommandMessages != nil && *cfg.UI.DeleteUserCommandMessages,
		SmartReplyWithKeyboard:    cfg.UI.SmartReplyWithKeyboard != nil && *cfg.UI.SmartReplyWithKeyboard,
		DiagMaxChars:              cfg.UI.DiagMaxChars,
		CompatInlineKeyboard:      cfg.UI.CompatInlineKeyboard != nil && *cfg.UI.CompatInlineKeyboard,
	}
	disp.WelcomeKeyboard = func() any {
		return tg.ReplyKeyboardForTopic("per_router")
	}
	disp.WelcomeVisibleKeyboard = func() any {
		return tg.OperatorMenuInlineKeyboardForTopic("per_router")
	}
	notifier := callbacks.NewNotifierWithUI(tgClient, uiSnap)
	routesCache := &callbacks.RoutesCache{TTL: 30 * time.Second}
	// Build upstream version cache from configured GitHub repos. Skip sources
	// without a configured repo — graceful "no warning" beats fabricated data.
	var upSources []upstream.Source
	if cfg.Upstream.AwgmgrRepo != "" {
		upSources = append(upSources, upstream.Source{Name: "awgmgr", GitHubRepo: cfg.Upstream.AwgmgrRepo})
	}
	if cfg.Upstream.HrneoRepo != "" {
		upSources = append(upSources, upstream.Source{Name: "hrneo", GitHubRepo: cfg.Upstream.HrneoRepo})
	}
	upCache := upstream.NewCache(cfg.Upstream.CacheTTL, upSources)

	// Build the callbacks router BEFORE the mux Deps so we can derive the
	// MaintNotifier from it (its internal cooldown + audit cache stores
	// must be shared between handlers and notifier).
	cb := callbacks.NewRouterWithSink(d, tgClient, cmdQueue, callbacks.Config{
		ChatID:             cfg.Telegram.ChatID,
		ExtraChatIDs:       cfg.Telegram.ExtraChatIDs,
		AdminUserID:        cfg.Telegram.AdminUserID,
		MuteCutoffHour:     cfg.State.MuteCutoffHour,
		BackendVersion:     Version,
		PublicBaseURL:      cfg.PublicBaseURL,
		UI:                 uiSnap,
		AmneziaBaseURL:     cfg.Amnezia.BaseURL,
		AmneziaSecretsPath: cfg.Amnezia.SecretsPath,
		SelfHostedAmnezia:  cfg.SelfHostedAmnezia,
		HideMyBaseURL:      cfg.HideMy.BaseURL,
		HideMySecretsPath:  cfg.HideMy.SecretsPath,
	})
	cb.SetRoutesCache(routesCache)
	notifier.TunnelsRefreshSink = cmdQueue
	routesNotifier := &callbacks.RoutesPanelNotifier{
		TG:    tgClient,
		Cache: routesCache,
		DB:    d,
		Store: cb.RouteWizardStore(),
		Sink:  cmdQueue,
	}
	cb.SetUpstream(upCache)
	notifier.DiagCache = cb.DiagCache()
	maintNotifier := cb.NewMaintNotifier(tgClient, upCache)
	cb.SetPingCheck(cmdQueue)
	cb.SetDiagDrillDown()
	pingcheckNotifier := cb.NewPingCheckNotifier()

	// OPKG feed repair plumbing — store holds tokens for pending 🔧 button taps;
	// action consumes them and enqueues opkg_feed_disable commands. Notifier
	// renders the message + buttons when CommandResult comes back. All in-memory.
	opkgRepairStore := callbacks.NewPendingOpkgRepairStore()
	opkgRepairAction := callbacks.NewOpkgRepairAction(cmdQueue, opkgRepairStore, nil)
	cb.SetOpkgRepair(opkgRepairStore, opkgRepairAction)
	opkgNotifier := callbacks.NewOpkgResultNotifier(tgClient, uiSnap, opkgRepairStore, nil)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	dashboardMobileStaleAfter := cfg.Heartbeat.StaleAfterMobileSec
	if mobileLifecycle {
		dashboardMobileStaleAfter = cfg.Heartbeat.MobileSleepAfterSec
	}

	// Router-provisioning engine (dashboard "Provision router" + repair
	// repoint/reinstall — see internal/backend/provision). BaseCtx is this
	// same process-owned ctx (cancelled on SIGINT/SIGTERM, same one wired as
	// ShutdownCtx below), NEVER a request ctx: the worker derives its own
	// timeout-bound contexts from it, so a run started by an HTTP handler
	// survives that handler returning (see provision.Deps' own doc). LastSeen
	// reads the exact column POST /v1/report updates (provisionLastSeen, this
	// package) — anything else would make verify_online lag or miss reports.
	provisionStore := provision.NewStore()
	provisionDeps := provision.Deps{
		Store:    provisionStore,
		BaseCtx:  ctx,
		Relay:    provision.DefaultRelay,
		LastSeen: provisionLastSeen(d),
		Now:      time.Now,
		Logger:   logger.With("component", "provision"),
	}

	mux := backend.NewMux(backend.Deps{
		Logger:              logger,
		DB:                  d,
		Dispatcher:          disp,
		Resumer:             watcher,
		CommandSink:         cmdQueue,
		TGNotifier:          notifier,
		RoutesNotifier:      routesNotifier,
		MaintNotifier:       maintNotifier,
		BulkNotifier:        cb,
		OpkgNotifier:        opkgNotifier,
		PingCheckNotifier:   pingcheckNotifier,
		WakeNotifier:        wakeNotifier,
		DeployNotifier:      deployNotifier,
		UI:                  cfg.UI,
		Thresholds:          state.Thresholds{Fail: cfg.State.FailThreshold, Recovery: cfg.State.RecoveryThreshold},
		AlertPolicy:         backend.AlertPolicy{NoisyFailThreshold: cfg.State.NoisyFailThreshold, NoisyRecoveryThreshold: cfg.State.NoisyRecoveryThreshold},
		MobileFailThreshold: cfg.State.MobileFailThreshold,
		// Wire the server-shutdown ctx so cmd-result relay goroutines respect
		// SIGTERM and don't outlive srv.Shutdown (BUG-15).
		ShutdownCtx: ctx,
		// Per-token rate limit on /v1/report (API-06). Defaults applied in
		// LoadConfig so production yaml without rate_limit section gets sane
		// throttling automatically.
		ReportRatePerSec:          cfg.RateLimit.ReportPerSec,
		ReportBurst:               cfg.RateLimit.ReportBurst,
		MobileWakeAfter:           time.Duration(cfg.Heartbeat.MobileSleepAfterSec) * time.Second,
		DashboardStaleAfterStatic: time.Duration(cfg.Heartbeat.StaleAfterStaticSec) * time.Second,
		DashboardStaleAfterMobile: time.Duration(dashboardMobileStaleAfter) * time.Second,
		WizardToken:               cfg.Wizard.Token,
		DashboardToken:            cfg.Dashboard.Token,
		TelegramPrimaryChatID:     cfg.Telegram.ChatID,
		TelegramExtraChatIDs:      cfg.Telegram.ExtraChatIDs,
		BackendUpdatePath:         backend.DefaultBackendUpdatePath(cfg),
		PublicBaseURL:             cfg.PublicBaseURL,
		Provision:                 provisionDeps,
	})
	srv := &http.Server{
		Addr:    cfg.Listen,
		Handler: mux,
		// Timeouts защищают от slow-loris и за-висших клиентов.
		// ReadHeaderTimeout — отдельно жёстко (5s), даже long-poll должен
		// прислать заголовки моментально. ReadTimeout 30s покрывает обычные
		// /v1/report (агенты в 4G) с запасом. WriteTimeout 90s = 60s
		// max-cmdWait (cmdGetHandler) + 30s slack — иначе long-poll
		// завершается раньше времени. IdleTimeout даёт keep-alive 120s.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      90 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// notifyDegradation surfaces a fatal background-goroutine exit to the
	// admin via TG. Без этого backend продолжал отвечать /healthz=200 пока
	// alert dispatcher / realert poller / callbacks router молча умерли —
	// операторы узнавали о проблеме только когда переставали приходить
	// уведомления. ctx canceled (SIGTERM/Int) → nil notify (graceful).
	notifyDegradation := func(component string, err error) {
		if err == nil || ctx.Err() != nil {
			return
		}
		text := "🛑 *backend " + component + " exited*\n```\n" + err.Error() + "\n```\nрестарт нужен."
		alertCtx, alertCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer alertCancel()
		if _, sendErr := tgClient.SendMessage(alertCtx, cfg.Telegram.ChatID, nil, text, "MarkdownV2", nil); sendErr != nil {
			logger.Error("degradation TG alert failed", "component", component, "err", sendErr)
		}
	}

	go func() {
		watcher.Run(ctx)
	}()

	retentionPolicy := &retention.Policy{
		DB: d,
		Cfg: retention.Config{
			EventsDays:         cfg.Retention.EventsDays,
			VacuumInterval:     cfg.Retention.VacuumInterval,
			WALCheckpointEvery: cfg.Retention.WALCheckpointEvery,
		},
		Logger: logger,
	}
	go retentionPolicy.Run(ctx)

	// Janitor: evict origin/result entries from the in-memory cmd.Queue older
	// than 1h. Production relay-path purges origins on consume; this guards
	// against the orphan-result and crashed-agent cases (LOGIC-02).
	go func() {
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if o, r := cmdQueue.Sweep(1 * time.Hour); o > 0 || r > 0 {
					logger.Debug("cmd queue swept", "origins", o, "results", r)
				}
			}
		}
	}()

	// Provision-job janitor: evict terminal (success/failed) provisioning
	// jobs once they're past their retention window (provision.Store.Sweep;
	// jobTTL is 30m — see internal/backend/provision/job.go) so a long-running
	// backend doesn't accumulate an unbounded map of old installs/repairs.
	// Running jobs are never evicted regardless of age.
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				provisionStore.Sweep()
			}
		}
	}()

	go func() {
		if err := cb.Run(ctx); err != nil {
			logger.Error("callbacks router exited", "err", err)
			notifyDegradation("callbacks router", err)
		}
	}()

	rp := realert.NewPoller(d, tgClient, realert.Config{
		ChatID:             cfg.Telegram.ChatID,
		RealertEvery:       time.Duration(cfg.State.RealertEverySec) * time.Second,
		MobileRealertEvery: time.Duration(cfg.State.MobileRealertEverySec) * time.Second,
		TickEvery:          time.Duration(cfg.State.RealertTickSec) * time.Second,
	})
	go func() {
		if err := rp.Run(ctx); err != nil {
			logger.Error("realert poller exited", "err", err)
			notifyDegradation("realert poller", err)
		}
	}()

	// Dead-man digest: opt-in daily "monitor alive" heartbeat to the primary
	// chat. Its ABSENCE is the signal — silence from a dead backend becomes
	// visible. Disabled by default; a real external probe stays recommended.
	if cfg.Digest.Enabled {
		dp := digest.NewPoller(d, tgClient, digest.Config{
			ChatID:       cfg.Telegram.ChatID,
			HourMSK:      cfg.Digest.HourMSK,
			OnlineWindow: time.Duration(cfg.Digest.OnlineWindowSec) * time.Second,
		})
		go dp.Run(ctx)
		logger.Info("dead-man digest enabled", "hour_msk", cfg.Digest.HourMSK)
	}

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
