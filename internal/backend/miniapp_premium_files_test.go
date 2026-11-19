package backend

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/premiumfiles"
)

type fakePremiumFiles struct {
	fakeCabinetKeys
	opened, downloaded int
	conf               []byte
}

func (f *fakePremiumFiles) OpenPremiumFiles(context.Context, int64, string, string) (premiumfiles.Account, string, error) {
	f.opened++
	return premiumfiles.Account{Countries: []premiumfiles.Country{{Code: "nl", Name: "Нидерланды"}}}, "key1", nil
}
func (f *fakePremiumFiles) DownloadPremiumFile(context.Context, int64, string, string, string) ([]byte, error) {
	f.downloaded++
	if f.conf != nil {
		return f.conf, nil
	}
	return []byte("[Interface]\nPrivateKey = synthetic\n[Peer]\n"), nil
}

func TestPremiumFileOwnerGateAndExplicitConfirmation(t *testing.T) {
	files := &fakePremiumFiles{}
	env := newCabinetEnv(t, func(d *Deps) { d.VPNCabinetKeys = files })
	accountPath := "/v1/miniapp/routers/{id}/cabinets/amnezia/files/account"
	downloadPath := "/v1/miniapp/routers/{id}/cabinets/amnezia/files/download"
	for _, user := range []int64{cabOperator, cabStranger} {
		rec := env.do(t, user, http.MethodPost, accountPath, `{"vpn_key":"vpn://synthetic"}`)
		if rec.Code != http.StatusNotFound || files.opened != 0 {
			t.Fatalf("unauthorized request: %d", rec.Code)
		}
	}
	rec := env.do(t, cabOwner, http.MethodPost, accountPath, `{"vpn_key":"vpn://synthetic"}`)
	if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), "vpn://") {
		t.Fatalf("account response %d %s", rec.Code, rec.Body.String())
	}
	rec = env.do(t, cabOwner, http.MethodPost, downloadPath, `{"key_id":"key1","country":"nl","declared_country":"ru"}`)
	if rec.Code != 400 || files.downloaded != 0 {
		t.Fatalf("unconfirmed generation: %d", rec.Code)
	}
	rec = env.do(t, cabOwner, http.MethodPost, downloadPath, `{"key_id":"key1","country":"nl","declared_country":"ru","confirm":true}`)
	if rec.Code != 200 || files.downloaded != 1 || !strings.Contains(rec.Body.String(), "amnezia_nl.conf") {
		t.Fatalf("download %d %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("config may be cached")
	}
	if strings.Contains(env.logs.String(), "synthetic") {
		t.Fatal("secret leaked")
	}
}

func TestPremiumDownloadFailureExplainsUncertainResult(t *testing.T) {
	rec := httptest.NewRecorder()
	premiumFileFailure(rec, errors.New("private upstream response"), true)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "premium_file_uncertain") || !strings.Contains(rec.Body.String(), "проверьте страну перед повтором") {
		t.Fatalf("download error: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "private upstream") {
		t.Fatal("provider response leaked")
	}
}

func TestPremiumExplicitInstallSanitizesOnlyRouterCopy(t *testing.T) {
	original := "# VPN Key: vpn://synthetic\r\n[Interface]\r\nPrivateKey = synthetic\r\nS1 = 12\r\n[Peer]\r\nPublicKey = synthetic\r\nEndpoint = vpn.example.com:443\r\n"
	files := &fakePremiumFiles{conf: []byte(original)}
	env := newCabinetEnv(t, func(d *Deps) { d.VPNCabinetKeys = files })
	path := "/v1/miniapp/routers/{id}/cabinets/amnezia/files/install"
	body := `{"key_id":"key1","country":"nl","declared_country":"ru","confirm":true}`
	for _, user := range []int64{cabOperator, cabStranger} {
		rec := env.do(t, user, http.MethodPost, path, body)
		if rec.Code != 404 || files.downloaded != 0 {
			t.Fatalf("gate: %d", rec.Code)
		}
	}
	rec := env.do(t, cabOwner, http.MethodPost, path, body)
	if rec.Code != 200 || len(env.sink.enqueued) != 1 {
		t.Fatalf("install: %d %s", rec.Code, rec.Body.String())
	}
	cmd := env.sink.enqueued[0]
	conf, err := base64.StdEncoding.DecodeString(cmd.Args["conf"].(string))
	if err != nil || string(conf) != strings.TrimPrefix(original, "# VPN Key: vpn://synthetic\r\n") {
		t.Fatal("router copy changed protocol bytes")
	}
	if cmd.Action != "tunnel_import" || cmd.Args["backend"] != "nativewg" || cmd.Args["replace"] != false {
		t.Fatalf("wrong command")
	}
	if string(files.conf) != original || strings.Contains(rec.Body.String(), "synthetic") {
		t.Fatal("original changed or leaked")
	}
	rec = env.do(t, cabOwner, http.MethodPost, "/v1/miniapp/routers/{id}/cabinets/amnezia/files/download", body)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "vpn://synthetic") {
		t.Fatal("download lost original marker")
	}
}

func TestPremiumInstallRejectsBeforeProviderWithoutSinkOrConfirmation(t *testing.T) {
	for _, missingSink := range []bool{false, true} {
		files := &fakePremiumFiles{}
		env := newCabinetEnv(t, func(d *Deps) {
			d.VPNCabinetKeys = files
			if missingSink {
				d.CommandSink = nil
			}
		})
		body := `{"key_id":"key1","country":"nl","declared_country":"ru","confirm":false}`
		if missingSink {
			body = strings.Replace(body, "false", "true", 1)
		}
		rec := env.do(t, cabOwner, http.MethodPost, "/v1/miniapp/routers/{id}/cabinets/amnezia/files/install", body)
		if rec.Code < 400 || files.downloaded != 0 {
			t.Fatalf("unsafe generation %d", rec.Code)
		}
	}
}

func TestPremiumRouterCopyRejectsUnexpectedMarker(t *testing.T) {
	for _, conf := range []string{"# other vpn://synthetic\n[Interface]\n", "[Interface]\nPrivateKey = vpn://synthetic\n"} {
		files := &fakePremiumFiles{conf: []byte(conf)}
		env := newCabinetEnv(t, func(d *Deps) { d.VPNCabinetKeys = files })
		rec := env.do(t, cabOwner, http.MethodPost, "/v1/miniapp/routers/{id}/cabinets/amnezia/files/install", `{"key_id":"key1","country":"nl","declared_country":"ag","confirm":true}`)
		if rec.Code != 400 || len(env.sink.enqueued) != 0 {
			t.Fatalf("unexpected marker accepted: %d", rec.Code)
		}
	}
}

func TestPremiumInstallDoesNotReissueKnownInstalledCountry(t *testing.T) {
	files := &fakePremiumFiles{}
	env := newCabinetEnv(t, func(d *Deps) { d.VPNCabinetKeys = files })
	digest := sha256.Sum256([]byte("key1"))
	name := fmt.Sprintf("amnezia_nl_%x", digest[:6])
	details := fmt.Sprintf(`{"tunnel_id":"awg12","tunnel_name":%q}`, name)
	if err := env.d.Events().Insert(env.ownedID, "tunnel_awg12", "ok", details, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	rec := env.do(t, cabOwner, http.MethodPost, "/v1/miniapp/routers/{id}/cabinets/amnezia/files/install", `{"key_id":"key1","country":"nl","declared_country":"ru","confirm":true}`)
	if rec.Code != 409 || files.downloaded != 0 || len(env.sink.enqueued) != 0 {
		t.Fatalf("known installed: %d", rec.Code)
	}
}
