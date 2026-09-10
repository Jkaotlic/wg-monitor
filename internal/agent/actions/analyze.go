package actions

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
)

// analyzeOutput — ответ tunnel_analyze, его читает мастер замены на бэкенде.
// Supported=false — панель ручки не знает: проверить нечем, это не отказ.
type analyzeOutput struct {
	Supported bool                  `json:"supported"`
	Version   string                `json:"version,omitempty"`
	Errors    []awgmgr.AnalyzeIssue `json:"errors,omitempty"`
	Warnings  []awgmgr.AnalyzeIssue `json:"warnings,omitempty"`
}

// AnalyzeTunnel проверяет конфиг до импорта: примет ли его модуль роутера.
// confB64 — тот же base64, что уходит в tunnel_import, чтобы проверялось
// ровно то, что потом будет заведено.
func AnalyzeTunnel(ctx context.Context, client *awgmgr.Client, confB64 string) (string, error) {
	if confB64 == "" {
		return "", errors.New("tunnel_analyze: conf is required")
	}
	confData, err := base64.StdEncoding.DecodeString(confB64)
	if err != nil {
		return "", fmt.Errorf("tunnel_analyze: decode conf: %w", err)
	}
	var out analyzeOutput
	res, err := client.AnalyzeConf(ctx, string(confData))
	switch {
	case errors.Is(err, awgmgr.ErrAnalyzeUnsupported):
		out = analyzeOutput{Supported: false}
	case err != nil:
		return "", err
	default:
		out = analyzeOutput{Supported: true, Version: res.Version, Errors: res.Errors, Warnings: res.Warnings}
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
