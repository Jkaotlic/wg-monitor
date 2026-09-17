package backend

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
)

// miniappJobStep -- шаг без omitempty: форма ответа постоянная, клиент не
// гадает, отсутствует поле или пусто.
type miniappJobStep struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type miniappJobResp struct {
	ID       string           `json:"id"`
	Kind     string           `json:"kind"`
	Nickname string           `json:"nickname"`
	RouterID *int64           `json:"router_id"`
	State    string           `json:"state"`
	Steps    []miniappJobStep `json:"steps"`
	Version  string           `json:"version"`
	Hint     string           `json:"hint"`
	Tail     string           `json:"tail"`
}

// miniappJobHandler -- GET /v1/miniapp/jobs/{job_id}: ход установки,
// переустановки или перенаправления. Задание к роутеру не привязано (установка
// его только создаёт), поэтому гейт -- админ; router_id -- когда роутер с этим
// именем уже есть. tail движок уже очистил от токена (provision/runner.go).
func miniappJobHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := miniappAdminOrNotFound(d, w, r); !ok {
			return
		}
		if d.Provision.Store == nil {
			writeMiniappOpsError(w, http.StatusServiceUnavailable, "provision_not_configured")
			return
		}
		job, ok := d.Provision.Store.Get(strings.TrimSpace(r.PathValue("job_id")))
		if !ok {
			writeMiniappOpsError(w, http.StatusNotFound, "job_not_found")
			return
		}
		resp := miniappJobFrom(job)
		if d.DB != nil {
			if u, err := lookupExistingUser(d.DB, job.Nickname); err == nil && u != nil {
				id := u.ID
				resp.RouterID = &id
			}
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func miniappJobFrom(job provision.Job) miniappJobResp {
	out := miniappJobResp{
		ID:       job.ID,
		Kind:     string(job.Kind),
		Nickname: job.Nickname,
		State:    string(job.State),
		Steps:    make([]miniappJobStep, 0, len(job.Steps)),
		Version:  job.Version,
		Hint:     job.Hint,
		Tail:     job.Tail,
	}
	for _, s := range job.Steps {
		out.Steps = append(out.Steps, miniappJobStep{Name: s.Name, Status: string(s.Status), Detail: s.Detail})
	}
	return out
}
