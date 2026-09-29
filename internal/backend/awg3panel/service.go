package awg3panel

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/sync/singleflight"
)

const (
	// OnlineWindow -- порог панели (awg3-panel issuer/summary.go:15).
	OnlineWindow     = 180 * time.Second
	DefaultCacheTTL  = 30 * time.Second
	DefaultPause     = 15 * time.Minute
	RouterPeerPrefix = "wgmon-"
	maxPeerName      = 40
)

var (
	ErrInstanceNotFound = errors.New("панель не найдена")
	ErrInstanceExists   = errors.New("панель с таким коротким именем уже есть")
	ErrInstanceDisabled = errors.New("панель выключена")
	ErrIfaceNotFound    = errors.New("интерфейс панели не найден")
	ErrNameTaken        = errors.New("пир с таким именем на интерфейсе уже есть")
)

type Options struct {
	RootCAs  *x509.CertPool
	Timeout  time.Duration
	CacheTTL time.Duration
	Pause    time.Duration
	Now      func() time.Time
	Logger   *slog.Logger
}

// View -- панель без секретов: то, что можно отдать экрану.
type View struct {
	ID           string
	Label        string
	BaseURL      string
	User         string
	Enabled      bool
	PasswordSet  bool
	CertSet      bool
	CertSubject  string
	CertNotAfter time.Time
	Lock         Lock
	PausedUntil  time.Time
	Readonly     bool
}

func viewOf(inst Instance) View {
	return View{
		ID: inst.ID, Label: inst.Label, BaseURL: inst.BaseURL, User: inst.User, Enabled: inst.Enabled,
		PasswordSet: inst.Password != "", CertSet: inst.CertPEM != "" && inst.KeyPEM != "",
		CertSubject: inst.CertSubject, CertNotAfter: inst.CertNotAfter,
		Lock: inst.Lock, PausedUntil: inst.PausedUntil, Readonly: inst.Readonly,
	}
}

// Input -- форма панели. На правке пустой Password и nil P12 -- «не менять».
type Input struct {
	ID          string
	Label       string
	BaseURL     string
	User        string
	Password    string
	P12         []byte
	P12Password string
	Enabled     *bool
}

func (Input) String() string       { return hiddenValue }
func (Input) GoString() string     { return hiddenValue }
func (Input) LogValue() slog.Value { return slog.StringValue(hiddenValue) }

// CheckResult -- итог «Сохранить и проверить». Ran=false -- запрос не
// уходил (пауза 429 ещё идёт).
type CheckResult struct {
	Ran    bool
	OK     bool
	Kind   Kind
	Until  time.Time
	Ifaces int
}

type Page struct {
	Panel     View
	Ifaces    []Iface
	Iface     string
	Summary   Summary
	Peers     []Peer
	FetchedAt time.Time
}

// RouterConfig -- конфиг для роутера. Conf уходит только в команду агенту.
type RouterConfig struct {
	Conf   []byte
	PeerID string
	Reused bool
}

func (RouterConfig) String() string       { return hiddenValue }
func (RouterConfig) GoString() string     { return hiddenValue }
func (RouterConfig) LogValue() slog.Value { return slog.StringValue(hiddenValue) }

type cachedPage struct {
	page Page
	at   time.Time
}

type cachedIfaces struct {
	list []Iface
	at   time.Time
}

type cachedClient struct {
	fp [32]byte
	c  *Client
}

// Service -- панели для мини-аппа. Фонового опроса нет: панель спрашивается,
// только когда админ открыл экран или нажал кнопку.
type Service struct {
	path string
	opts Options

	storeMu sync.Mutex
	locks   sync.Map // id -> *sync.Mutex
	group   singleflight.Group

	cacheMu sync.Mutex
	pages   map[string]cachedPage   // id|iface
	ifaces  map[string]cachedIfaces // id
	clients map[string]cachedClient // id
}

