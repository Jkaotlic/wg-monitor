package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

type bindTGUserOpts struct {
	DBPath   string
	Nickname string
	TGUserID int64
	Clear    bool
	Out      io.Writer
}

func cmdBindTGUser(args []string) {
	fs := flag.NewFlagSet("bind-tg-user", flag.ExitOnError)
	dbPath := fs.String("db", "/var/lib/wg-monitor/state.db", "path to SQLite DB")
	nick := fs.String("nickname", "", "router nickname to bind")
	tgID := fs.Int64("tg-id", 0, "Telegram numeric user id of the owner (positive; get from @userinfobot)")
	clear := fs.Bool("clear", false, "instead of binding, clear the existing owner (re-enables TOFU on next tap)")
	_ = fs.Parse(args)
	if err := runBindTGUser(bindTGUserOpts{DBPath: *dbPath, Nickname: *nick, TGUserID: *tgID, Clear: *clear, Out: os.Stdout}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runBindTGUser(o bindTGUserOpts) error {
	if o.Nickname == "" {
		return fmt.Errorf("--nickname is required")
	}
	if !o.Clear && o.TGUserID <= 0 {
		return fmt.Errorf("--tg-id must be a positive integer (or use --clear)")
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
	bindTo := o.TGUserID
	if o.Clear {
		bindTo = 0
	}
	if err := d.Users().SetTelegramUserID(u.ID, bindTo); err != nil {
		return fmt.Errorf("set telegram_user_id: %w", err)
	}
	if o.Clear {
		fmt.Fprintf(o.Out, "OK — cleared owner binding for %s (TOFU will re-bind on next tap in their topic).\n", o.Nickname)
	} else {
		fmt.Fprintf(o.Out, "OK — bound %s to Telegram user_id=%d.\n", o.Nickname, o.TGUserID)
	}
	return nil
}
