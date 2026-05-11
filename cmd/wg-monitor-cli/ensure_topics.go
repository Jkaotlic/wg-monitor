package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	backendpkg "github.com/Jkaotlic/wg-monitor/internal/backend"
	"github.com/Jkaotlic/wg-monitor/internal/backend/alerts"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/tg"
)

// topicCreator mirrors alerts.TopicCreator locally so tests can drop a
// mock in. Production binds *tg.Client which satisfies the interface.
type topicCreator = alerts.TopicCreator

type ensureTopicsOpts struct {
	DBPath   string
	ChatID   int64
	Nickname string // empty → all users without a topic
	// Force=true rebuilds a topic even when telegram_thread_id is set.
	// The old TG topic is not deleted server-side — historic messages
	// stay, the orphan can be removed by the operator if desired.
	Force bool
	Sleep time.Duration
	// Creator must implement alerts.TopicCreator.
	Creator topicCreator
	// Welcomer is optional — when non-nil, a welcome message is sent
	// into each freshly-created topic so reply-keyboard buttons attach
	// immediately. nil disables (used by older tests that don't model TG).
	Welcomer alerts.WelcomeSender
	// WelcomeKeyboard returns the reply-markup attached to the welcome.
	// Mirrors Dispatcher.WelcomeKeyboard contract.
	WelcomeKeyboard func() any
	Out             io.Writer
}

func cmdEnsureTopics(args []string) {
	fs := flag.NewFlagSet("ensure-topics", flag.ExitOnError)
	configPath := fs.String("config", "/etc/wg-monitor/backend.yaml", "path to backend config.yaml (for chat_id and bot_token_file)")
	dbPathOverride := fs.String("db", "", "override path to SQLite DB (default: from config)")
	nick := fs.String("nickname", "", "create topic only for this nickname (default: all users without telegram_thread_id)")
	force := fs.Bool("force", false, "rebuild topic even if one is already bound (old TG topic is left intact)")
	sleepMs := fs.Int("sleep-ms", 350, "sleep between TG createForumTopic calls to dodge flood-wait")
	_ = fs.Parse(args)

	cfg, err := backendpkg.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: load config:", err)
		os.Exit(1)
	}
	dbPath := cfg.DBPath
	if *dbPathOverride != "" {
		dbPath = *dbPathOverride
	}
	tgClient := &tg.Client{
		BaseURL: tg.DefaultBaseURL,
		Token:   cfg.Telegram.BotToken,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
	if err := runEnsureTopics(context.Background(), ensureTopicsOpts{
		DBPath:          dbPath,
		ChatID:          cfg.Telegram.ChatID,
		Nickname:        *nick,
		Force:           *force,
		Sleep:           time.Duration(*sleepMs) * time.Millisecond,
		Creator:         tgClient,
		Welcomer:        tgClient,
		WelcomeKeyboard: func() any { return tg.ReplyKeyboardForTopic("per_router") },
		Out:             os.Stdout,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// runEnsureTopics is the testable core. Creates forum topics for users
// whose telegram_thread_id is NULL (or for every targeted user when
// Force=true), persisting each successful id before moving to the next.
// Partial failures are reported but do not abort the run — a flaky or
// rate-limited TG response on user N must not strand users N+1..M.
func runEnsureTopics(ctx context.Context, o ensureTopicsOpts) error {
	if o.ChatID == 0 {
		return errors.New("chat_id is required")
	}
	if o.Creator == nil {
		return errors.New("topic creator is required")
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	d, err := db.Open(o.DBPath)
	if err != nil {
		return fmt.Errorf("open db %s: %w", o.DBPath, err)
	}
	defer d.Close()

	var targets []db.User
	if o.Nickname != "" {
		u, err := d.Users().GetByNickname(o.Nickname)
		if err != nil {
			return fmt.Errorf("lookup user %q: %w", o.Nickname, err)
		}
		targets = []db.User{*u}
	} else {
		all, err := d.Users().GetAll()
		if err != nil {
			return fmt.Errorf("list users: %w", err)
		}
		targets = all
	}

	var created, skipped, failed int
	calls := 0
	for _, u := range targets {
		if !o.Force && u.TelegramThreadID != nil {
			fmt.Fprintf(o.Out, "= skip %s — already has topic id=%d (use --force to rebuild)\n", u.Nickname, *u.TelegramThreadID)
			skipped++
			continue
		}
		if calls > 0 && o.Sleep > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(o.Sleep):
			}
		}
		calls++
		oldID := int64(0)
		if u.TelegramThreadID != nil {
			oldID = *u.TelegramThreadID
		}
		tid, err := alerts.EnsureTopicForUser(ctx, o.Creator, d, o.ChatID, u.ID, o.Force)
		if err != nil {
			fmt.Fprintf(o.Out, "! fail %s — %v\n", u.Nickname, err)
			failed++
			continue
		}
		if oldID != 0 && tid != oldID {
			fmt.Fprintf(o.Out, "+ rebuilt %s — old topic id=%d → new topic id=%d (old topic left intact in TG)\n", u.Nickname, oldID, tid)
		} else {
			fmt.Fprintf(o.Out, "+ created %s — topic id=%d\n", u.Nickname, tid)
		}
		// Send welcome so reply-keyboard attaches to the new topic.
		// Non-fatal: log to stdout and continue.
		if o.Welcomer != nil && o.WelcomeKeyboard != nil {
			if werr := alerts.SendWelcome(ctx, o.Welcomer, o.ChatID, tid, u.Nickname, o.WelcomeKeyboard()); werr != nil {
				fmt.Fprintf(o.Out, "  (welcome send failed for %s: %v)\n", u.Nickname, werr)
			}
		}
		created++
	}
	fmt.Fprintf(o.Out, "\nDone: %d created, %d skipped, %d failed\n", created, skipped, failed)
	if failed > 0 {
		return fmt.Errorf("%d user(s) failed", failed)
	}
	return nil
}
