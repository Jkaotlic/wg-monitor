package awgmgr

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// IPCheck -- /api/test/ip (сверка С1, awg-manager 2.19.9: 200 за ~1,2 с).
type IPCheck struct {
	DirectIP   string `json:"directIp"`
	VPNIP      string `json:"vpnIp"`
	EndpointIP string `json:"endpointIp"`
	IPChanged  bool   `json:"ipChanged"`
}

// TestIP сравнивает адрес выхода через VPN-туннель с адресом голого WAN.
// service не передаётся никогда: awg-manager ждёт в нём URL сервиса, метка
// даёт 400 IP_CHECK_FAILED (С1), а умолчание его устраивает.
func (c *Client) TestIP(ctx context.Context, tunnelID string) (*IPCheck, error) {
	var env Envelope[IPCheck]
	if err := c.get(ctx, "/api/test/ip?id="+url.QueryEscape(tunnelID), &env); err != nil {
		if IsEndpointMissing(err) {
			return nil, fmt.Errorf("%w: проверка адреса выхода появилась в awg-manager позже", ErrUnsupportedByRouter)
		}
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr test/ip: success=false")
	}
	return &env.Data, nil
}

// PingCheckLogEntry -- одна проба пингчека. Timestamp строкой: пустое время
// у старой сборки не должно ронять разбор всего ответа.
type PingCheckLogEntry struct {
	Timestamp   string  `json:"timestamp"`
	TunnelID    string  `json:"tunnelId"`
	TunnelName  string  `json:"tunnelName"`
	Success     bool    `json:"success"`
	Latency     float64 `json:"latency"`
	FailCount   int     `json:"failCount"`
	Threshold   int     `json:"threshold"`
	StateChange string  `json:"stateChange"`
	Error       string  `json:"error"`
	Backend     string  `json:"backend"`
}

// Time разбирает Timestamp (RFC3339 с дробной частью или без).
func (e PingCheckLogEntry) Time() (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(e.Timestamp))
	return t.UTC(), err == nil
}

// pingCheckLogsLimit -- потолок тела: ~163 записи на VPN-туннель (С1), по
// ~250 байт; 4 МиБ хватает на сотню VPN-туннелей.
const pingCheckLogsLimit = 4 << 20

// PingCheckLogs -- журнал проб всех VPN-туннелей сразу (без tunnelId), свежие
// сверху. Параметра limit у API нет (С1): буфер ~2 часа отдаётся целиком.
func (c *Client) PingCheckLogs(ctx context.Context) ([]PingCheckLogEntry, error) {
	var env Envelope[[]PingCheckLogEntry]
	if err := c.getLimited(ctx, "/api/pingcheck/logs", &env, pingCheckLogsLimit); err != nil {
		if IsEndpointMissing(err) {
			return nil, fmt.Errorf("%w: журнал проверки связи появился в awg-manager позже", ErrUnsupportedByRouter)
		}
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr pingcheck/logs: success=false")
	}
	return env.Data, nil
}

// LogsQuery -- фильтры журнала. Поля sanitize здесь нет и не будет:
// немаскированный журнал агент не умеет даже попросить.
type LogsQuery struct {
	Level string
	Group string
	Limit int
}

// LogsPage -- /api/logs .data (С1: массив называется logs).
type LogsPage struct {
	Enabled   bool       `json:"enabled"`
	Logs      []LogEntry `json:"logs"`
	Total     int        `json:"total"`
	Sanitized *bool      `json:"sanitized"`
}

type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Group     string `json:"group"`
	Subgroup  string `json:"subgroup"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	Message   string `json:"message"`
	Repeats   int    `json:"repeats"`
	LastSeen  string `json:"lastSeen"`
	Sanitized *bool  `json:"sanitized"`
}

// Logs читает журнал awg-manager (bucket=app).
func (c *Client) Logs(ctx context.Context, q LogsQuery) (*LogsPage, error) {
	v := url.Values{}
	v.Set("bucket", "app")
	if q.Level != "" {
		v.Set("level", q.Level)
	}
	if q.Group != "" {
		v.Set("group", q.Group)
	}
	if q.Limit > 0 {
		v.Set("limit", strconv.Itoa(q.Limit))
	}
	var env Envelope[LogsPage]
	if err := c.get(ctx, "/api/logs?"+v.Encode(), &env); err != nil {
		if IsEndpointMissing(err) {
			return nil, fmt.Errorf("%w: журнал появился в awg-manager позже", ErrUnsupportedByRouter)
		}
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr logs: success=false")
	}
	return &env.Data, nil
}

// WANStatus -- подключения к провайдеру (С1): роль задаёт priority, 0 -- не участвует.
type WANStatus struct {
	Interfaces map[string]WANInterface `json:"interfaces"`
	AnyWANUp   bool                    `json:"anyWANUp"`
}

type WANInterface struct {
	Up       bool   `json:"up"`
	Label    string `json:"label"`
	Priority int    `json:"priority"`
}

func (c *Client) WANStatus(ctx context.Context) (*WANStatus, error) {
	var env Envelope[WANStatus]
	if err := c.get(ctx, "/api/wan/status", &env); err != nil {
		if IsEndpointMissing(err) {
			return nil, fmt.Errorf("%w: состояние подключений появилось в awg-manager позже", ErrUnsupportedByRouter)
		}
		return nil, err
	}
	if !env.Success {
		return nil, fmt.Errorf("awgmgr wan/status: success=false")
	}
	return &env.Data, nil
}
