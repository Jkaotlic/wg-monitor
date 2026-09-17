package backend

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// Импорт своего .conf (цикл 4, решение 3).
//
// Файл выбирает человек в приложении, но на роутер конфиг кладёт сервер и
// только после предпросмотра:
//
//  1. POST tunnels/import -- сервер разбирает файл сам (адрес, DNS, MTU,
//     точка подключения) и спрашивает роутер tunnel_analyze, примет ли модуль
//     такой конфиг. Конфиг остаётся в памяти процесса под одноразовым токеном
//     (5 минут, этот роутер, этот человек); клиенту обратно не отдаётся.
//  2. GET tunnels/import/{token} -- тот же предпросмотр, пока роутер ещё
//     отвечает на анализ (сервер ждёт не дольше miniappAgentAskWait).
//  3. POST tunnels/import/confirm -- tunnel_import replace:false: только
//     «Добавить как новый». Замена рабочего VPN-туннеля идёт через «добавить
//     -- перенести правила -- удалить старый» или мастер замены: там всё
//     обратимо до последнего шага.
//
// Приватный ключ не пишется в журнал и не возвращается ни в одном ответе.

const (
	miniappImportMaxConf    = 50 << 10
	miniappImportMaxBody    = 96 << 10
	miniappImportTTL        = 5 * time.Minute
	miniappImportNameWindow = 10 * time.Minute
	miniappAnalyzeMinAgent  = "v0.28.0"
	miniappImportAnalyzing  = "analyzing"
	miniappImportReady      = "ready"
)

const (
	miniappImportNoteOldAgent = "Агент роутера старше v0.28.0 и не умеет проверять конфиг до загрузки — проверка пропущена"
	miniappImportNoteOldPanel = "awg-manager на роутере не умеет проверять конфиг до загрузки — проверка пропущена"
	miniappImportNoteFailed   = "Роутер не смог проверить конфиг — проверка пропущена"
)

