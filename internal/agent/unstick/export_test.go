package unstick

import "time"

// Deps -- для тестов пакета: подменить побочные эффекты после New.
func (w *Watcher) Deps() *Deps { return &w.d }

func (w *Watcher) serviceAtZero() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.serviceAt.IsZero()
}

// clearStartGuard снимает тишину, которую New ставит после старта агента:
// тесты, не про неё, начинают с чистого окна.
func (w *Watcher) clearStartGuard() {
	w.mu.Lock()
	w.cmdRouter = time.Time{}
	w.mu.Unlock()
}