func NewService(path string, o Options) *Service {
	if o.Timeout <= 0 {
		o.Timeout = DefaultTimeout
	}
	if o.CacheTTL <= 0 {
		o.CacheTTL = DefaultCacheTTL
	}
	if o.Pause <= 0 {
		o.Pause = DefaultPause
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Service{path: path, opts: o, pages: map[string]cachedPage{}, ifaces: map[string]cachedIfaces{}, clients: map[string]cachedClient{}}
}

func normID(id string) string { return strings.ToLower(strings.TrimSpace(id)) }

func (s *Service) lockInstance(id string) func() {
	v, _ := s.locks.LoadOrStore(id, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *Service) update(fn func(*Store) error) error {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	st, err := LoadStore(s.path)
	if err != nil {
		return err
	}
	if err := fn(&st); err != nil {
		return err
	}
	return SaveStore(s.path, st)
}

func (s *Service) find(id string) (Instance, error) {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	st, err := LoadStore(s.path)
	if err != nil {
		return Instance{}, err
	}
	for _, inst := range st.Instances {
		if inst.ID == id {
			return inst, nil
		}
	}
	return Instance{}, ErrInstanceNotFound
}

func (s *Service) persist(id string, fn func(*Instance)) {
	err := s.update(func(st *Store) error {
		for i := range st.Instances {
			if st.Instances[i].ID == id {
				fn(&st.Instances[i])
				return nil
			}
		}
		return ErrInstanceNotFound
	})
	if err != nil {
		s.opts.Logger.Warn("awg3-панель: состояние не записано", "panel", id, "err", err)
	}
}

func (s *Service) List() ([]View, error) {
	s.storeMu.Lock()
	defer s.storeMu.Unlock()
	st, err := LoadStore(s.path)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(st.Instances))
	for _, inst := range st.Instances {
		out = append(out, viewOf(inst))
	}
	return out, nil
}

// Create: поля → .p12 (без сети) → запись → ровно одна проверка.
func (s *Service) Create(ctx context.Context, in Input) (View, CheckResult, error) {
	inst := Instance{
		ID: normID(in.ID), Label: strings.TrimSpace(in.Label), User: strings.TrimSpace(in.User),
		Password: in.Password, Enabled: in.Enabled == nil || *in.Enabled,
	}
	if inst.Label == "" {
		inst.Label = inst.ID
	}
	inst.BaseURL = strings.TrimSpace(in.BaseURL)
	// Поля формы проверяются до .p12: человек видит первую ошибку сверху.
	probe := inst
	probe.CertPEM, probe.KeyPEM = "-", "-"
	if err := validateInstance(probe); err != nil {
		return View{}, CheckResult{}, err
	}
	inst.BaseURL, _ = NormalizeBaseURL(inst.BaseURL)
	cc, err := ParseP12(in.P12, in.P12Password, s.opts.Now())
	if err != nil {
		return View{}, CheckResult{}, err
	}
	inst.CertPEM, inst.KeyPEM, inst.CertSubject, inst.CertNotAfter = cc.CertPEM, cc.KeyPEM, cc.Subject, cc.NotAfter
	unlock := s.lockInstance(inst.ID)
	defer unlock()
	if err := s.update(func(st *Store) error {
		for _, cur := range st.Instances {
			if cur.ID == inst.ID {
				return ErrInstanceExists
			}
		}
		st.Instances = append(st.Instances, inst)
		return nil
	}); err != nil {
		return View{}, CheckResult{}, err
	}
	s.forget(inst.ID)
	s.opts.Logger.Info("awg3-панель добавлена", "panel", inst.ID)
	res := s.checkLocked(ctx, inst.ID)
	saved, err := s.find(inst.ID)
	if err != nil {
		return View{}, res, err
	}
	return viewOf(saved), res, nil
}

// Update: пустой пароль и nil .p12 -- «не менять». Проверка -- только при
// смене учётных данных; правка названия или включённости к панели не ходит.
func (s *Service) Update(ctx context.Context, id string, in Input) (View, *CheckResult, error) {
	id = normID(id)
	unlock := s.lockInstance(id)
	defer unlock()
	credsChanged := false
	err := s.update(func(st *Store) error {
		i := -1
		for k := range st.Instances {
			if st.Instances[k].ID == id {
				i = k
			}
		}
		if i < 0 {
			return ErrInstanceNotFound
		}
		cur := st.Instances[i]
		next := cur
		next.Label = strings.TrimSpace(in.Label)
		if next.Label == "" {
			next.Label = id
		}
		next.User = strings.TrimSpace(in.User)
		base, err := NormalizeBaseURL(in.BaseURL)
		if err != nil {
			return err
		}
		next.BaseURL = base
		if in.Enabled != nil {
			next.Enabled = *in.Enabled
		}
		moved := next.BaseURL != cur.BaseURL || next.User != cur.User
		if in.Password != "" {
			next.Password = in.Password
		} else if moved {
			return &FieldError{Field: "password", Reason: "Адрес или логин изменены — введите пароль заново"}
		}
		if err := validateInstance(next); err != nil {
			return err
		}
		if in.P12 != nil {
			cc, err := ParseP12(in.P12, in.P12Password, s.opts.Now())
			if err != nil {
				return err
			}
			next.CertPEM, next.KeyPEM, next.CertSubject, next.CertNotAfter = cc.CertPEM, cc.KeyPEM, cc.Subject, cc.NotAfter
		}
		credsChanged = in.Password != "" || in.P12 != nil || moved
		if credsChanged {
			next.Lock = LockNone
		}
		if next.BaseURL != cur.BaseURL {
			next.Readonly = false
		}
		st.Instances[i] = next
		return nil
	})
	if err != nil {
		return View{}, nil, err
	}
	s.forget(id)
	s.opts.Logger.Info("awg3-панель изменена", "panel", id, "credentials_changed", credsChanged)
	var res *CheckResult
	if credsChanged {
		r := s.checkLocked(ctx, id)
		res = &r
	}
	saved, err := s.find(id)
	if err != nil {
		return View{}, res, err
	}
	return viewOf(saved), res, nil
}

func (s *Service) Delete(id string) error {
	id = normID(id)
	unlock := s.lockInstance(id)
	defer unlock()
	err := s.update(func(st *Store) error {
		for i, inst := range st.Instances {
			if inst.ID == id {
				st.Instances = append(st.Instances[:i:i], st.Instances[i+1:]...)
				return nil
			}
		}
		return ErrInstanceNotFound
	})
	if err == nil {
		s.forget(id)
		s.opts.Logger.Info("awg3-панель удалена", "panel", id)
	}
	return err
}

// checkLocked -- ровно один GET /api/ifaces; зовётся под замком экземпляра.
func (s *Service) checkLocked(ctx context.Context, id string) CheckResult {
	_, c, err := s.ready(id)
	if err != nil {
		var pe *Error
		errors.As(err, &pe)
		res := CheckResult{Kind: KindOf(err)}
		if pe != nil {
			res.Until = pe.Until
		}
		return res
	}
	list, err := c.Ifaces(ctx)
	if err != nil {
		err = s.trip(id, err)
		var pe *Error
		errors.As(err, &pe)
		res := CheckResult{Ran: true, Kind: KindOf(err)}
		if pe != nil {
			res.Until = pe.Until
		}
		return res
	}
	s.putIfaces(id, list)
	s.opts.Logger.Info("awg3-панель: проверка прошла", "panel", id, "ifaces", len(list))
	return CheckResult{Ran: true, OK: true, Ifaces: len(list)}
}

// ready -- предохранитель и клиент. Ни одного запроса, если панель выключена,
// под замком 401/TLS или на паузе 429.
func (s *Service) ready(id string) (Instance, *Client, error) {
	inst, err := s.find(id)
	if err != nil {
		return Instance{}, nil, err
	}
	if !inst.Enabled {
		return Instance{}, nil, ErrInstanceDisabled
	}
	switch inst.Lock {
	case LockBadPassword:
		return Instance{}, nil, &Error{Kind: KindBadPassword, Msg: "панель не приняла логин или пароль — пересохраните учётные данные"}
	case LockCert:
		return Instance{}, nil, &Error{Kind: KindCert, Msg: "сертификат не принят — загрузите .p12 заново"}
	case LockServerCert:
		return Instance{}, nil, &Error{Kind: KindServerCert, Msg: "сертификат панели не прошёл проверку — проверьте адрес и сертификат на сервере"}
	}
	if s.opts.Now().Before(inst.PausedUntil) {
		return Instance{}, nil, &Error{Kind: KindBanned, Until: inst.PausedUntil, Msg: "панель ограничила вход"}
	}
	c, err := s.client(inst)
	if err != nil {
		return Instance{}, nil, s.trip(id, err)
	}
	return inst, c, nil
}

func (s *Service) client(inst Instance) (*Client, error) {
	fp := sha256.Sum256([]byte(inst.BaseURL + "\x00" + inst.User + "\x00" + inst.Password + "\x00" + inst.CertPEM + "\x00" + inst.KeyPEM))
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	if cc, ok := s.clients[inst.ID]; ok && cc.fp == fp {
		return cc.c, nil
	}
	c, err := NewClient(Credentials{BaseURL: inst.BaseURL, User: inst.User, Password: inst.Password, CertPEM: []byte(inst.CertPEM), KeyPEM: []byte(inst.KeyPEM)},
		ClientOptions{RootCAs: s.opts.RootCAs, Timeout: s.opts.Timeout})
	if err != nil {
		return nil, err
	}
	s.clients[inst.ID] = cachedClient{fp: fp, c: c}
	return c, nil
}

// trip взводит предохранитель по отказу и возвращает отказ (у паузы -- с Until).
func (s *Service) trip(id string, err error) error {
	var pe *Error
	if !errors.As(err, &pe) {
		return err
	}
	switch pe.Kind {
	case KindBadPassword:
		s.persist(id, func(i *Instance) { i.Lock = LockBadPassword })
	case KindCert:
		s.persist(id, func(i *Instance) { i.Lock = LockCert })
	case KindServerCert:
		s.persist(id, func(i *Instance) { i.Lock = LockServerCert })
	case KindBanned:
		until := s.opts.Now().Add(s.opts.Pause)
		s.persist(id, func(i *Instance) { i.PausedUntil = until })
		cp := *pe
		cp.Until = until
		err = &cp
	case KindReadonly:
		s.persist(id, func(i *Instance) { i.Readonly = true })
	case KindNotFound:
		s.forget(id)
	}
	s.opts.Logger.Warn("awg3-панель: отказ", "panel", id, "kind", pe.Kind, "status", pe.Status)
	return err
}

func (s *Service) forget(id string) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	delete(s.ifaces, id)
	delete(s.clients, id)
	for k := range s.pages {
		if strings.HasPrefix(k, id+"|") {
			delete(s.pages, k)
		}
	}
}

