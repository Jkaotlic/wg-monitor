package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

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
