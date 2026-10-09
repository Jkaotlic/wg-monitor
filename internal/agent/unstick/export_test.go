package unstick

// Deps -- для тестов пакета: подменить побочные эффекты после New.
func (w *Watcher) Deps() *Deps { return &w.d }

func (w *Watcher) serviceAtZero() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.serviceAt.IsZero()
}