func (s *Service) forgetPages(id string) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	for k := range s.pages {
		if strings.HasPrefix(k, id+"|") {
			delete(s.pages, k)
		}
	}
}

func (s *Service) putIfaces(id string, list []Iface) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	s.ifaces[id] = cachedIfaces{list: list, at: s.opts.Now()}
}

func (s *Service) cachedIfacesFor(id string) ([]Iface, bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	c, ok := s.ifaces[id]
	if !ok || s.opts.Now().Sub(c.at) > s.opts.CacheTTL {
		return nil, false
	}
	return c.list, true
}

func (s *Service) cachedPageFor(key string) (Page, bool) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	c, ok := s.pages[key]
	if !ok || s.opts.Now().Sub(c.at) > s.opts.CacheTTL {
		return Page{}, false
	}
	return c.page, true
}

// Peers -- интерфейсы, пиры и сводка. Параллельные открытия одного экрана
// склеиваются, ответ живёт 30 с.
func (s *Service) Peers(ctx context.Context, id, iface string) (Page, error) {
	id, iface = normID(id), strings.TrimSpace(iface)
	v, err, _ := s.group.Do("peers|"+id+"|"+iface, func() (any, error) {
		return s.loadPage(context.WithoutCancel(ctx), id, iface)
	})
	if err != nil {
		return Page{}, err
	}
	return v.(Page), nil
}

