package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/anex/wg-monitor/internal/backend/db"
)

type bindTopicOpts struct {
	DBPath   string
	Nickname string
	ThreadID int64
	Clear    bool
	Out      io.Writer
}

func cmdBindTopic(args []string) {
	fs := flag.NewFlagSet("bind-topic", flag.ExitOnError)
	dbPath := fs.String("db", "/var/lib/wg-monitor/state.db", "path to SQLite DB")
	nick := fs.String("nickname", "", "router nickname to bind a topic to")
	thread := fs.Int64("thread-id", 0, "existing TG forum-topic message_thread_id to associate with this router")
	clear := fs.Bool("clear", false, "clear the existing topic binding (next HARD alert will create a fresh one)")
	_ = fs.Parse(args)
	if err := runBindTopic(bindTopicOpts{DBPath: *dbPath, Nickname: *nick, ThreadID: *thread, Clear: *clear, Out: os.Stdout}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// runBindTopic associates an existing TG forum topic (the operator
// already knows the thread_id, e.g. created the topic by hand) with a
// router. Use --clear to unbind; the next HARD alert will then re-create
// a fresh topic via Dispatcher.ensureTopic.
func runBindTopic(o bindTopicOpts) error {
	if o.Nickname == "" {
		return fmt.Errorf("--nickname is required")
	}
	if !o.Clear && o.ThreadID <= 0 {
		return fmt.Errorf("--thread-id must be a positive integer (or use --clear)")
	}
	d, err := db.Open(o.DBPath)
	if err != nil {
		return fmt.Errorf("open db %s: %w", o.DBPath, err)
	}
	defer d.Close()
	u, err := d.Users().GetByNickname(o.Nickname)
	if err != nil {
		return fmt.Errorf("lookup user %q: %w", o.Nickname, err)
	}
	if o.Clear {
		if err := d.Users().ClearThreadID(u.ID); err != nil {
			return fmt.Errorf("clear thread id: %w", err)
		}
		fmt.Fprintf(o.Out, "OK — cleared topic binding for %s (next HARD alert will create a fresh one).\n", o.Nickname)
		return nil
	}
	if err := d.Users().UpdateThreadID(u.ID, o.ThreadID); err != nil {
		return fmt.Errorf("set thread id: %w", err)
	}
	fmt.Fprintf(o.Out, "OK — bound %s to topic thread_id=%d.\n", o.Nickname, o.ThreadID)
	return nil
}
