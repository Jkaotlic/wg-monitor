package backend

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backup"
)

// Тревога админу в личку о бэкапе (v0.53). Один поллер на процесс, тик раз в
// несколько минут -- как digest и realert; на запрос ничего не запускается.
//
// Три причины, у каждой свой срок молчания (не чаще раза в сутки):
//   - stale:  последний удачный малый архив старше 26 часов;
//   - failed: последний прогон любого вида кончился ошибкой, либо отметка
//     «прогон не завершён» стоит дольше двух часов (убитый по памяти прогон);
//   - verify: проверка восстановления не прошла.
//
// Правило для свежей установки: «бэкап ещё ни разу не удался» (нет ни одного
// удачного малого) тревожит только когда бэкенд работает дольше 26 часов, то
// есть ночной прогон давно должен был быть. Раньше -- молчим, чтобы не пугать
// до первой ночи. Провал уже сделанного прогона -- отдельная причина failed,
// он тревожит сразу, файл состояния есть или нет.
//
// Состояние тревог лежит в tg_state (через KV): перезапуск бэкенда не
// повторяет отправленное и не теряет «снова в порядке».

const (
	backupAlertStaleAfter = 26 * time.Hour
	backupAlertRunningFor = 2 * time.Hour
	backupAlertRepeat     = 24 * time.Hour
	backupAlertTick       = 10 * time.Minute

	backupAlertKeyPrefix = "backup_alert."
	backupAlertActiveKey = backupAlertKeyPrefix + "active"
	backupAlertWatchKey  = backupAlertKeyPrefix + "watch_since"

	backupRecoveredText = "Бэкап снова в порядке."
	backupAlertFooter   = "Подробности — в приложении, в «Парке», карточка «Бэкенд»."
)

// BackupAlertKV -- хранилище состояния тревог (db.KVRepo).
type BackupAlertKV interface {
	Get(key string) (string, error)
	Set(key, value string) error
}

// BackupAlertSender -- отправка в личку (tg.Client).
type BackupAlertSender interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
}

type BackupAlertConfig struct {
	StatusPath  string
	KV          BackupAlertKV
	Sender      BackupAlertSender
	AdminUserID int64
	// Now -- часы (nil -- настоящие). StartedAt -- когда запущен бэкенд: от
	// него считается «работает дольше 26 часов».
	Now       func() time.Time
	StartedAt time.Time
	TickEvery time.Duration
}

type BackupAlerter struct {
	cfg BackupAlertConfig
	mu  sync.Mutex
	// lastSent -- нижняя планка в памяти рядом с хранилищем: если tg_state не
	// пишется, тревога всё равно не повторяется каждый тик.
	lastSent map[string]time.Time
}

func NewBackupAlerter(cfg BackupAlertConfig) *BackupAlerter {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.TickEvery <= 0 {
		cfg.TickEvery = backupAlertTick
	}
	if cfg.StartedAt.IsZero() {
		cfg.StartedAt = cfg.Now()
	}
	return &BackupAlerter{cfg: cfg, lastSent: map[string]time.Time{}}
}

// Run крутит Tick, пока жив ctx.
func (a *BackupAlerter) Run(ctx context.Context) {
	t := time.NewTicker(a.cfg.TickEvery)
	defer t.Stop()
	a.Tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.Tick(ctx)
		}
	}
}

type backupAlertReason struct {
	key  string
	text string
}

// Tick -- один проход: найти причины, отправить новые (не чаще раза в
// сутки каждую), при возвращении в порядок сказать об этом один раз.
func (a *BackupAlerter) Tick(ctx context.Context) {
	if a.cfg.AdminUserID == 0 || a.cfg.Sender == nil || a.cfg.KV == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.cfg.Now()
	watch := a.watchSince(now)
	reasons, state := a.reasons(now, watch)

	if len(reasons) == 0 {
		// «Снова в порядке» -- только когда состояние решено: файл прочитан и
		// ни один прогон не идёт. Идущий прогон ещё может провалиться.
		if state.undecided() {
			return
		}
		if active, _ := a.cfg.KV.Get(backupAlertActiveKey); active == "1" {
			if a.send(ctx, backupRecoveredText) {
				a.forget()
			}
		}
		return
	}
	for _, r := range reasons {
		last, err := a.cfg.KV.Get(backupAlertKeyPrefix + r.key)
		if err != nil {
			slog.Warn("backup alert: state not read", "reason", r.key, "err", err.Error())
		}
		at, perr := time.Parse(time.RFC3339, last)
		if perr != nil {
			at = time.Time{}
		}
		if mem := a.lastSent[r.key]; mem.After(at) {
			at = mem
		}
		if !at.IsZero() && now.Sub(at) < backupAlertRepeat {
			// Молчим, но «активна» помним: восстановление должно сработать.
			a.set(backupAlertActiveKey, "1")
			continue
		}
		if !a.send(ctx, r.text) {
			continue // не записываем: следующий тик попробует снова
		}
		a.lastSent[r.key] = now
		a.set(backupAlertKeyPrefix+r.key, now.UTC().Format(time.RFC3339))
		a.set(backupAlertActiveKey, "1")
	}
}

