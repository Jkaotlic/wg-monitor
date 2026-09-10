package awgmgr

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Ответы сняты с живого awg-manager 2.18.2 (10.09.2026) на выдуманных
// конфигах: корректном и с пересекающимися H1/H2.
const (
	analyzeOKResp     = `{"success":true,"data":{"version":"awg1.0","interface":{"mtu":1280},"peer":{"endpoint":"203.0.113.10:51820"},"errors":[],"warnings":[]}}`
	analyzeErrorsResp = `{"success":true,"data":{"version":"awg1.5","errors":[{"code":"h_overlap","message":"H1 и H2 пересекаются (1, 1): модуль отвергает такой конфиг"}],"warnings":[]}}`
)

func analyzeServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/awg/analyze" {
			t.Errorf("запрос %s %s, ждали POST /api/awg/analyze", r.Method, r.URL.Path)
		}
		var req struct {
			Conf string `json:"conf"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Conf == "" {
			t.Errorf("в теле нет conf: %v %+v", err, req)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestClient_AnalyzeConf_Errors(t *testing.T) {
	c := New(analyzeServer(t, http.StatusOK, analyzeErrorsResp).URL)
	res, err := c.AnalyzeConf(context.Background(), "[Interface]\nPrivateKey = x\n")
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "awg1.5" || len(res.Errors) != 1 || res.Errors[0].Code != "h_overlap" || res.Errors[0].Message == "" {
		t.Fatalf("ошибки анализа разобраны не так: %+v", res)
	}
}

func TestClient_AnalyzeConf_OK(t *testing.T) {
	c := New(analyzeServer(t, http.StatusOK, analyzeOKResp).URL)
	res, err := c.AnalyzeConf(context.Background(), "[Interface]\nPrivateKey = x\n")
	if err != nil {
		t.Fatal(err)
	}
	if res.Version != "awg1.0" || len(res.Errors) != 0 || len(res.Warnings) != 0 {
		t.Fatalf("корректный конфиг: %+v", res)
	}
}

// Панель старше 2.18 ручки не знает: это «проверить нечем», а не отказ.
func TestClient_AnalyzeConf_OldPanelIsUnsupported(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		c := New(analyzeServer(t, status, `{"error":true,"code":"NOT_FOUND"}`).URL)
		if _, err := c.AnalyzeConf(context.Background(), "[Interface]\n"); !errors.Is(err, ErrAnalyzeUnsupported) {
			t.Errorf("HTTP %d: ждали ErrAnalyzeUnsupported, получили %v", status, err)
		}
	}
}

func TestClient_AnalyzeConf_ServerErrorIsNotUnsupported(t *testing.T) {
	c := New(analyzeServer(t, http.StatusInternalServerError, `{"error":true}`).URL)
	_, err := c.AnalyzeConf(context.Background(), "[Interface]\n")
	if err == nil || errors.Is(err, ErrAnalyzeUnsupported) {
		t.Fatalf("500 — это сбой, а не «не умеет»: %v", err)
	}
}
