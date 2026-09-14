package actions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

type infoStep struct {
	info *awgmgr.SystemInfo
	err  error
}

// fakeAwgUpdate -- awg-manager, который перезапускается во время обновления:
// SystemInfo отдаёт шаги по очереди, последний повторяется. UpdateCheck умеет
// то же самое через checks, если он задан (ruling M3: "checking" переспрашивается).
type fakeAwgUpdate struct {
	check      awgmgr.UpdateCheck
	checks     []awgmgr.UpdateCheck // когда задан -- отдаётся по очереди, последний повторяется
	checkCalls int
	checkErr   error
	applyErr   error
	applyCalls int
	infos      []infoStep
	infoCalls  int
}

func (f *fakeAwgUpdate) UpdateCheck(ctx context.Context, force bool) (*awgmgr.UpdateCheck, error) {
	if !force {
		return nil, errors.New("обновление обязано спрашивать с force=true")
	}
	if f.checkErr != nil {
		return nil, f.checkErr
	}
	if len(f.checks) > 0 {
		i := f.checkCalls
		if i >= len(f.checks) {
			i = len(f.checks) - 1
		}
		f.checkCalls++
		c := f.checks[i]
		return &c, nil
	}
	f.checkCalls++
	c := f.check
	return &c, nil
}

func (f *fakeAwgUpdate) UpdateApply(ctx context.Context) error {
	f.applyCalls++
	return f.applyErr
}

func (f *fakeAwgUpdate) SystemInfo(ctx context.Context) (*awgmgr.SystemInfo, error) {
	i := f.infoCalls
	if i >= len(f.infos) {
		i = len(f.infos) - 1
	}
	f.infoCalls++
	return f.infos[i].info, f.infos[i].err
}

// fakeUpdateClock -- часы, которые двигает только sleep: тест таймаута идёт
// за миллисекунды, а не за пять минут.
func fakeUpdateClock() (func() time.Time, func(context.Context, time.Duration) error, *int) {
	clock := time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC)
	sleeps := 0
	now := func() time.Time { return clock }
	sleep := func(ctx context.Context, d time.Duration) error {
		clock = clock.Add(d)
		sleeps++
		return nil
	}
	return now, sleep, &sleeps
}

func decodeAwgmUpdate(t *testing.T, out string) wire.AwgmUpdateResult {
	t.Helper()
	var res wire.AwgmUpdateResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("ответ не JSON: %v; %q", err, out)
	}
	return res
}

func TestAwgmUpdate_NoUpdateIsOKWithoutApply(t *testing.T) {
	cli := &fakeAwgUpdate{check: awgmgr.UpdateCheck{Available: false, CurrentVersion: "2.19.0+r2", LatestVersion: "2.19.0+r2"}}
	now, sleep, _ := fakeUpdateClock()
	out, err := AwgmUpdate(context.Background(), cli, sleep, now)
	if err != nil {
		t.Fatal(err)
	}
	res := decodeAwgmUpdate(t, out)
	if res.Updated || res.From != "2.19.0+r2" || res.To != "2.19.0+r2" {
		t.Errorf("res = %+v", res)
	}
	if cli.applyCalls != 0 {
		t.Errorf("обновления нет, а apply вызван %d раз", cli.applyCalls)
	}
}

// Демон перезапускается: два обрыва подряд -- ожидаемое поведение, а не ошибка.
func TestAwgmUpdate_VersionChangesAfterTwoDrops(t *testing.T) {
	cli := &fakeAwgUpdate{
		check:    awgmgr.UpdateCheck{Available: true, CurrentVersion: "2.19.0+r2", LatestVersion: "2.19.1"},
		applyErr: errors.New("awgmgr POST /api/system/update/apply: EOF"),
		infos: []infoStep{
			{err: errors.New("awgmgr GET /api/system/info: connection refused")},
			{err: errors.New("awgmgr GET /api/system/info: connection refused")},
			{info: &awgmgr.SystemInfo{Version: "2.19.1", KernelModuleVersion: "3.2.20260930", KernelModuleLoadedVersion: "3.1.20260906"}},
		},
	}
	now, sleep, sleeps := fakeUpdateClock()
	out, err := AwgmUpdate(context.Background(), cli, sleep, now)
	if err != nil {
		t.Fatal(err)
	}
	res := decodeAwgmUpdate(t, out)
	want := wire.AwgmUpdateResult{Updated: true, From: "2.19.0+r2", To: "2.19.1", KmodInstalled: "3.2.20260930", KmodLoaded: "3.1.20260906", RebootNeeded: true}
	if res != want {
		t.Errorf("res = %+v\nwant %+v", res, want)
	}
	if *sleeps != 3 || cli.applyCalls != 1 {
		t.Errorf("sleeps=%d apply=%d, хотим 3 и 1", *sleeps, cli.applyCalls)
	}
}

