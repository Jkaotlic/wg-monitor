package revive

import (
	"strings"
	"testing"
	"time"
)

func TestNoticeTexts_SpeakToOwner(t *testing.T) {
	texts := []string{
		noticeRevived("bronya", "v0.34.0-rc2"),
		noticeRevived("bronya", ""),
		noticeAliveItself("bronya"),
		noticeAuthFailed("bronya"),
		noticeFailed("bronya", reasonNoSecret),
		noticeGaveUp("bronya", 5, "роутер не смог скачать агент"),
		noticeExpired("bronya", time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)),
	}
	for _, text := range texts {
		assertOwnerText(t, text)
		if !strings.Contains(text, "«bronya»") {
			t.Fatalf("имя роутера в «ёлочках» обязательно: %q", text)
		}
		if !strings.Contains(text, "стёрт") {
			t.Fatalf("человек должен знать, что пароль стёрт: %q", text)
		}
	}
	for _, reason := range []string{reasonRevived, reasonAliveItself, reasonAuthFailed, reasonExpired, reasonNoSecret,
		reasonSecretUnreadable, reasonNoAWGMURL, reasonLaunchFailed, reasonJobLost, reasonUnknownFailure} {
		assertOwnerText(t, reason)
	}
	if !strings.Contains(noticeAuthFailed("bronya"), "пароль не подошёл — поставьте оживление заново") {
		t.Fatal("формулировка из спеки")
	}
	if !strings.Contains(noticeExpired("bronya", time.Date(2026, 10, 15, 9, 0, 0, 0, time.UTC)), "15.10.2026") {
		t.Fatal("дата срока")
	}
}
