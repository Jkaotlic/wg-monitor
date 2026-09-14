package callbacks

import (
	"context"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Кнопка web_app в Telegram работает только в личном чате: в группе API
// отвергает ВСЁ сообщение, и панель обслуживания не открылась бы вовсе.
// Поэтому адрес кнопки строится только для лички и только когда адрес панели
// сохранён и годен.
func TestMaintPanelAppURL(t *testing.T) {
	good := "https://panel.example.com"
	bad := "javascript:alert(1)"
	const (
		base    = "https://wgm.example.com"
		ownerTG = int64(123456)
		adminTG = int64(999)
		opTG    = int64(555)
	)
	owner := ownerTG
	user := &db.User{ID: 42, AWGMURL: &good, TelegramUserID: &owner}
	want := "https://wgm.example.com/miniapp/?router=42&open=settings"

	// В личке chat_id -- это Telegram ID того, кто смотрит панель.
	if got := maintPanelAppURL(base, user, ownerTG, adminTG); got != want {
		t.Errorf("личка владельца: %q", got)
	}
	if got := maintPanelAppURL(base, user, adminTG, adminTG); got != want {
		t.Errorf("личка админа: %q", got)
	}
	// Оператор роутера в панель обслуживания попадает, а секции панели в
	// настройках у него нет (решение оператора № 9): кнопка была бы тупиком.
	if got := maintPanelAppURL(base, user, opTG, adminTG); got != "" {
		t.Errorf("личка оператора: %q, хотим пусто", got)
	}
	if got := maintPanelAppURL(base, user, -1001234567890, adminTG); got != "" {
		t.Errorf("группа: %q, хотим пусто", got)
	}
	if got := maintPanelAppURL(base, &db.User{ID: 42, TelegramUserID: &owner}, ownerTG, adminTG); got != "" {
		t.Errorf("без адреса панели: %q", got)
	}
	if got := maintPanelAppURL(base, &db.User{ID: 42, AWGMURL: &bad, TelegramUserID: &owner}, ownerTG, adminTG); got != "" {
		t.Errorf("негодный адрес панели: %q", got)
	}
	if got := maintPanelAppURL("", user, ownerTG, adminTG); got != "" {
		t.Errorf("без адреса приложения: %q", got)
	}
}

// Уведомитель панели обслуживания получает публичный адрес из конфига бота:
// без него кнопка «Панель роутера» пропадала бы после первого же обновления
// панели, хотя при открытии была.
func TestNewMaintNotifierCarriesMiniAppBase(t *testing.T) {
	d, _ := newTestDB(t)
	r := NewRouter(d, &fakeRouterTGFull{}, Config{ChatID: -100, AdminUserID: 12345, PublicBaseURL: "https://wgm.example.com"})
	n := r.NewMaintNotifier(nil, nil)
	if n.MiniAppBaseURL != "https://wgm.example.com" || n.AdminUserID != 12345 {
		t.Errorf("MiniAppBaseURL = %q, AdminUserID = %d", n.MiniAppBaseURL, n.AdminUserID)
	}
}

// Все пути отрисовки панели обслуживания собирают аргументы одной функцией, и
// кнопка панели входит в них -- её нельзя потерять ни на одном пути.
func TestBuildMaintPanelArgsCarriesPanelButton(t *testing.T) {
	good := "https://panel.example.com"
	owner := int64(123456)
	user := &db.User{ID: 42, Nickname: "x", AWGMURL: &good, TelegramUserID: &owner}
	args := buildMaintPanelArgs(context.Background(), user, wire.VersionAudit{}, nil, newCooldownStore(), "https://wgm.example.com", owner, 999)
	if args.PanelAppURL != "https://wgm.example.com/miniapp/?router=42&open=settings" {
		t.Errorf("PanelAppURL = %q", args.PanelAppURL)
	}
}
