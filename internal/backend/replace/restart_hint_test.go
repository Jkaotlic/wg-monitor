package replace

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
)

// I-1: provision выбирает текст после рестарта по строке вида задания
// (импортировать этот пакет оттуда нельзя -- цикл). Тест держит пару:
// переименуют вид -- текст «запустите ещё раз» не вернётся молча.
func TestRestartHint_NoRerunAdvice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	s := provision.NewPersistentStore(path)
	job := s.Create(KindReplaceConfig, "r1", []provision.Step{{Name: "a", Status: provision.StepActive}})
	got, _ := provision.NewPersistentStore(path).Get(job.ID)
	if !strings.Contains(got.Hint, "посреди замены конфига") || strings.Contains(got.Hint, "запустите ещё раз") {
		t.Fatalf("Hint = %q", got.Hint)
	}
}