func TestAwgmUpdate_TimeoutWithoutVersionChange(t *testing.T) {
	cli := &fakeAwgUpdate{
		check: awgmgr.UpdateCheck{Available: true, CurrentVersion: "2.19.0+r2"},
		infos: []infoStep{{info: &awgmgr.SystemInfo{Version: "2.19.0+r2"}}},
	}
	now, sleep, sleeps := fakeUpdateClock()
	_, err := AwgmUpdate(context.Background(), cli, sleep, now)
	if err == nil || err.Error() != "awg-manager не вернулся с новой версией за 5 минут" {
		t.Fatalf("err = %v", err)
	}
	if *sleeps != 60 {
		t.Errorf("опросов %d, хотим 60 (5 минут по 5 секунд)", *sleeps)
	}
}

// Ruling K2: отказ apply HTTP-кодом (кроме 401/403/404) НЕ валит действие
// сразу -- автоустановщик мог поставить обновление параллельно. Опрос
// SystemInfo продолжается до 5 минут, и постусловие "версия сменилась"
// решает спор.
func TestAwgmUpdate_HTTPRefusalPollsUntilVersionChanges(t *testing.T) {
	cli := &fakeAwgUpdate{
		check:    awgmgr.UpdateCheck{Available: true, CurrentVersion: "2.19.0+r2"},
		applyErr: errors.New("awgmgr POST /api/system/update/apply: HTTP 409: update already running"),
		infos:    []infoStep{{info: &awgmgr.SystemInfo{Version: "2.19.1"}}},
	}
	now, sleep, sleeps := fakeUpdateClock()
	out, err := AwgmUpdate(context.Background(), cli, sleep, now)
	if err != nil {
		t.Fatal(err)
	}
	res := decodeAwgmUpdate(t, out)
	if !res.Updated || res.To != "2.19.1" {
		t.Errorf("res = %+v", res)
	}
	if *sleeps == 0 {
		t.Errorf("постусловие проверяется через опрос, а не мгновенно: sleeps=%d", *sleeps)
	}
}

// Ruling K2: тот же отказ 409, но версия так и не сменилась -- таймаут через
// пять минут тем же текстом, что у обычного "не вернулся", а не мгновенная
// ошибка.
func TestAwgmUpdate_HTTPRefusalWithoutVersionChangeTimesOut(t *testing.T) {
	cli := &fakeAwgUpdate{
		check:    awgmgr.UpdateCheck{Available: true, CurrentVersion: "2.19.0+r2"},
		applyErr: errors.New("awgmgr POST /api/system/update/apply: HTTP 409: update already running"),
		infos:    []infoStep{{info: &awgmgr.SystemInfo{Version: "2.19.0+r2"}}},
	}
	now, sleep, sleeps := fakeUpdateClock()
	_, err := AwgmUpdate(context.Background(), cli, sleep, now)
	if err == nil || err.Error() != "awg-manager не вернулся с новой версией за 5 минут" {
		t.Fatalf("err = %v", err)
	}
	if *sleeps != 60 {
		t.Errorf("опросов %d, хотим 60", *sleeps)
	}
}

// Ruling K2: 401/403/404 -- отказ по самой природе запроса (не авторизован,
// эндпоинта нет). Ждать пять минут нечего -- ошибка сразу.
func TestAwgmUpdate_NotFoundFailsFast(t *testing.T) {
	cli := &fakeAwgUpdate{
		check:    awgmgr.UpdateCheck{Available: true, CurrentVersion: "2.19.0+r2"},
		applyErr: errors.New("awgmgr POST /api/system/update/apply: HTTP 404: not found"),
		infos:    []infoStep{{info: &awgmgr.SystemInfo{Version: "2.19.0+r2"}}},
	}
	now, sleep, sleeps := fakeUpdateClock()
	_, err := AwgmUpdate(context.Background(), cli, sleep, now)
	if err == nil || !strings.Contains(err.Error(), "awg-manager отказался обновляться") {
		t.Fatalf("err = %v", err)
	}
	if *sleeps != 0 {
		t.Errorf("401/403/404 -- отказ немедленно: sleeps=%d", *sleeps)
	}
}

