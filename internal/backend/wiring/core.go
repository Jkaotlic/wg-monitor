// Package wiring -- боевая связка бэкенда: диспетчер тревог, очередь команд,
// кабинеты, мастер замены, движок автопочинки и зависимости HTTP-мукса.
//
// Вынесена из cmd/backend, чтобы сквозной контур (internal/e2e) собирал ровно
// то же, что процесс на проде: связка, скопированная в тест, разошлась бы с
// настоящей на первой же новой строке, и контур проверял бы сам себя.
//
// Фоновые петли (сторож молчания, напоминания, уборщики) и сервер запускает
// вызывающий: здесь только сборка.
package wiring

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/callbacks"
	"github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/heartbeat"
	"github.com/Jkaotlic/wg-monitor/internal/backend/linkrepair"
	"github.com/Jkaotlic/wg-monitor/internal/backend/notify"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/replace"
	"github.com/Jkaotlic/wg-monitor/internal/backend/state"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
	"github.com/Jkaotlic/wg-monitor/internal/backend/upstream"
	"github.com/Jkaotlic/wg-monitor/internal/backup"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Inputs -- то, что процесс создаёт сам: сюда входят только ресурсы с местом
// жительства (база, Telegram, файлы хранилищ) и контекст остановки.
type Inputs struct {
	// Ctx -- контекст процесса (SIGINT/SIGTERM), а не запроса.
	Ctx    context.Context
	Cfg    *backend.Config
	DB     *db.DB
	TG     *tg.Client
	Logger *slog.Logger
	// Provision -- движок установки роутера: Store у него общий с мастером
	// замены и автопочинкой.
	Provision provision.Deps
	// Awg3Panels -- один сервис на мини-апп и на источник автопочинки.
	Awg3Panels backend.Awg3Panels
	// SelfHosted -- свои VPN-серверы для мини-аппа; nil -- раздел выключен.
	SelfHosted         backend.SelfHostedVPS
	CabinetSealWarning string
}

// Core -- собранная связка. Mux заполнен всем, кроме Deps.Revive: оживление
// вызывающий поднимает сам, у него своя фоновая работа и свой порядок старта.
type Core struct {
	Dispatcher *alerts.Dispatcher
	Watcher    *heartbeat.Watcher
	Queue      *cmd.Queue
	Upstream   *upstream.Cache
	Callbacks  *callbacks.Router
	Replace    *replace.Deps
	LinkRepair *linkrepair.Deps
	Repairs    *notify.Repairs
	Mux        backend.Deps
}

