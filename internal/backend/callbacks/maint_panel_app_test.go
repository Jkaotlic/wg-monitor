package callbacks

import (
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Кнопка web_app в Telegram работает только в личном чате: в группе API
// отвергает ВСЁ сообщение, и панель обслуживания не открылась бы вовсе.
// Поэтому адрес кнопки строится только для лички и только когда адрес панели
// сохранён и годен.
func TestMaintPanelAppURL(t *testing.T) {
	good := "https://panel.example.com"
	bad := "javascript:alert(1)"
	user := &db.User{ID: 42, AWGMURL: &good}
	const base = "https://wgm.example.com"

	if got := maintPanelAppURL(base, user, 123456); got != "https://wgm.example.com/miniapp/?router=42&open=settings" {
		t.Errorf("личка: %q", got)
	}
	if got := maintPanelAppURL(base, user, -1001234567890); got != "" {
		t.Errorf("группа: %q, хотим пусто", got)
	}
	if got := maintPanelAppURL(base, &db.User{ID: 42}, 123456); got != "" {
		t.Errorf("без адреса панели: %q", got)
	}
	if got := maintPanelAppURL(base, &db.User{ID: 42, AWGMURL: &bad}, 123456); got != "" {
		t.Errorf("негодный адрес панели: %q", got)
	}
	if got := maintPanelAppURL("", user, 123456); got != "" {
		t.Errorf("без адреса приложения: %q", got)
	}
}

// Уведомитель панели обслуживания получает публичный адрес из конфига бота:
// без него кнопка «Панель роутера» пропадала бы после первого же обновления
// панели, хотя при открытии была.
func TestNewMaintNotifierCarriesMiniAppBase(t *testing.T) {
	d, _ := newTestDB(t)
	r := NewRouter(d, &fakeRouterTGFull{}, Config{ChatID: -100, AdminUserID: 12345, PublicBaseURL: "https://wgm.example.com"})
	if got := r.NewMaintNotifier(nil, nil).MiniAppBaseURL; got != "https://wgm.example.com" {
		t.Errorf("MiniAppBaseURL = %q", got)
	}
}
