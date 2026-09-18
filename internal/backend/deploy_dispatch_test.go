package backend

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	cmdpkg "github.com/Jkaotlic/wg-monitor/internal/backend/cmd"
	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

// «Обновить всех отставших» на 11 роутерах: отметку получают все, а
// self_update в раздаче одновременно -- не больше, чем слотов у прокси.
// Остальные ждут своего опроса, попытку при этом не тратят.
func TestFleetDeploy_DispatchStaggeredBySlots(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "stagger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	q := cmdpkg.New()
	AttachDeployDispatchLimit(q)
	deps := Deps{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), DB: d, CommandSink: q}

	const routers = 11
	var ids []int64
	for i := 0; i < routers; i++ {
		tok := "c1" + strconv.Itoa(1000+i)
		for len(tok) < 64 {
			tok += "0"
		}
		uid, err := d.Users().InsertWithKind("r"+strconv.Itoa(i), tok, "198.51.100.9", "awg0", db.KindMobile)
		if err != nil {
			t.Fatal(err)
		}
		u, err := d.Users().GetByID(uid)
		if err != nil {
			t.Fatal(err)
		}
		if _, derr := agentDeployCore(deps, u, "v0.45.0", agentDeployOpts{RepoBaseURL: "https://backend.example.com"}); derr != nil {
			t.Fatal(derr)
		}
		ids = append(ids, uid)
	}
	dispatched := 0
	for _, uid := range ids {
		st, _ := d.Users().PendingDeploy(uid)
		if st.Version != "v0.45.0" {
			t.Fatalf("роутер %d без отметки", uid)
		}
		if c, ok := q.Dequeue(context.Background(), uid, time.Millisecond); ok && c.Action == "self_update" {
			dispatched++
		}
	}
	if dispatched != maxConcurrentReleaseProxy {
		t.Fatalf("в раздаче %d, ждали %d", dispatched, maxConcurrentReleaseProxy)
	}
}
