package checks

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/agent/redact"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// v0.65: слово awg-manager на карточке туннеля («Нет связи», «Активен»)
// человек хочет видеть рядом с вердиктом бота -- они расходятся (router4car4
// 02.11: роутер писал «Нет связи», бот -- «всё ок»). Агент шлёт только сырьё,
// слово считает мини-апп: правило правится раскаткой бэкенда, без агентов.
// Вердикт проверки от этих полей НЕ зависит: проверка связи awg-manager не
// замерена на парке, и тревоги по ней были бы новым источником ложных.

// awgmConnTimeout -- срок на все вопросы о связи за обход (и, значит, на
// каждый). У мёртвого туннеля awg-manager ждёт ответа сам; срок на каждый
// вопрос по отдельности при пяти мёртвых туннелях растянул бы обход вдвое.
// 8 с, а не 10: срок всей проверки (perCheckTimeout) -- те же 10 с.
// var, а не const: тест сжимает его до долей секунды.
var awgmConnTimeout = 8 * time.Second

// awgmConnMargin -- запас до срока всей проверки (perCheckTimeout у
// репортёра): после ответов о связи проверке ещё дописать поля и отдать
// выдачу; вопросы, съевшие весь её срок, уронили бы её целиком.
const awgmConnMargin = 2 * time.Second

// awgmConnParallel -- сколько вопросов разом. Каждый у awg-manager -- живая
// проба через туннель; десяток одновременно на слабом роутере -- нагрузка.
const awgmConnParallel = 4

// awgmTextRunes -- потолок длины причины и пояснения: это строка экрана, а не
// журнал.
const awgmTextRunes = 120

// awgmConn -- ответ на вопрос о связи одного туннеля.
type awgmConn struct {
	state   string // connected | disconnected | error
	latency *int
	reason  string
}

// awgmConnAsked -- у кого UI awg-manager сам спрашивает связь: только у
// running и broken. Спросить больше -- показать то, чего на карточке нет.
func awgmConnAsked(tu awgmgr.Tunnel) bool {
	switch strings.ToLower(strings.TrimSpace(tu.Status)) {
	case "running", "broken":
		return true
	}
	return false
}

// collectAwgmConn спрашивает связь у подходящих туннелей, не больше
// awgmConnParallel разом, один раз за обход, и все вместе -- не дольше
// awgmConnTimeout и не позже срока ctx минус awgmConnMargin. Не успевший
// вопрос -- error («awg-manager не ответил»). Сборка без эндпоинта (404) --
// туннеля в ответе нет: «не спрашивали», а не «нет связи».
func collectAwgmConn(ctx context.Context, c *awgmgr.Client, tunnels []awgmgr.Tunnel) map[string]awgmConn {
	out := map[string]awgmConn{}
	budget := awgmConnTimeout
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl) - awgmConnMargin; left < budget {
			budget = left
		}
	}
	ctx, cancelAll := context.WithTimeout(ctx, budget)
	defer cancelAll()
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, awgmConnParallel)
	)
	for _, tu := range tunnels {
		if !tunnelTypeChecked(tu) || !awgmConnAsked(tu) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			res, err := c.TestConnectivity(ctx, id)
			var conn awgmConn
			switch {
			case errors.Is(err, awgmgr.ErrUnsupportedByRouter):
				return
			case err != nil:
				conn.state = "error"
			case res.Connected:
				conn.state, conn.latency = "connected", res.Latency
			default:
				conn.state, conn.reason = "disconnected", res.Reason
			}
			mu.Lock()
			out[id] = conn
			mu.Unlock()
		}(tu.ID)
	}
	wg.Wait()
	return out
}

// annotateAwgmConns -- отдельный проход по готовым проверкам туннелей после
// вердиктов: сырьё awg-manager только дописывается к деталям.
func annotateAwgmConns(out []wire.Check, tunnels []awgmgr.Tunnel, conns map[string]awgmConn) {
	byID := make(map[string]awgmgr.Tunnel, len(tunnels))
	for _, tu := range tunnels {
		byID[tu.ID] = tu
	}
	for i := range out {
		id, ok := strings.CutPrefix(out[i].Name, tunnelCheckPrefix)
		if !ok || id == "" {
			continue
		}
		tu, ok := byID[id]
		if !ok {
			continue
		}
		conn, asked := conns[id]
		annotateAwgmConn(&out[i], tu, conn, asked)
	}
}

// annotateAwgmConn кладёт сырьё в детали проверки туннеля. Пустое не пишется:
// нет ключа -- «не спрашивали», и экран обязан сказать именно это.
func annotateAwgmConn(chk *wire.Check, tu awgmgr.Tunnel, conn awgmConn, asked bool) {
	if chk.Details == nil {
		chk.Details = map[string]any{}
	}
	if sd := awgmText(tu.StatusDetails); sd != "" {
		chk.Details["awgm_status_details"] = sd
	}
	if !asked {
		return
	}
	chk.Details["awgm_conn"] = conn.state
	if conn.state == "connected" && conn.latency != nil {
		chk.Details["awgm_conn_latency_ms"] = *conn.latency
	}
	if r := awgmText(conn.reason); r != "" {
		chk.Details["awgm_conn_reason"] = r
	}
}

// awgmText -- текст awg-manager для экрана: без адресов (redact) и не длиннее
// awgmTextRunes, считая «…».
func awgmText(s string) string {
	s = strings.TrimSpace(redact.Text(s))
	if utf8.RuneCountInString(s) <= awgmTextRunes {
		return s
	}
	return string([]rune(s)[:awgmTextRunes-1]) + "…"
}

// tunnelTypeChecked -- тот же фильтр, что у выпуска проверок: чужие типы
// (не awg/wg) проверка пропускает, спрашивать о них незачем.
func tunnelTypeChecked(tu awgmgr.Tunnel) bool {
	return tu.Type == "" || tu.Type == "awg" || tu.Type == "wg"
}
