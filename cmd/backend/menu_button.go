package main

import "strings"

// MiniAppMenuButtonText is what the private-chat menu button says once the
// mini app is reachable. Short on purpose: TG renders it inside the button
// next to the input bar, where a long caption is truncated.
const MiniAppMenuButtonText = "Открыть приложение"

// miniAppMenuURL turns the configured public base URL into the mini app's
// entry point, or returns "" when there is nothing to point a button at.
//
// The path is built here rather than at the call site so the whole backend
// spells it one way -- the same `/miniapp/` that the alert web_app button
// uses (alerts/dispatcher.go). The scheme check is not pedantry: TG opens a
// web_app only over HTTPS, and an http button would sit in the menu doing
// nothing, which is worse than leaving the slash-command list in place.
func miniAppMenuURL(publicBaseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
	if base == "" || !strings.HasPrefix(base, "https://") {
		return ""
	}
	return base + "/miniapp/"
}