func (s *Service) loadPage(ctx context.Context, id, iface string) (Page, error) {
	unlock := s.lockInstance(id)
	defer unlock()
	inst, c, err := s.ready(id)
	if err != nil {
		return Page{}, err
	}
	list, ok := s.cachedIfacesFor(id)
	if !ok {
		if list, err = c.Ifaces(ctx); err != nil {
			return Page{}, s.trip(id, err)
		}
		s.putIfaces(id, list)
	}
	page := Page{Panel: viewOf(inst), Ifaces: list, Peers: []Peer{}, FetchedAt: s.opts.Now()}
	if len(list) == 0 {
		return page, nil
	}
	if iface == "" {
		iface = list[0].ID
	}
	known := false
	for _, i := range list {
		known = known || i.ID == iface
	}
	if !known {
		return Page{}, ErrIfaceNotFound
	}
	key := id + "|" + iface
	if cached, ok := s.cachedPageFor(key); ok {
		cached.Panel, cached.Ifaces = viewOf(inst), list
		return cached, nil
	}
	peers, err := c.Peers(ctx, iface)
	if err != nil {
		return Page{}, notFoundAsIface(s.trip(id, err))
	}
	sum, err := c.Summary(ctx, iface)
	if err != nil {
		return Page{}, notFoundAsIface(s.trip(id, err))
	}
	page.Iface, page.Peers, page.Summary = iface, peers, sum
	s.cacheMu.Lock()
	s.pages[key] = cachedPage{page: page, at: s.opts.Now()}
	s.cacheMu.Unlock()
	return page, nil
}

