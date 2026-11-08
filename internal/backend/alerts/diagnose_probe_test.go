package alerts

import (
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// workrouter 18.09: обмен ключами свежий, а проба awg-manager через туннель не
// проходит. Объяснение не должно молчать и не должно говорить «обмена нет».
func TestDiagnoseTunnel_FreshHandshakeProbeFailed(t *testing.T) {
	got := diagnoseTunnel(map[string]any{"handshake_age_sec": float64(44), "matrix_ok": false}, nil)
	if !strings.Contains(got, "проверка awg-manager") {
		t.Fatalf("нет объяснения про провал пробы: %q", got)
	}
	if strings.Contains(got, "Обмена ключами нет") || strings.Contains(got, "не было ни разу") {
		t.Fatalf("обмен ключами идёт -- говорить обратное нельзя: %q", got)
	}
}

// These checks protect what the operator can conclude from the evidence, not
// the exact wording of the notification.
func TestTunnelAlertSeparatesEvidenceFromDiagnosis(t *testing.T) {
	tests := []struct {
		name    string
		details map[string]any
		want    []string
		absent  []string
	}{
		{"missing handshake", map[string]any{}, []string{"Данные о последнем обмене ключами отсутствуют"}, []string{"ни разу", "провайдер может резать", "не отвечает", "сейчас туда не доходит"}},
		{"stale handshake", map[string]any{"handshake_age_sec": 900}, []string{"устарел", "простой"}, []string{"точно лежит", "провайдер режет", "не отвечает", "сейчас туда не доходит"}},
		{"legacy matrix failure", map[string]any{"handshake_age_sec": 44, "matrix_ok": false}, []string{"проверка awg-manager", "не подтверждён"}, []string{"трафик теряется", "блокировку", "сервер не работает", "не отвечает", "сейчас туда не доходит"}},
		{"probe target failed but tunnel reachable", map[string]any{"handshake_age_sec": 44, "matrix_ok": false, "failure_kind": "probe_target", "tunnel_confirmation": "reachable"}, []string{"Независимая", "отвечает", "контрольной цели"}, []string{"не отвечает", "не работает", "трафик теряется", "Нажмите «Починить»"}},
		{"independent confirmation unavailable", map[string]any{"handshake_age_sec": 44, "matrix_ok": false, "failure_kind": "confirmation_unavailable", "tunnel_confirmation": "unknown"}, []string{"недоступна", "не подтверждён"}, []string{"трафик теряется", "блокировку"}},
		{"independently confirmed data failure", map[string]any{"handshake_age_sec": 44, "matrix_ok": false, "failure_kind": "data_path", "tunnel_confirmation": "unreachable"}, []string{"Независимые HTTPS-проверки", "не получили ответа", "причину"}, []string{"блокировку", "провайдер режет"}},
		{"unconfirmed data failure", map[string]any{"failure_kind": "data_path", "tunnel_confirmation": "unknown"}, []string{"не подтверждён"}, []string{"Независимые HTTPS-проверки не получили ответа"}},
		{"stopped interface", map[string]any{"failure_kind": "interface", "tunnel_confirmation": "reachable"}, []string{"Интерфейс VPN-туннеля остановлен"}, []string{"ни разу"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatHard(HardArgs{CheckName: "tunnel_awg1", Check: wire.Check{Status: "fail", Details: tt.details}})
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("missing evidence %q in:\n%s", want, got)
				}
			}
			for _, absent := range tt.absent {
				if strings.Contains(got, absent) {
					t.Errorf("unsupported conclusion %q in:\n%s", absent, got)
				}
			}
		})
	}
}
