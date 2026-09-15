package revive

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Статусы намерения (см. db.Revive*).
const (
	StatusWaiting   = db.ReviveWaiting
	StatusRunning   = db.ReviveRunning
	StatusDone      = db.ReviveDone
	StatusFailed    = db.ReviveFailed
	StatusCancelled = db.ReviveCancelled
	StatusExpired   = db.ReviveExpired
)

// Умолчания из спеки.
const (
	DefaultExpiryDays       = 30
	DefaultMaxAttempts      = 5
	DefaultProbeEvery       = 3 * time.Minute
	DefaultAgentFreshWindow = 10 * time.Minute
	DefaultReachableProbes  = 2
	DefaultConfirmGap       = 30 * time.Second
)

// Intent -- намерение оживления как оно лежит в базе.
type Intent = db.ReviveIntent

// ScheduleRequest -- то, что админ ввёл при постановке. Пароли живут в
// памяти только до шифрования.
type ScheduleRequest struct {
	RootPassword string
	AWGMLogin    string
	AWGMPassword string
	AWGMAPIKey   string
	AWGMURL      string
	ExpiresDays  int
	RequestedBy  int64
}

// IntentView -- состояние для экрана. Тексты русские, без секретов и
// внутренних имён.
type IntentView struct {
	Status        string    `json:"status"`
	ExpiresAt     time.Time `json:"expires_at"`
	Attempts      int       `json:"attempts"`
	LastErrorText string    `json:"last_error_text"`
	LastProbeText string    `json:"last_probe_text"`
	LastProbeAt   time.Time `json:"last_probe_at,omitzero"`
}

// Engine -- движок переустановки (адаптер в пакете backend). Launch получает
// расшифрованные секреты в памяти и обязан не писать их никуда, кроме
// задания для терминала роутера. Outcome: ok=false -- задание неизвестно
// (вытеснено или бэкенд перезапускался).
type Engine interface {
	Launch(ctx context.Context, routerID int64, s Secrets, targetVersion string) (jobID string, err error)
	Outcome(jobID string) (Outcome, bool)
}

// Outcome -- итог задания переустановки. Text -- русская причина неудачи.
type Outcome struct {
	Finished   bool
	Success    bool
	AuthFailed bool
	Version    string
	Text       string
}

// LaunchError -- отказ запуска. Permanent -- повторять бессмысленно
// (нет адреса панели, даунгрейд); Text -- русская причина без секретов.
type LaunchError struct {
	Permanent bool
	Text      string
}

func (e *LaunchError) Error() string { return e.Text }

// Notifier -- рассылка по получателям роутера; *notify.Fanout подходит как есть
// (админ получает всё).
type Notifier interface {
	Send(ctx context.Context, routerUserID int64, text, parseMode string) (int, error)
}

type Config struct {
	DB       *db.DB
	Key      []byte
	Engine   Engine
	Notifier Notifier
	// Probe -- опрос панели; nil -- NewProber(ProbeTimeout).Probe.
	Probe func(ctx context.Context, awgmURL string) string
	Now   func() time.Time
	// Sleep ждёт d или отмены ctx; false -- отменено. nil -- настоящий таймер.
	Sleep   func(ctx context.Context, d time.Duration) bool
	BaseCtx context.Context
	Logger  *slog.Logger

	MaxAttempts      int
	ProbeEvery       time.Duration
	AgentFreshWindow time.Duration
	ReachableProbes  int
	ConfirmGap       time.Duration
}

// Service -- оживление агента. nil-получатель означает «функция выключена».
type Service struct {
	cfg    Config
	box    *Box
	logger *slog.Logger

	// work сериализует обработку намерений: воркер и проверка после
	// постановки не запускают одно и то же дважды.
	work sync.Mutex

	jobsMu sync.Mutex
	jobs   map[int64]string // routerID -> jobID идущей переустановки

	wg sync.WaitGroup
}