func notFoundAsIface(err error) error {
	if KindOf(err) == KindNotFound {
		return ErrIfaceNotFound
	}
	return err
}

func validatePeerName(name string) error {
	if name == "" || utf8.RuneCountInString(name) > maxPeerName || strings.ContainsAny(name, "[]") || hasControl(name) {
		return &FieldError{Field: "name", Reason: "Имя устройства: от 1 до 40 знаков, без «[», «]» и переводов строки"}
	}
	return nil
}

// IssueDevice -- новый пир для телефона или ноутбука. Под замком экземпляра
// свежий список: пир с тем же именем (двойной клик, повтор) -- отказ, а не
// второй пир.
func (s *Service) IssueDevice(ctx context.Context, id, iface, name string) (Issued, error) {
	name = strings.TrimSpace(name)
	if err := validatePeerName(name); err != nil {
		return Issued{}, err
	}
	if strings.HasPrefix(strings.ToLower(name), RouterPeerPrefix) {
		return Issued{}, &FieldError{Field: "name", Reason: "Имена «wgmon-…» бот оставляет роутерам — выберите другое"}
	}
	iface = strings.TrimSpace(iface)
	if iface == "" {
		return Issued{}, &FieldError{Field: "iface", Reason: "Выберите интерфейс панели"}
	}
	id = normID(id)
	unlock := s.lockInstance(id)
	defer unlock()
	inst, c, err := s.ready(id)
	if err != nil {
		return Issued{}, err
	}
	if inst.Readonly {
		return Issued{}, &Error{Kind: KindReadonly, Msg: "панель только для просмотра"}
	}
	peers, err := c.Peers(ctx, iface)
	if err != nil {
		return Issued{}, notFoundAsIface(s.trip(id, err))
	}
	for _, p := range peers {
		if strings.TrimSpace(p.Name) == name {
			return Issued{}, ErrNameTaken
		}
	}
	// WithoutCancel: ушедший вызывающий (закрытая вкладка) не должен превращать
	// уже начатое создание пира на панели в ошибку -- получим и запомним
	// результат, таймаут всё равно даёт клиент (10 с).
	issued, err := c.AddPeer(context.WithoutCancel(ctx), iface, name)
	if err != nil {
		return Issued{}, notFoundAsIface(s.trip(id, err))
	}
	s.forgetPages(id)
	s.opts.Logger.Info("awg3-панель: выпущен пир устройства", "panel", id, "iface", iface, "peer_id", issued.ID)
	return issued, nil
}

