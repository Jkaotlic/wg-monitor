package callbacks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/anex/wg-monitor/internal/backend/selfhostedamnezia"
	"github.com/anex/wg-monitor/internal/backend/tg"
)

func TestSelfHostedAmneziaIssueConfirmKeepsTokenWhenQueueFails(t *testing.T) {
	d, uid := newTestDB(t)
	f := &fakeRouterTG{}
	sink := &fakeEnqueuer{err: errors.New("queue offline")}
	installFakeDocker(t)
	storePath := filepath.Join(t.TempDir(), "selfhosted.json")
	store := selfhostedamnezia.Store{Version: 1}
	if err := store.Upsert(selfhostedamnezia.Instance{ID: "home", Label: "Home VPS", Enabled: true, EndpointHost: "vpn.example.com", EndpointPort: 47567}); err != nil {
		t.Fatal(err)
	}
	if err := selfhostedamnezia.SaveStore(storePath, store); err != nil {
		t.Fatal(err)
	}
	r := NewRouterWithSink(d, f, sink, Config{
		ChatID:      -100,
		AdminUserID: 12345,
		SelfHostedAmnezia: selfhostedamnezia.Config{
			StorePath: storePath,
		},
	})

	r.HandleCallback(context.Background(), &tg.CallbackQuery{
		ID:      "selfhosted-issue-ask",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}},
		Data:    "amz_selfhosted_issue:" + itoa(uid) + ":_panel_:home",
	})
	confirmData := firstCallbackWithPrefix(t, f.editMarkups[0], "amz_selfhosted_confirm:"+itoa(uid)+":_panel_:home:")
	confirmArgs, err := Parse(confirmData)
	if err != nil {
		t.Fatal(err)
	}

	r.HandleCallback(context.Background(), &tg.CallbackQuery{
		ID:      "selfhosted-issue-confirm-queue-fails",
		From:    tg.User{ID: 12345},
		Message: tg.Message{MessageID: 7, Chat: tg.Chat{ID: -100}},
		Data:    confirmData,
	})

	if len(sink.calls) != 0 {
		t.Fatalf("failed queue should not record calls: %+v", sink.calls)
	}
	if _, ok := r.pendingConfirms.consume(uid, 12345, nil, "amz_selfhosted_confirm", "home", confirmArgs.ConfirmToken); !ok {
		t.Fatalf("failed enqueue after issuing config should keep token retryable")
	}
}

func installFakeDocker(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	serverConf := filepath.Join(binDir, "server.conf")
	clientsTable := filepath.Join(binDir, "clients.json")
	serverPub := filepath.Join(binDir, "server.pub")
	psk := filepath.Join(binDir, "server.psk")
	if err := os.WriteFile(serverConf, []byte("[Interface]\nAddress = 10.8.1.0/24\n\n[Peer]\nPublicKey = old\nPresharedKey = psk\nAllowedIPs = 10.8.1.12/32\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clientsTable, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(serverPub, []byte("server-public\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(psk, []byte("server-psk\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		body := "@echo off\r\n" +
			"if \"%4\"==\"cat\" (\r\n" +
			"  if \"%5\"==\"/opt/amnezia/awg/awg0.conf\" type \"" + serverConf + "\"\r\n" +
			"  if \"%5\"==\"/opt/amnezia/awg/clientsTable\" type \"" + clientsTable + "\"\r\n" +
			"  if \"%5\"==\"/opt/amnezia/awg/wireguard_server_public_key.key\" type \"" + serverPub + "\"\r\n" +
			"  if \"%5\"==\"/opt/amnezia/awg/wireguard_psk.key\" type \"" + psk + "\"\r\n" +
			")\r\n" +
			"exit /b 0\r\n"
		if err := os.WriteFile(filepath.Join(binDir, "docker.cmd"), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	} else {
		body := "#!/bin/sh\n" +
			"if [ \"$4\" = \"cat\" ]; then\n" +
			"  case \"$5\" in\n" +
			"    /opt/amnezia/awg/awg0.conf) cat " + shellQuoteForTest(serverConf) + " ;;\n" +
			"    /opt/amnezia/awg/clientsTable) cat " + shellQuoteForTest(clientsTable) + " ;;\n" +
			"    /opt/amnezia/awg/wireguard_server_public_key.key) cat " + shellQuoteForTest(serverPub) + " ;;\n" +
			"    /opt/amnezia/awg/wireguard_psk.key) cat " + shellQuoteForTest(psk) + " ;;\n" +
			"  esac\n" +
			"fi\n"
		if err := os.WriteFile(filepath.Join(binDir, "docker"), []byte(body), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func shellQuoteForTest(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
