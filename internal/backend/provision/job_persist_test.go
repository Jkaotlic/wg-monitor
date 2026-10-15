package provision

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPersistentStore_RunningJobComesBackFailedAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provision-jobs.json")
	s := NewPersistentStore(path)
	job := s.Create(KindRepairReinstall, "r1", []Step{{Name: "a", Status: StepPending}, {Name: "b", Status: StepPending}})
	s.Update(job.ID, func(j *Job) { j.Steps[0].Status = StepActive })

	s2 := NewPersistentStore(path)
	got, ok := s2.Get(job.ID)
	if !ok {
		t.Fatal("задание не пережило перезапуск")
	}
	if got.State != StateFailed {
		t.Fatalf("State = %q, want failed", got.State)
	}
	if !strings.Contains(got.Hint, "Сервер перезапустился во время установки") {
		t.Fatalf("Hint = %q", got.Hint)
	}
	if got.Steps[0].Status != StepFailed {
		t.Fatalf("активный шаг = %q, want failed", got.Steps[0].Status)
	}
	if got.Nickname != "r1" || got.Kind != KindRepairReinstall {
		t.Fatalf("поля потеряны: %+v", got)
	}
	// повторная загрузка не меняет итог
	s3 := NewPersistentStore(path)
	if g, _ := s3.Get(job.ID); g.State != StateFailed {
		t.Fatalf("третий старт: %q", g.State)
	}
}

func TestPersistentStore_FinishedJobKeptAndExpiredPruned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provision-jobs.json")
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	s := NewPersistentStore(path)
	s.now = func() time.Time { return now }
	ok := s.Create(KindProvision, "r1", []Step{{Name: "a"}})
	old := s.Create(KindProvision, "r2", []Step{{Name: "a"}})
	s.Update(ok.ID, func(j *Job) { j.State = StateSuccess; j.Version = "v1" })
	s.now = func() time.Time { return now.Add(-time.Hour) }
	s.Update(old.ID, func(j *Job) { j.State = StateSuccess })
	s.now = func() time.Time { return now }

	s2 := &Store{jobs: map[string]*Job{}, locks: map[string]bool{}, now: func() time.Time { return now.Add(time.Minute) }, path: path}
	s2.load()
	g, found := s2.Get(ok.ID)
	if !found || g.State != StateSuccess || g.Version != "v1" {
		t.Fatalf("итог потерян: %+v %v", g, found)
	}
	if _, found := s2.Get(old.ID); found {
		t.Fatal("просроченное задание не вычищено при загрузке")
	}
}

func TestPersistentStore_NoFileOrGarbageIsEmptyStore(t *testing.T) {
	dir := t.TempDir()
	if _, ok := NewPersistentStore(filepath.Join(dir, "none.json")).Get("x"); ok {
		t.Fatal("откуда задание")
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewPersistentStore(bad)
	j := s.Create(KindProvision, "r", nil)
	if _, ok := s.Get(j.ID); !ok {
		t.Fatal("хранилище не работает после битого файла")
	}
}

// I-1: задание, которое могло уже потратить платный конфиг или завести
// туннель (замена, починка), после рестарта не зовёт «запустите ещё раз»:
// итог неизвестен, сначала посмотреть VPN-туннели.
func TestPersistentStore_RestartHintByKind(t *testing.T) {
	cases := []struct {
		kind      JobKind
		wantRerun bool
		wantPart  string
	}{
		{KindProvision, true, "во время установки"},
		{KindRepairReinstall, true, "во время установки"},
		{KindRepairRepoint, true, "во время установки"},
		{JobKind("replace_config"), false, "посреди замены конфига"},
		{JobKind("link_repair"), false, "посреди починки"},
		{JobKind("something_new"), false, "Проверьте VPN-туннели"},
	}
	for _, c := range cases {
		t.Run(string(c.kind), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "jobs.json")
			s := NewPersistentStore(path)
			job := s.Create(c.kind, "r1", []Step{{Name: "a", Status: StepActive}})
			got, _ := NewPersistentStore(path).Get(job.ID)
			if got.State != StateFailed {
				t.Fatalf("State = %q", got.State)
			}
			if !strings.Contains(got.Hint, c.wantPart) {
				t.Fatalf("Hint = %q, want contains %q", got.Hint, c.wantPart)
			}
			if rerun := strings.Contains(got.Hint, "запустите ещё раз"); rerun != c.wantRerun {
				t.Fatalf("Hint = %q: «запустите ещё раз» = %v, want %v", got.Hint, rerun, c.wantRerun)
			}
			if !c.wantRerun && !strings.Contains(got.Hint, "Проверьте VPN-туннели") {
				t.Fatalf("Hint = %q: нет совета проверить туннели", got.Hint)
			}
		})
	}
}