// miniappImportNameRe -- то же правило имени, что было у бота
// (callbacks/import_util.go до цикла 4).
var miniappImportNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,31}$`)

type miniappTunnelImportReq struct {
	Name    string `json:"name"`
	ConfB64 string `json:"conf_b64"`
}

// Заглушки печати: запрос с конфигом не должен попасть ни в журнал, ни в
// fmt.Errorf, даже если кто-нибудь когда-нибудь положит его туда целиком.
func (miniappTunnelImportReq) String() string   { return "miniappTunnelImportReq{скрыто}" }
func (miniappTunnelImportReq) GoString() string { return "miniappTunnelImportReq{скрыто}" }
func (miniappTunnelImportReq) LogValue() slog.Value {
	return slog.StringValue("скрыто")
}

type miniappImportProblem struct {
	Severity string `json:"severity"`
	Code     string `json:"code,omitempty"`
	Message  string `json:"message"`
}

type miniappImportPreview struct {
	Endpoint  string                 `json:"endpoint"`
	Addresses []string               `json:"addresses"`
	DNS       []string               `json:"dns"`
	MTU       int                    `json:"mtu,omitempty"`
	Problems  []miniappImportProblem `json:"problems"`
}

type miniappImportResp struct {
	Token      string               `json:"token"`
	Name       string               `json:"name"`
	State      string               `json:"state"`
	Analyzed   bool                 `json:"analyzed"`
	Note       string               `json:"note,omitempty"`
	CanConfirm bool                 `json:"can_confirm"`
	Preview    miniappImportPreview `json:"preview"`
}

// parseMiniappImportConf -- предпросмотр по тексту конфига. Годен конфиг с
// [Interface] PrivateKey и [Peer] PublicKey + Endpoint: без них агент всё
// равно откажет (ParseWGConf), только позже. Ключи в предпросмотр не
// попадают вовсе.
func parseMiniappImportConf(raw []byte) (miniappImportPreview, bool) {
	if len(raw) == 0 || !utf8.Valid(raw) {
		return miniappImportPreview{}, false
	}
	p := miniappImportPreview{Addresses: []string{}, DNS: []string{}, Problems: []miniappImportProblem{}}
	section := ""
	privateKey, publicKey := false, false
	for _, line := range strings.Split(string(raw), "\n") {
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.ToLower(strings.TrimSpace(line[1 : len(line)-1]))
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		switch section {
		case "interface":
			switch key {
			case "privatekey":
				privateKey = privateKey || val != ""
			case "address":
				p.Addresses = append(p.Addresses, miniappConfList(val)...)
			case "dns":
				p.DNS = append(p.DNS, miniappConfList(val)...)
			case "mtu":
				if n, err := strconv.Atoi(val); err == nil && n > 0 && n <= 65535 {
					p.MTU = n
				}
			}
		case "peer":
			switch key {
			case "publickey":
				publicKey = publicKey || val != ""
			case "endpoint":
				if p.Endpoint == "" {
					p.Endpoint = val
				}
			}
		}
	}
	if !privateKey || !publicKey || p.Endpoint == "" {
		return miniappImportPreview{}, false
	}
	return p, true
}

func miniappConfList(val string) []string {
	out := []string{}
	for _, part := range strings.Split(val, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// miniappImportEntry -- предпросмотр в памяти. conf -- байты файла, наружу
// не уходят никогда.
type miniappImportEntry struct {
	routerID int64
	tgUser   int64
	name     string
	conf     []byte
	preview  miniappImportPreview
	state    string
	analyzed bool
	note     string
	expires  time.Time
}

type miniappImportPreviews struct {
	mu  sync.Mutex
	ttl time.Duration
	now func() time.Time
	m   map[string]*miniappImportEntry
}

func newMiniappImportPreviews(ttl time.Duration, now func() time.Time) *miniappImportPreviews {
	return &miniappImportPreviews{ttl: ttl, now: now, m: map[string]*miniappImportEntry{}}
}

// put кладёт предпросмотр и отдаёт токен. У человека на роутере предпросмотр
// один: новый вытесняет прежний -- так память ограничена числом людей, а
// не числом нажатий.
func (s *miniappImportPreviews) put(e miniappImportEntry) (string, error) {
	token, err := newCmdID()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, old := range s.m {
		if !now.Before(old.expires) || (old.routerID == e.routerID && old.tgUser == e.tgUser) {
			delete(s.m, k)
		}
	}
	e.expires = now.Add(s.ttl)
	s.m[token] = &e
	return token, nil
}

func (s *miniappImportPreviews) lookupLocked(token string, routerID, tgUser int64) (*miniappImportEntry, bool) {
	e, ok := s.m[token]
	if !ok || e.routerID != routerID || e.tgUser != tgUser {
		return nil, false
	}
	if !s.now().Before(e.expires) {
		delete(s.m, token)
		return nil, false
	}
	return e, true
}

func (s *miniappImportPreviews) get(token string, routerID, tgUser int64) (miniappImportEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.lookupLocked(token, routerID, tgUser)
	if !ok {
		return miniappImportEntry{}, false
	}
	return *e, true
}

// take -- одноразовое подтверждение: предпросмотр забирается целиком.
func (s *miniappImportPreviews) take(token string, routerID, tgUser int64) (miniappImportEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.lookupLocked(token, routerID, tgUser)
	if !ok {
		return miniappImportEntry{}, false
	}
	delete(s.m, token)
	return *e, true
}

// restore возвращает предпросмотр, если команда не встала в очередь:
// человек повторит нажатие, а не загрузит файл заново.
func (s *miniappImportPreviews) restore(token string, e miniappImportEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[token] = &e
}

func (s *miniappImportPreviews) setAnalysis(token string, e miniappImportEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.m[token]; ok {
		cur.preview, cur.state, cur.analyzed, cur.note = e.preview, e.state, e.analyzed, e.note
	}
}

type miniappAnalyzeIssue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func miniappImportProblemFrom(severity string, is miniappAnalyzeIssue) miniappImportProblem {
	msg := strings.TrimSpace(is.Message)
	if msg == "" {
		msg = is.Code
	}
	return miniappImportProblem{Severity: severity, Code: is.Code, Message: msg}
}

// miniappImportAnalyze доводит предпросмотр до «готово», если роутер уже
// ответил на анализ. Молчание роутера -- «в процессе»; отказ, старая панель
// или ошибка -- «готово» без анализа, со словами (как в мастере замены:
// проверить нечем -- не значит, что конфиг плох).
func miniappImportAnalyze(ctx context.Context, d Deps, previews *miniappImportPreviews, questions *miniappAgentQuestions, token string, e miniappImportEntry) miniappImportEntry {
	if e.state == miniappImportReady {
		return e
	}
	res, answered, err := questions.ask(ctx, d.CommandSink, e.routerID, "tunnel_analyze/"+token, "tunnel_analyze",
		map[string]any{"conf": base64.StdEncoding.EncodeToString(e.conf)})
	switch {
	case err != nil:
		miniappCabinetLogger(d).Warn("импорт .conf: анализ не запрошен", "router_id", e.routerID, "err", err)
		e.state, e.note = miniappImportReady, miniappImportNoteFailed
	case !answered:
		return e
	case res.Status != "ok":
		e.state, e.note = miniappImportReady, miniappImportNoteFailed
	default:
		var out struct {
			Supported bool                  `json:"supported"`
			Errors    []miniappAnalyzeIssue `json:"errors"`
			Warnings  []miniappAnalyzeIssue `json:"warnings"`
		}
		if json.Unmarshal([]byte(res.Output), &out) != nil || !out.Supported {
			e.state, e.note = miniappImportReady, miniappImportNoteOldPanel
			break
		}
		problems := append([]miniappImportProblem{}, e.preview.Problems...)
		for _, is := range out.Errors {
			problems = append(problems, miniappImportProblemFrom("error", is))
		}
		for _, is := range out.Warnings {
			problems = append(problems, miniappImportProblemFrom("warning", is))
		}
		e.preview.Problems = problems
		e.state, e.analyzed = miniappImportReady, true
	}
	previews.setAnalysis(token, e)
	return e
}

func miniappImportRespFrom(token string, e miniappImportEntry) miniappImportResp {
	can := e.state == miniappImportReady
	for _, p := range e.preview.Problems {
		if p.Severity == "error" {
			can = false
		}
	}
	return miniappImportResp{Token: token, Name: e.name, State: e.state, Analyzed: e.analyzed, Note: e.note, CanConfirm: can, Preview: e.preview}
}

// miniappAgentCanAnalyze -- знает ли агент tunnel_analyze. Нечитаемая версия
// не повод пропускать проверку: старый агент ответит ошибкой, и шаг
// пропустится со словами.
func miniappAgentCanAnalyze(version string) bool {
	if _, ok := agentSemver(version); !ok {
		return true
	}
	return agentAtLeast(version, miniappAnalyzeMinAgent)
}

// miniappTunnelNameTaken -- называл ли роутер это имя в отчётах последних
// десяти минут. Удобство, а не граница: окно короткое, чтобы имя давно
// удалённого VPN-туннеля не считалось занятым.
func miniappTunnelNameTaken(d Deps, routerID int64, name string) bool {
	rows, err := d.DB.Events().LatestEventsByPrefixSince(routerID, miniappTunnelPrefix, time.Now().UTC().Add(-miniappImportNameWindow))
	if err != nil {
		return false
	}
	for _, row := range rows {
		if tu, ok := miniappTunnelFromEvent(row); ok && strings.EqualFold(strings.TrimSpace(tu.Name), name) {
			return true
		}
	}
	return false
}

func decodeMiniappImportBody(w http.ResponseWriter, r *http.Request, dst *miniappTunnelImportReq) bool {
	if ct := strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]); ct != "" && !strings.EqualFold(ct, "application/json") {
		writeMiniappTunnelError(w, http.StatusUnsupportedMediaType, errCodeUnsupportedCT)
		return false
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, miniappImportMaxBody)).Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeMiniappTunnelError(w, http.StatusBadRequest, "conf_too_large")
		} else {
			writeMiniappTunnelError(w, http.StatusBadRequest, errCodeBadJSON)
		}
		return false
	}
	return true
}

func miniappTunnelImportHandler(d Deps, previews *miniappImportPreviews, questions *miniappAgentQuestions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := miniappCabinetRouter(d, w, r, miniappCabinetOwner)
		if !ok {
			return
		}
		if d.CommandSink == nil {
			writeMiniappTunnelError(w, http.StatusServiceUnavailable, "commands_not_configured")
			return
		}
		var req miniappTunnelImportReq
		if !decodeMiniappImportBody(w, r, &req) {
			return
		}
		name := strings.ToLower(strings.TrimSpace(req.Name))
		if !miniappImportNameRe.MatchString(name) {
			writeMiniappTunnelError(w, http.StatusBadRequest, "invalid_name")
			return
		}
		conf, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.ConfB64))
		if err != nil || len(conf) == 0 {
			writeMiniappTunnelError(w, http.StatusBadRequest, "invalid_conf")
			return
		}
		if len(conf) > miniappImportMaxConf {
			writeMiniappTunnelError(w, http.StatusBadRequest, "conf_too_large")
			return
		}
		preview, ok := parseMiniappImportConf(conf)
		if !ok {
			writeMiniappTunnelError(w, http.StatusBadRequest, "invalid_conf")
			return
		}
		if miniappTunnelNameTaken(d, u.ID, name) {
			writeMiniappTunnelError(w, http.StatusConflict, "name_taken")
			return
		}
		tgUser, _ := miniappUserFromContext(r.Context())
		entry := miniappImportEntry{routerID: u.ID, tgUser: tgUser, name: name, conf: conf, preview: preview, state: miniappImportAnalyzing}
		version := ""
		if u.LastDeployedVersion != nil {
			version = *u.LastDeployedVersion
		}
		if !miniappAgentCanAnalyze(version) {
			entry.state, entry.note = miniappImportReady, miniappImportNoteOldAgent
		}
		token, err := previews.put(entry)
		if err != nil {
			writeMiniappTunnelError(w, http.StatusInternalServerError, errCodeInternal)
			return
		}
		entry = miniappImportAnalyze(r.Context(), d, previews, questions, token, entry)
		miniappCabinetLogger(d).Info("miniapp tunnel import preview",
			"router_id", u.ID, "nickname", u.Nickname, "name", name, "bytes", len(conf), "state", entry.state, "analyzed", entry.analyzed)
		writeMiniappCabinetJSON(w, http.StatusOK, miniappImportRespFrom(token, entry))
	}
}

func miniappTunnelImportPreviewHandler(d Deps, previews *miniappImportPreviews, questions *miniappAgentQuestions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := miniappCabinetRouter(d, w, r, miniappCabinetOwner)
		if !ok {
			return
		}
		if d.CommandSink == nil {
			writeMiniappTunnelError(w, http.StatusServiceUnavailable, "commands_not_configured")
			return
		}
		tgUser, _ := miniappUserFromContext(r.Context())
		token := strings.TrimSpace(r.PathValue("token"))
		entry, ok := previews.get(token, u.ID, tgUser)
		if !ok {
			writeMiniappTunnelError(w, http.StatusGone, "preview_expired")
			return
		}
		entry = miniappImportAnalyze(r.Context(), d, previews, questions, token, entry)
		writeMiniappCabinetJSON(w, http.StatusOK, miniappImportRespFrom(token, entry))
	}
}

func miniappTunnelImportConfirmHandler(d Deps, previews *miniappImportPreviews, questions *miniappAgentQuestions) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := miniappCabinetRouter(d, w, r, miniappCabinetOwner)
		if !ok {
			return
		}
		if d.CommandSink == nil {
			writeMiniappTunnelError(w, http.StatusServiceUnavailable, "commands_not_configured")
			return
		}
		var req struct {
			Token string `json:"token"`
		}
		if !decodeMiniappCabinetBody(w, r, &req) {
			return
		}
		tgUser, _ := miniappUserFromContext(r.Context())
		token := strings.TrimSpace(req.Token)
		entry, ok := previews.get(token, u.ID, tgUser)
		if !ok {
			writeMiniappTunnelError(w, http.StatusGone, "preview_expired")
			return
		}
		entry = miniappImportAnalyze(r.Context(), d, previews, questions, token, entry)
		if entry.state != miniappImportReady {
			writeMiniappTunnelError(w, http.StatusConflict, "preview_not_ready")
			return
		}
		if !miniappImportRespFrom(token, entry).CanConfirm {
			writeMiniappTunnelError(w, http.StatusConflict, "conf_rejected")
			return
		}
		entry, ok = previews.take(token, u.ID, tgUser)
		if !ok {
			writeMiniappTunnelError(w, http.StatusGone, "preview_expired")
			return
		}
		cmdID, err := newCmdID()
		if err != nil {
			previews.restore(token, entry)
			writeMiniappTunnelError(w, http.StatusInternalServerError, errCodeInternal)
			return
		}
		cmd := wire.Command{
			ID:     cmdID,
			Action: "tunnel_import",
			Args: map[string]any{
				"conf":    base64.StdEncoding.EncodeToString(entry.conf),
				"name":    entry.name,
				"replace": false,
				"backend": "nativewg",
			},
			IssuedAt: time.Now().UTC(),
		}
		if err := d.CommandSink.Enqueue(u.ID, cmd); err != nil {
			previews.restore(token, entry)
			miniappCabinetLogger(d).Warn("импорт .conf: команда не встала в очередь", "router_id", u.ID, "err", err)
			writeMiniappTunnelError(w, http.StatusInternalServerError, errCodeInternal)
			return
		}
		resp := miniappTunnelStateResp{State: "queued", CmdID: cmdID, TunnelName: entry.name}
		resp.RouterAsleep, resp.RouterStatus, resp.WakeWindowMin = miniappWakeWindow(d, u, "tunnel_import", time.Now().UTC())
		miniappCabinetLogger(d).Info("miniapp tunnel import queued", "router_id", u.ID, "nickname", u.Nickname, "name", entry.name, "cmd_id", cmdID)
		writeMiniappCabinetJSON(w, http.StatusAccepted, resp)
	}
}
