package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	backendpkg "github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// announceSender -- та часть tg.Client, которая нужна объявлению.
type announceSender interface {
	SendMessage(ctx context.Context, chatID int64, threadID *int64, text, parseMode string, replyTo *int64) (int64, error)
}

// announceText -- прощание темы. Главное в нём -- предложение открыть бота:
// Telegram не даёт боту написать первым, пока человек сам не заговорил, и без
// этого шага он просто перестанет получать тревоги.
const announceText = "Уведомления переезжают в личку бота.\n\n" +
	"Дальше о поломках этого роутера бот напишет вам лично. " +
	"Чтобы получать их, откройте бота и нажмите «Запустить» — " +
	"без этого Telegram не даст ему написать первым.\n\n" +
	"Эта тема больше пополняться не будет."

// announceKey -- отметка о публикации. Ключ на роутер, а не общий флаг: темы
// могли завестись в разное время, и охватывать их можно по частям.
func announceKey(userID int64) string {
	return "announce_dm_migration:" + strconv.FormatInt(userID, 10)
}

// announceDMMigration публикует прощальное объявление в тему каждого роутера.
//
// Идемпотентна: отметка о публикации хранится в kv, и повторный запуск темы не
// засоряет. Команду гоняют не один раз -- пока не убедятся, что охвачены все,
// -- и вторая копия объявления была бы платой за эту осторожность.
//
// dryRun печатает, кому ушло бы, и не трогает ни Telegram, ни отметки.
func announceDMMigration(ctx context.Context, d *db.DB, sender announceSender, dryRun bool, out io.Writer) error {
	users, err := d.Users().GetAll()
	if err != nil {
		return fmt.Errorf("announce: список роутеров: %w", err)
	}
	sent, skipped := 0, 0
	for _, u := range users {
		if u.TelegramThreadID == nil || *u.TelegramThreadID == 0 {
			// Темы нет -- объявлению негде появиться.
			skipped++
			continue
		}
		done, err := d.KV().Get(announceKey(u.ID))
		if err != nil {
			return fmt.Errorf("announce: отметка для %s: %w", u.Nickname, err)
		}
		if done != "" {
			skipped++
			continue
		}
		if dryRun {
			fmt.Fprintf(out, "объявление ушло бы: %s (тема %d)\n", u.Nickname, *u.TelegramThreadID)
			sent++
			continue
		}
		chatID := u.EffectiveTelegramChatID(0)
		if chatID == 0 {
			skipped++
			continue
		}
		tid := *u.TelegramThreadID
		if _, err := sender.SendMessage(ctx, chatID, &tid, announceText, "", nil); err != nil {
			fmt.Fprintf(out, "не удалось объявить в теме %s: %v\n", u.Nickname, err)
			continue
		}
		if err := d.KV().Set(announceKey(u.ID), time.Now().UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("announce: пометить %s: %w", u.Nickname, err)
		}
		fmt.Fprintf(out, "объявлено: %s\n", u.Nickname)
		sent++
	}
	fmt.Fprintf(out, "итого: объявлено %d, пропущено %d\n", sent, skipped)
	return nil
}

func cmdAnnounceDMMigration(args []string) {
	fs := flag.NewFlagSet("announce-dm-migration", flag.ExitOnError)
	configPath := fs.String("config", "/etc/wg-monitor/backend.yaml", "путь к backend.yaml")
	dbPathOverride := fs.String("db", "", "переопределить путь к базе")
	dryRun := fs.Bool("dry-run", false, "показать, кому ушло бы объявление, и ничего не отправлять")
	_ = fs.Parse(args)

	cfg, err := backendpkg.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	dbPath := cfg.DBPath
	if *dbPathOverride != "" {
		dbPath = *dbPathOverride
	}
	d, err := db.Open(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "db: %v\n", err)
		os.Exit(1)
	}
	defer d.Close()

	client := &tg.Client{
		BaseURL: tg.DefaultBaseURL,
		Token:   cfg.Telegram.BotToken,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := announceDMMigration(ctx, d, client, *dryRun, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "announce: %v\n", err)
		os.Exit(1)
	}
}
