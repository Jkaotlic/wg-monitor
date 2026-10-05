package replace

import (
	"context"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func probeDeps(t *testing.T, replies map[string]wire.CommandResult) (Deps, *fakeCommander) {
	t.Helper()
	cmd := &fakeCommander{replies: replies}
	return deps(t, cmd, fakeCabinet{}, &fakeOrigin{}, &noteLog{}), cmd
}

func TestAnalyzeConf_SkippedWhenUnsupported(t *testing.T) {
	d, _ := probeDeps(t, map[string]wire.CommandResult{
		"tunnel_analyze": {Status: "ok", Output: analyzeUnsupportedOut},
	})
	detail, skipped, err := d.AnalyzeConf(context.Background(), 1, []byte("[Interface]\n"))
	if err != nil || !skipped || detail == "" {
		t.Fatalf("detail=%q skipped=%v err=%v", detail, skipped, err)
	}
}

func TestAnalyzeConf_ErrorsRefuse(t *testing.T) {
	d, _ := probeDeps(t, map[string]wire.CommandResult{
		"tunnel_analyze": {Status: "ok", Output: analyzeErrorsOut},
	})
	_, skipped, err := d.AnalyzeConf(context.Background(), 1, []byte("[Interface]\n"))
	if skipped || err == nil || !strings.Contains(err.Error(), "H1 и H2 пересекаются") {
		t.Fatalf("skipped=%v err=%v", skipped, err)
	}
}

func TestAnalyzeConf_WarningsPass(t *testing.T) {
	d, _ := probeDeps(t, map[string]wire.CommandResult{
		"tunnel_analyze": {Status: "ok", Output: analyzeWarningsOut},
	})
	detail, skipped, err := d.AnalyzeConf(context.Background(), 1, []byte("[Interface]\n"))
	if err != nil || skipped || !strings.Contains(detail, "замечания") {
		t.Fatalf("detail=%q skipped=%v err=%v", detail, skipped, err)
	}
}

func TestVerifyExit_SameAsDirectFails(t *testing.T) {
	d, _ := probeDeps(t, map[string]wire.CommandResult{
		"check_via_tunnel": {Status: "ok", Output: "Exit IP: 203.0.113.5"},
		"check_direct":     {Status: "ok", Output: "Exit IP: 203.0.113.5"},
	})
	if _, err := d.VerifyExit(context.Background(), 1); err == nil {
		t.Fatal("тот же адрес, что и напрямую, должен быть ошибкой")
	}
}

func TestVerifyExit_DifferentPasses(t *testing.T) {
	d, _ := probeDeps(t, map[string]wire.CommandResult{
		"check_via_tunnel": {Status: "ok", Output: "Exit IP: 203.0.113.19"},
		"check_direct":     {Status: "ok", Output: "Exit IP: 203.0.113.7"},
	})
	verdict, err := d.VerifyExit(context.Background(), 1)
	if err != nil || !strings.Contains(verdict, "203.0.113.19") {
		t.Fatalf("verdict=%q err=%v", verdict, err)
	}
}

func TestWaitHandshake_ByID(t *testing.T) {
	d, _ := probeDeps(t, nil)
	// Снимок фейка: awg21 с обменом ключами, awg11 без него.
	if err := d.WaitHandshake(context.Background(), 1, "awg21", "amnezia_nl"); err != nil {
		t.Fatalf("awg21: %v", err)
	}
	if err := d.WaitHandshake(context.Background(), 1, "awg11", "old"); err == nil {
		t.Fatal("awg11 без обмена ключами должен дать ошибку")
	}
	if err := d.WaitHandshake(context.Background(), 1, "awg99", "нет такого"); err == nil {
		t.Fatal("туннеля нет в снимке -- ошибка")
	}
}
