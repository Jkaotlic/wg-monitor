package alerts

import (
	"strings"
	"testing"
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