// set пишет в хранилище и громко жалуется, если не вышло.
func (a *BackupAlerter) set(key, value string) {
	if err := a.cfg.KV.Set(key, value); err != nil {
		slog.Warn("backup alert: state not written", "key", key, "err", err.Error())
	}
}

// watchSince -- якорь «с каких пор следим»: записывается на первом тике и
// переживает перезапуски; от него, а не от запуска процесса, считается
// «ни разу не делался». Хранилище недоступно -- запасной якорь: запуск процесса.
func (a *BackupAlerter) watchSince(now time.Time) time.Time {
	v, err := a.cfg.KV.Get(backupAlertWatchKey)
	if err == nil {
		if t, perr := time.Parse(time.RFC3339, v); perr == nil {
			return t
		}
		a.set(backupAlertWatchKey, now.UTC().Format(time.RFC3339))
		return now
	}
	slog.Warn("backup alert: anchor not read", "err", err.Error())
	return a.cfg.StartedAt
}

func (a *BackupAlerter) send(ctx context.Context, text string) bool {
	if _, err := a.cfg.Sender.SendMessage(ctx, a.cfg.AdminUserID, nil, text, "", nil); err != nil {
		slog.Warn("backup alert: send failed", "err", err.Error())
		return false
	}
	return true
}

func (a *BackupAlerter) forget() {
	for _, k := range []string{"stale", "failed", "verify"} {
		a.set(backupAlertKeyPrefix+k, "")
		delete(a.lastSent, k)
	}
	a.set(backupAlertActiveKey, "")
	// watch_since не трогаем: это якорь, а не метка тревоги.
}

// alertState -- насколько можно доверять тому, что причин нет.
type alertState struct {
	unreadable bool // файл состояния не прочитан
	running    bool // какой-то прогон идёт (свежая отметка «не завершён»)
}

func (s alertState) undecided() bool { return s.unreadable || s.running }

// reasons -- какие причины для тревоги есть прямо сейчас.
func (a *BackupAlerter) reasons(now, watch time.Time) ([]backupAlertReason, alertState) {
	var state alertState
	st, err := backup.LoadStatus(a.cfg.StatusPath)
	if err != nil {
		state.unreadable = true
		// Нет файла или он битый: судить не по чему. Остаётся правило
		// «давно работает, а бэкапа так и нет».
		st = backup.Status{}
	}
	age := func(iso string) (time.Duration, bool) {
		t, perr := time.Parse(time.RFC3339, iso)
		if perr != nil {
			return 0, false
		}
		return now.Sub(t), true
	}
	running := func(k backup.KindStatus) bool {
		d, ok := age(k.LastRunAt)
		return k.Error == backup.RunUnfinishedText && ok && d < backupAlertRunningFor
	}
	state.running = running(st.Small) || running(st.Full)
	failedRun := func(k backup.KindStatus) bool { return !k.OK && k.LastRunAt != "" && !running(k) }

	var out []backupAlertReason
	smallFailed := failedRun(st.Small)

	if d, ok := age(st.Small.LastOKAt); ok {
		if d > backupAlertStaleAfter {
			out = append(out, backupAlertReason{"stale", fmt.Sprintf(
				"Бэкап не делался больше суток: последний удачный малый архив был около %d ч назад. %s", int(d.Hours()), backupAlertFooter)})
		}
	} else if !smallFailed && !running(st.Small) && now.Sub(watch) > backupAlertStaleAfter {
		out = append(out, backupAlertReason{"stale", "Бэкап ещё ни разу не делался, хотя ночной прогон давно должен был пройти. " + backupAlertFooter})
	}

	var lines []string
	if smallFailed {
		lines = append(lines, "Малый бэкап не удался"+alertReason(st.Small.Error)+".")
	}
	if failedRun(st.Full) {
		lines = append(lines, "Полный бэкап не удался"+alertReason(st.Full.Error)+".")
	}
	if len(lines) > 0 {
		out = append(out, backupAlertReason{"failed", strings.Join(lines, "\n") + "\n" + backupAlertFooter})
	}

	if st.Verify.LastRunAt != "" && !st.Verify.OK {
		text := "Проверка восстановления бэкапа не прошла" + alertReason(st.Verify.Error) + ". "
		// «Архивы есть» -- неправда, когда причина как раз в их отсутствии или возрасте.
		if w := backupReasonWords(st.Verify.Error); w != "малого архива ещё нет" && !strings.Contains(w, "старше 48 часов") {
			text += "Архивы есть, но по ним могут не восстановиться. "
		}
		out = append(out, backupAlertReason{"verify", text + backupAlertFooter})
	}
	return out, state
}

// alertReason -- «: «причина словами»»; причина из закрытого набора фраз, в
// ёлочках, потому что может содержать имена (Telegram) латиницей.
func alertReason(raw string) string {
	return ": «" + backupReasonWords(raw) + "»"
}
