package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// Снимки экрана для miniapp/test/e2eScreen.test.js: настоящие ответы мини-аппа из
// контура прогоняются через настоящие функции экрана (pathState, routerHeadline).
// Обычный прогон сверяет файлы с контуром, E2E_WRITE=1 их переписывает: снимок не
// может молча разойтись с тем, что бэкенд отдаёт на самом деле.

const screenFixtureDir = "../../miniapp/test/fixtures/e2e"

// screenFixtureTime -- подстановка вместо времени: снимок не должен зависеть от часов.
const screenFixtureTime = "2026-11-02T00:00:00Z"

// normalizeScreen убирает из ответа всё, что меняется от прогона к прогону.
func normalizeScreen(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			switch k {
			case "ts", "last_seen_at", "hard_since", "matrix_updated_at":
				x[k] = screenFixtureTime
			case "handshake_age_sec":
				// Форма значения важнее секунд: свежий или простаивающий.
				if n, _ := val.(float64); n < 180 {
					x[k] = float64(int(freshAge / time.Second))
				} else {
					x[k] = float64(int(idleAge / time.Second))
				}
			case "last_seen_age_sec":
				if n, _ := val.(float64); n < 3600 {
					x[k] = float64(0)
				} else {
					x[k] = float64(7200)
				}
			default:
				x[k] = normalizeScreen(val)
			}
		}
	case []any:
		for i := range x {
			x[i] = normalizeScreen(x[i])
		}
	}
	return v
}

func decodeAny(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// checkScreenFixture записывает снимок или сверяет его с файлом.
func checkScreenFixture(t *testing.T, name string, s Screen) {
	t.Helper()
	router, events := decodeAny(t, s.RouterJSON), decodeAny(t, s.EventsJSON)
	// Тревоги одного отчёта различаются только долями секунды: порядок не важен снимку.
	if list, ok := router["incidents"].([]any); ok {
		sort.Slice(list, func(i, j int) bool {
			a, _ := list[i].(map[string]any)
			b, _ := list[j].(map[string]any)
			return fmt.Sprint(a["check_name"]) < fmt.Sprint(b["check_name"])
		})
	}
	fixture := map[string]any{
		"router":             normalizeScreen(router["router"]),
		"incidents":          normalizeScreen(router["incidents"]),
		"reserve_only_alert": s.Router.ReserveOnlyAlert,
		"tunnels":            normalizeScreen(events["tunnels"]),
		"traffic":            normalizeScreen(events["traffic"]),
	}
	got, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join(screenFixtureDir, name+".json")
	if os.Getenv("E2E_WRITE") != "" {
		if err := os.MkdirAll(screenFixtureDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("снимка нет (E2E_WRITE=1 go test ./internal/e2e -run TestScreenFixtures): %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s разошёлся с контуром (E2E_WRITE=1 перепишет, сперва понять почему):\n%s", path, got)
	}
}

func TestScreenFixtures(t *testing.T) {
	t.Run("s1_dead_carrier", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.Tick()
		kill(r, carrierID)
		r.Ticks(failThreshold)
		r.DrainCommands()
		// Снимок -- из отчёта до увода: несущий ещё мёртвое первое звено.
		checkScreenFixture(t, "s1_dead_carrier", r.Screen())
	})
	t.Run("s1_below_threshold", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.Tick()
		kill(r, carrierID)
		r.Ticks(failThreshold - 1)
		checkScreenFixture(t, "s1_below_threshold", r.Screen())
	})
	t.Run("s2_idle_reserve", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.Ticks(failThreshold + 1)
		checkScreenFixture(t, "s2_idle_reserve", r.Screen())
	})
	t.Run("s5_dead_reserve", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.Tick()
		kill(r, backup1ID)
		r.Ticks(failThreshold)
		r.DrainCommands()
		checkScreenFixture(t, "s5_dead_reserve", r.Screen())
	})
	t.Run("s11_awgm_silent", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.Tick()
		r.awgm.SetDown(true)
		r.Ticks(failThreshold)
		checkScreenFixture(t, "s11_awgm_silent", r.Screen())
	})
	t.Run("s12_agent_silent", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.Tick()
		r.killAgent()
		const silence = 2 * time.Hour
		r.AgeLastSeen(silence)
		r.ScanHeartbeat(silence)
		checkScreenFixture(t, "s12_agent_silent", r.Screen())
	})
	t.Run("s13_moved", func(t *testing.T) {
		r := newRig(t, rigOpts{})
		r.Tick()
		kill(r, carrierID)
		r.Ticks(failThreshold)
		r.DrainCommands()
		r.Tick() // первый отчёт после увода
		checkScreenFixture(t, "s13_moved", r.Screen())
	})
}