// Автоустановщик awg-manager успел поставить обновление сам: apply отказал, но
// постусловие «версия сменилась» верно -- это успех.
func TestAwgmUpdate_RefusalButAutoInstallerAlreadyUpdated(t *testing.T) {
	cli := &fakeAwgUpdate{
		check:    awgmgr.UpdateCheck{Available: true, CurrentVersion: "2.19.0+r2"},
		applyErr: errors.New("awgmgr /api/system/update/apply: success=false: already up to date"),
		infos:    []infoStep{{info: &awgmgr.SystemInfo{Version: "2.19.1", KernelModuleVersion: "3.1.20260906", KernelModuleLoadedVersion: "3.1.20260906"}}},
	}
	now, sleep, _ := fakeUpdateClock()
	out, err := AwgmUpdate(context.Background(), cli, sleep, now)
	if err != nil {
		t.Fatal(err)
	}
	res := decodeAwgmUpdate(t, out)
	if !res.Updated || res.To != "2.19.1" || res.RebootNeeded {
		t.Errorf("res = %+v", res)
	}
}

func TestAwgmUpdate_EmptyCurrentVersionFallsBackToSystemInfo(t *testing.T) {
	cli := &fakeAwgUpdate{
		check: awgmgr.UpdateCheck{Available: false},
		infos: []infoStep{{info: &awgmgr.SystemInfo{Version: "2.19.0+r2"}}},
	}
	now, sleep, _ := fakeUpdateClock()
	out, err := AwgmUpdate(context.Background(), cli, sleep, now)
	if err != nil {
		t.Fatal(err)
	}
	if res := decodeAwgmUpdate(t, out); res.From != "2.19.0+r2" {
		t.Errorf("from = %q", res.From)
	}
}

// Ruling M3: "checking":true -- awg-manager ещё считает. Переспрашивать
// каждые 3с до 30с, прежде чем решить «уже последняя».
func TestAwgmUpdate_ChecksAgainWhileCheckingBeforeDecidingLatest(t *testing.T) {
	cli := &fakeAwgUpdate{
		checks: []awgmgr.UpdateCheck{
			{Checking: true, CurrentVersion: "2.19.0+r2"},
			{Checking: true, CurrentVersion: "2.19.0+r2"},
			{Available: false, CurrentVersion: "2.19.0+r2", LatestVersion: "2.19.0+r2"},
		},
	}
	now, sleep, sleeps := fakeUpdateClock()
	out, err := AwgmUpdate(context.Background(), cli, sleep, now)
	if err != nil {
		t.Fatal(err)
	}
	res := decodeAwgmUpdate(t, out)
	if res.Updated {
		t.Errorf("res = %+v", res)
	}
	if *sleeps != 2 {
		t.Errorf("sleeps=%d, хотим 2 (по 3с между двумя checking:true)", *sleeps)
	}
	if cli.checkCalls != 3 {
		t.Errorf("checkCalls=%d, хотим 3", cli.checkCalls)
	}
}

// Ruling M3: "checking" не проходит и за 30с -- решаем по последнему ответу,
// не виснем навсегда.
func TestAwgmUpdate_CheckingTimesOutAfter30sAndProceeds(t *testing.T) {
	cli := &fakeAwgUpdate{
		checks: []awgmgr.UpdateCheck{{Checking: true, CurrentVersion: "2.19.0+r2"}},
	}
	now, sleep, sleeps := fakeUpdateClock()
	out, err := AwgmUpdate(context.Background(), cli, sleep, now)
	if err != nil {
		t.Fatal(err)
	}
	res := decodeAwgmUpdate(t, out)
	if res.Updated {
		t.Errorf("res = %+v", res)
	}
	if *sleeps != 10 {
		t.Errorf("sleeps=%d, хотим 10 (30с / 3с)", *sleeps)
	}
}

func TestRunner_AwgmUpdate_NoClient(t *testing.T) {
	res := (&Runner{}).Execute(context.Background(), wire.Command{ID: "u1", Action: "awgm_update"})
	if res.Status != "err" || !strings.Contains(res.Output, "awgmgr client not configured") {
		t.Errorf("res = %+v", res)
	}
}

func TestActionTimeoutFor_MaintenanceUpdates(t *testing.T) {
	if got := actionTimeoutFor("awgm_update"); got != 360*time.Second {
		t.Errorf("awgm_update budget = %v, want 360s: пятиминутный опрос должен успеть сказать своё", got)
	}
}
