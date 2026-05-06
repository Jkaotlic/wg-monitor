// cmd/wg-monitor-cli/list_users.go
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"text/tabwriter"
	"time"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

type listUsersOpts struct {
	DBPath string
	Now    time.Time // injectable for deterministic tests; zero ⇒ time.Now()
	Out    io.Writer
}

func cmdListUsers(args []string) {
	fs := flag.NewFlagSet("list-users", flag.ExitOnError)
	dbPath := fs.String("db", "/var/lib/wg-monitor/state.db", "path to SQLite DB")
	_ = fs.Parse(args)
	if err := runListUsers(listUsersOpts{DBPath: *dbPath, Out: os.Stdout}); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runListUsers(o listUsersOpts) error {
	d, err := db.Open(o.DBPath)
	if err != nil {
		return fmt.Errorf("open db %s: %w", o.DBPath, err)
	}
	defer d.Close()

	users, err := d.Users().GetAll()
	if err != nil {
		return fmt.Errorf("list users: %w", err)
	}

	now := o.Now
	if now.IsZero() {
		now = time.Now()
	}

	tw := tabwriter.NewWriter(o.Out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NICKNAME\tKIND\tIFACE\tEXIT_IP\tLAST_SEEN")
	for _, u := range users {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			u.Nickname, u.Kind, u.AWGIface, u.ExpectedExitIP,
			renderLastSeen(u.LastSeenAt, now),
		)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(o.Out, "\n%d user(s) total.\n", len(users))
	return nil
}

// renderLastSeen formats the gap between last_seen_at and now in the
// largest reasonable unit. "never" when the column is NULL.
func renderLastSeen(ts *time.Time, now time.Time) string {
	if ts == nil {
		return "never"
	}
	d := now.Sub(*ts)
	if d < 0 {
		return "future?"
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
