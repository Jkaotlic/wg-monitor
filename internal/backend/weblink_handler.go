package backend

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// Вход в веб-управление по личной ссылке.
//
// Зачем он есть: единственный способ войти в дашборд -- вставить в форму
// общий токен. С телефона это неудобно настолько, что токен начинают носить
// в переписке, а он не истекает никогда. Ссылка живёт 12 часов, адресована
// одному человеку, отзывается по одной и оставляет след на каждый обмен.
//
// Кому: только админу. У дашборда нет разграничения по владельцам -- он
// управляет всем парком, включая провижининг с root-паролем, поэтому у
// владельца роутера такая ссылка означала бы доступ ко всему чужому.
const (
	// Срок сказан человеку словами в webLinkCopyNotice -- менять его здесь,
	// не поменяв текст, нельзя: тест сверяет одно с другим.
	webLinkTTL = 12 * time.Hour
	// 32 байта случайности: перебор по такому значению не строится, а
	// длина ссылки остаётся годной для переписки.
	webLinkTokenBytes = 32

	webLinkCopyNotice       = "Ссылка личная и живёт 12 часов. Не пересылайте её: по ней всё это время открывается управление всем парком."
	webLinkCopyDead         = "Ссылка больше не действует — попросите новую."
	webLinkCopyAdminOnly    = "Эта команда только для админа."
	webLinkCopyNoPublicBase = "Публичный адрес не настроен по https — ссылку выдать нельзя."
	webLinkCopyLimit        = "Живыми остаются три последние ссылки: выдали новую — самая старая перестала работать."
)

// webLinkNow -- часы выдачи и обмена. Отдельные от dashboardNow: срок
// ссылки и срок сессии дашборда совпадают числом, но это разные сроки.
var webLinkNow = time.Now

// ErrWebLinkNoPublicBase -- публичного адреса по https нет, значит ссылке
// некуда вести. Отдельная ошибка, потому что отвечать на неё надо словами,
// а не «внутренняя ошибка».
var ErrWebLinkNoPublicBase = errors.New("web-link: публичный адрес не настроен по https")

// WebLinkGrant -- выданная ссылка и всё, что о ней надо сказать человеку.
type WebLinkGrant struct {
	URL         string
	ExpiresAt   time.Time
	Notice      string
	LimitNotice string
}

type webLinkIssueResp struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
	// Notice и LimitNotice -- текст для человека. Срок жизни обязан быть
	// сказан в ответе, а не спрятан в коде: ссылку пересылают именно тогда,
	// когда не знают, сколько она живёт.
	Notice      string `json:"notice"`
	LimitNotice string `json:"limit_notice"`
}

// newWebLinkToken возвращает случайное значение и его sha256. Наружу уходит
// первое, в базу ложится второе -- образец тот же, что у токенов агентов.
func newWebLinkToken() (raw, hash string, err error) {
	b := make([]byte, webLinkTokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	raw = hex.EncodeToString(b)
	return raw, webLinkHash(raw), nil
}

func webLinkHash(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// webLinkHashPrefix -- то, что можно писать в журнал: по префиксу хеша два
// обмена можно связать между собой, а войти по нему нельзя.
func webLinkHashPrefix(raw string) string {
	return webLinkHash(raw)[:12]
}

// webLinkContains -- сравнение постоянного времени по всему списку. Выход
// «нашли -- прекращаем» здесь запрещён: он превращает время ответа в
// подсказку о том, насколько предъявленное значение похоже на живое.
func webLinkContains(hashes []string, hash string) bool {
	found := 0
	for _, h := range hashes {
		found |= subtle.ConstantTimeCompare([]byte(h), []byte(hash))
	}
	return found == 1
}

// IssueWebLink выдаёт человеку личную ссылку на веб-управление.
//
// Экспортирована ради бота: кнопка «Открыть в браузере» живёт в хабе /panel
// (пакет callbacks), и второй копии выдачи там быть не должно.
func IssueWebLink(database *db.DB, telegramUserID int64, publicBaseURL string, logger *slog.Logger) (WebLinkGrant, error) {
	base := strings.TrimRight(strings.TrimSpace(publicBaseURL), "/")
	// Только https: ссылка несёт вход в систему, и по http её увидит любой
	// узел на пути.
	if !strings.HasPrefix(strings.ToLower(base), "https://") {
		return WebLinkGrant{}, ErrWebLinkNoPublicBase
	}
	if database == nil {
		return WebLinkGrant{}, errors.New("web-link: база не подключена")
	}
	raw, hash, err := newWebLinkToken()
	if err != nil {
		return WebLinkGrant{}, err
	}
	expiresAt := webLinkNow().UTC().Add(webLinkTTL)
	if err := database.WebLinks().Issue(hash, telegramUserID, expiresAt); err != nil {
		return WebLinkGrant{}, err
	}
	if logger != nil {
		// В журнале -- факт, получатель и префикс хеша. Самого гранта здесь
		// нет и быть не может.
		logger.Info("веб-ссылка выдана",
			"telegram_user_id", telegramUserID,
			"token_hash_prefix", hash[:12],
			"expires_at", expiresAt.Format(time.RFC3339),
		)
	}
	return WebLinkGrant{
		// Фрагмент, а не query: он не уходит ни в Referer, ни в журнал
		// сервера, а страница входа стирает его из адресной строки сразу
		// после обмена.
		URL:         base + "/dashboard/login#token=" + raw,
		ExpiresAt:   expiresAt,
		Notice:      webLinkCopyNotice,
		LimitNotice: webLinkCopyLimit,
	}, nil
}

// webLinkIssueHandler -- POST /v1/miniapp/web-link. Только админу.
//
// Отказ -- 404, а не 403: так ведут себя роутерные эндпоинты мини-аппа, и по
// коду ответа нельзя понять, существует ли поверхность вообще.
func webLinkIssueHandler(d Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		telegramUserID, _ := miniappUserFromContext(r.Context())
		if !miniappIsAdmin(telegramUserID, d.TelegramAdminUserID) {
			writeJSONError(w, http.StatusNotFound, "not_found", "not found")
			return
		}
		grant, err := IssueWebLink(d.DB, telegramUserID, d.PublicBaseURL, d.Logger)
		if errors.Is(err, ErrWebLinkNoPublicBase) {
			writeJSONError(w, http.StatusConflict, "public_base_url_missing", webLinkCopyNoPublicBase)
			return
		}
		if err != nil {
			if d.Logger != nil {
				d.Logger.Error("веб-ссылка: выдать не удалось", "telegram_user_id", telegramUserID, "err", err)
			}
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "не удалось выдать ссылку")
			return
		}
		// Ответ несёт секрет: ни кэшу, ни релею его хранить не нужно.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(webLinkIssueResp{
			URL:         grant.URL,
			ExpiresAt:   grant.ExpiresAt.Format(time.RFC3339),
			Notice:      grant.Notice,
			LimitNotice: grant.LimitNotice,
		})
	}
}

