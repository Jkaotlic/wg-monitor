package backend

import (
	"sort"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// incidentStoryEvent is the deliberately small input vocabulary shared by
// checks, repair jobs and (later) the operation journal. It carries identity
// and outcome, never raw command output or infrastructure coordinates.
type incidentStoryEvent struct {
	At        time.Time
	Kind      string
	Status    string
	CommandID string
	Detail    string
}

func incidentStoryFromCheckRows(rows []db.EventRow) []incidentStoryStep {
	events := make([]incidentStoryEvent, 0, len(rows))
	started := false
	for _, row := range rows {
		if !started {
			if row.Status != "fail" {
				continue
			}
			started = true
		}
		events = append(events, incidentStoryEvent{At: row.TS, Kind: "check", Status: row.Status})
	}
	return foldIncidentStory(events)
}

type incidentStoryStep struct {
	At         string `json:"at"`
	Kind       string `json:"kind"`
	ReasonCode string `json:"reason_code"`
	Tone       string `json:"tone"`
	Title      string `json:"title"`
	Detail     string `json:"detail,omitempty"`
	CommandID  string `json:"command_id,omitempty"`
}

func foldIncidentStory(events []incidentStoryEvent) []incidentStoryStep {
	ordered := append([]incidentStoryEvent(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].At.Before(ordered[j].At) })
	steps := make([]incidentStoryStep, 0, len(ordered))
	lastCheck := ""
	for _, event := range ordered {
		if event.Kind == "check" {
			if event.Status == lastCheck {
				continue
			}
			lastCheck = event.Status
		}
		step, ok := incidentStoryStepFor(event)
		if !ok {
			continue
		}
		steps = append(steps, step)
	}
	return steps
}

func incidentStoryStepFor(event incidentStoryEvent) (incidentStoryStep, bool) {
	step := incidentStoryStep{At: event.At.UTC().Format(time.RFC3339), CommandID: event.CommandID}
	switch {
	case event.Kind == "check" && event.Status == "fail":
		step.Kind, step.ReasonCode, step.Tone = "failure_detected", "check_failed", "danger"
		step.Title, step.Detail = "Обнаружен сбой", "VPN-туннель перестал проходить проверку"
	case event.Kind == "check" && event.Status == "ok":
		step.Kind, step.ReasonCode, step.Tone = "recovered", "check_recovered", "ok"
		step.Title, step.Detail = "Связь восстановилась", "Новая проверка прошла успешно"
	case event.Kind == "failover" && event.Status == "ok":
		step.Kind, step.ReasonCode, step.Tone = "failover_completed", "traffic_moved", "warn"
		step.Title, step.Detail = "Трафик уведён на резерв", "Основной путь не используется до восстановления"
	case event.Kind == "failover" && event.Status == "failed":
		step.Kind, step.ReasonCode, step.Tone = "failover_failed", "traffic_move_failed", "danger"
		step.Title, step.Detail = "На резерв перейти не удалось", "Нужна ручная проверка маршрута"
	case event.Kind == "repair" && event.Status == "started":
		step.Kind, step.ReasonCode, step.Tone = "repair_started", "repair_started", "warn"
		step.Title, step.Detail = "Запущена автоматическая починка", "Сервис проверяет безопасные способы восстановления"
	case event.Kind == "repair" && event.Status == "ok":
		step.Kind, step.ReasonCode, step.Tone = "repair_completed", "repair_completed", "ok"
		step.Title, step.Detail = "Починка завершена", "Ожидаем подтверждение новой проверкой"
	case event.Kind == "repair" && event.Status == "failed":
		step.Kind, step.ReasonCode, step.Tone = "repair_failed", "repair_failed", "danger"
		step.Title, step.Detail = "Починка не завершилась", "Автоматическая починка не завершилась"
	default:
		return incidentStoryStep{}, false
	}
	return step, true
}
