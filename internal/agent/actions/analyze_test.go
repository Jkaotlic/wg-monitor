package actions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// tunnel_analyze — проверка конфига до импорта: примет ли его модуль роутера.
// Ответ агента — JSON, который читает мастер замены на бэкенде.
type analyzeOut struct {
	Supported bool   `json:"supported"`
	Version   string `json:"version"`
	Errors    []struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
}

func runAnalyze(t *testing.T, mux *http.ServeMux, args map[string]any) (wire.CommandResult, analyzeOut) {
	t.Helper()
	r := Runner{AwgClient: awgmgrFake(t, mux), Now: mockNow()}
	res := r.Execute(context.Background(), wire.Command{ID: "an1", Action: "tunnel_analyze", Args: args})
	var out analyzeOut
	if res.Status == "ok" {
		if err := json.Unmarshal([]byte(res.Output), &out); err != nil {
			t.Fatalf("ответ агента не JSON: %v: %q", err, res.Output)
		}
	}
	return res, out
}

func TestRunner_TunnelAnalyze_ReportsErrors(t *testing.T) {
	want, _ := base64.StdEncoding.DecodeString(testConfB64)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/awg/analyze", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Conf string `json:"conf"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		// В awg-manager уходит тот же текст конфига, что потом пойдёт в импорт.
		if req.Conf != string(want) {
			t.Errorf("в анализ ушёл не тот конфиг: %q", req.Conf)
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"version":"awg1.5","errors":[{"code":"h_overlap","message":"H1 и H2 пересекаются"}],"warnings":[]}}`))
	})
	res, out := runAnalyze(t, mux, map[string]any{"conf": testConfB64})
	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if !out.Supported || out.Version != "awg1.5" || len(out.Errors) != 1 || out.Errors[0].Code != "h_overlap" {
		t.Fatalf("ответ анализа: %+v", out)
	}
}

// Старая панель ручки не знает — агент отвечает «не поддерживается», а не
// ошибкой: мастер пропустит проверку, а не сорвёт замену.
func TestRunner_TunnelAnalyze_OldPanelIsUnsupported(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/awg/analyze", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	res, out := runAnalyze(t, mux, map[string]any{"conf": testConfB64})
	if res.Status != "ok" || out.Supported {
		t.Fatalf("старая панель: status=%q out=%+v output=%q", res.Status, out, res.Output)
	}
}

func TestRunner_TunnelAnalyze_RequiresConf(t *testing.T) {
	res, _ := runAnalyze(t, http.NewServeMux(), map[string]any{})
	if res.Status != "err" {
		t.Fatalf("без конфига анализировать нечего: status=%q", res.Status)
	}
}
