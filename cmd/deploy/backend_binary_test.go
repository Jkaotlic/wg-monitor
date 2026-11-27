package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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

func TestBackendSwapQuotesPathsAndRollsBackAfterSwapFailure(t *testing.T) {
	local := filepath.Join(t.TempDir(), "backend")
	if err := os.WriteFile(local, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := newFakeRemote()
	fake.reply = func(cmd string) (string, string, int, error) {
		switch {
		case strings.HasPrefix(cmd, "sha256sum "):
			return hashHex([]byte("new")) + "\n", "", 0, nil
		case strings.Contains(cmd, "chmod 755") && strings.Contains(cmd, "mv "):
			return "", "chmod failed", 1, nil
		case strings.Contains(cmd, "test -f "):
			return "", "", 0, nil
		case strings.Contains(cmd, "start backend"):
			return "", "", 1, nil
		default:
			return "", "", 0, nil
		}
	}
	plan := backendSwapPlan{
		Name:           "systemd:test",
		RemotePath:     "/opt/wg monitor/backend",
		StopCmd:        "stop backend",
		StartCmd:       "start backend",
		StopBeforeSwap: true,
	}
	err := stepUploadBackendAndRestart(fake, local, plan)
	if err == nil {
		t.Fatal("expected failed swap")
	}
	commands := strings.Join(fake.calls, "\n")
	if !strings.Contains(commands, `'/opt/wg monitor/backend.new'`) {
		t.Fatalf("path not quoted in commands:\n%s", commands)
	}
	rollback := fake.indexOfCall("mv '/opt/wg monitor/backend.bak' '/opt/wg monitor/backend'")
	if rollback < 0 {
		t.Fatalf("rollback not attempted after partial swap:\n%s", commands)
	}
}
