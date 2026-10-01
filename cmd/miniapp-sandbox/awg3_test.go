package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel"
)

func TestSandboxAwg3Seeds(t *testing.T) {
	dir := t.TempDir()
	p12 := filepath.Join(dir, "main.p12")
	sb, err := newSandboxAwg3(dir, p12)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()
	views, err := sb.svc.List()
	if err != nil || len(views) != 4 {
		t.Fatalf("панели: %d %v", len(views), err)
	}
	byID := map[string]awg3panel.View{}
	for _, v := range views {
		byID[v.ID] = v
	}
	if byID["old"].Lock != awg3panel.LockBadPassword || !byID["ban"].PausedUntil.After(time.Now()) {
		t.Fatalf("состояния: %+v %+v", byID["old"], byID["ban"])
	}
	if len(byID["old"].Issuers) != 1 || byID["old"].Issuers[0].TelegramUserID != 4242 {
		t.Fatalf("допуск к недоступной панели: %+v", byID["old"].Issuers)
	}
	// Решение оператора: nl2 засевается readonly в хранилище -- экран должен
	// увидеть «только для просмотра» с первого открытия, не дожидаясь 405 от
	// настоящей панели (у поддельной панели readonly и так нет маршрутов
	// мутации, поэтому 405 получить неоткуда).
	if !byID["nl2"].Readonly {
		t.Fatalf("nl2 должна быть readonly в хранилище: %+v", byID["nl2"])
	}
	ctx := context.Background()
	page, err := sb.svc.Peers(ctx, "main", "")
	if err != nil || len(page.Ifaces) != 2 || len(page.Peers) != 5 {
		t.Fatalf("main: %v ifaces=%d peers=%d", err, len(page.Ifaces), len(page.Peers))
	}
	if _, err := sb.svc.IssueDevice(ctx, "nl2", "awg1", "iphone"); awg3panel.KindOf(err) != awg3panel.KindReadonly {
		t.Fatalf("nl2 должна быть readonly: %v", err)
	}
	raw, err := os.ReadFile(p12)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := awg3panel.ParseP12(raw, "sandbox", time.Now()); err != nil {
		t.Fatalf(".p12 для формы: %v", err)
	}
	var hint struct {
		URL, User, Password string
		P12Password         string `json:"p12_password"`
	}
	b, _ := os.ReadFile(p12 + ".json")
	if json.Unmarshal(b, &hint) != nil || hint.URL == "" || hint.P12Password != "sandbox" {
		t.Fatalf("подсказка: %s", b)
	}
}
