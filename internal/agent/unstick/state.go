package unstick

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/fileown"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// persisted -- unstick-state.json: «сдался» по туннелям, время последнего
// перезапуска службы и журнал -- всё, что не должно сбрасываться
// рестартом агента (иначе он пойдёт по кругу заново).
type persisted struct {
	GaveUp    map[string]giveUp   `json:"gave_up,omitempty"`
	ServiceAt time.Time           `json:"service_at,omitempty"`
	Events    []wire.UnstickEvent `json:"events,omitempty"`
}

func (w *Watcher) load() {
	path := w.cfg.StatePath
	if path == "" {
		return
	}
	body, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	var p persisted
	if err == nil {
		err = json.Unmarshal(body, &p)
	}
	if err != nil {
		w.log.Warn("unstick: файл состояния не прочитан -- чистый старт", "path", path, "err", err)
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if p.GaveUp != nil {
		w.gaveUp = p.GaveUp
	}
	w.serviceAt = p.ServiceAt
	w.events = p.Events
	w.pruneEventsLocked(w.now())
}

func (w *Watcher) save() {
	path := w.cfg.StatePath
	if path == "" {
		return
	}
	w.mu.Lock()
	p := persisted{GaveUp: w.gaveUp, ServiceAt: w.serviceAt, Events: w.events}
	body, err := json.Marshal(p)
	same := err == nil && w.lastSaved != nil && bytes.Equal(body, w.lastSaved)
	w.mu.Unlock()
	if err != nil {
		w.log.Error("unstick: состояние не закодировано", "err", err)
		return
	}
	if same {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		w.log.Error("unstick: каталог состояния", "path", path, "err", err)
		return
	}
	tmp := path + ".tmp"
	// остаток прошлой попытки сохранил бы свои права при OpenFile
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		w.log.Error("unstick: запись состояния", "path", tmp, "err", err)
		return
	}
	_, werr := f.Write(body)
	if werr == nil {
		// контейнер/агент от root: владелец файла -- как у каталога
		werr = fileown.MatchDir(f, dir)
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(tmp)
		w.log.Error("unstick: запись состояния", "path", tmp, "err", werr)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		w.log.Error("unstick: замена состояния", "path", path, "err", err)
		return
	}
	w.mu.Lock()
	w.lastSaved = body
	w.mu.Unlock()
}