func New(cfg Config) (*Service, error) {
	if cfg.DB == nil {
		return nil, errors.New("revive: нет базы")
	}
	if cfg.Engine == nil {
		return nil, errors.New("revive: нет движка переустановки")
	}
	box, err := NewBox(cfg.Key)
	if err != nil {
		return nil, err
	}
	cfg.Key = nil // ключ дальше живёт только внутри AEAD
	if cfg.Probe == nil {
		cfg.Probe = NewProber(ProbeTimeout).Probe
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = realSleep
	}
	if cfg.BaseCtx == nil {
		cfg.BaseCtx = context.Background()
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
	}
	if cfg.ProbeEvery <= 0 {
		cfg.ProbeEvery = DefaultProbeEvery
	}
	if cfg.AgentFreshWindow <= 0 {
		cfg.AgentFreshWindow = DefaultAgentFreshWindow
	}
	if cfg.ReachableProbes <= 0 {
		cfg.ReachableProbes = DefaultReachableProbes
	}
	if cfg.ConfirmGap <= 0 {
		cfg.ConfirmGap = DefaultConfirmGap
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{cfg: cfg, box: box, logger: logger, jobs: map[int64]string{}}, nil
}

func (s *Service) Enabled() bool { return s != nil && s.box != nil }

// Wait дожидается фоновых проверок, запущенных постановкой. Для тестов и
// аккуратной остановки.
func (s *Service) Wait() {
	if s != nil {
		s.wg.Wait()
	}
}

func (s *Service) now() time.Time { return s.cfg.Now().UTC() }

// Schedule ставит (или переставляет) оживление. Порядок отказов: выключено,
// нет роутера, нет учётных данных, адрес панели, живой агент, уже идёт.
func (s *Service) Schedule(ctx context.Context, routerID int64, req ScheduleRequest) (Intent, error) {
	if !s.Enabled() {
		return Intent{}, ErrDisabled
	}
	u, err := s.cfg.DB.Users().GetByID(routerID)
	if errors.Is(err, db.ErrUserNotFound) {
		return Intent{}, ErrRouterNotFound
	}
	if err != nil {
		return Intent{}, err
	}

	creds := NewSecrets(
		strings.TrimSpace(req.RootPassword),
		strings.TrimSpace(req.AWGMLogin),
		req.AWGMPassword,
		strings.TrimSpace(req.AWGMAPIKey),
	)
	if !creds.Usable() {
		return Intent{}, ErrNoCredentials
	}

	stored := strings.TrimSpace(derefString(u.AWGMURL))
	asked := strings.TrimSpace(req.AWGMURL)
	switch {
	case asked != "" && stored != "":
		return Intent{}, ErrURLAlreadySet
	case asked == "" && stored == "":
		return Intent{}, ErrNoAWGMURL
	}
	if asked != "" {
		norm, ok := normalizeAWGMURL(asked)
		if !ok {
			return Intent{}, ErrInvalidURL
		}
		asked = norm
	}

	now := s.now()
	if s.agentFresh(u, now) {
		return Intent{}, ErrAgentAlive
	}
	if cur, err := s.cfg.DB.Revive().Get(routerID); err != nil {
		return Intent{}, err
	} else if cur != nil && cur.Status == StatusRunning {
		return Intent{}, ErrRunning
	}

	if asked != "" {
		ok, err := s.cfg.DB.Users().SetAWGMURLIfEmpty(routerID, asked)
		if err != nil {
			return Intent{}, err
		}
		if !ok {
			return Intent{}, ErrURLAlreadySet
		}
	}

	nonce, ct, err := s.box.Seal(routerID, creds)
	if err != nil {
		return Intent{}, err
	}
	// Срок: 1..30 дней; 0, отрицательное и больше 30 -- 30 (умолчание спеки).
	days := req.ExpiresDays
	if days <= 0 || days > DefaultExpiryDays {
		days = DefaultExpiryDays
	}
	expiry := time.Duration(days) * 24 * time.Hour
	err = s.cfg.DB.Revive().Put(db.ReviveIntent{
		RouterID: routerID, CreatedAt: now, ExpiresAt: now.Add(expiry), RequestedBy: req.RequestedBy,
	}, nonce, ct)
	if errors.Is(err, db.ErrReviveRunning) {
		return Intent{}, ErrRunning
	}
	if err != nil {
		return Intent{}, err
	}
	s.logger.Info("оживление агента поставлено", "router_id", routerID, "expires_at", now.Add(expiry), "requested_by", req.RequestedBy)

	got, err := s.cfg.DB.Revive().Get(routerID)
	if err != nil || got == nil {
		return Intent{}, errors.Join(errors.New("revive: намерение не прочиталось"), err)
	}
	return *got, nil
}

// Cancel снимает ожидающее оживление и стирает секрет. Идущую переустановку
// отменить нельзя (пре-флайт 15.09): прерванная на полпути она оставит роутер
// без агента, а её итог всё равно придёт -- отвечаем ErrRunning.
func (s *Service) Cancel(ctx context.Context, routerID int64) (bool, error) {
	if !s.Enabled() {
		return false, ErrDisabled
	}
	if cur, err := s.cfg.DB.Revive().Get(routerID); err == nil && cur != nil && cur.Status == StatusRunning {
		return false, ErrRunning
	}
	ok, err := s.cfg.DB.Revive().Finish(routerID, []string{StatusWaiting}, StatusCancelled, "отменено", s.now())
	if err != nil {
		return false, err
	}
	s.forgetJob(routerID)
	if ok {
		s.logger.Info("оживление агента отменено", "router_id", routerID)
	}
	return ok, nil
}

// StatusFor -- состояние для экрана; nil, если оживление не ставили.
func (s *Service) StatusFor(routerID int64) (*IntentView, error) {
	if s == nil || s.cfg.DB == nil {
		return nil, nil
	}
	in, err := s.cfg.DB.Revive().Get(routerID)
	if err != nil || in == nil {
		return nil, err
	}
	return &IntentView{
		Status:        in.Status,
		ExpiresAt:     in.ExpiresAt,
		Attempts:      in.Attempts,
		LastErrorText: in.LastError,
		LastProbeText: probeText(in.LastProbeState),
		LastProbeAt:   in.LastProbeAt,
	}, nil
}

func (s *Service) agentFresh(u *db.User, now time.Time) bool {
	return u.LastSeenAt != nil && now.Sub(u.LastSeenAt.UTC()) < s.cfg.AgentFreshWindow
}

func (s *Service) setJob(routerID int64, jobID string) {
	s.jobsMu.Lock()
	s.jobs[routerID] = jobID
	s.jobsMu.Unlock()
}

func (s *Service) job(routerID int64) (string, bool) {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	id, ok := s.jobs[routerID]
	return id, ok
}

func (s *Service) forgetJob(routerID int64) {
	s.jobsMu.Lock()
	delete(s.jobs, routerID)
	s.jobsMu.Unlock()
}

func normalizeAWGMURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" ||
		u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	return strings.TrimRight(u.String(), "/"), true
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func realSleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