// ConfigForRouter -- конфиг для роутера: пир «wgmon-<ник>» уже есть -- его
// конфиг заново (слот не тратится), нет -- новый пир.
func (s *Service) ConfigForRouter(ctx context.Context, id, iface, nickname string) (RouterConfig, error) {
	name, err := RouterPeerName(nickname)
	if err != nil {
		return RouterConfig{}, err
	}
	iface = strings.TrimSpace(iface)
	if iface == "" {
		return RouterConfig{}, &FieldError{Field: "iface", Reason: "Выберите интерфейс панели"}
	}
	id = normID(id)
	unlock := s.lockInstance(id)
	defer unlock()
	inst, c, err := s.ready(id)
	if err != nil {
		return RouterConfig{}, err
	}
	if inst.Readonly {
		return RouterConfig{}, &Error{Kind: KindReadonly, Msg: "панель только для просмотра"}
	}
	peers, err := c.Peers(ctx, iface)
	if err != nil {
		return RouterConfig{}, notFoundAsIface(s.trip(id, err))
	}
	if p, ok := newestNamed(peers, name); ok {
		conf, err := c.PeerConfig(ctx, iface, p.ID)
		if err == nil {
			s.opts.Logger.Info("awg3-панель: конфиг роутера по имеющемуся пиру", "panel", id, "iface", iface, "peer_id", p.ID)
			return RouterConfig{Conf: conf, PeerID: p.ID, Reused: true}, nil
		}
		if KindOf(err) != KindNotFound {
			return RouterConfig{}, s.trip(id, err)
		}
		// Пира удалили между списком и выдачей -- выпускаем новый.
	}
	// WithoutCancel: см. IssueDevice -- ушедший вызывающий не должен ронять
	// уже начатое создание пира.
	issued, err := c.AddPeer(context.WithoutCancel(ctx), iface, name)
	if err != nil {
		return RouterConfig{}, notFoundAsIface(s.trip(id, err))
	}
	s.forgetPages(id)
	s.opts.Logger.Info("awg3-панель: выпущен пир роутера", "panel", id, "iface", iface, "peer_id", issued.ID)
	return RouterConfig{Conf: []byte(issued.Config), PeerID: issued.ID}, nil
}

// newestNamed -- среди пиров с этим именем самый свежий по created_at (RFC3339
// UTC сравнивается строкой); при равенстве -- последний в списке.
func newestNamed(peers []Peer, name string) (Peer, bool) {
	var best Peer
	found := false
	for _, p := range peers {
		if strings.TrimSpace(p.Name) != name {
			continue
		}
		if !found || p.CreatedAt >= best.CreatedAt {
			best, found = p, true
		}
	}
	return best, found
}

// RouterPeerName -- имя пира роутера на панели: «wgmon-<ник>», не длиннее 40
// рун и без символов, которые панель отвергнет.
func RouterPeerName(nickname string) (string, error) {
	n := strings.TrimSpace(nickname)
	name := RouterPeerPrefix + n
	if n == "" || utf8.RuneCountInString(name) > maxPeerName || strings.ContainsAny(name, "[]") || hasControl(name) {
		return "", &FieldError{Field: "router", Reason: "Имя роутера не годится для пира панели: до 34 знаков, без «[», «]»"}
	}
	return name, nil
}

// PeerState -- точка на экране: online -- handshake не старше 180 с, idle --
// давно, never -- ни разу, off -- пир выключен. ageSec -- секунды с последнего
// handshake, -1 -- не было ни одного. Handshake из будущего (часы VPS
// спешат) -- «только что».
func PeerState(p Peer, now time.Time) (string, int64) {
	if p.NeverConnected || p.LastHandshake <= 0 {
		if !p.Enabled {
			return "off", -1
		}
		return "never", -1
	}
	age := now.Unix() - p.LastHandshake
	if age < 0 {
		age = 0
	}
	switch {
	case !p.Enabled:
		return "off", age
	case age <= int64(OnlineWindow/time.Second):
		return "online", age
	default:
		return "idle", age
	}
}

// TunnelName -- имя VPN-туннеля на роутере: «<панель>_<интерфейс>», по тем же
// правилам, что selfhostedamnezia.TunnelName (латиница, цифры, «_», «-», до 32).
// Повторный выпуск с той же панели и интерфейса заменяет туннель (replace:true).
func TunnelName(instanceID, iface string) string {
	s := strings.ToLower(instanceID + "_" + iface)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if out == "" || out[0] < 'a' || out[0] > 'z' {
		out = "awg3-" + out
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}
