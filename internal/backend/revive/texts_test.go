package revive

import (
	"strings"
	"testing"
	"time"
)

func TestNoticeTexts_SpeakToOwner(t *testing.T) {
	d := time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)
	for _, stored := range []bool{false, true} {
		texts := []string{
			noticeRevived("bronya", "v0.34.0-rc2", stored),
			noticeRevived("bronya", "", stored),
			noticeAliveItself("bronya", stored),
			noticeFailed("bronya", reasonNoStoredEntry, stored),
			noticeGaveUp("bronya", 5, "роутер не смог скачать агент", stored),
			noticeExpired("bronya", d, stored),
		}
		for _, text := range texts {
			assertOwnerText(t, text)
			if !strings.Contains(text, "«bronya»") {
				t.Fatalf("имя роутера в «ёлочках» обязательно: %q", text)
			}
			// REV-01: «стёрт» -- только когда пароля на сервере нет.
			if stored && (strings.Contains(text, "стёрт") || !strings.Contains(text, "хранится")) {
				t.Fatalf("пароль сохранён -- текст о хранении, без «стёрт»: %q", text)
			}
			if !stored && !strings.Contains(text, "стёрт") {
				t.Fatalf("человек должен знать, что пароль стёрт: %q", text)
			}
		}
	}
	assertOwnerText(t, noticeAuthFailed("bronya"))
	if !strings.Contains(noticeAuthFailed("bronya"), "стёрт") {
		t.Fatal("отказ входа: введённый пароль стёрт")
	}
	for _, reason := range []string{reasonRevived, reasonAliveItself, reasonAuthFailed, reasonExpired, reasonNoStoredEntry,
		reasonEntryUnreadable, reasonNoAWGMURL, reasonLaunchFailed, reasonJobLost, reasonUnknownFailure} {
		assertOwnerText(t, reason)
	}
	if !strings.Contains(noticeAuthFailed("bronya"), "пароль не подошёл — поставьте оживление заново") {
		t.Fatal("формулировка из спеки")
	}
	if !strings.Contains(noticeExpired("bronya", d, false), "15.10.2026") {
		t.Fatal("дата срока")
	}
}
