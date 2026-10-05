package dnswatch

import (
	"strings"
	"testing"
)

// Оператор правкой файла сменил endpoint, пока сторож держал роутер на
// запасных: запись помнит СТАРУЮ свою строку. Возврат обязан поставить строку
// нового endpoint, а не воскресить старую.
func TestWatch_EndpointChangedWhileOnFallbackReturnsToTheNewOwnLine(t *testing.T) {
	const newEndpoint = "https://dns2.example.com/other-secret"
	h := newHarness(t, newRouter(ownLine, unrelated))
	h.goFallback()

	// «Перезапуск» с новым endpoint в файле.
	h.endpoint = newEndpoint
	h.w = h.newWatcher()

	h.p.setOwn(nil)
	h.tick(5 * 60e9)
	h.tick(60e9)
	h.tick(60e9)

	lines := h.r.snapshotLines()
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "dns.example.com/secret-path") {
		t.Errorf("воскресла строка старого endpoint:\n%s\nlogs:\n%s", joined, h.logs)
	}
	if !strings.Contains(joined, "dns2.example.com/other-secret") {
		t.Errorf("строки нового endpoint нет на роутере:\n%s\nlogs:\n%s", joined, h.logs)
	}
}
