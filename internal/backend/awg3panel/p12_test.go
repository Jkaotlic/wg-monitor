package awg3panel

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/awg3panel/awg3paneltest"
)

func fieldOf(err error) string {
	var fe *FieldError
	if errors.As(err, &fe) {
		return fe.Field
	}
	return ""
}

func testCA(t *testing.T) *awg3paneltest.CA {
	t.Helper()
	ca, err := awg3paneltest.NewCA("test-ca")
	if err != nil {
		t.Fatal(err)
	}
	return ca
}

func TestParseP12ModernAndLegacy(t *testing.T) {
	ca := testCA(t)
	now := time.Now()
	for _, legacy := range []bool{false, true} {
		pfx, err := ca.P12("anex", now.Add(-time.Hour), now.Add(48*time.Hour), "p12-pw", legacy)
		if err != nil {
			t.Fatal(err)
		}
		cc, err := ParseP12(pfx, "p12-pw", now)
		if err != nil {
			t.Fatalf("legacy=%v: %v", legacy, err)
		}
		if !strings.Contains(cc.CertPEM, "BEGIN CERTIFICATE") || !strings.Contains(cc.KeyPEM, "BEGIN PRIVATE KEY") || cc.Subject != "anex" || cc.NotAfter.Before(now) {
			t.Fatalf("legacy=%v: subject=%q notAfter=%v", legacy, cc.Subject, cc.NotAfter)
		}
		if strings.Contains(cc.String(), "PRIVATE") {
			t.Fatal("ClientCert печатает ключ")
		}
	}
}

func TestParseP12Rejects(t *testing.T) {
	ca := testCA(t)
	now := time.Now()
	good, _ := ca.P12("anex", now.Add(-time.Hour), now.Add(time.Hour), "p12-pw", false)
	expired, _ := ca.P12("anex", now.Add(-48*time.Hour), now.Add(-time.Hour), "p12-pw", false)
	future, _ := ca.P12("anex", now.Add(time.Hour), now.Add(48*time.Hour), "p12-pw", false)
	mismatched, _ := ca.P12Mismatched("p12-pw")
	cases := []struct {
		name, pw, field, text string
		data                  []byte
	}{
		{"неверный пароль", "wrong", "p12_password", "не подошёл", good},
		{"пусто", "p12-pw", "p12", "Выберите", nil},
		{"не p12", "p12-pw", "p12", "не читается", []byte("-----BEGIN CERTIFICATE-----\nnot a pfx\n")},
		{"слишком большой", "p12-pw", "p12", "64 КБ", bytes.Repeat([]byte{0x30}, 65<<10)},
		{"истёк", "p12-pw", "p12", "истёк", expired},
		{"ещё не действует", "p12-pw", "p12", "начнёт действовать", future},
		{"чужой ключ", "p12-pw", "p12", "не подходит", mismatched},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseP12(tc.data, tc.pw, now)
			var fe *FieldError
			if !errors.As(err, &fe) || fe.Field != tc.field || !strings.Contains(fe.Reason, tc.text) {
				t.Fatalf("%v", err)
			}
			if strings.Contains(err.Error(), tc.pw) && tc.pw != "" {
				t.Fatal("пароль в тексте ошибки")
			}
		})
	}
}

// PEM из .p12 -- ровно то, чем клиент входит в панель по mTLS.
func TestParseP12ResultWorksForMTLS(t *testing.T) {
	p := startPanel(t, awg3paneltest.Options{})
	pfx, err := p.CA.P12("anex", time.Now().Add(-time.Hour), time.Now().Add(time.Hour), "", false)
	if err != nil {
		t.Fatal(err)
	}
	cc, err := ParseP12(pfx, "", time.Now())
	if err != nil {
		t.Fatalf("p12 без пароля: %v", err)
	}
	c, err := NewClient(Credentials{BaseURL: p.URL, User: "admin", Password: testPanelPass, CertPEM: []byte(cc.CertPEM), KeyPEM: []byte(cc.KeyPEM)}, ClientOptions{RootCAs: p.CA.Pool})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Ifaces(context.Background()); err != nil {
		t.Fatalf("mTLS с сертификатом из .p12: %v", err)
	}
}
