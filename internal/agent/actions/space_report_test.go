package actions

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

const spaceDf = "Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/sda1 200000 150000 50000 75% /opt\n"

func TestSpaceReportParsesDfAndTopDirs(t *testing.T) {
	du := strings.Join([]string{
		"150000\t/opt",
		"90000\t/opt/var",
		"80000\t/opt/var/log",
		"du: /opt/proc/1: Permission denied", // мусор
		"30000\t/opt/lib",
		"",
		"abc\t/opt/bad",
		"20000\t/opt/share/my dir", // путь с пробелом
		"10000 /opt/bin",           // без табуляции
		"9\t/opt/a", "8\t/opt/b", "7\t/opt/c", "6\t/opt/d", "5\t/opt/e", "4\t/opt/f",
	}, "\n")
	exec := fakeOpkgCronExec(map[string]fakeExecResult{
		"df -k /opt":            {out: spaceDf},
		"sh -c " + spaceDuShell: {out: du, err: errors.New("exit status 1")},
	})

	rep, err := SpaceReport(context.Background(), exec)

	if err != nil {
		t.Fatal(err)
	}
	if rep.FreeKB != 50000 || rep.TotalKB != 200000 {
		t.Fatalf("df: %+v", rep)
	}
	if len(rep.Top) != 10 {
		t.Fatalf("want 10 entries, got %d: %+v", len(rep.Top), rep.Top)
	}
	if rep.Top[0] != (wire.SpaceEntry{Path: "/opt/var", KB: 90000}) || rep.Top[3] != (wire.SpaceEntry{Path: "/opt/share/my dir", KB: 20000}) ||
		rep.Top[4] != (wire.SpaceEntry{Path: "/opt/bin", KB: 10000}) {
		t.Fatalf("top: %+v", rep.Top)
	}
	for _, e := range rep.Top {
		if e.Path == "/opt" || e.Path == "/opt/bad" {
			t.Fatalf("bad entry %+v", e)
		}
	}
}

// du ничего не дал -- место всё равно показываем, список пуст (не null).
func TestSpaceReportWithoutDu(t *testing.T) {
	exec := fakeOpkgCronExec(map[string]fakeExecResult{
		"df -k /opt":            {out: spaceDf},
		"sh -c " + spaceDuShell: {err: errors.New("du: not found")},
	})
	rep, err := SpaceReport(context.Background(), exec)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(rep)
	if !strings.Contains(string(b), `"top":[]`) {
		t.Fatalf("%s", b)
	}
}

func TestSpaceReportDfFailureIsError(t *testing.T) {
	exec := fakeOpkgCronExec(map[string]fakeExecResult{"df -k /opt": {err: errors.New("no df")}})
	if _, err := SpaceReport(context.Background(), exec); err == nil {
		t.Fatal("df failure swallowed")
	}
}

func TestRunnerSpaceReportDispatches(t *testing.T) {
	exec := fakeOpkgCronExec(map[string]fakeExecResult{
		"df -k /opt":            {out: spaceDf},
		"sh -c " + spaceDuShell: {out: "150000\t/opt\n90000\t/opt/var\n"},
	})
	r := Runner{Now: mockNow(), Exec: exec}
	res := r.Execute(context.Background(), wire.Command{ID: "s", Action: "space_report"})
	if res.Status != "ok" {
		t.Fatalf("%q %s", res.Status, res.Output)
	}
	var rep wire.SpaceReport
	if err := json.Unmarshal([]byte(res.Output), &rep); err != nil || len(rep.Top) != 1 {
		t.Fatalf("%v %s", err, res.Output)
	}
}
