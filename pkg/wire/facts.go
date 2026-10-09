package wire

import (
	"encoding/json"
	"slices"
	"sort"
	"time"
	"unicode/utf8"
)

// TriggerHook -- отчёт разбужен ndm-хуком KeenOS (v0.47), а не тикером. Бэкенд
// пишет его события, но автомат тревог им не двигает: три таких отчёта за
// минуту иначе дали бы тревогу втрое быстрее (FSM твердеет по счёту).
const TriggerHook = "hook"

// Источник замера адреса выхода.
const (
	ExitSourceAwgm  = "awgm"  // /api/test/ip самого awg-manager
	ExitSourceAgent = "agent" // свой trace через интерфейс VPN-туннеля
)

// Потолки блока фактов. Весь отчёт ограничен 64 КиБ (backend maxReportBytes),
// факты обязаны жить в 16 КиБ на любом роутере.
const (
	MaxExitTunnels    = 16
	MaxPingRuns       = 20
	MaxUnstickEvents  = 20
	MaxWANLinks       = 8
	MaxNativeDNSLists = 40
	MaxFactText       = 80
)

// MaxFactsBytes -- жёсткий бюджет JSON-блока фактов после Clamp. Потолки
// отдельных полей (MaxExitTunnels, MaxPingRuns, ...) рассчитаны на типичный
// отчёт, но на роутере с длинными именами туннелей/списков и отказавшими
// разом источниками их сумма (все потолки заполнены, тексты обрезаны до
// MaxFactText рун кириллицей) даёт около 35 КиБ -- почти вдвое больше
// бюджета. Clamp досекает по факту размера, а не только по числу элементов.
const MaxFactsBytes = 16 << 10

// ReportFacts -- знание о роутере, а не вердикт. Проверкой это не заводится:
// каждая проверка -- строка events на каждый отчёт, а таблица горячая.
// Каждый блок необязателен: агент шлёт его, когда тот изменился или раз в
// 10 минут; старый агент не шлёт ничего.
type ReportFacts struct {
	Exit      *ExitFacts      `json:"exit,omitempty"`
	PingRuns  []PingRun       `json:"ping_runs,omitempty"`
	PingLog   *PingLogFacts   `json:"ping_log,omitempty"`
	WAN       *WANFacts       `json:"wan,omitempty"`
	NativeDNS *NativeDNSFacts `json:"native_dns,omitempty"`
	Hooks     *HookFacts      `json:"hooks,omitempty"`
	Unstick   *UnstickFacts   `json:"unstick,omitempty"`
}

// ExitFacts -- адрес выхода по VPN-туннелям. Tunnels несёт только туннели
// последнего инвентаря: удалённый пропадает сам.
type ExitFacts struct {
	At      time.Time            `json:"at"`
	Tunnels map[string]ExitProbe `json:"tunnels"`
}

// ExitProbe -- один замер. Changed -- указатель: нет замера -- не «не изменился».
type ExitProbe struct {
	VPNIP      string    `json:"vpn_ip,omitempty"`
	DirectIP   string    `json:"direct_ip,omitempty"`
	EndpointIP string    `json:"endpoint_ip,omitempty"`
	Changed    *bool     `json:"changed,omitempty"`
	Source     string    `json:"source"`
	At         time.Time `json:"at"`
	Err        string    `json:"err,omitempty"`
}

