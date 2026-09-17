// Package callbacks -- Telegram-бот (уведомления в личке, /start, кнопка
// «Тише на час») и кабинеты провайдеров для мини-аппа.
package callbacks

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Args -- разобранная callback_data кнопки «Тише на час».
type Args struct {
	Action    string // всегда "silence"
	UserID    int64
	CheckName string
	TTL       time.Duration
}

// Parse разбирает "silence:<router_id>:<check>:<ttl>". Прочие кнопки бот не
// разбирает: nmute и ушедшие в приложение отвечаются до Parse.
func Parse(data string) (Args, error) {
	parts := strings.Split(data, ":")
	if len(parts) < 3 {
		return Args{}, fmt.Errorf("malformed callback_data: %q", data)
	}
	if parts[0] != "silence" {
		return Args{}, fmt.Errorf("unknown action: %q", parts[0])
	}
	if len(parts) != 4 {
		return Args{}, fmt.Errorf("silence requires ttl: %q", data)
	}
	uid, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || uid <= 0 {
		return Args{}, fmt.Errorf("bad user_id %q", parts[1])
	}
	if parts[2] == "" {
		return Args{}, fmt.Errorf("silence requires check name: %q", data)
	}
	ttl, err := parseTTL(parts[3])
	if err != nil {
		return Args{}, err
	}
	return Args{Action: "silence", UserID: uid, CheckName: parts[2], TTL: ttl}, nil
}

func parseTTL(s string) (time.Duration, error) {
	switch s {
	case "1h":
		return 1 * time.Hour, nil
	case "4h":
		return 4 * time.Hour, nil
	case "24h":
		return 24 * time.Hour, nil
	}
	return 0, fmt.Errorf("invalid ttl: %q (must be 1h|4h|24h)", s)
}

// parseSlashCommand -- "/cmd@bot arg" → ("/cmd", "arg", true).
func parseSlashCommand(text string) (cmd, arg string, ok bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", "", false
	}
	parts := strings.SplitN(text, " ", 2)
	cmd = parts[0]
	if at := strings.Index(cmd, "@"); at >= 0 {
		cmd = cmd[:at]
	}
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}
	return cmd, arg, true
}
