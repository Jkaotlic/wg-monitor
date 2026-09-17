package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
)

// Установка, переустановка и перенаправление агента в песочнице. Движок
// заданий настоящий (provision.Deps.Start, шаги, подсказки, коммит токена на
// config_written) -- подменены только три края: терминал роутера (relay),
// отчёт агента о выходе на связь и загрузка checksums с GitHub. Экран «Ход
// работы» проверяется на той же машине состояний, что в проде.

// sandboxStepDelay -- пауза между шагами: чтобы пульс текущего шага было
// видно глазами и на скриншоте, а весь мастер укладывался в несколько секунд.
const sandboxStepDelay = 700 * time.Millisecond

// sandboxRelay печатает маркеры шагов тем же форматом, что awgm-relay.py
// ("__WG_STEP__ имя [подробность]"). Ник с подстрокой "fail" проваливает
// установку на скачивании -- проверить экран провала с hint и tail.
func sandboxRelay(ctx context.Context, _ string, jobJSON []byte, onLine func(string)) (int, error) {
	var job struct {
		Mode     string `json:"mode"`
		Nickname string `json:"nickname"`
	}
	if err := json.Unmarshal(jobJSON, &job); err != nil {
		return 2, err
	}
	steps := []string{provision.StepTerminalConnected, provision.StepBackendURLRewrite, provision.StepServiceRestarted}
	if job.Mode == "bootstrap_install" {
		steps = []string{
			provision.StepTerminalConnected,
			provision.StepArchDetected + " arm64",
			provision.StepDownloading,
			provision.StepChecksumOK,
			provision.StepConfigWritten,
			provision.StepInitInstalled,
			provision.StepServiceStarted,
		}
	}
	for _, step := range steps {
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(sandboxStepDelay):
		}
		onLine(provision.StepMarker + " " + step)
		if strings.Contains(job.Nickname, "fail") && strings.HasPrefix(step, provision.StepDownloading) {
			onLine("curl: (22) The requested URL returned error: 404")
			return 12, nil
		}
	}
	return 0, nil
}

// sandboxLastSeen -- «агент вышел на связь» сразу после установки.
func sandboxLastSeen(string) (time.Time, bool) { return time.Now().Add(time.Second), true }

// sandboxChecksums -- выпуск «проверен» без GitHub. Суммы ни с чем не
// сверяются: фальшивый relay ничего не скачивает.
func sandboxChecksums(context.Context, string, string) (map[string]string, error) {
	zero := strings.Repeat("0", 64)
	return map[string]string{"wg-monitor-agent-linux-arm64": zero, "wg-monitor-agent-linux-mipsle": zero}, nil
}

// watchBackendUpdate -- юнит обновления бэкенда в песочнице: подбирает файл
// заявки и через 5 секунд «перезапускается» новой версией (apply) или молчит
// (ignore) -- чтобы проверить оба исхода экрана «Бэкенд обновляется».
//
// SetVersion пишет глобальную строку, которую читают обработчики: гонка
// данных, допустимая только здесь -- песочница не собирается с -race и
// обслуживает одного человека.
func watchBackendUpdate(ctx context.Context, path string, apply bool) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		_ = os.Remove(path)
		var req struct {
			TargetVersion string `json:"target_version"`
		}
		if json.Unmarshal(raw, &req) != nil || req.TargetVersion == "" {
			slog.Warn("песочница: заявка на раскатку бэкенда не разобрана")
			continue
		}
		slog.Info("песочница: заявка на раскатку бэкенда", "target_version", req.TargetVersion, "apply", apply)
		if !apply {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
		backend.SetVersion(req.TargetVersion)
		slog.Info("песочница: бэкенд «перезапущен»", "version", req.TargetVersion)
	}
}
