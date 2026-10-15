package provision

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/fileown"
)

// restartHint -- что видит человек, если сервер перезапустился посреди
// задания. Продолжать установку после рестарта не умеем: честно говорим,
// что она оборвана.
const restartHint = "Сервер перезапустился во время установки — запустите ещё раз"

// persistedJob -- то, что экрану прогресса нужно после перезапуска. Секретов
// в Job нет (токен и пароли живут в StartReq и в памяти воркера), Tail уже
// очищен от них.
type persistedJob struct {
	Job
	Ended time.Time `json:"ended,omitempty"`
}

// NewPersistentStore -- Store, который хранит задания в файле path (каталог
// хранилищ, том /data): после перезапуска экран получает правду, а не 404.
// Незавершённые на старте задания помечаются проваленными. Ошибки чтения и
// записи не фатальны: без файла Store работает как обычный, в памяти.
func NewPersistentStore(path string) *Store {
	s := NewStore()
	s.path = path
	s.load()
	return s
}

// load читает файл, вычищает просроченное и переводит running в failed.
func (s *Store) load() {
	if s.path == "" {
		return
	}
	body, err := os.ReadFile(s.path) // #nosec G304 -- путь из конфига
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			slog.Warn("provision: файл заданий не прочитан", "path", s.path, "err", err)
		}
		return
	}
	var list []persistedJob
	if err := json.Unmarshal(body, &list); err != nil {
		slog.Warn("provision: файл заданий повреждён, начинаем с пустого", "path", s.path, "err", err)
		return
	}
	now := s.nowFn()
	cutoff := now.Add(-jobTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range list {
		j := list[i].Job
		j.ended = list[i].Ended
		if j.ID == "" {
			continue
		}
		if j.State == StateRunning {
			j.State = StateFailed
			j.Hint = restartHint
			for k := range j.Steps {
				if j.Steps[k].Status == StepActive {
					j.Steps[k].Status = StepFailed
				}
			}
			j.ended = now
		}
		if j.ended.Before(cutoff) {
			continue
		}
		s.jobs[j.ID] = &j
	}
	s.saveLocked()
}

// saveLocked пишет задания в файл атомарно (временный файл рядом + rename),
// владелец -- как у каталога (контейнер работает от root). Зовётся под s.mu.
func (s *Store) saveLocked() {
	if s.path == "" {
		return
	}
	list := make([]persistedJob, 0, len(s.jobs))
	for _, j := range s.jobs {
		list = append(list, persistedJob{Job: *j, Ended: j.ended})
	}
	body, err := json.Marshal(list)
	if err != nil {
		slog.Error("provision: задания не закодированы", "err", err)
		return
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		slog.Error("provision: каталог заданий", "path", dir, "err", err)
		return
	}
	tmp := s.path + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600) // #nosec G304 -- путь из конфига
	if err != nil {
		slog.Error("provision: запись заданий", "path", tmp, "err", err)
		return
	}
	_, werr := f.Write(body)
	if werr == nil {
		werr = fileown.MatchDir(f, dir)
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		slog.Error("provision: запись заданий", "path", tmp, "err", werr)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		slog.Error("provision: замена файла заданий", "path", s.path, "err", err)
	}
}
