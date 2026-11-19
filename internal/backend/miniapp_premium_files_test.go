package backend

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/premiumfiles"
)

type fakePremiumFiles struct {
	fakeCabinetKeys
	opened, downloaded int
}

func (f *fakePremiumFiles) OpenPremiumFiles(context.Context, int64, string, string) (premiumfiles.Account, string, error) {
	f.opened++
	return premiumfiles.Account{Countries: []premiumfiles.Country{{Code: "nl", Name: "Нидерланды"}}}, "key1", nil
}
func (f *fakePremiumFiles) DownloadPremiumFile(context.Context, int64, string, string, string) ([]byte, error) {
	f.downloaded++
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
