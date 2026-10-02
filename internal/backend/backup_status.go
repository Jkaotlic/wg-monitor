package backend

import (
	"strings"
	"sync"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backup"
)

// Состояние бэкапа для экрана. Источник -- backup-status.json рядом с базой:
// его пишет CLI (`backup`, `backup verify`), бэкенд только читает. Сырые
// тексты ошибок из файла наружу не идут никому: в ответ попадает причина
// словами из закрытого набора фраз (backupReasonWords), без путей и адресов.

// backupStatusCacheTTL -- как долго прочитанное состояние живёт в памяти:
// экран опрашивает сводку часто, а файл меняется раз в сутки.
const backupStatusCacheTTL = 30 * time.Second

type backupSummary struct {
	// Known=false -- файла нет или он не разбирается: «состояние неизвестно».
	// Это не ошибка всего ответа.
	Known  bool                 `json:"known"`
	Small  *backupKindSummary   `json:"small,omitempty"`
	Full   *backupKindSummary   `json:"full,omitempty"`
	Verify *backupVerifySummary `json:"verify,omitempty"`
}

type backupKindSummary struct {
	LastOKAt  string `json:"last_ok_at"`
	LastRunAt string `json:"last_run_at"`
	OK        bool   `json:"ok"`
	// Unfinished -- прогон начат и не завершён (идёт или убит).
	Unfinished bool   `json:"unfinished,omitempty"`
	SizeBytes  int64  `json:"size_bytes"`
	Telegram   string `json:"telegram"`
	Offsite    string `json:"offsite,omitempty"`
	// Reason -- причина провала словами; пусто, когда всё хорошо.
	Reason string `json:"reason,omitempty"`
}

type backupVerifySummary struct {
	LastRunAt string `json:"last_run_at"`
	OK        bool   `json:"ok"`
	Routers   int    `json:"routers"`
	Reason    string `json:"reason,omitempty"`
}

// BackupStatusSource читает backup-status.json по требованию, с коротким
// кэшем. Безопасен для одновременных вызовов.
type BackupStatusSource struct {
	path string
	now  func() time.Time

	mu     sync.Mutex
	cached backupSummary
	at     time.Time
	have   bool
}

// NewBackupStatusSource -- источник для файла path (backup.StatusPath от
// db_path). now == nil -- настоящие часы.
func NewBackupStatusSource(path string, now func() time.Time) *BackupStatusSource {
	if now == nil {
		now = time.Now
	}
	return &BackupStatusSource{path: path, now: now}
}

// Summary -- текущее состояние. Файла нет или он битый -- Known=false.
func (s *BackupStatusSource) Summary() backupSummary {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.have && now.Sub(s.at) < backupStatusCacheTTL {
		return s.cached
	}
	s.cached, s.at, s.have = s.read(), now, true
	return s.cached
}

func (s *BackupStatusSource) read() backupSummary {
	st, err := backup.LoadStatus(s.path)
	if err != nil {
		return backupSummary{}
	}
	return summarizeBackupStatus(st)
}

func summarizeBackupStatus(st backup.Status) backupSummary {
	kind := func(k backup.KindStatus) *backupKindSummary {
		out := &backupKindSummary{
			LastOKAt: k.LastOKAt, LastRunAt: k.LastRunAt, OK: k.OK,
			SizeBytes: k.SizeBytes, Telegram: k.Telegram, Offsite: k.Offsite,
		}
		if !k.OK && k.LastRunAt != "" {
			out.Unfinished = k.Error == backup.RunUnfinishedText
			out.Reason = backupReasonWords(k.Error)
		}
		return out
	}
	v := &backupVerifySummary{LastRunAt: st.Verify.LastRunAt, OK: st.Verify.OK, Routers: st.Verify.Routers}
	if !st.Verify.OK && st.Verify.LastRunAt != "" {
		v.Reason = backupReasonWords(st.Verify.Error)
	}
	return backupSummary{Known: true, Small: kind(st.Small), Full: kind(st.Full), Verify: v}
}

// backupReasonWords превращает сырой текст ошибки из файла состояния в
// короткую фразу из закрытого набора. Пути, адреса и куски вывода команд
// сюда не попадают по построению: фраза выбирается, а не вырезается.
func backupReasonWords(raw string) string {
	t := strings.ToLower(raw)
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(t, w) {
				return true
			}
		}
		return false
	}
	switch {
	case raw == backup.RunUnfinishedText:
		return "прогон не завершён"
	case has("а telegram принимает до"):
		return "архив больше лимита Telegram"
	case has("admin_user_id"):
		return "в настройках нет получателя для Telegram"
	case has("токен бота"):
		return "токен бота не прочитан"
	case has("telegram"):
		return "Telegram не принял архив"
	case has("внешний сервер"):
		switch {
		case has("ключ сервера изменился"):
			return "внешний сервер: ключ сервера изменился"
		case has("отказ в доступе"):
			return "внешний сервер: отказ в доступе"
		case has("нет места"):
			return "внешний сервер: нет места"
		case has("сервер недоступен"):
			return "внешний сервер недоступен"
		case has("--offsite-key"):
			return "внешний сервер: не задан ключ SSH"
		}
		return "внешний сервер не принял архив"
	case has("парольн", "passphrase"):
		return "парольная фраза бэкапа не прочитана"
	case has("архив не записан", "временный каталог"):
		return "архив не записан на диск"
	case has("архив не собран", "small database"):
		return "не удалось собрать архив"
	case has("--kind"):
		return "неверная настройка запуска бэкапа"
	case has("конфигурац"):
		return "конфигурация бэкапа не читается"
	case has("малого архива ещё нет"):
		return "малого архива ещё нет"
	case has("старше 48 часов"):
		return "последний малый архив старше 48 часов"
	case has("не разворачивается"):
		return "архив не открывается"
	case has("нет счётчиков"):
		return "в архиве нет счётчиков для сверки"
	case has("нет хранилища"):
		return "в архиве нет хранилища, записанного при бэкапе"
	case has("в манифесте"):
		return "числа в архиве не сходятся с записанными при бэкапе"
	case has("хранилищ", "не json"):
		return "хранилище в архиве повреждено или потеряно"
	case has("база из архива"):
		return "база в архиве повреждена"
	}
	return "подробности в журнале службы бэкапа"
}
