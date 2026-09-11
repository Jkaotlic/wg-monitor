package callbacks

import (
	"strings"
	"testing"
)

// v0.30 задача 2: «Диагностика» теперь всегда запускает проверку заново
// (agwmgr /api/diagnostics/stream?restart=false), но ни один VPN-туннель при
// этом не перезапускается. Владелец должен узнать про «заново» и про
// отсутствие перезапуска, и текст больше не должен выдавать старый отчёт за
// новый фразой «последний отчёт».
func TestDiagNowSentTextSaysFreshCheck(t *testing.T) {
	s := formatQueuedStatus("diag_now", "")
	if strings.Contains(s, "последний отчёт") {
		t.Fatalf("текст больше не должен обещать последний (старый) отчёт: %q", s)
	}
	if !strings.Contains(s, "заново") {
		t.Fatalf("владелец должен знать, что роутер проверяет себя заново: %q", s)
	}
	if !strings.Contains(s, "не перезапускаются") {
		t.Fatalf("владелец должен знать, что VPN-туннели не перезапускаются: %q", s)
	}
}
