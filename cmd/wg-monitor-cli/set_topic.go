package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/anex/wg-monitor/internal/backend/db"
)

type setTopicOpts struct {
	DBPath   string
	Kind     string
	ThreadID int64
	Out      io.Writer
}

func cmdSetTopic(args []string) {
	fs := flag.NewFlagSet("set-topic", flag.ExitOnError)
	dbPath := fs.String("db", "/var/lib/wg-monitor/state.db", "path to SQLite DB")
	kind := fs.String("kind", "", "topic kind: summary|systemic")
	thread := fs.Int64("thread-id", 0, "Telegram message_thread_id of the topic")
	_ = fs.Parse(args)
	if err := runSetTopic(setTopicOpts{DBPath: *dbPath, Kind: *kind, ThreadID: *thread, Out: os.Stdout}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runSetTopic(o setTopicOpts) error {
	if o.Kind != "summary" && o.Kind != "systemic" {
		return fmt.Errorf("--kind=%q must be summary|systemic", o.Kind)
	}
	if o.ThreadID == 0 {
		return fmt.Errorf("--thread-id is required (the message_thread_id of the topic)")
	}
	d, err := db.Open(o.DBPath)
	if err != nil {
		return fmt.Errorf("open db %s: %w", o.DBPath, err)
	}
	defer d.Close()
	if err := d.KV().SetTopicID(o.Kind, o.ThreadID); err != nil {
		return fmt.Errorf("set topic id: %w", err)
	}
	fmt.Fprintf(o.Out, "OK — kind=%s thread_id=%d\n", o.Kind, o.ThreadID)
	return nil
}
