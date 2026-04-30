package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

func cmdInitMenu(args []string) {
	fs := flag.NewFlagSet("init-menu", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/wg-monitor/backend.yaml", "path to backend config yaml")
	nick := fs.String("nickname", "", "user nickname (must already exist via add-user)")
	tunnelsCSV := fs.String("tunnels", "", "comma-separated tunnel ids (override auto-discovery from events)")
	pin := fs.Bool("pin", false, "pin the message in the topic (requires bot to have can_pin_messages)")
	_ = fs.Parse(args)

	if *nick == "" {
		fmt.Fprintln(os.Stderr, "error: --nickname required")
		os.Exit(2)
	}

	cfg, err := backend.LoadConfig(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load config:", err)
		os.Exit(1)
	}
	d, err := db.Open(cfg.DBPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open db:", err)
		os.Exit(1)
	}
	defer d.Close()

	user, err := d.Users().GetByNickname(*nick)
	if err != nil || user == nil {
		fmt.Fprintln(os.Stderr, "user not found:", *nick)
		os.Exit(1)
	}
	if user.TelegramThreadID == nil {
		fmt.Fprintln(os.Stderr,
			"user has no telegram thread yet — wait for first HARD alert (creates topic) then re-run")
		os.Exit(1)
	}

	tunnels := resolveTunnels(d, user.ID, *tunnelsCSV)
	kb := tg.ControlPanelKeyboard(user.ID, tunnels, user.IsMobile())
	text := tg.ControlPanelText(user.Nickname, tunnels)

	tgC := &tg.Client{
		BaseURL: tg.DefaultBaseURL,
		Token:   cfg.Telegram.BotToken,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	msgID, err := tgC.SendMessageWithKeyboard(ctx, cfg.Telegram.ChatID, user.TelegramThreadID, text, "", nil, &kb)
	if err != nil {
		fmt.Fprintln(os.Stderr, "send menu:", err)
		os.Exit(1)
	}
	fmt.Printf("Menu posted: message_id=%d in topic=%d (%d tunnel buttons + %d wide)\n",
		msgID, *user.TelegramThreadID, len(tunnels), wideCount(user.IsMobile()))

	if *pin {
		if err := pinChatMessage(ctx, cfg.Telegram.BotToken, cfg.Telegram.ChatID, msgID); err != nil {
			fmt.Fprintln(os.Stderr, "warn: pin failed (bot may lack can_pin_messages):", err)
		} else {
			fmt.Println("Message pinned.")
		}
	} else {
		fmt.Println("Tip: open the topic and pin this message manually so it stays at the top.")
	}
}

// resolveTunnels picks tunnel entries either from explicit CSV or from the
// most recent per-tunnel events for this user. Returns sorted by check_name
// for deterministic button ordering.
func resolveTunnels(d *db.DB, userID int64, csv string) []tg.TunnelEntry {
	if csv != "" {
		var out []tg.TunnelEntry
		for _, id := range strings.Split(csv, ",") {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			out = append(out, tg.TunnelEntry{Name: id, CheckName: "tunnel_" + id})
		}
		return out
	}
	rows, err := d.Events().LatestEventsByPrefix(userID, "tunnel_")
	if err != nil {
		return nil
	}
	var out []tg.TunnelEntry
	for _, r := range rows {
		entry := tg.TunnelEntry{CheckName: r.CheckName}
		if r.DetailsJSON != "" {
			var det map[string]any
			_ = json.Unmarshal([]byte(r.DetailsJSON), &det)
			if v, ok := det["tunnel_name"].(string); ok && v != "" {
				entry.Name = v
			}
		}
		if entry.Name == "" {
			entry.Name = strings.TrimPrefix(r.CheckName, "tunnel_")
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CheckName < out[j].CheckName })
	return out
}

func wideCount(isMobile bool) int {
	if isMobile {
		return 2
	}
	return 1
}

// pinChatMessage calls TG Bot API pinChatMessage. Built inline (not in
// internal/backend/tg) because pin is a one-off init-time op, not part of the
// hot-path TG client used by the backend.
func pinChatMessage(ctx context.Context, token string, chatID, messageID int64) error {
	body, _ := json.Marshal(map[string]any{
		"chat_id":              chatID,
		"message_id":           messageID,
		"disable_notification": true,
	})
	url := fmt.Sprintf("https://api.telegram.org/bot%s/pinChatMessage", token)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
