package dnswatch

import (
	"context"
	"testing"
	"time"
)

// CHK-04: ndmc изредка отдаёт пустой running-config с кодом 0. Пустой вывод
// читался как «строк нет»: своя строка «пропала» -> сторож уходил в idle и
// стирал запись о запасных строках -> проверка теряла foreign_leftover.
// Пустой вывод -- не прочитано, а не «роутер пуст».
func TestWatch_EmptyRunningConfigIsUnreadableNotEmpty(t *testing.T) {
	h := stuckCloudflareReturn(t)
	if c := runCheck(t, h.w); c.Details["reason"] != DetailReasonForeignLeftover {
		t.Fatalf("setup: check = %s %#v", c.Status, c.Details)
	}
	empty := true
	real := h.w.exec
	h.w.exec = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if empty && len(args) == 2 && args[1] == "show running-config" {
			return []byte("  \n"), nil
		}
		return real(ctx, name, args...)
	}
	h.tick(time.Minute)
	if s := h.w.Snapshot(); s.Idle {
		t.Fatalf("an empty read sent the watchdog idle: %+v", s)
	}
	if p := h.state(); !contains(p.Leftover, cloudflareForeign) {
		t.Fatalf("an empty read lost the leftover record: %+v", p)
	}
	if c := runCheck(t, h.w); c.Status != "fail" || c.Details["reason"] != DetailReasonForeignLeftover {
		t.Fatalf("check after an empty read = %s %#v, want fail foreign_leftover", c.Status, c.Details)
	}
	empty = false
	h.tick(time.Minute)
	if c := runCheck(t, h.w); c.Details["reason"] != DetailReasonForeignLeftover {
		t.Fatalf("check after a good read = %s %#v", c.Status, c.Details)
	}
}
