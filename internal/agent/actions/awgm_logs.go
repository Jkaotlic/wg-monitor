package actions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Jkaotlic/wg-monitor/internal/agent/awgmgr"
	"github.com/Jkaotlic/wg-monitor/internal/agent/redact"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

var (
	awgmLogLevels = map[string]bool{"error": true, "warn": true, "info": true}
	// Закрытый список групп (С1).
	awgmLogGroups = map[string]bool{"tunnel": true, "routing": true, "system": true, "server": true}
)

const (
	awgmLogsDefaultLimit = 100
	awgmLogsMaxLimit     = 200
	awgmLogsMaxOutput    = 48 << 10
	awgmLogMessageRunes  = 500
)

func awgmLogsLimit(v any) (int, error) {
	switch n := v.(type) {
	case nil:
		return awgmLogsDefaultLimit, nil
	case float64:
		if n != float64(int(n)) {
			return 0, fmt.Errorf("awgm_logs: limit must be an integer")
		}
		return int(n), nil
	case int:
		return n, nil
	default:
		return 0, fmt.Errorf("awgm_logs: limit must be an integer")
	}
}

func clipRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// AwgmLogsJSON -- команда awgm_logs: журнал awg-manager по кнопке. Маску
// выключить нельзя: клиент Logs параметра sanitize не имеет, а запись без
// пометки sanitized маскируется здесь ещё раз.
func AwgmLogsJSON(ctx context.Context, c *awgmgr.Client, args map[string]any) (string, error) {
	if c == nil {
		return "", fmt.Errorf("awgm_logs: awg-manager client is required")
	}
	level, _ := args["level"].(string)
	level = strings.TrimSpace(level)
	if level == "" {
		level = "warn"
	}
	if !awgmLogLevels[level] {
		return "", fmt.Errorf("awgm_logs: level must be error|warn|info")
	}
	group, _ := args["group"].(string)
	group = strings.TrimSpace(group)
	if group != "" && !awgmLogGroups[group] {
		return "", fmt.Errorf("awgm_logs: group must be tunnel|routing|system|server")
	}
	limit, err := awgmLogsLimit(args["limit"])
	if err != nil {
		return "", err
	}
	if limit < 1 || limit > awgmLogsMaxLimit {
		return "", fmt.Errorf("awgm_logs: limit must be 1..%d", awgmLogsMaxLimit)
	}
	out := wire.AwgmLogs{Entries: []wire.AwgmLogEntry{}}
	page, err := c.Logs(ctx, awgmgr.LogsQuery{Level: level, Group: group, Limit: limit})
	if err != nil {
		if errors.Is(err, awgmgr.ErrUnsupportedByRouter) {
			out.Unsupported = true
			b, _ := json.Marshal(out)
			return string(b), nil
		}
		return "", fmt.Errorf("awgm_logs: %w", err)
	}
	out.Enabled, out.Total = page.Enabled, page.Total
	pageSanitized := page.Sanitized != nil && *page.Sanitized
	type stamped struct {
		e  wire.AwgmLogEntry
		ts time.Time
	}
	items := make([]stamped, 0, len(page.Logs))
	for _, e := range page.Logs {
		target, message := e.Target, e.Message
		// P5: по-записный флаг sanitized перекрывает флаг страницы -- запись
		// с явным sanitized:false маскируется агентом, даже если страница в
		// целом отдана как sanitized:true.
		masked := pageSanitized
		if e.Sanitized != nil {
			masked = *e.Sanitized
		}
		if !masked {
			target, message = redact.Text(target), redact.Text(message)
		}
		ts, _ := time.Parse(time.RFC3339Nano, e.Timestamp)
		items = append(items, stamped{ts: ts, e: wire.AwgmLogEntry{
			TS: e.Timestamp, Level: e.Level, Group: e.Group, Action: e.Action,
			Target: clipRunes(target, awgmLogMessageRunes), Message: clipRunes(message, awgmLogMessageRunes), Repeats: e.Repeats,
		}})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].ts.After(items[j].ts) })
	for _, it := range items {
		out.Entries = append(out.Entries, it.e)
	}
	for {
		b, err := json.Marshal(out)
		if err != nil {
			return "", err
		}
		if len(b) <= awgmLogsMaxOutput || len(out.Entries) == 0 {
			return string(b), nil
		}
		out.Entries = out.Entries[:len(out.Entries)*3/4]
		out.Truncated = true
	}
}
