package tg

import "testing"

// Кнопка «Открыть в приложении» под панелями бота ведёт прямо на вкладку:
// VPN-туннели и маршруты переехали туда (цикл 4).
func TestMiniAppRouterTabURL(t *testing.T) {
	if got := MiniAppRouterTabURL("https://wgmon.example.com/", 42, "tunnels", ""); got != "https://wgmon.example.com/miniapp/?router=42&tab=tunnels" {
		t.Fatalf("tunnels: %q", got)
	}
	if got := MiniAppRouterTabURL("https://wgmon.example.com", 42, "router", "routes"); got != "https://wgmon.example.com/miniapp/?router=42&tab=router&open=routes" {
		t.Fatalf("routes: %q", got)
	}
	if MiniAppRouterTabURL("http://wgmon.example.com", 42, "tunnels", "") != "" || MiniAppRouterTabURL("", 42, "tunnels", "") != "" {
		t.Fatal("не https -- пусто")
	}
}

// web_app-кнопку Telegram принимает только в личке: у лички id
// положительный (он же id человека), у групп -- отрицательный.
func TestIsPrivateChatAndOpenInAppButton(t *testing.T) {
	if !IsPrivateChat(777) || IsPrivateChat(-100500) || IsPrivateChat(0) {
		t.Fatal("IsPrivateChat")
	}
	b := OpenInAppButton("https://wgmon.example.com/miniapp/?router=42&tab=tunnels")
	if b.Text != "📱 Открыть в приложении" || b.WebApp == nil || b.WebApp.URL != "https://wgmon.example.com/miniapp/?router=42&tab=tunnels" || b.CallbackData != "" {
		t.Fatalf("кнопка: %+v", b)
	}
}
