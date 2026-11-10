package backend

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/Jkaotlic/wg-monitor/internal/awgmrelay"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/provision"
	"github.com/Jkaotlic/wg-monitor/internal/backend/revive"
	"strconv"
	"strings"
	"time"
)

const entwareRecoveryKind provision.JobKind = "entware_recovery"

// recoveryRefusal carries deliberate UI copy, as the other admin operations do.
type entwareRecoveryRefusal struct{ Text string }

func (e entwareRecoveryRefusal) Error() string { return e.Text }
func recoveryRefusal(text string) error        { return entwareRecoveryRefusal{Text: text} }

var recoverySteps = []string{"recovery_check", "recovery_hold", "recovery_links", "recovery_verify"}

// EntwareRecoveryService is shared by bot and UI. Its only execution input is
// a stored router ID; URLs, commands and scripts cannot come from a caller.
type EntwareRecoveryService struct {
	DB      *db.DB
	Store   *provision.Store
	Load    func(int64) (revive.Secrets, bool, error)
	Relay   provision.RelayFunc
	BaseCtx context.Context
	Notify  func(string)
}

func NewEntwareRecovery(d Deps) *EntwareRecoveryService {
	relay := d.Provision.Relay
	if relay == nil {
		relay = provision.DefaultRelay
	}
	return &EntwareRecoveryService{DB: d.DB, Store: d.Provision.Store, Load: d.Revive.PanelCredentials, Relay: relay, BaseCtx: d.Provision.BaseCtx}
}

func recoveryKey(id int64) string { return "entware_recovery:last:" + strconv.FormatInt(id, 10) }

func (s *EntwareRecoveryService) Start(routerID int64) (string, error) {
	if s == nil || s.DB == nil || s.Store == nil || s.Load == nil || s.Relay == nil {
		return "", recoveryRefusal("Восстановление не настроено.")
	}
	u, err := s.DB.Users().GetByID(routerID)
	if err != nil {
		return "", recoveryRefusal("Роутер не найден.")
	}
	raw := ""
	if u.AWGMURL != nil {
		raw = *u.AWGMURL
	}
	url, ok := revive.NormalizePanelURL(raw)
	if !ok {
		return "", recoveryRefusal("Нужен сохранённый HTTPS-адрес awg-manager.")
	}
	creds, ok, err := s.Load(routerID)
	if err != nil || !ok {
		return "", recoveryRefusal("Нет сохранённого входа в awg-manager. Скачайте скрипт для ручного запуска через Expert или SSH.")
	}
	if !s.Store.TryLock(u.Nickname) {
		return "", recoveryRefusal("На роутере уже идёт обслуживание. Дождитесь результата.")
	}
	steps := make([]provision.Step, len(recoverySteps))
	for i, n := range recoverySteps {
		steps[i] = provision.Step{Name: n, Status: provision.StepPending}
	}
	job := s.Store.Create(entwareRecoveryKind, u.Nickname, steps)
	if err := s.DB.KV().Set(recoveryKey(routerID), job.ID); err != nil {
		s.Store.Update(job.ID, func(j *provision.Job) {
			j.State = provision.StateFailed
			j.Hint = "Не удалось сохранить задание. Роутер не изменён."
		})
		s.Store.Unlock(u.Nickname)
		return "", recoveryRefusal("Не удалось сохранить задание.")
	}
	nonce := rand.Text()
	cfg := map[string]any{"mode": "entware_recover", "base_url": url, "login": creds.AWGMLogin(), "password": creds.AWGMPassword(), "api_key": creds.AWGMAPIKey(), "recovery_script": string(awgmrelay.EntwareRecoveryScript), "recovery_nonce": nonce}
	body, err := json.Marshal(cfg)
	if err != nil {
		s.Store.Unlock(u.Nickname)
		return "", recoveryRefusal("Не удалось подготовить задание.")
	}
	go s.run(job.ID, u.Nickname, nonce, body)
	return job.ID, nil
}

func (s *EntwareRecoveryService) Latest(routerID int64) (provision.Job, bool) {
	if s == nil || s.DB == nil || s.Store == nil {
		return provision.Job{}, false
	}
	id, err := s.DB.KV().Get(recoveryKey(routerID))
	if err != nil || id == "" {
		return provision.Job{}, false
	}
	job, ok := s.Store.Get(id)
	if !ok || job.Kind != entwareRecoveryKind {
		return provision.Job{}, false
	}
	return job, true
}

func (s *EntwareRecoveryService) run(id, nick, nonce string, body []byte) {
	defer s.Store.Unlock(nick)
	base := s.BaseCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, 3*time.Minute)
	defer cancel()
	result := struct {
		Held      bool     `json:"held"`
		ShellOK   bool     `json:"shell_ok"`
		CleanupOK bool     `json:"cleanup_ok"`
		Links     int      `json:"links_created"`
		Warnings  []string `json:"warnings"`
	}{}
	confirmed := false
	next := 0
	s.Store.Update(id, func(j *provision.Job) { j.Steps[0].Status = provision.StepActive })
	rc, err := s.Relay(ctx, "", body, func(line string) {
		// Never persist arbitrary relay/panel stdout, even for an error.
		if name, ok := strings.CutPrefix(line, "__WG_RECOVERY__ "+nonce+" "); ok {
			if next >= len(recoverySteps) || name != recoverySteps[next] {
				return
			}
			pos := next
			next++
			s.Store.Update(id, func(j *provision.Job) {
				j.Steps[pos].Status = provision.StepDone
				if next < len(j.Steps) {
					j.Steps[next].Status = provision.StepActive
				}
			})
		}
		if raw, ok := strings.CutPrefix(line, "__WG_RECOVERY_RESULT__ "+nonce+" "); ok && len(raw) < 2048 && next == len(recoverySteps) {
			if json.Unmarshal([]byte(raw), &result) == nil && result.Held && result.ShellOK && result.CleanupOK && result.Links >= 0 && result.Links <= 4096 {
				confirmed = true
			}
		}
	})
	success := err == nil && rc == 0 && confirmed && ctx.Err() == nil
	text := "Восстановление не подтверждено. Hold или часть ссылок могли сохраниться. Проверьте панель перед повторным запуском."
	if success {
		text = fmt.Sprintf("BusyBox защищён, команды Entware проверены. Создано ссылок: %d.", result.Links)
	}
	// Only a fixed warning can enter the job; never echo router-supplied strings.
	warning := ""
	if len(result.Warnings) > 0 {
		warning = "Сохранены старые неработающие ссылки. Они требуют отдельной проверки."
	}
	s.Store.Update(id, func(j *provision.Job) {
		if success {
			j.State = provision.StateSuccess
			j.Hint = warning
			j.Steps[len(j.Steps)-1].Detail = text
			if warning != "" {
				j.Steps[len(j.Steps)-1].Detail += " " + warning
			}
		} else {
			j.State = provision.StateFailed
			j.Hint = text
			for i := range j.Steps {
				if j.Steps[i].Status == provision.StepActive {
					j.Steps[i].Status = provision.StepFailed
					break
				}
			}
			if next == len(j.Steps) {
				j.Steps[len(j.Steps)-1].Status = provision.StepFailed
			}
		}
	})
	if s.Notify != nil {
		s.Notify(nick + ": " + text + " " + warning)
	}
}
