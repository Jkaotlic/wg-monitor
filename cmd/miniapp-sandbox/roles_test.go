package main

import (
	"path/filepath"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
)

func TestAttachViewerRoles(t *testing.T) {
	const viewer, fleet int64 = 4242, 5242
	for role, want := range map[string]int{"owner1": 1, "owner3": 3, "operator": 1, "issuer": 1, "none": 0} {
		t.Run(role, func(t *testing.T) {
			d, err := db.Open(filepath.Join(t.TempDir(), "sandbox.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer d.Close()
			ids, err := seed(d, fleet)
			if err != nil {
				t.Fatal(err)
			}
			if err := attachViewer(d, ids, viewer, role); err != nil {
				t.Fatal(err)
			}
			got, err := d.AccessibleRouterIDs(viewer)
			if err != nil || len(got) != want {
				t.Fatalf("доступных роутеров %d, want %d (%v)", len(got), want, err)
			}
			for _, l := range sandboxRoles[role] {
				if r, _ := d.RouterAccessRole(ids[l.nick], viewer); r != l.role {
					t.Fatalf("%s: роль %q, want %q", l.nick, r, l.role)
				}
			}
		})
	}
	if err := attachViewer(nil, nil, viewer, "boss"); err == nil {
		t.Fatal("неизвестная роль принята")
	}
}
