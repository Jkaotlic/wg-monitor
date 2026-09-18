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
	"github.com/Jkaotlic/wg-monitor/internal/backend/linkrepair"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/realert"
	"github.com/Jkaotlic/wg-monitor/internal/backend/replace"
	"github.com/Jkaotlic/wg-monitor/internal/backend/retention"
	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/internal/backend/updatespoll"
	"github.com/Jkaotlic/wg-monitor/internal/backend/upstream"
)

var Version = "0.8.0-tunnel-import"

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
	// Меню команд стирается во всех трёх областях, а кнопка меню ведёт в
	// приложение. Группа (если она ещё задана в конфиге) нужна ровно здесь и
	// больше нигде -- очистить в ней меню и забыть.
	smcCtx, smcCancel := context.WithTimeout(context.Background(), 5*time.Second)
	for _, err := range clearBotCommandMenus(smcCtx, tgClient, cfg.Telegram.AdminUserID, cfg.Telegram.ChatID) {
		logger.Warn("setMyCommands cleanup failed (non-fatal)", "err", err)
	}
	// Кнопка меню приватного чата ведёт в мини-апп. Ставится на каждом старте
	// намеренно: иначе кнопка, выставленная руками через BotFather, жила бы до
	// первого рестарта. Без https-адреса кнопку не трогаем вовсе -- списка
	// команд за ней больше нет.
	if miniURL := miniAppMenuURL(cfg.PublicBaseURL); miniURL != "" {
		if err := tgClient.SetWebAppMenuButton(smcCtx, MiniAppMenuButtonText, miniURL); err != nil {
			logger.Warn("setChatMenuButton web_app failed (non-fatal)", "err", err, "url", miniURL)
		}
	}
	smcCancel()
	disp := alerts.NewDispatcher(d, tgClient, alerts.Config{
		FailThreshold:     cfg.State.FailThreshold,
		RecoveryThreshold: cfg.State.RecoveryThreshold,
		MiniAppBaseURL:    cfg.PublicBaseURL,
		AdminUserID:       cfg.Telegram.AdminUserID,
	})

	mobileLifecycle := cfg.Heartbeat.MobileLifecycle == nil || *cfg.Heartbeat.MobileLifecycle
	muteCutoffHour := 9
	if cfg.State.MuteCutoffHour != nil {
		muteCutoffHour = *cfg.State.MuteCutoffHour
	}
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
	wakeNotifier := alerts.NewWakeNotifier(d, tgClient, cfg.Telegram.AdminUserID)
	wakeNotifier.SetMiniAppBaseURL(cfg.PublicBaseURL)
	sleepNotifier := alerts.NewSleepNotifier(d, tgClient, cfg.Telegram.AdminUserID)
	deployNotifier := alerts.NewDeployNotifier(d, tgClient, cfg.Telegram.AdminUserID)
	watcher.SetSleepNotifier(sleepNotifier)

	cmdQueue := cmd.New()
	cmdQueue.SetLogger(logger.With("component", "cmd_queue"))
	backend.AttachDeployExpiryHandler(cmdQueue, logger)
	// Очередь пустая после старта, а назначенные обновления записаны в базе:
	// без этого роутер, которому обновление назначили до рестарта, оставался
	// «в ожидании» навсегда -- команду ему уже никто не слал.
	if n := backend.ResumePendingDeploys(d, cmdQueue, cfg.PublicBaseURL, cfg.PublicIP, logger); n > 0 {
		logger.Info("pending deploys re-queued after restart", "count", n)
	}
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

	// Бот и кабинеты -- один callbacks.Router: кабинеты мини-аппа, мастер
	// замены и уведомления о починке берут его же.
	cb := callbacks.NewRouter(d, tgClient, callbacks.Config{
		AdminUserID:        cfg.Telegram.AdminUserID,
		PublicBaseURL:      cfg.PublicBaseURL,
		AmneziaBaseURL:     cfg.Amnezia.BaseURL,
		AmneziaSecretsPath: cfg.Amnezia.SecretsPath,
		HideMyBaseURL:      cfg.HideMy.BaseURL,
		HideMySecretsPath:  cfg.HideMy.SecretsPath,
	})

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

	// Оживление агента на выключенном роутере (цикл 2б). Без ключа -- nil, и
	// маршруты мини-аппа отвечают revive_disabled. Recover (внутри
	// newReviveService) отрабатывает синхронно, ДО того как Deps.Revive
	// вообще станет виден HTTP-обработчикам ниже (carry #5).
	reviveSvc := newReviveService(ctx, cfg, d, provisionDeps, tgClient, logger)
	if reviveSvc != nil {
		go reviveSvc.Run(ctx)
		// Авто-оживление давно не обновлявшихся (v0.45): по сохранённому
		// паролю root, рядом с воркером оживления.
		go backend.RunAutoRevive(ctx, backend.AutoReviveDeps{
			DB: d, Revive: reviveSvc, CommandSink: cmdQueue, Logger: logger.With("component", "auto-revive"),
		})
	} else {
		// Fix round 1, Important #2 (мандатное ревью): без ключа Service.Run
		// никогда не пройдёт по базе, и просроченные секреты лежали бы в
		// revive_secrets вечно -- решение оператора «затем стирается»
		// действует и без ключа. Беспарольный сторож не расшифровывает
		// ничего, ключ ему не нужен.
		go revive.RunJanitor(ctx, d, time.Now, revive.DefaultJanitorEvery, logger.With("component", "revive-janitor"))
	}

	// Мастер замены конфига: задание из шести шагов с откатом. Store общий с
	// провижном намеренно -- блокировка в нём по имени роутера, и ставить
	// агента заново посреди замены конфига было бы нельзя в любом случае.
	replaceEngine := &replace.Deps{
		Store:    provisionStore,
		Commands: cmdQueue,
		Cabinet:  backend.ReplaceCabinet(cb),
		Origin:   backend.ReplaceOrigin(d),
		Notify: func(ctx context.Context, routerID int64, text string) {
			if err := cb.NotifyRouterTopic(ctx, routerID, text); err != nil {
				logger.Warn("replace: notify failed", "router_id", routerID, "err", err)
			}
		},
		BaseCtx: ctx,
		Now:     time.Now,
		Logger:  logger.With("component", "replace"),
	}

	// Движок починки линии. Store общий с мастером замены: замок один на
	// двоих, иначе починка и замена столкнулись бы на одном роутере.
	repairEngine := &linkrepair.Deps{
		Store:    provisionStore,
		Replace:  *replaceEngine,
		Origin:   backend.LinkRepairOrigin(d),
		Attempts: linkrepair.Attempts{KV: d.KV()},
		AutoRepair: func(routerID int64) bool {
			on, err := d.RepairSettings().WithDefault(cfg.Repair.AutoDefault).AutoRepair(routerID)
			if err != nil {
				logger.Warn("linkrepair: настройка не прочиталась", "router_id", routerID, "err", err)
				return false
			}
			return on
		},
		Commands: cmdQueue,
		Notify: func(ctx context.Context, routerID int64, text string) {
			if err := cb.NotifyRouterTopic(ctx, routerID, text); err != nil {
				logger.Warn("linkrepair: notify failed", "router_id", routerID, "err", err)
			}
		},
		BaseCtx: ctx,
		Now:     time.Now,
		Logger:  logger.With("component", "linkrepair"),
	}

	mux := backend.NewMux(backend.Deps{
		Logger:         logger,
		HeartbeatStats: watcher.Snapshot,
		DB:             d,
		Dispatcher:     disp,
		Resumer:        watcher,
		CommandSink:    cmdQueue,
		// Кэш релизов апстрима: второй поход в GitHub сжёг бы лимит анонимного API.
		Upstream: upCache,
		// Кабинеты провайдеров для мини-аппа: ключи и клиенты живут в
		// callbacks.Router, и он же реализует контракт backend.VPNCabinet.
		VPNCabinet: cb,
		// Ключи и коды кабинетов для мини-аппа -- тот же callbacks.Router.
		VPNCabinetKeys: cb,
		// Свои VPN-серверы -- только админу в мини-аппе.
		SelfHosted:          newSelfHostedService(cfg.SelfHostedAmnezia, logger),
		Replace:             replaceEngine,
		LinkRepair:          repairEngine,
		StartLinkRepair:     repairEngine.Start,
		WakeNotifier:        wakeNotifier,
		DeployNotifier:      deployNotifier,
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
		TelegramBotToken:          cfg.Telegram.BotToken,
		TelegramAdminUserID:       cfg.Telegram.AdminUserID,
		TelegramPrimaryChatID:     cfg.Telegram.ChatID,
		TelegramExtraChatIDs:      cfg.Telegram.ExtraChatIDs,
		MiniappTG:                 tgClient,
		// Файл .conf в личку нажавшему (кабинет роутера в мини-аппе).
		MiniappDocs:       tgClient,
		MuteCutoffHour:    muteCutoffHour,
		BackendUpdatePath: backend.DefaultBackendUpdatePath(cfg),
		PublicBaseURL:     cfg.PublicBaseURL,
		PublicIP:          cfg.PublicIP,
		Provision:         provisionDeps,
		Revive:            reviveSvc,
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

	// Отвалившаяся фоновая горутина -- в личку админа (degradation.go).
	// Отмена ctx (SIGTERM/Int) -- это штатная остановка, а не поломка.
	notifyDegradationTG := func(component string, err error) {
		if ctx.Err() != nil {
			return
		}
		if sendErr := notifyDegradation(tgClient, cfg.Telegram.AdminUserID, component, err); sendErr != nil {
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

	// Уборщик: раз в 15 минут выметает из очереди в памяти итоги и записи о
	// выдаче старше часа. Итог забирает опрос мини-аппа, но осиротевший ответ
	// и упавший агент оставили бы запись навсегда (LOGIC-02).
	go func() {
		t := time.NewTicker(15 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if r := cmdQueue.Sweep(1 * time.Hour); r > 0 {
					logger.Debug("cmd queue swept", "results", r)
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
			notifyDegradationTG("callbacks router", err)
		}
	}()

	rp := realert.NewPoller(d, tgClient, realert.Config{
		RealertEvery:       time.Duration(cfg.State.RealertEverySec) * time.Second,
		MobileRealertEvery: time.Duration(cfg.State.MobileRealertEverySec) * time.Second,
		TickEvery:          time.Duration(cfg.State.RealertTickSec) * time.Second,
		MiniAppBaseURL:     cfg.PublicBaseURL,
		AdminUserID:        cfg.Telegram.AdminUserID,
	})
	go func() {
		if err := rp.Run(ctx); err != nil {
			logger.Error("realert poller exited", "err", err)
			notifyDegradationTG("realert poller", err)
		}
	}()

	// Dead-man digest: opt-in daily "monitor alive" heartbeat to the primary
	// chat. Its ABSENCE is the signal — silence from a dead backend becomes
	// visible. Disabled by default; a real external probe stays recommended.
	if cfg.Digest.Enabled {
		dp := digest.NewPoller(d, tgClient, digest.Config{
			AdminUserID:  cfg.Telegram.AdminUserID,
			HourMSK:      cfg.Digest.HourMSK,
			OnlineWindow: time.Duration(cfg.Digest.OnlineWindowSec) * time.Second,
		})
		go dp.Run(ctx)
		logger.Info("dead-man digest enabled", "hour_msk", cfg.Digest.HourMSK)
	}

	// Суточный опрос версий. Обычный отчёт приносит панель, прошивку,
	// KeeneticOS и модуль ядра бесплатно, но про версию HydraRoute Neo и про
	// ДОСТУПНУЮ прошивку не знает вовсе -- их знает только version_audit.
	// Поллер доспрашивает ровно ради этих двух полей: не чаще раза в сутки,
	// молчащим команду не ставит (у неё TTL, а мобильный роутер спит) и в
	// GitHub не ходит ни разу -- сравнение живёт в общем upCache.
	updPoller := updatespoll.NewPoller(d, cmdQueue, updatespoll.Config{})
	go updPoller.Run(ctx)

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
	// Фоновые проверки оживления (confirmSoon, уведомление итога) пишут в
	// базу, а d.Close() отложен выше: ждём их, но ограниченно.
	if reviveSvc != nil && !waitBounded(reviveSvc.Wait, 10*time.Second) {
		logger.Warn("оживление: фоновые проверки не завершились за 10 с, останавливаемся без них")
	}
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
