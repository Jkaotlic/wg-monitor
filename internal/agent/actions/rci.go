package actions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/agent/keenetic"
)

// RCIFunc -- один запрос к локальному REST-интерфейсу KeenOS (RCI).
// path -- вида "/rci/components/list"; ответ -- тело целиком.
//
// Зачем он рядом с ExecFunc: `ndmc -c` живёт ровно одну команду, а часть
// команд прошивки -- «продолжаемые»: задача живёт, пока жива сессия CLI.
// `ndmc -c "components commit"` выходит сразу, и KeenOS в ту же секунду рвёт
// установку (в журнале -- сбивающее с толку «Ndss: cannot connect»). RCI
// сессии не держит: задача остаётся жить в самом роутере.
type RCIFunc func(ctx context.Context, method, path string, body []byte) ([]byte, error)

// ErrRCIUnreachable -- RCI на этом роутере нет: порт никто не слушает или
// пути нет (404). Вызывающий вправе уйти на старый путь ndmc.
// Любая другая ошибка RCI -- это ответ роутера, а не его отсутствие.
var ErrRCIUnreachable = errors.New("rci unreachable")

// ErrRCIRejected -- роутер ответил, но запрос не принял (HTTP 4xx, кроме
// 404): команда не выполнялась. Это отказ, а не повод идти в ndmc.
var ErrRCIRejected = errors.New("rci rejected the request")

const (
	// rciLoopbackBase -- RCI на самом роутере: с петли вход не требуется.
	rciLoopbackBase = "http://127.0.0.1:79"
	// rciMaxAnswerBytes -- потолок ответа. `components/list` весит ~260 КБ.
	rciMaxAnswerBytes = 2 << 20
	rciTimeout        = 15 * time.Second
)

// DefaultRCI ходит в RCI самого роутера по петле.
var DefaultRCI = newRCIClient(rciLoopbackBase)

// newRCIClient собирает клиента к base. Адрес задаётся один раз здесь, а не
// приходит с командой: из команды берётся только путь, и только под /rci/.
func newRCIClient(base string) RCIFunc {
	client := &http.Client{
		Timeout: rciTimeout,
		// Перенаправление с петли увело бы запрос на чужой адрес.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy:                 nil, // петля, прокси из окружения не нужен
			DialContext:           (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
			ResponseHeaderTimeout: rciTimeout,
			DisableKeepAlives:     true,
		},
	}
	return func(ctx context.Context, method, p string, body []byte) ([]byte, error) {
		if !validRCIPath(p) {
			return nil, fmt.Errorf("rci: bad path %q", keenetic.Excerpt(p, 1))
		}
		req, err := http.NewRequestWithContext(ctx, method, base+p, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("rci %s: %w", p, err)
		}
		if len(body) > 0 {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			// Отмена и срок -- не «RCI нет»: иначе оборванная по времени команда
			// ушла бы вторым запуском в ndmc.
			if ctx.Err() != nil {
				return nil, fmt.Errorf("rci %s: %w", p, ctx.Err())
			}
			// «RCI нет» -- только когда порт никто не слушает.
			var op *net.OpError
			if errors.As(err, &op) && op.Op == "dial" && errors.Is(err, syscall.ECONNREFUSED) {
				return nil, fmt.Errorf("rci %s: %w: connection refused", p, ErrRCIUnreachable)
			}
			return nil, fmt.Errorf("rci %s: %w", p, err)
		}
		defer resp.Body.Close()
		// Неизвестный путь RCI отвечает 404 с пустым телом (роутер, 02.10):
		// на этой прошивке такой команды в RCI нет.
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("rci %s: HTTP %d: %w", p, resp.StatusCode, ErrRCIUnreachable)
		}
		if resp.StatusCode >= 400 && resp.StatusCode <= 499 {
			return nil, fmt.Errorf("rci %s: HTTP %d: %w", p, resp.StatusCode, ErrRCIRejected)
		}
		if resp.StatusCode < 200 || resp.StatusCode > 299 {
			// Тело в ошибку не кладём: у RCI оно бывает в сотни килобайт.
			return nil, fmt.Errorf("rci %s: HTTP %d", p, resp.StatusCode)
		}
		out, err := io.ReadAll(io.LimitReader(resp.Body, rciMaxAnswerBytes+1))
		if err != nil {
			return nil, fmt.Errorf("rci %s: read answer: %w", p, err)
		}
		if len(out) > rciMaxAnswerBytes {
			return nil, fmt.Errorf("rci %s: answer too large (over %d bytes)", p, rciMaxAnswerBytes)
		}
		return out, nil
	}
}

// validRCIPath пускает только чистый путь под /rci/: без запроса, якоря,
// «..» и двойных слэшей, которыми путь превращается в другой адрес.
func validRCIPath(p string) bool {
	if !strings.HasPrefix(p, "/rci/") || strings.ContainsAny(p, "?#\\ \r\n") {
		return false
	}
	return path.Clean(p) == p
}

// rciStatus -- запись из массива status ответа RCI.
type rciStatus struct {
	Status  string `json:"status"`
	Code    string `json:"code"`
	Ident   string `json:"ident"`
	Message string `json:"message"`
}

// rciAnswer -- общая часть ответа RCI. Status приходит массивом, а на
// некоторых командах одним объектом -- разбираем оба вида.
type rciAnswer struct {
	Continued bool            `json:"continued"`
	Status    json.RawMessage `json:"status"`
}

func (a rciAnswer) statuses() []rciStatus {
	if len(a.Status) == 0 {
		return nil
	}
	var many []rciStatus
	if json.Unmarshal(a.Status, &many) == nil {
		return many
	}
	var one rciStatus
	if json.Unmarshal(a.Status, &one) == nil {
		return []rciStatus{one}
	}
	return nil
}

// errorText -- текст отказа из ответа RCI («Ident: message»), выдержкой не
// длиннее keenetic.ExcerptMaxRunes; пусто, если отказа нет.
func (a rciAnswer) errorText() string {
	var msgs []string
	for _, st := range a.statuses() {
		if st.Status != "error" {
			continue
		}
		m := strings.TrimSpace(st.Message)
		if m == "" {
			m = "error " + st.Code
		}
		if st.Ident != "" {
			m = st.Ident + ": " + m
		}
		msgs = append(msgs, m)
	}
	return keenetic.Excerpt(strings.Join(msgs, "\n"), 3)
}

func (a rciAnswer) hasMessage(sub string) bool {
	for _, st := range a.statuses() {
		if st.Status != "error" && strings.Contains(st.Message, sub) {
			return true
		}
	}
	return false
}