// NewCore собирает связку. Сетевых вызовов и горутин здесь нет.
func NewCore(in Inputs) *Core {
	cfg, d, tgClient, logger, ctx := in.Cfg, in.DB, in.TG, in.Logger, in.Ctx

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
	backend.AttachDeployDispatchLimit(cmdQueue)

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
		AmneziaSecretsPath: cfg.Amnezia.SecretsPath,
		HideMyBaseURL:      cfg.HideMy.BaseURL,
		HideMySecretsPath:  cfg.HideMy.SecretsPath,
	})

	dashboardMobileStaleAfter := cfg.Heartbeat.StaleAfterMobileSec
	if mobileLifecycle {
		dashboardMobileStaleAfter = cfg.Heartbeat.MobileSleepAfterSec
	}

	provisionStore := in.Provision.Store

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
		// Списанный заменой VPN-туннель уносит с собой настройку автопочинки.
		Retired: backend.LinkRepairDropSetting(d, logger.With("component", "replace")),
	}

	// Движок починки VPN-туннеля -- лесенка v0.54. Store общий с мастером
	// замены: замок один на двоих, иначе починка и замена столкнулись бы на
	// одном роутере. Проверки (обмен ключами, выход, анализ конфига) -- те же,
	// что у мастера. Ход починки дописывается в саму тревогу (notify.Repairs):
	// текст тревоги помнит диспетчер, он же спрашивает у починки, сказано ли
	// уже «починил», и печатает в тревоге строку «Автопочинка включена».
	repairs := notify.NewRepairs(
		notify.NewFanout(d, tgClient, logger.With("component", "linkrepair_notify"), cfg.Telegram.AdminUserID),
		tgClient, d, disp, time.Now)
	repairs.SetMiniAppBaseURL(cfg.PublicBaseURL)
	disp.SetAutoRepairHint(backend.AutoRepairHint(d))
	disp.SetTrafficFacts(backend.AlertTrafficFacts(d))
	disp.SetCovered(repairs.TakeCovered)
	disp.SetHardHook(repairs.Uncover)
	// «Тише на час» в боте -- в память тревог, от которой правит починка.
	cb.SetAlertMemory(disp)
	repairEngine := &linkrepair.Deps{
		Store:           provisionStore,
		Probe:           *replaceEngine,
		Source:          backend.RepairSource(cb, in.Awg3Panels, d, cfg.Telegram.AdminUserID),
		Settings:        backend.LinkRepairSettings(d, logger.With("component", "linkrepair")),
		SaveOption:      backend.LinkRepairSaveOption(d, logger.With("component", "linkrepair")),
		SaveUnconfirmed: backend.LinkRepairSaveUnconfirmed(d, logger.With("component", "linkrepair")),
		SpendRelocation: backend.LinkRepairSpendRelocation(d),
		SourceOff:       backend.RepairSourceOff,
		Attempts:        linkrepair.Attempts{KV: d.KV()},
		LatestCheck: func(routerID int64, check string) (wire.Check, error) {
			row, ok, err := d.Events().LatestEvent(routerID, check)
			if err != nil {
				return wire.Check{}, err
			}
			if !ok {
				return wire.Check{}, fmt.Errorf("triggering check missing")
			}
			c := wire.Check{Name: check, Status: row.Status}
			if row.DetailsJSON != "" {
				if err := json.Unmarshal([]byte(row.DetailsJSON), &c.Details); err != nil {
					return wire.Check{}, err
				}
			}
			return c, nil
		},
		LineDead: backend.LinkRepairLineDead(d, logger.With("component", "linkrepair")),
		Moves:    linkrepair.NewMoveState(),
		Commands: cmdQueue,
		Report:   repairs,
		BaseCtx:  ctx,
		Now:      time.Now,
		Logger:   logger.With("component", "linkrepair"),
	}

	mux := backend.Deps{
		Logger:             logger,
		CabinetSealWarning: in.CabinetSealWarning,
		HeartbeatStats:     watcher.Snapshot,
		// Состояние ночного бэкапа: файл рядом с базой, читается по требованию.
		BackupStatus: backend.NewBackupStatusSource(backup.StatusPath(cfg.DBPath), nil),
		DB:           d,
		Dispatcher:   disp,
		AlertMemory:  disp,
		Resumer:      watcher,
		CommandSink:  cmdQueue,
		// Кэш релизов апстрима: второй поход в GitHub сжёг бы лимит анонимного API.
		Upstream: upCache,
		// Кабинеты провайдеров для мини-аппа: ключи и клиенты живут в
		// callbacks.Router, и он же реализует контракт backend.VPNCabinet.
		VPNCabinet: cb,
		// Ключи и коды кабинетов для мини-аппа -- тот же callbacks.Router.
		VPNCabinetKeys: cb,
		// Свои VPN-серверы -- только админу в мини-аппе.
		SelfHosted:      in.SelfHosted,
		Awg3Panels:      in.Awg3Panels,
		Replace:         replaceEngine,
		LinkRepair:      repairEngine,
		StartLinkRepair: repairEngine.Start,
		WakeNotifier:    wakeNotifier,
		UnstickNotifier: alerts.NewUnstickNotifier(
			notify.NewFanout(d, tgClient, logger.With("component", "unstick_notify"), cfg.Telegram.AdminUserID)),
		DeployNotifier:      deployNotifier,
		Thresholds:          state.Thresholds{Fail: cfg.State.FailThreshold, Recovery: cfg.State.RecoveryThreshold},
		AlertPolicy:         backend.AlertPolicy{NoisyFailThreshold: cfg.State.NoisyFailThreshold, NoisyRecoveryThreshold: cfg.State.NoisyRecoveryThreshold, BypassLeakEnabled: cfg.Alerts.BypassLeak.Enabled},
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
		DistrustForwardedFor:      cfg.TrustForwardedFor != nil && !*cfg.TrustForwardedFor,
		TelegramBotToken:          cfg.Telegram.BotToken,
		TelegramAdminUserID:       cfg.Telegram.AdminUserID,
		TelegramPrimaryChatID:     cfg.Telegram.ChatID,
		TelegramExtraChatIDs:      cfg.Telegram.ExtraChatIDs,
		MiniappTG:                 tgClient,
		// Файл .conf в личку нажавшему (кабинет роутера в мини-аппе).
		MiniappDocs: tgClient,
		// getChat -- имена владельцев и операторов в справочнике людей (v0.58).
		PeopleTG:          tgClient,
		MuteCutoffHour:    muteCutoffHour,
		BackendUpdatePath: backend.DefaultBackendUpdatePath(cfg),
		ReleaseCacheDir:   backend.DefaultReleaseCacheDir(cfg),
		PublicBaseURL:     cfg.PublicBaseURL,
		PublicIP:          cfg.PublicIP,
		Provision:         in.Provision,
	}
	return &Core{
		Dispatcher: disp,
		Watcher:    watcher,
		Queue:      cmdQueue,
		Upstream:   upCache,
		Callbacks:  cb,
		Replace:    replaceEngine,
		LinkRepair: repairEngine,
		Repairs:    repairs,
		Mux:        mux,
	}
}
