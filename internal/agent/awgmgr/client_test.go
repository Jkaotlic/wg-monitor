package awgmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClient_DiagResult_HappyPath(t *testing.T) {
	const wantPayload = `{"success":true,"data":{"summary":"all green","checks":42}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/diagnostics/result" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing X-Requested-With header")
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(wantPayload))
	}))
	defer srv.Close()
	c := New(srv.URL)
	out, err := c.DiagResult(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "all green") {
		t.Errorf("expected summary in body, got %q", out)
	}
}

func TestClient_DiagResult_5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()
	c := New(srv.URL)
	_, err := c.DiagResult(context.Background())
	if err == nil {
		t.Fatal("expected error on 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("err must mention status: %v", err)
	}
}

func TestClient_ImportConf_OK(t *testing.T) {
	want := `{"success":true,"data":{"id":"abc123","name":"sg","type":"awg","status":"running","enabled":false,"defaultRoute":false}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: %s", r.Method)
		}
		if r.URL.Path != "/api/import/conf" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing X-Requested-With")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type: %s", r.Header.Get("Content-Type"))
		}
		var body struct {
			Backend string `json:"backend"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Backend != "nativewg" {
			t.Fatalf("backend=%q, want nativewg", body.Backend)
		}
		w.Write([]byte(want))
	}))
	defer srv.Close()
	c := New(srv.URL)
	tun, err := c.ImportConf(context.Background(), "[Interface]\nPrivateKey=x\n", "sg", "nativewg")
	if err != nil {
		t.Fatal(err)
	}
	if tun.ID != "abc123" {
		t.Errorf("id: %q", tun.ID)
	}
}

func TestClient_ImportConf_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":true,"message":"parse conf: missing Address","code":"IMPORT_FAILED"}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	_, err := c.ImportConf(context.Background(), "", "x", "")
	if err == nil {
		t.Fatal("expected error on 400")
	}
}

func TestClient_DeleteTunnel_OK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: %s", r.Method)
		}
		if r.URL.Path != "/api/tunnels/delete" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.URL.RawQuery != "id=abc123" {
			t.Errorf("query: %q", r.URL.RawQuery)
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing X-Requested-With")
		}
		w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	if err := c.DeleteTunnel(context.Background(), "abc123"); err != nil {
		t.Fatal(err)
	}
}

func TestClient_DeleteTunnel_404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		w.Write([]byte("not found"))
	}))
	defer srv.Close()
	c := New(srv.URL)
	err := c.DeleteTunnel(context.Background(), "bad-id")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected 404 error, got %v", err)
	}
}

func TestClient_TunnelControlEndpoints(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method: %s", r.Method)
		}
		got = append(got, r.URL.Path+"?"+r.URL.RawQuery)
		w.Write([]byte(`{"success":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL)
	ctx := context.Background()
	if err := c.StopTunnel(ctx, "abc123"); err != nil {
		t.Fatal(err)
	}
	if err := c.ToggleEnabled(ctx, "abc123"); err != nil {
		t.Fatal(err)
	}
	if err := c.ToggleDefaultRoute(ctx, "abc123"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/api/control/stop?id=abc123",
		"/api/control/toggle-enabled?id=abc123",
		"/api/control/toggle-default-route?id=abc123",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls=\n%s\nwant=\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestClient_DiagRun_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/diagnostics/run" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method: %q", r.Method)
		}
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			t.Errorf("missing X-Requested-With header")
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":true,"data":{"status":"running"}}`))
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: &http.Client{Timeout: 2 * time.Second}}
	if err := c.DiagRun(context.Background()); err != nil {
		t.Errorf("DiagRun: %v", err)
	}
}

func TestClient_DiagRun_BubblesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":true,"message":"down"}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: &http.Client{Timeout: 2 * time.Second}}
	err := c.DiagRun(context.Background())
	if err == nil || !strings.Contains(err.Error(), "HTTP_503") {
		t.Errorf("expected HTTP_503 in error, got: %v", err)
	}
}

func TestClient_DiagResult_TypedNoReportOnHTTP400(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":true,"message":"no report available","code":"NO_REPORT"}`))
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, HTTP: &http.Client{Timeout: 2 * time.Second}}
	_, err := c.DiagResult(context.Background())
	if err == nil {
		t.Fatal("expected error on 400, got nil")
	}
	if !strings.Contains(err.Error(), "NO_REPORT") {
		t.Errorf("expected NO_REPORT in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "HTTP_400") {
		t.Errorf("expected HTTP_400 prefix, got: %v", err)
	}
}

func TestClient_UsesSessionCookieWhenCredentialsConfigured(t *testing.T) {
	var loginHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			loginHits++
			if r.Method != http.MethodPost {
				t.Errorf("login method: %s", r.Method)
			}
			http.SetCookie(w, &http.Cookie{Name: "awg_session", Value: "session-1", Path: "/"})
			_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
		case "/api/tunnels/all":
			ck, err := r.Cookie("awg_session")
			if err != nil || ck.Value != "session-1" {
				t.Fatalf("missing session cookie: %v / %#v", err, ck)
			}
			_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[]}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	c.SetCredentials("admin", "secret")
	if _, err := c.TunnelsAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if loginHits != 1 {
		t.Fatalf("login hits: got %d want 1", loginHits)
	}
	if _, err := c.TunnelsAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if loginHits != 1 {
		t.Fatalf("session should be reused, login hits=%d", loginHits)
	}
}

