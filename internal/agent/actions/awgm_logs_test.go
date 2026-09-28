package actions

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func logsFake(t *testing.T, body string, status int, queries *[]string) *awgmgr.Client {
	t.Helper()
	return awgmgrFake(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/logs" {
			w.WriteHeader(404)
			return
		}
		if queries != nil {
			*queries = append(*queries, r.URL.RawQuery)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
}

func runLogs(t *testing.T, cli *awgmgr.Client, args map[string]any) (wire.CommandResult, wire.AwgmLogs) {
	t.Helper()
	r := Runner{AwgClient: cli, Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "l1", Action: "awgm_logs", Args: args})
	var out wire.AwgmLogs
	if res.Status == "ok" {
		if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
			t.Fatalf("output не wire.AwgmLogs: %v (%s)", err, res.Output)
		}
	}
	return res, out
}

func TestAwgmLogsDefaultsNewestFirstNoSanitize(t *testing.T) {
	var q []string
	cli := logsFake(t, `{"success":true,"data":{"enabled":true,"sanitized":true,"total":2,"logs":[
		{"timestamp":"2026-09-28T06:00:00Z","level":"warn","group":"routing","action":"boot","message":"первая"},
		{"timestamp":"2026-09-28T07:00:00Z","level":"error","group":"tunnel","action":"start","message":"вторая","repeats":3}]}}`, 200, &q)
	res, out := runLogs(t, cli, map[string]any{"sanitize": false})
	if res.Status != "ok" {
		t.Fatalf("status=%q out=%s", res.Status, res.Output)
	}
	if strings.Contains(q[0], "sanitize") || !strings.Contains(q[0], "level=warn") || !strings.Contains(q[0], "limit=100") {
		t.Fatalf("query = %q", q[0])
	}
	if len(out.Entries) != 2 || out.Entries[0].Message != "вторая" || out.Entries[0].Repeats != 3 {
		t.Fatalf("свежие сверху: %+v", out.Entries)
	}
}

func TestAwgmLogsMasksUnsanitizedEntries(t *testing.T) {
	cli := logsFake(t, `{"success":true,"data":{"enabled":true,"total":1,"logs":[
		{"timestamp":"2026-09-28T07:00:00Z","level":"warn","group":"tunnel","target":"198.51.100.7","message":"peer 198.51.100.7:51820 via vpn.example.com"}]}}`, 200, nil)
	_, out := runLogs(t, cli, nil)
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "198.51.100.7") || strings.Contains(string(b), "vpn.example.com") {
		t.Fatalf("немаскированный адрес дошёл до экрана: %s", b)
	}
	if !strings.Contains(out.Entries[0].Message, "198.*.*.7") {
		t.Fatalf("message = %q", out.Entries[0].Message)
	}
}

// Fix round 1 (P5): по-записный sanitized перекрывает флаг страницы в обе
// стороны -- запись sanitized:false обязана маскироваться, даже если
// страница в целом sanitized:true.
func TestAwgmLogsPerEntrySanitizedFalseOverridesPageTrue(t *testing.T) {
	cli := logsFake(t, `{"success":true,"data":{"enabled":true,"sanitized":true,"total":1,"logs":[
		{"timestamp":"2026-09-28T07:00:00Z","level":"warn","target":"198.51.100.7","message":"peer 198.51.100.7 down","sanitized":false}]}}`, 200, nil)
	_, out := runLogs(t, cli, nil)
	b, _ := json.Marshal(out)
	if strings.Contains(string(b), "198.51.100.7") {
		t.Fatalf("запись sanitized:false при странице sanitized:true не замаскирована: %s", b)
	}
	if !strings.Contains(out.Entries[0].Message, "198.*.*.7") {
		t.Fatalf("message = %q", out.Entries[0].Message)
	}
}

// Зеркальный случай P5: флага sanitized у страницы нет вовсе (по умолчанию
// значит false), но у самой записи sanitized:true -- текст не трогаем.
func TestAwgmLogsPerEntrySanitizedTrueWithoutPageFlag(t *testing.T) {
	cli := logsFake(t, `{"success":true,"data":{"enabled":true,"total":1,"logs":[
		{"timestamp":"2026-09-28T07:00:00Z","level":"warn","message":"peer 198.51.100.7 up","sanitized":true}]}}`, 200, nil)
	_, out := runLogs(t, cli, nil)
	if out.Entries[0].Message != "peer 198.51.100.7 up" {
		t.Fatalf("запись sanitized:true без флага страницы испорчена: %q", out.Entries[0].Message)
	}
}

func TestAwgmLogsKeepsSanitizedText(t *testing.T) {
	cli := logsFake(t, `{"success":true,"data":{"enabled":true,"sanitized":true,"total":1,"logs":[
		{"timestamp":"2026-09-28T07:00:00Z","level":"warn","message":"listen-порт переехал: 12*****.1:9000","sanitized":true}]}}`, 200, nil)
	_, out := runLogs(t, cli, nil)
	if out.Entries[0].Message != "listen-порт переехал: 12*****.1:9000" {
		t.Fatalf("маска awg-manager испорчена: %q", out.Entries[0].Message)
	}
}

func TestAwgmLogsUnsupportedIsAnAnswer(t *testing.T) {
	cli := logsFake(t, "404 page not found", 404, nil)
	res, out := runLogs(t, cli, nil)
	if res.Status != "ok" || !out.Unsupported {
		t.Fatalf("старая сборка -- ответ «не умеет», а не ошибка: %q %s", res.Status, res.Output)
	}
}

func TestAwgmLogsDisabled(t *testing.T) {
	cli := logsFake(t, `{"success":true,"data":{"enabled":false,"logs":[],"total":0}}`, 200, nil)
	_, out := runLogs(t, cli, nil)
	if out.Enabled || out.Unsupported {
		t.Fatalf("out = %+v", out)
	}
}

func TestAwgmLogsRejectsBadArgs(t *testing.T) {
	cli := logsFake(t, `{"success":true,"data":{"enabled":true,"logs":[]}}`, 200, nil)
	for _, args := range []map[string]any{{"level": "debug"}, {"group": "evil"}, {"limit": float64(500)}, {"limit": "x"}} {
		if res, _ := runLogs(t, cli, args); res.Status != "err" {
			t.Errorf("args %v: status %q", args, res.Status)
		}
	}
}

func TestAwgmLogsTruncatesToBudget(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"success":true,"data":{"enabled":true,"sanitized":true,"total":200,"logs":[`)
	msg := strings.Repeat("ж", 600)
	for i := 0; i < 200; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"timestamp":"2026-09-28T07:00:00Z","level":"info","message":"` + msg + `"}`)
	}
	sb.WriteString(`]}}`)
	cli := logsFake(t, sb.String(), 200, nil)
	res, out := runLogs(t, cli, map[string]any{"limit": float64(200), "level": "info"})
	if len(res.Output) > 48<<10 || !out.Truncated {
		t.Fatalf("вывод %d байт, truncated=%v", len(res.Output), out.Truncated)
	}
}
