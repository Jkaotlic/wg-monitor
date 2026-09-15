package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
)

type reviveWiringSender struct{}

func (reviveWiringSender) SendMessage(context.Context, int64, *int64, string, string, *int64) (int64, error) {
	return 1, nil
}

func TestNewReviveService(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	prov := provision.Deps{Store: provision.NewStore(), BaseCtx: context.Background()}

	keyB64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	good := filepath.Join(dir, "revive.key")
	if err := os.WriteFile(good, []byte(keyB64+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	short := filepath.Join(dir, "short.key")
	if err := os.WriteFile(short, []byte(base64.StdEncoding.EncodeToString([]byte("короткий"))), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, c := range []struct {
		name    string
		keyFile string
		enabled bool
	}{
		{"ключ есть", good, true},
		{"ключ не задан", "", false},
		{"файла нет", filepath.Join(dir, "missing.key"), false},
		{"ключ короткий", short, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			var logs bytes.Buffer
			cfg := &backend.Config{PublicBaseURL: "https://wgmon.example.com", Revive: backend.ReviveConfig{KeyFile: c.keyFile}}
			svc := newReviveService(context.Background(), cfg, database, prov, reviveWiringSender{}, slog.New(slog.NewTextHandler(&logs, nil)))
			if svc.Enabled() != c.enabled {
				t.Fatalf("Enabled=%v, want %v; логи: %s", svc.Enabled(), c.enabled, logs.String())
			}
			if !c.enabled && svc != nil {
				t.Fatal("выключенное оживление -- nil, чтобы Deps.Revive был nil")
			}
			if bytes.Contains(logs.Bytes(), []byte(keyB64)) {
				t.Fatal("ключ попал в журнал")
			}
		})
	}
}

// Minor #4 (мандатное ревью): carry #5 утверждал в комментарии, что Recover
// отрабатывает синхронно внутри newReviveService, ДО того как Deps.Revive
// вообще станет виден HTTP-обработчикам -- но код ни разу это не проверял.
// Здесь строка намерения имитирует "рестарт бэкенда": running без задания в
// памяти (задания и не может быть -- они живут только в оперативной памяти
// уже другого, старого процесса). Если Recover действительно вызывается,
// после newReviveService строка обязана вернуться в waiting.
func TestNewReviveService_RecoversLeftoverRunningIntents(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	prov := provision.Deps{Store: provision.NewStore(), BaseCtx: context.Background()}

	id, err := database.Users().Insert("bronya", "tok-bronya", "198.51.100.20", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := database.Revive().Put(db.ReviveIntent{
		RouterID: id, CreatedAt: now, ExpiresAt: now.Add(time.Hour), RequestedBy: 1,
	}, []byte("nonce-12byte"), []byte("cipher")); err != nil {
		t.Fatal(err)
	}
	if ok, err := database.Revive().MarkRunning(id, now); err != nil || !ok {
		t.Fatalf("mark running: %v %v", ok, err)
	}
	// Дальше -- "рестарт": задание в памяти прежнего процесса потеряно
	// безвозвратно, строка так и осталась running.

	keyB64 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))
	keyPath := filepath.Join(dir, "revive.key")
	if err := os.WriteFile(keyPath, []byte(keyB64+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &backend.Config{PublicBaseURL: "https://wgmon.example.com", Revive: backend.ReviveConfig{KeyFile: keyPath}}

	svc := newReviveService(context.Background(), cfg, database, prov, reviveWiringSender{}, slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil)))
	if svc == nil || !svc.Enabled() {
		t.Fatal("ключ годный -- сервис обязан включиться")
	}
	in, err := database.Revive().Get(id)
	if err != nil || in == nil || in.Status != db.ReviveWaiting {
		t.Fatalf("Recover обязан был вернуть running в waiting: %+v %v", in, err)
	}
}
