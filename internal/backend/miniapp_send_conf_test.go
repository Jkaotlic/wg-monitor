package backend

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

const sendConfPath = "/v1/miniapp/routers/{id}/vpn/send-conf"

func TestMiniappSendConfGoesToPresserDM(t *testing.T) {
	env := newCabinetEnv(t)
	for _, who := range []int64{cabStranger, cabOperator} {
		rec := env.do(t, who, http.MethodPost, sendConfPath, `{"provider":"amnezia","option_id":"nl"}`)
		if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusNotFound || code != "not_found" {
			t.Fatalf("от %d: %d %s", who, rec.Code, rec.Body.String())
		}
	}
	for _, who := range []int64{cabOwner, cabAdmin} {
		rec := env.do(t, who, http.MethodPost, sendConfPath, `{"provider":"amnezia","option_id":"nl"}`)
		if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"sent_to":"dm"`) {
			t.Fatalf("от %d: %d %s", who, rec.Code, rec.Body.String())
		}
	}
	if len(env.docs.sent) != 2 || env.docs.sent[0].chatID != cabOwner || env.docs.sent[1].chatID != cabAdmin {
		t.Fatalf("документ не в личку нажавшему: %+v", env.docs.sent)
	}
	doc := env.docs.sent[0]
	if doc.filename != "amnezia_nl.conf" || !strings.Contains(string(doc.data), "CONF-SECRET-MUST-NOT-LEAK") || !strings.Contains(doc.caption, "приватный ключ") {
		t.Fatalf("документ: %+v", doc)
	}
	if len(env.sink.enqueued) != 0 {
		t.Fatal("файл в личку ничего не ставит в очередь роутера")
	}
}

func TestMiniappSendConfKeepsConfigOutOfResponseAndLog(t *testing.T) {
	env := newCabinetEnv(t)
	rec := env.do(t, cabOwner, http.MethodPost, sendConfPath, `{"provider":"hidemyname","option_id":"a1b2c3d4e5f6"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	for _, where := range []string{rec.Body.String(), env.logs.String()} {
		if strings.Contains(where, "CONF-SECRET") || strings.Contains(where, "PrivateKey") {
			t.Fatalf("конфиг утёк: %s", where)
		}
	}
}

func TestMiniappSendConfDMUnreachableAndFailures(t *testing.T) {
	env := newCabinetEnv(t)
	env.docs.err = &tg.APIError{Method: "sendDocument", Code: 403, Description: "Forbidden: bot can't initiate conversation with a user"}
	rec := env.do(t, cabOwner, http.MethodPost, sendConfPath, `{"provider":"amnezia","option_id":"nl"}`)
	if code, msg, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusConflict || code != "dm_unreachable" || !strings.Contains(msg, "/start") {
		t.Fatalf("личка закрыта: %d %s", rec.Code, rec.Body.String())
	}
	env.docs.err = errors.New("tg sendDocument: transport error")
	rec = env.do(t, cabOwner, http.MethodPost, sendConfPath, `{"provider":"amnezia","option_id":"nl"}`)
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusBadGateway || code != "dm_failed" {
		t.Fatalf("сбой Telegram: %d %s", rec.Code, rec.Body.String())
	}
	env.docs.err = nil

	cases := []struct {
		name, body, code string
		status           int
		cabErr           error
	}{
		{"занятый слот", `{"provider":"amnezia","option_id":"fi"}`, "slot_busy", http.StatusConflict, ErrVPNSlotBusy},
		{"кабинет не ответил", `{"provider":"amnezia","option_id":"fi"}`, "cabinet_failed", http.StatusBadGateway, errors.New("amnezia login: HTTP 502")},
		{"неизвестный провайдер", `{"provider":"someone","option_id":"x"}`, "unknown_provider", http.StatusBadRequest, nil},
		{"нет выбора", `{"provider":"amnezia"}`, "missing_option", http.StatusBadRequest, nil},
		{"битый json", `{"provider":`, "bad_json", http.StatusBadRequest, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env.cab.err = tc.cabErr
			rec := env.do(t, cabOwner, http.MethodPost, sendConfPath, tc.body)
			if code, _, _ := cabinetErrorBody(t, rec); rec.Code != tc.status || code != tc.code {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
		})
	}

	noDM := newCabinetEnv(t, func(d *Deps) { d.MiniappDocs = nil })
	rec = noDM.do(t, cabOwner, http.MethodPost, sendConfPath, `{"provider":"amnezia","option_id":"nl"}`)
	if code, _, _ := cabinetErrorBody(t, rec); rec.Code != http.StatusServiceUnavailable || code != "dm_not_configured" {
		t.Fatalf("без Telegram: %d %s", rec.Code, rec.Body.String())
	}
}
