package main

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func TestSandboxPeopleOrderAndRoles(t *testing.T) {
	const viewer int64 = 4242
	d, err := db.Open(filepath.Join(t.TempDir(), "sandbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := seed(d, viewer); err != nil {
		t.Fatal(err)
	}
	people, err := sandboxPeople(d, viewer, viewer, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// Первыми -- ждущие доступа, писавшие недавно, свежие выше: Ольга (5 мин),
	// rook42 (40 мин), хозяин чужого парка без роутеров в этом seed (26 ч), Пётр (3 дня).
	var got []int64
	for _, p := range people[:4] {
		got = append(got, p.TelegramUserID)
	}
	if want := []int64{777001, 777003, viewer + 1000, 777002}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("порядок ждущих %v, want %v", got, want)
	}
	// Не виденный -- последним.
	if last := people[len(people)-1]; last.TelegramUserID != 777004 || last.LastSeenAt != nil {
		t.Fatalf("последний %+v", last)
	}
	for _, p := range people {
		if p.TelegramUserID == viewer {
			if !p.IsAdmin || len(p.Routers) == 0 {
				t.Fatalf("зритель: админ и с ролями, got %+v", p)
			}
			return
		}
	}
	t.Fatal("зрителя нет в справочнике")
}