func TestClient_RelogsInWhenSessionExpires(t *testing.T) {
	var loginHits int
	var tunnelHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/login":
			loginHits++
			http.SetCookie(w, &http.Cookie{Name: "awg_session", Value: fmt.Sprintf("session-%d", loginHits), Path: "/"})
			_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
		case "/api/tunnels/all":
			tunnelHits++
			ck, err := r.Cookie("awg_session")
			if err != nil {
				t.Fatalf("missing session cookie: %v", err)
			}
			switch {
			case tunnelHits == 1 && ck.Value == "session-1":
				_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[]}}`))
			case tunnelHits == 2 && ck.Value == "session-1":
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`expired`))
			case tunnelHits == 3 && ck.Value == "session-2":
				_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[]}}`))
			default:
				t.Fatalf("unexpected tunnel request hit=%d cookie=%q", tunnelHits, ck.Value)
			}
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	c.SetCredentials("admin", "secret")
	if _, err := c.TunnelsAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.TunnelsAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if loginHits != 2 {
		t.Fatalf("login hits: got %d want 2", loginHits)
	}
	if tunnelHits != 3 {
		t.Fatalf("tunnel hits: got %d want 3", tunnelHits)
	}
}

// Фаза F: ряд трафика роутер ведёт сам, агенту остаётся его забрать.
// Точки приходят как {t, rx, tx}; id и period едут запросом.
func TestClient_TunnelTraffic_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tunnels/traffic" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("id"); got != "awg11" {
			t.Errorf("id: %q", got)
		}
		if got := r.URL.Query().Get("period"); got != "24h" {
			t.Errorf("period: %q", got)
		}
		w.WriteHeader(200)
		// Форма ответа списана с живого роутера (2.17.2+r21): t -- unix-секунды
		// числом, rx/tx -- дробные СКОРОСТИ, объём лежит в stats.
		_, _ = w.Write([]byte(`{"success":true,"data":{"points":[{"t":1787219289,"rx":4606.3,"tx":12785.8},{"t":1787219299,"rx":1118473.6,"tx":641049.7}],"stats":{"points":2,"currentRx":1118473.6,"currentTx":641049.7,"volumeRx":3707681860,"volumeTx":1845407616}}}`))
	}))
	defer srv.Close()

	got, err := New(srv.URL).TunnelTraffic(context.Background(), "awg11", "24h")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Points) != 2 || got.Points[0].T != 1787219289 || got.Points[1].RX != 1118473.6 {
		t.Fatalf("points = %+v", got.Points)
	}
	if got.Stats.VolumeRx != 3707681860 || got.Stats.VolumeTx != 1845407616 {
		t.Fatalf("stats = %+v", got.Stats)
	}
}

func TestClient_TunnelTraffic_SuccessFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"success":false}`))
	}))
	defer srv.Close()

	if _, err := New(srv.URL).TunnelTraffic(context.Background(), "awg11", "24h"); err == nil {
		t.Fatal("success=false must be an error, not an empty series: пустой ряд читается как «трафика не было»")
	}
}

// Флот разноверсионный: у оператора сборка +r21, у остальных стабильный
// 2.17.2. Конверт /api/presets между сборками менялся, поэтому клиент обязан
// понимать все три виденные формы -- иначе каталог наборов у половины флота
// окажется пустым, и экран покажет пустоту вместо 87 наборов.
func TestClient_Presets_AcceptsEveryEnvelopeShape(t *testing.T) {
	bodies := map[string]string{
		"data-объект (2.17.2+r21)": `{"success":true,"data":{"presets":[{"id":"figma","name":"Figma","engines":{"dns":{"domains":["figma.com"]}}}]}}`,
		"data-массив":              `{"success":true,"data":[{"id":"figma","name":"Figma","engines":{"dns":{"domains":["figma.com"]}}}]}`,
		"без конверта":             `{"presets":[{"id":"figma","name":"Figma","engines":{"dns":{"domains":["figma.com"]}}}]}`,
	}
	for label, body := range bodies {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		got, err := New(srv.URL).Presets(context.Background())
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if len(got) != 1 || got[0].ID != "figma" {
			t.Fatalf("%s: presets = %+v", label, got)
		}
	}
}

// Обмен по туннелю появился в awg-manager не сразу: на стабильном 2.17.2
// этого маршрута может не быть вовсе. Тогда ответ обязан говорить «эта
// сборка не умеет», а не «HTTP 404: 404 page not found» -- человек читает
// экран, а не наш лог.
func TestClient_TunnelTraffic_UnsupportedBuildSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		_, _ = w.Write([]byte("404 page not found"))
	}))
	defer srv.Close()

	_, err := New(srv.URL).TunnelTraffic(context.Background(), "awg11", "24h")
	if err == nil {
		t.Fatal("404 обязан быть ошибкой")
	}
	if !errors.Is(err, ErrUnsupportedByRouter) {
		t.Fatalf("err = %v, want ErrUnsupportedByRouter", err)
	}
}
