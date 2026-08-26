package main

import "testing"

// Путь к docker-бинарю бэкенда обязан выводиться из ФАКТИЧЕСКОГО ssh-пользователя
// бэкенда, а не быть строковой константой. Раньше он был захардкожен, и при
// несовпадении имени учётки detectBackendSwapPlan молча уходил в systemd-ветку:
// ни ошибки, ни предупреждения — просто другой способ рестарта, чем ожидал оператор.
func TestBackendDockerBinaryFollowsBackendUser(t *testing.T) {
	cases := map[string]string{
		"deploy": "/home/deploy/wg-monitor/bin/wg-monitor-backend",
		"root":   "/root/wg-monitor/bin/wg-monitor-backend",
		"":       "/root/wg-monitor/bin/wg-monitor-backend",
		"  ":     "/root/wg-monitor/bin/wg-monitor-backend",
	}
	for user, want := range cases {
		if got := backendDockerBinary(user); got != want {
			t.Errorf("backendDockerBinary(%q) = %q, want %q", user, got, want)
		}
	}
}
