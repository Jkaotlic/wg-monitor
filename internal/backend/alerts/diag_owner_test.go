package alerts

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Настоящий отчёт awg-manager 2.18.2 (рабочий роутер, 10.09.2026): проверки
// лежат плоским списком tests[], у каждой name/description/status/detail, а у
// проверок VPN-туннеля ещё tunnelId/tunnelName. Прежний разбор ждал
// выдуманную форму tunnels → {id → {проверка}} и не находил ни одной
// проверки — кнопок по провалам под сводкой не было никогда.
func loadDiagFixture(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile("testdata/diag_report_awgm_2_18_2.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func diagJSON(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// failDiagTest помечает проверку name на VPN-туннеле tunnelID проваленной.
func failDiagTest(t *testing.T, m map[string]any, name, tunnelID, detail string) {
	t.Helper()
	for _, x := range m["tests"].([]any) {
		tt := x.(map[string]any)
		if tt["name"] == name && (tunnelID == "" || tt["tunnelId"] == tunnelID) {
			tt["status"] = "fail"
			tt["detail"] = detail
			return
		}
	}
	t.Fatalf("в образце нет проверки %s/%s", name, tunnelID)
}

func findDiagTest(tests []TestDetail, id string) *TestDetail {
	for i := range tests {
		if tests[i].ID == id {
			return &tests[i]
		}
	}
	return nil
}

func TestParseDiagTests_RealReport(t *testing.T) {
	tests := ParseDiagTests(diagJSON(t, loadDiagFixture(t)))
	if len(tests) == 0 {
		t.Fatal("из настоящего отчёта не разобралось ни одной проверки")
	}

	hs := findDiagTest(tests, "awg_handshake")
	if hs == nil {
		t.Fatal("нет проверки awg_handshake")
	}
	if !strings.Contains(hs.Label, "Обмен ключами") || strings.Contains(hs.Label, "Handshake") {
		t.Errorf("проверка названа словами движка: %q", hs.Label)
	}
	if hs.Status != "ok" {
		t.Errorf("awg_handshake: статус %q, ждали ok", hs.Status)
	}
	got := map[string]string{}
	for _, p := range hs.PerTunnel {
		got[p.TunnelLabel] = p.Status
	}
	// VPN-туннель подписан именем, которое дал владелец, а не id=awg10.
	if len(got) != 2 || got["macmini3.1top-nl2"] != "ok" || got["macmini3.1top-hipvps"] != "ok" {
		t.Errorf("VPN-туннели проверки: %+v", got)
	}

	wan := findDiagTest(tests, "wan_connectivity")
	if wan == nil || len(wan.PerTunnel) != 0 || wan.Status != "ok" {
		t.Errorf("общая проверка wan_connectivity разобрана не так: %+v", wan)
	}
	if km := findDiagTest(tests, "kernel_module"); km == nil || km.Status != "skip" {
		t.Errorf("пропущенная проверка kernel_module: %+v", km)
	}
}

// Провал на одном VPN-туннеле делает проваленной всю проверку: владелец ищет,
// что сломалось, а не среднее по туннелям.
func TestParseDiagTests_FailingTunnel(t *testing.T) {
	m := loadDiagFixture(t)
	failDiagTest(t, m, "tunnel_connectivity", "awg10", "timeout after 5s")
	tc := findDiagTest(ParseDiagTests(diagJSON(t, m)), "tunnel_connectivity")
	if tc == nil || tc.Status != "fail" {
		t.Fatalf("проверка с провалом на одном VPN-туннеле: %+v", tc)
	}
	for _, p := range tc.PerTunnel {
		switch p.TunnelLabel {
		case "macmini3.1top-nl2":
			if p.Status != "fail" || p.Reason != "timeout after 5s" {
				t.Errorf("проваленный VPN-туннель: %+v", p)
			}
		case "macmini3.1top-hipvps":
			if p.Status != "ok" {
				t.Errorf("исправный VPN-туннель: %+v", p)
			}
		default:
			t.Errorf("VPN-туннель подписан не именем: %q", p.TunnelLabel)
		}
	}
}

// Сводка — то, что владелец читает первым. Инженерия (версия панели, модуль
// ядра, интерфейсы WAN, логи) уходит на шаг глубже — в полный отчёт.
func TestParseDiagReport_OwnerSummaryAllGood(t *testing.T) {
	summary, bullets, fallback := ParseDiagReport(diagJSON(t, loadDiagFixture(t)))
	if fallback {
		t.Fatal("настоящий отчёт ушёл в сырой вид")
	}
	if !strings.Contains(summary, "всё в порядке") || !strings.Contains(summary, "27") {
		t.Errorf("сводка не говорит, что всё в порядке и сколько проверено: %q", summary)
	}
	text := summary + "\n" + strings.Join(bullets, "\n")
	for _, bad := range []string{"awg-manager", "2.18.2", "WAN", "Logs", "аптайм", "модуль AWG", "kernel", "память"} {
		if strings.Contains(text, bad) {
			t.Errorf("в сводке для владельца инженерия %q:\n%s", bad, text)
		}
	}
	if words := ownerLatin(text); len(words) > 0 {
		t.Errorf("в сводке латиница %v:\n%s", words, text)
	}
}

func TestParseDiagReport_OwnerSummaryNamesFailure(t *testing.T) {
	m := loadDiagFixture(t)
	failDiagTest(t, m, "tunnel_connectivity", "awg10", "timeout after 5s")
	summary, bullets, _ := ParseDiagReport(diagJSON(t, m))
	if !strings.Contains(summary, "нашлись проблемы") {
		t.Errorf("сводка молчит о провале: %q", summary)
	}
	joined := strings.Join(bullets, "\n")
	if !strings.Contains(joined, "Интернет через VPN-туннель") || !strings.Contains(joined, "VPN-туннель «macmini3.1top-nl2»") {
		t.Errorf("сводка не называет, что и где сломалось:\n%s", joined)
	}
	// Идентификатора владелец не видел нигде, а сырая деталь — на странице
	// проверки, на шаг глубже.
	if strings.Contains(joined, "awg10") || strings.Contains(joined, "timeout after 5s") {
		t.Errorf("в сводке id или сырая деталь:\n%s", joined)
	}
	assertSaysVPNTunnel(t, "сводка диагностики", summary+"\n"+joined)
}