// PingRun -- серия неудач пингчека awg-manager: от первой неудачи до первого
// успеха. Удачные пробы не пересылаются вовсе.
type PingRun struct {
	TunnelID   string    `json:"tunnel_id"`
	TunnelName string    `json:"tunnel_name,omitempty"`
	From       time.Time `json:"from"`
	To         time.Time `json:"to"`
	Fails      int       `json:"fails"`
	WentDown   bool      `json:"went_down,omitempty"`
	Recovered  bool      `json:"recovered,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// PingLogFacts -- читает ли агент журнал пингчека и у каких VPN-туннелей он
// есть. Без него экран не вправе сказать «awg-manager связь не терял».
type PingLogFacts struct {
	At      time.Time `json:"at"`
	State   string    `json:"state"`
	Tunnels []string  `json:"tunnels,omitempty"`
	Err     string    `json:"err,omitempty"`
}

// WANFacts -- подключения к провайдеру (/api/wan/status awg-manager).
type WANFacts struct {
	At          time.Time `json:"at"`
	Unsupported bool      `json:"unsupported,omitempty"`
	Err         string    `json:"err,omitempty"`
	Links       []WANLink `json:"links,omitempty"`
}

// WANLink -- одно подключение. PingCheck: nil -- не знаем (до сверки С3 и на
// старой прошивке), "" -- профиля нет, иначе имя профиля.
type WANLink struct {
	Name      string  `json:"name"`
	Label     string  `json:"label,omitempty"`
	Role      string  `json:"role"`
	Up        bool    `json:"up"`
	Priority  int     `json:"priority"`
	PingCheck *string `json:"pingcheck,omitempty"`
}

// NativeDNSFacts -- списки «по имени сайта» самой прошивки KeenOS.
type NativeDNSFacts struct {
	At               time.Time       `json:"at"`
	UnverifiedReason string          `json:"unverified_reason,omitempty"`
	Lists            []NativeDNSList `json:"lists,omitempty"`
}

// NativeDNSList -- один список. Домены не пересылаются -- только их число.
type NativeDNSList struct {
	Name     string `json:"name"`
	Domains  int    `json:"domains"`
	Target   string `json:"target,omitempty"`
	TunnelID string `json:"tunnel_id,omitempty"`
	Mode     string `json:"mode,omitempty"`
	Owner    string `json:"owner"`
	Issue    string `json:"issue,omitempty"`
}

// HookFacts -- состояние ndm-хука. State: installed|unsupported|disabled|error.
type HookFacts struct {
	At           time.Time  `json:"at"`
	State        string     `json:"state"`
	Err          string     `json:"err,omitempty"`
	LastWakeAt   *time.Time `json:"last_wake_at,omitempty"`
	Wakes1h      int        `json:"wakes_1h"`
	Suppressed1h int        `json:"suppressed_1h"`
}

// AwgmLogs -- ответ команды awgm_logs. Записи свежими сверху.
type AwgmLogs struct {
	Unsupported bool           `json:"unsupported,omitempty"`
	Enabled     bool           `json:"enabled"`
	Total       int            `json:"total"`
	Truncated   bool           `json:"truncated,omitempty"`
	Entries     []AwgmLogEntry `json:"entries"`
}

type AwgmLogEntry struct {
	TS      string `json:"ts"`
	Level   string `json:"level"`
	Group   string `json:"group,omitempty"`
	Action  string `json:"action,omitempty"`
	Target  string `json:"target,omitempty"`
	Message string `json:"message"`
	Repeats int    `json:"repeats,omitempty"`
}

// Empty -- в блоке нечего слать.
func (f *ReportFacts) Empty() bool {
	return f == nil || (f.Exit == nil && len(f.PingRuns) == 0 && f.PingLog == nil &&
		f.WAN == nil && f.NativeDNS == nil && f.Hooks == nil && f.Unstick == nil)
}

// Clamp обрезает блок до потолков, а затем -- если сумма всё ещё не влезла
// в MaxFactsBytes -- досекает по факту размера JSON. Лишние VPN-туннели
// отбрасываются по порядку id -- детерминированно, чтобы хеш блока не
// прыгал от отчёта к отчёту.
//
// Байтовый бюджет режет с хвоста: сперва NativeDNS.Lists (менее срочно для
// тревог, чем состояние туннелей), затем PingRuns. Оба реза -- обычный
// reslice (f.X = f.X[:n]), а не запись поверх элементов: backing-array не
// трогается, значит отдельный слайс ожидающих серий, который может держать
// вызывающий агент (Task 5 -- pending PingRuns), не портится, даже если
// делит с f.PingRuns одну и ту же память. Clamp работает над значением f,
// которое уйдёт в отчёт, а не над состоянием агента.
func (f *ReportFacts) Clamp() {
	if f == nil {
		return
	}
	if f.Exit != nil {
		ids := make([]string, 0, len(f.Exit.Tunnels))
		for id := range f.Exit.Tunnels {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		// Карта -- ссылочный тип: agent (Task 2) может держать ту же карту как
		// свой «последний инвентарь» и передать её сюда напрямую. delete()/запись
		// по ключу мутировали бы чужие данные в обход правила «Clamp работает над
		// значением f, а не над состоянием вызывающего» (P1). Строим новую карту
		// только из удержанных id и кладём в неё копии ExitProbe с обрезанным
		// текстом -- исходная карта вызывающего никакими операциями не трогается.
		n := len(ids)
		if n > MaxExitTunnels {
			n = MaxExitTunnels
		}
		clamped := make(map[string]ExitProbe, n)
		for _, id := range ids[:n] {
			p := f.Exit.Tunnels[id]
			p.Err = ClipText(p.Err)
			clamped[id] = p
		}
		f.Exit.Tunnels = clamped
	}
	if len(f.PingRuns) > MaxPingRuns {
		f.PingRuns = f.PingRuns[:MaxPingRuns]
	}
	for i := range f.PingRuns {
		f.PingRuns[i].TunnelName = ClipText(f.PingRuns[i].TunnelName)
		f.PingRuns[i].Error = ClipText(f.PingRuns[i].Error)
	}
	if f.PingLog != nil {
		if len(f.PingLog.Tunnels) > MaxExitTunnels {
			f.PingLog.Tunnels = f.PingLog.Tunnels[:MaxExitTunnels]
		}
		f.PingLog.Err = ClipText(f.PingLog.Err)
	}
	if f.WAN != nil {
		if len(f.WAN.Links) > MaxWANLinks {
			f.WAN.Links = f.WAN.Links[:MaxWANLinks]
		}
		f.WAN.Err = ClipText(f.WAN.Err)
		for i := range f.WAN.Links {
			f.WAN.Links[i].Name = ClipText(f.WAN.Links[i].Name)
			f.WAN.Links[i].Label = ClipText(f.WAN.Links[i].Label)
		}
	}
	if f.NativeDNS != nil {
		if len(f.NativeDNS.Lists) > MaxNativeDNSLists {
			f.NativeDNS.Lists = f.NativeDNS.Lists[:MaxNativeDNSLists]
		}
		f.NativeDNS.UnverifiedReason = ClipText(f.NativeDNS.UnverifiedReason)
		for i := range f.NativeDNS.Lists {
			f.NativeDNS.Lists[i].Name = ClipText(f.NativeDNS.Lists[i].Name)
			f.NativeDNS.Lists[i].Target = ClipText(f.NativeDNS.Lists[i].Target)
		}
	}
	if f.Hooks != nil {
		f.Hooks.Err = ClipText(f.Hooks.Err)
	}
	if f.Unstick != nil {
		evs := f.Unstick.Events
		if len(evs) > MaxUnstickEvents {
			evs = evs[len(evs)-MaxUnstickEvents:]
		}
		clipped := make([]UnstickEvent, len(evs))
		for i, e := range evs {
			e.TunnelName = ClipText(e.TunnelName)
			e.From = ClipText(e.From)
			e.To = ClipText(e.To)
			e.Steps = slices.Clone(e.Steps)
			clipped[i] = e
		}
		f.Unstick = &UnstickFacts{Events: clipped}
	}

	// Потолки полей ограничивают число элементов, но не гарантируют бюджет
	// байтов: на маршрутизаторе с длинными именами и отказавшими разом
	// источниками сумма всё ещё может превысить MaxFactsBytes. Досекаем
	// детерминированно с хвоста, пока не влезем.
	for f.jsonLen() > MaxFactsBytes {
		if f.NativeDNS != nil && len(f.NativeDNS.Lists) > 0 {
			f.NativeDNS.Lists = f.NativeDNS.Lists[:len(f.NativeDNS.Lists)-1]
			continue
		}
		if len(f.PingRuns) > 0 {
			f.PingRuns = f.PingRuns[:len(f.PingRuns)-1]
			continue
		}
		break
	}
}

// jsonLen -- размер текущего блока в кодировке JSON. Используется только
// внутри Clamp для байтового бюджета.
func (f *ReportFacts) jsonLen() int {
	b, err := json.Marshal(f)
	if err != nil {
		return 0
	}
	return len(b)
}

// ClipText режет строку до MaxFactText рун, не разрывая руну.
func ClipText(s string) string {
	if utf8.RuneCountInString(s) <= MaxFactText {
		return s
	}
	r := []rune(s)
	return string(r[:MaxFactText-1]) + "…"
}

// UnstickFacts -- журнал сторожа зависаний awg-manager (v0.59): последние
// MaxUnstickEvents лесенок за сутки, по возрастанию времени. Бэкенд
// дедуплицирует по ID -- журнал шлётся целиком, пока не изменится.
type UnstickFacts struct {
	Events []UnstickEvent `json:"events"`
}

const (
	UnstickFixed  = "fixed"
	UnstickGaveUp = "gave_up"
)

// UnstickEvent -- одна лесенка по одному туннелю. Steps -- "restart",
// "start", "stop", "stop_start", "service_restart" по порядку.
type UnstickEvent struct {
	ID         string    `json:"id"`
	TunnelID   string    `json:"tunnel_id"`
	TunnelName string    `json:"tunnel_name,omitempty"`
	From       string    `json:"from"`
	Steps      []string  `json:"steps"`
	Result     string    `json:"result"`
	To         string    `json:"to,omitempty"`
	At         time.Time `json:"at"`
}