// webLinkRedeemHandler -- POST /v1/dashboard/web-link/redeem.
//
// Предъявление POST'ом, а не переходом по GET: значение лежит во фрагменте
// адреса, страница входа достаёт его сама и тут же стирает. Так грант не
// попадает ни в журнал сервера, ни в Referer соседних запросов.
//
// Отказ один на все причины: просроченная, отозванная, чужая и выдуманная
// ссылка снаружи неразличимы. Разные причины идут в журнал -- это канал
// оператора, а не подсказка подбирающему.
func webLinkRedeemHandler(d Deps) http.HandlerFunc {
	type redeemReq struct {
		Token string `json:"token"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireJSONContentType(w, r) {
			return
		}
		var req redeemReq
		if !decodeWizardJSON(w, r, &req) {
			return
		}
		remote := remoteRateKey(r.RemoteAddr)
		refuse := func(reason string, token string) {
			if d.Logger != nil {
				prefix := ""
				if token != "" {
					prefix = webLinkHashPrefix(token)
				}
				d.Logger.Warn("веб-ссылка: вход отклонён",
					"reason", reason,
					"remote", remote,
					"token_hash_prefix", prefix,
					"presented_len", len(token),
				)
			}
			writeJSONError(w, http.StatusUnauthorized, "unauthorized", webLinkCopyDead)
		}

		token := strings.TrimSpace(req.Token)
		if token == "" {
			refuse("empty_token", "")
			return
		}
		if d.DB == nil {
			refuse("db_not_configured", token)
			return
		}
		// Адресат сверяется с админом из конфига на КАЖДОМ обмене: смена
		// админа гасит все ссылки прежнего, и отдельного механизма отзыва
		// «по смене роли» заводить не нужно.
		admin := d.TelegramAdminUserID
		if admin == 0 {
			refuse("admin_not_configured", token)
			return
		}
		now := webLinkNow().UTC()
		hash := webLinkHash(token)
		live, err := d.DB.WebLinks().ActiveFor(admin, now)
		if err != nil {
			if d.Logger != nil {
				d.Logger.Error("веб-ссылка: чтение грантов не удалось", "err", err)
			}
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "не удалось проверить ссылку")
			return
		}
		if !webLinkContains(live, hash) {
			// Нулевое время -- «все гранты этого человека, включая
			// просроченные»: только так «просрочена» отличается от «такой
			// ссылки нет» в журнале. Ссылка прежнего админа сюда не попадает
			// намеренно -- для нынешнего админа она и есть «неизвестная».
			all, aerr := d.DB.WebLinks().ActiveFor(admin, time.Time{})
			switch {
			case aerr != nil:
				refuse("lookup_failed", token)
			case webLinkContains(all, hash):
				refuse("link_expired", token)
			default:
				refuse("link_unknown", token)
			}
			return
		}
		rows, err := d.DB.WebLinks().Redeem(hash, now, remote)
		if err != nil {
			if d.Logger != nil {
				d.Logger.Error("веб-ссылка: запись обмена не удалась", "err", err)
			}
			writeJSONError(w, http.StatusInternalServerError, errCodeInternal, "не удалось проверить ссылку")
			return
		}
		if rows == 0 {
			// Между проверкой и записью грант успели отозвать или он истёк.
			refuse("link_expired", token)
			return
		}
		if d.Logger != nil {
			// Единственный момент, когда у дашборда есть личность: кто и
			// откуда вошёл. При многоразовой ссылке пишется КАЖДЫЙ обмен --
			// это и есть улика, которой оплачена снятая одноразовость.
			d.Logger.Info("веб-ссылка: вход состоялся",
				"telegram_user_id", admin,
				"token_hash_prefix", hash[:12],
				"remote", remote,
				"use_count_added", rows,
			)
		}
		// Та же cookie, что у обычного входа по токену: второго механизма
		// сессии не заводится, «выйти везде» продолжает гасить и этот вход.
		http.SetCookie(w, dashboardSessionCookie(r, d.DashboardToken))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(struct {
			OK bool `json:"ok"`
		}{OK: true})
	}
}
