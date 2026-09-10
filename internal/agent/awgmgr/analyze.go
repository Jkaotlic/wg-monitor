package awgmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"
)

// ErrAnalyzeUnsupported — панель awg-manager не знает /api/awg/analyze
// (появилась в 2.18.x). Это не отказ конфига, а «проверить нечем»: вызывающий
// пропускает проверку, а не валит замену.
var ErrAnalyzeUnsupported = errors.New("awg-manager: /api/awg/analyze не поддерживается")

// AnalyzeIssue — одна ошибка или предупреждение анализа. Message awg-manager
// пишет по-русски и для человека: «H1 и H2 пересекаются…».
type AnalyzeIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// AnalyzeResult — то, что из анализа нужно замене: версия протокола конфига,
// ошибки совместимости с модулем роутера (с ними модуль конфиг отвергнет) и
// предупреждения.
type AnalyzeResult struct {
	Version  string         `json:"version"`
	Errors   []AnalyzeIssue `json:"errors"`
	Warnings []AnalyzeIssue `json:"warnings"`
}

// AnalyzeConf спрашивает awg-manager, примет ли модуль роутера этот конфиг, —
// до импорта, ничего на роутере не заводя. Форма ответа снята с живого
// awg-manager 2.18.2 (10.09.2026).
func (c *Client) AnalyzeConf(ctx context.Context, rawConf string) (*AnalyzeResult, error) {
	start := time.Now()
	const path = "/api/awg/analyze"
	body, err := json.Marshal(struct {
		Conf string `json:"conf"`
	}{Conf: rawConf})
	if err != nil {
		return nil, err
	}
	resp, err := c.do(ctx, http.MethodPost, path, bytes.NewReader(body), "application/json")
	if err != nil {
		slog.Warn("awgmgr request failed", "method", "POST", "path", path, "err", err, "duration_ms", time.Since(start).Milliseconds())
		return nil, fmt.Errorf("awgmgr POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	slog.Debug("awgmgr", "method", "POST", "path", path, "status", resp.StatusCode, "duration_ms", time.Since(start).Milliseconds())
	rb, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("awgmgr read %s: %w", path, err)
	}
	// Панель старше 2.18 ручки не знает: 404 от роутера, 405 — если путь
	// занят под другой метод.
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return nil, ErrAnalyzeUnsupported
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("awgmgr %s: HTTP %d: %s", path, resp.StatusCode, snippet(rb))
	}
	var env Envelope[AnalyzeResult]
	if err := json.Unmarshal(rb, &env); err != nil {
		return nil, fmt.Errorf("awgmgr %s: decode: %w", path, err)
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr %s: success=false", path)
	}
	return &env.Data, nil
}
