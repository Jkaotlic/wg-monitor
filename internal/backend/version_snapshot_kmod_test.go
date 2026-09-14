package backend

import (
	"path/filepath"
	"testing"

	"github.com/Jkaotlic/wg-monitor/internal/backend/db"
	"github.com/Jkaotlic/wg-monitor/internal/backend/upstream"
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

// M2 (fix round 1), полный путь: отчёт сразу после настоящей перезагрузки
// пришёл с kernel_module_loaded=true (awg_manager сообщил о модуле), но
// kernel_module_loaded_version пуст -- это транзиент, а не «источник молчит».
// Пустая версия обязана форсированно записаться, и RebootHint перестаёт звать
// перезагрузку (общее правило «пусто -- оставить прежнее» здесь не при чём).
func TestVersionSnapshotReportForcesEmptyLoadedVersionWhenReported(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "kmod-forced.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	uid, err := d.Users().Insert("router-a", "tok-a", "198.51.100.10", "awg11")
	if err != nil {
		t.Fatal(err)
	}
	r := d.RouterVersions()
	if err := r.Upsert(uid, db.RouterVersionSnapshot{
		KmodVersion: "3.2.20260930", KmodLoadedVersion: "3.1.20260906",
		KmodLoadedVersionReported: true, Source: "report",
	}); err != nil {
		t.Fatal(err)
	}

	snap, ok := versionSnapshotFromReport(`{"version":"2.19.1","kernel_module_version":"3.2.20260930","kernel_module_loaded":true,"kernel_module_loaded_version":""}`)
	if !ok {
		t.Fatal("отчёт не разобрался")
	}
	if !snap.KmodLoadedVersionReported {
		t.Fatal("отчёт с kernel_module_loaded в JSON обязан взвести KmodLoadedVersionReported")
	}
	if err := r.Upsert(uid, snap); err != nil {
		t.Fatal(err)
	}

	row, err := r.Get(uid)
	if err != nil {
		t.Fatal(err)
	}
	if row.KmodLoadedVersion != "" {
		t.Errorf("пустая загруженная версия не форсировалась: %q", row.KmodLoadedVersion)
	}
	if hint := upstream.RebootHint(row.KmodVersion, row.KmodLoadedVersion); hint != "" {
		t.Errorf("RebootHint продолжает звать перезагрузку после форса пустой версии: %q", hint)
	}
}
