package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// AwgUpdateClient -- то, чем обновление awg-manager пользуется у клиента.
// Интерфейс, чтобы тест мог изобразить перезапуск демона без HTTP.
type AwgUpdateClient interface {
	UpdateCheck(ctx context.Context, force bool) (*awgmgr.UpdateCheck, error)
	UpdateApply(ctx context.Context) error
	SystemInfo(ctx context.Context) (*awgmgr.SystemInfo, error)
}

const (
	awgmUpdateTimeout      = 5 * time.Minute
	awgmUpdatePollInterval = 5 * time.Second

	// awgmCheckingTimeout/Interval -- ruling M3: пока awg-manager отвечает
	// checking:true, он ещё считает результат; переспрашиваем, а не решаем
	// «уже последняя» по половинчатому ответу. 30с -- потолок ожидания.
	awgmCheckingTimeout  = 30 * time.Second
	awgmCheckingInterval = 3 * time.Second
)

var errAwgmUpdateTimeout = errors.New("awg-manager не вернулся с новой версией за 5 минут")

// errAwgmStillChecking -- ruling M3 + fix round 1, п.1: awg-manager отвечает
// checking:true дольше 30с. Решать «уже последняя версия» по половинчатому
// ответу значило бы соврать владельцу -- честнее сказать «попробуй позже».
var errAwgmStillChecking = errors.New("awg-manager ещё проверяет обновления — повторите через минуту")

// AwgmUpdate обновляет awg-manager его собственным API и ждёт, пока демон
// вернётся с другой версией.
//
// Постусловие одно -- «версия отличается от исходной», и оно верно и тогда,
// когда обновление поставил автоустановщик awg-manager одновременно с нами.
//
// Ruling K2: HTTP-отказ apply (409 «уже обновляется», обрыв соединения и
// т.п.) НЕ валит действие сразу -- автоустановщик мог начать обновление
// параллельно, и постусловие «версия сменилась» разрешает спор через
// обычный опрос. Мгновенно падаем только на 401/403/404 -- это отказ по
// самой природе запроса (не авторизован, эндпоинта нет), ждать нечего.
func AwgmUpdate(ctx context.Context, cli AwgUpdateClient, sleep func(context.Context, time.Duration) error, now func() time.Time) (string, error) {
	check, err := cli.UpdateCheck(ctx, true)
	if err != nil {
		return "", fmt.Errorf("проверка обновления awg-manager: %w", err)
	}
	// Ruling M3 + fix round 1, п.1: первый запрос идёт с force=true (просим
	// свежий расчёт); пока демон отвечает checking:true, он этот расчёт уже
	// делает, и повторный force=true запустил бы его заново вхолостую --
	// поэтому переспрашиваем со force=false. Не сошлось за 30с -- явная
	// ошибка, а не тихое «уже последняя».
	checkDeadline := now().Add(awgmCheckingTimeout)
	for check.Checking && now().Before(checkDeadline) {
		if err := sleep(ctx, awgmCheckingInterval); err != nil {
			return "", err
		}
		check, err = cli.UpdateCheck(ctx, false)
		if err != nil {
			return "", fmt.Errorf("проверка обновления awg-manager: %w", err)
		}
	}
	if check.Checking {
		return "", errAwgmStillChecking
	}

	if !check.Available {
		from := strings.TrimSpace(check.CurrentVersion)
		if from == "" {
			info, err := cli.SystemInfo(ctx)
			if err != nil {
				return "", fmt.Errorf("версия awg-manager: %w", err)
			}
			from = info.Version
		}
		return encodeAwgmUpdate(wire.AwgmUpdateResult{From: from, To: from})
	}

	// Fix round 1, п.2: update/check.currentVersion и system/info.version --
	// разные эндпоинты, форматы могут расходиться («2.19.0» vs «2.19.0+r2»).
	// Базовую версию для постусловия читаем из SystemInfo ПРЯМО ПЕРЕД apply --
	// тем же эндпоинтом и в том же формате, что и опрос после, иначе первое же
	// расхождение форматов читалось бы как ложное «обновилось».
	baseline, err := cli.SystemInfo(ctx)
	if err != nil {
		return "", fmt.Errorf("версия awg-manager: %w", err)
	}
	from := baseline.Version

	if err := cli.UpdateApply(ctx); err != nil && awgmgrAuthOrNotFound(err) {
		return "", fmt.Errorf("awg-manager отказался обновляться: %w", err)
	}
	deadline := now().Add(awgmUpdateTimeout)
	for now().Before(deadline) {
		if err := sleep(ctx, awgmUpdatePollInterval); err != nil {
			return "", err
		}
		info, err := cli.SystemInfo(ctx)
		if err != nil {
			continue
		}
		if info.Version != "" && info.Version != from {
			return awgmUpdated(from, info)
		}
	}
	return "", errAwgmUpdateTimeout
}

func awgmUpdated(from string, info *awgmgr.SystemInfo) (string, error) {
	return encodeAwgmUpdate(wire.AwgmUpdateResult{
		Updated:       true,
		From:          from,
		To:            info.Version,
		KmodInstalled: info.KernelModuleVersion,
		KmodLoaded:    info.KernelModuleLoadedVersion,
		RebootNeeded:  wire.RebootNeeded(info.KernelModuleVersion, info.KernelModuleLoadedVersion),
	})
}

func encodeAwgmUpdate(res wire.AwgmUpdateResult) (string, error) {
	b, err := json.Marshal(res)
	if err != nil {
		return "", fmt.Errorf("encode awgm_update: %w", err)
	}
	return string(b), nil
}

// awgmgrAuthOrNotFound -- 401/403/404 значит отказ по самой природе запроса
// (не авторизован, эндпоинта нет) -- ждать пять минут нечего. Другие
// HTTP-коды (409 «уже обновляется» и т.п.) и обрывы соединения -- поводы
// продолжить опрос: постусловие «версия сменилась» разрешит спор (ruling K2).
func awgmgrAuthOrNotFound(err error) bool {
	s := err.Error()
	for _, code := range []string{"HTTP 401", "HTTP 403", "HTTP 404"} {
		if strings.Contains(s, code) {
			return true
		}
	}
	return false
}
