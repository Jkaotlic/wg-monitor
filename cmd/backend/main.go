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
	"github.com/anex/wg-monitor/internal/backend/retention"
	"github.com/anex/wg-monitor/internal/backend/state"
	"github.com/anex/wg-monitor/internal/backend/tg"
	"github.com/anex/wg-monitor/internal/backend/upstream"
)

var Version = "0.8.0-tunnel-import"

func main() {
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
	// Register slash-command menu so TG clients show the commands in the
	// picker. Non-fatal — the bot keeps working if TG refuses.
	smcCtx, smcCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := tgClient.SetMyCommands(smcCtx, []tg.BotCommand{
		{Command: "help", Description: "Справка по командам и кнопкам"},
		{Command: "keyboard", Description: "Восстановить кнопки в этом топике"},
		{Command: "panel", Description: "Открыть панель управления"},
		{Command: "ensure_topics", Description: "Создать темы для всех роутеров"},
		{Command: "recreate_topic", Description: "Пересоздать тему текущего роутера"},
		{Command: "this_is", Description: "Привязать этот топик к роутеру (укажи nickname)"},
		{Command: "topic_help", Description: "Шпаргалка по управлению темами"},
	}); err != nil {
		logger.Warn("setMyCommands failed (non-fatal)", "err", err)
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
	watcher.SetSleepNotifier(sleepNotifier)

	cmdQueue := cmd.New()
	cmdQueue.SetLogger(logger.With("component", "cmd_queue"))
	uiSnap := callbacks.UIConfigSnapshot{
		DeleteUserCommandMessages: cfg.UI.DeleteUserCommandMessages != nil && *cfg.UI.DeleteUserCommandMessages,
		SmartReplyWithKeyboard:    cfg.UI.SmartReplyWithKeyboard != nil && *cfg.UI.SmartReplyWithKeyboard,
		DiagMaxChars:              cfg.UI.DiagMaxChars,
		CompatInlineKeyboard:      cfg.UI.CompatInlineKeyboard != nil && *cfg.UI.CompatInlineKeyboard,
	}
	disp.WelcomeKeyboard = func() any {
		return uiSnap.KeyboardForTopic("per_router")
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
		ChatID:         cfg.Telegram.ChatID,
		AdminUserID:    cfg.Telegram.AdminUserID,
		MuteCutoffHour: cfg.State.MuteCutoffHour,
		UI:             uiSnap,
	})
	cb.SetRoutesCache(routesCache)
	routesNotifier := &callbacks.RoutesPanelNotifier{
		TG:    tgClient,
		Cache: routesCache,
		DB:    d,
		Store: cb.RouteWizardStore(),
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

	mux := backend.NewMux(backend.Deps{
		Logger:            logger,
		DB:                d,
		Dispatcher:        disp,
		Resumer:           watcher,
		CommandSink:       cmdQueue,
		TGNotifier:        notifier,
		RoutesNotifier:    routesNotifier,
		MaintNotifier:     maintNotifier,
		OpkgNotifier:      opkgNotifier,
		PingCheckNotifier: pingcheckNotifier,
		WakeNotifier:      wakeNotifier,
		UI:                cfg.UI,
		Thresholds:        state.Thresholds{Fail: cfg.State.FailThreshold, Recovery: cfg.State.RecoveryThreshold},
		// Wire the server-shutdown ctx so cmd-result relay goroutines respect
		// SIGTERM and don't outlive srv.Shutdown (BUG-15).
		ShutdownCtx: ctx,
		// Per-token rate limit on /v1/report (API-06). Defaults applied in
		// LoadConfig so production yaml without rate_limit section gets sane
		// throttling automatically.
		ReportRatePerSec: cfg.RateLimit.ReportPerSec,
		ReportBurst:      cfg.RateLimit.ReportBurst,
		WizardToken:      cfg.Wizard.Token,
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

	go func() {
		if err := cb.Run(ctx); err != nil {
			logger.Error("callbacks router exited", "err", err)
			notifyDegradation("callbacks router", err)
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
			notifyDegradation("realert poller", err)
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
