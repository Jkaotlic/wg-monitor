package revive

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Fix round 1, Important #2 (мандатное ревью): решение оператора «затем
// стирается» обязано выполняться, даже когда revive.key_file потерян или
// негоден и Service вообще не собран (nil, Deps.Revive == nil). Этот тест
// не создаёт ни ключа, ни Service -- только *db.DB, ровно то, что доступно
// бэкенду в выключенном состоянии.
func TestExpireOverdueSecrets_WorksWithoutServiceOrKey(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "revive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	overdueID, err := d.Users().Insert("bronya", "tok-bronya", "198.51.100.20", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	freshID, err := d.Users().Insert("gachi", "tok-gachi", "198.51.100.21", "awg1")
	if err != nil {
		t.Fatal(err)
	}

	t0 := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	now := t0.Add(48 * time.Hour)

	if err := d.Revive().Put(db.ReviveIntent{RouterID: overdueID, CreatedAt: t0, ExpiresAt: t0}, []byte("nonce-12byte"), []byte("cipher-o")); err != nil {
		t.Fatal(err)
	}
	if err := d.Revive().Put(db.ReviveIntent{RouterID: freshID, CreatedAt: t0, ExpiresAt: now.Add(24 * time.Hour)}, []byte("nonce-12byte"), []byte("cipher-f")); err != nil {
		t.Fatal(err)
	}

	n, err := ExpireOverdueSecrets(d, now)
	if err != nil {
		t.Fatalf("expire overdue secrets: %v", err)
	}
	if n != 1 {
		t.Fatalf("просроченных обязано быть 1, получили %d", n)
	}

	overdue, _ := d.Revive().Get(overdueID)
	if overdue.Status != db.ReviveExpired {
		t.Fatalf("просроченное: %+v", overdue)
	}
	if _, _, ok, _ := d.Revive().Secret(overdueID); ok {
		t.Fatal("секрет просроченного намерения обязан быть стёрт")
	}

	fresh, _ := d.Revive().Get(freshID)
	if fresh.Status != db.ReviveWaiting {
		t.Fatalf("непросроченное не должно было тронуться: %+v", fresh)
	}
	if _, _, ok, _ := d.Revive().Secret(freshID); !ok {
		t.Fatal("секрет непросроченного намерения обязан остаться")
	}

	// nil-безопасность: main.go может звать сторож даже до полной постройки
	// зависимостей (тесты, урезанные сборки) -- не паникует.
	if n, err := ExpireOverdueSecrets(nil, now); n != 0 || err != nil {
		t.Fatalf("nil DB: %d %v", n, err)
	}
}

// RunJanitor обязан подмести сразу на старте (не только через `every`) и
// затем продолжать по тикеру до отмены ctx -- иначе просроченный секрет,
// потерянный ключ которого починили только что перезапущенным процессом,
// ждал бы первого тика лишний час.
func TestRunJanitor_SweepsImmediatelyThenOnTickerUntilCancelled(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "revive.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })

	id, err := d.Users().Insert("bronya", "tok-bronya", "198.51.100.20", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	if err := d.Revive().Put(db.ReviveIntent{RouterID: id, CreatedAt: t0, ExpiresAt: t0}, []byte("nonce-12byte"), []byte("cipher")); err != nil {
		t.Fatal(err)
	}

	clock := &fakeClock{t: t0.Add(48 * time.Hour)} // уже просрочено к моменту первого прохода
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		RunJanitor(ctx, d, clock.Now, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if in, _ := d.Revive().Get(id); in != nil && in.Status == db.ReviveExpired {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("сторож не подмёл за разумное время")
		}
		time.Sleep(time.Millisecond)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunJanitor не завершился после отмены ctx")
	}
}

// nil DB и nil ctx.Done -- RunJanitor не паникует и не крутится вхолостую.
func TestRunJanitor_NilDBIsSafe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		RunJanitor(ctx, nil, time.Now, time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunJanitor(nil DB) обязан вернуться сразу")
	}
}
