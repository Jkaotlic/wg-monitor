package backend

import (
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

func TestVersionSnapshotCarriesLoadedKernelModule(t *testing.T) {
	snap, ok := versionSnapshotFromReport(`{"version":"2.19.0+r2","kernel_module_version":"3.2.20260930","kernel_module_loaded_version":"3.1.20260906"}`)
	if !ok || snap.KmodLoadedVersion != "3.1.20260906" {
		t.Errorf("из отчёта: ok=%v snap=%+v", ok, snap)
	}
	if got := VersionSnapshotFromAudit(wire.VersionAudit{AwgmgrVersion: "2.19.0+r2", KmodLoadedVersion: "3.1.20260906"}); got.KmodLoadedVersion != "3.1.20260906" {
		t.Errorf("из version_audit: %+v", got)
	}
	row := db.RouterVersionRow{RouterVersionSnapshot: db.RouterVersionSnapshot{KmodVersion: "3.2.20260930", KmodLoadedVersion: "3.1.20260906"}}
	if va := VersionAuditFromSnapshot(row); va.KmodLoadedVersion != "3.1.20260906" {
		t.Errorf("обратно в форму агента: %+v", va)
	}
}
