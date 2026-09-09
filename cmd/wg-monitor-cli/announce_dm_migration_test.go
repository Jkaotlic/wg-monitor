package main

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

type fakeAnnouncer struct {
	mu    sync.Mutex
	chats []int64
	texts []string
}

func (f *fakeAnnouncer) SendMessage(_ context.Context, chatID int64, _ *int64, text, _ string, _ *int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chats = append(f.chats, chatID)
	f.texts = append(f.texts, text)
	return 1, nil
}

func (f *fakeAnnouncer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.chats)
}

func announceTestDB(t *testing.T) (*db.DB, int64) {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	uid, err := d.Users().Insert("router-a", "tok-a", "1.1.1.1", "awg0")
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Users().UpdateTelegramTopic(uid, -100500, 42); err != nil {
		t.Fatal(err)
	}
	return d, uid
}

// Объявление публикуется один раз. Повторный запуск -- обычное дело: команду
// гоняют, пока не убедятся, что все темы охвачены, и вторая копия в каждой
// теме была бы платой за эту осторожность.
func TestAnnounceDMMigrationIsIdempotent(t *testing.T) {
	d, _ := announceTestDB(t)
	f := &fakeAnnouncer{}

	if err := announceDMMigration(context.Background(), d, f, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.count() != 1 {
		t.Fatalf("отправлено %d, ждали одно объявление", f.count())
	}
	if !strings.Contains(f.texts[0], "личк") {
		t.Fatalf("объявление обязано объяснять переезд: %q", f.texts[0])
	}

	if err := announceDMMigration(context.Background(), d, f, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.count() != 1 {
		t.Fatalf("отправлено %d, повторный запуск не должен публиковать снова", f.count())
	}
}

// Пробный прогон ничего не шлёт и ничего не помечает: иначе «посмотреть, кому
// уйдёт» тратило бы единственную попытку.
func TestAnnounceDMMigrationDryRunSendsNothing(t *testing.T) {
	d, _ := announceTestDB(t)
	f := &fakeAnnouncer{}

	if err := announceDMMigration(context.Background(), d, f, true, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.count() != 0 {
		t.Fatalf("пробный прогон отправил %d сообщений", f.count())
	}

	// После пробного прогона настоящий обязан сработать.
	if err := announceDMMigration(context.Background(), d, f, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.count() != 1 {
		t.Fatalf("после пробного прогона ждали одно объявление, отправлено %d", f.count())
	}
}

// У роутера без темы объявлению негде появиться -- он просто пропускается.
func TestAnnounceDMMigrationSkipsRoutersWithoutTopic(t *testing.T) {
	d, _ := announceTestDB(t)
	if _, err := d.Users().Insert("router-no-topic", "tok-b", "2.2.2.2", "awg0"); err != nil {
		t.Fatal(err)
	}
	f := &fakeAnnouncer{}

	if err := announceDMMigration(context.Background(), d, f, false, io.Discard); err != nil {
		t.Fatal(err)
	}
	if f.count() != 1 {
		t.Fatalf("отправлено %d, ждали одно -- второму роутеру писать некуда", f.count())
	}
}
