package main

import (
	"strings"
	"testing"
)

// v0.62 (BACK-04): превью восстановления перечисляет зашифрованные
// хранилища, которые без прежнего revive.key не откроются, -- а не только
// пароли роутеров.
func TestRestorePreviewNamesSealedStoresWithoutKey(t *testing.T) {
	entries := func(withKey bool) []tarEntryForTest {
		var out []tarEntryForTest
		for _, e := range smallArchiveEntries(validRestoreBackendYAML()) {
			switch e.name {
			case "amnezia-premium.json", "awg3-panels.json":
				e.body = "wg-monitor-sealed v1\nnot-really-ciphertext"
			case "revive.key":
				if !withKey {
					continue
				}
			}
			out = append(out, e)
		}
		return out
	}
	archive := writeEncryptedRestoreArchive(t, "wg-monitor-small-backup-20261007T020000Z.tgz.enc", entries(false))
	b, cleanup, err := InspectRestoreBackupWithPassphrase(archive, restoreTestPass)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	preview := RenderRestoreBackupPreview(b)
	for _, want := range []string{
		"sealed stores: amnezia-premium.json, awg3-panels.json",
		"без прежнего revive.key не откроются: ключи кабинетов, панелей VPS",
		"saved router passwords must be entered again",
	} {
		if !strings.Contains(preview, want) {
			t.Errorf("в превью нет %q:\n%s", want, preview)
		}
	}
	if strings.Contains(preview, "not-really-ciphertext") {
		t.Fatal("содержимое хранилища в превью")
	}

	// Ключ в архиве -- хранилища откроются им, предупреждать не о чем.
	yaml := restoreYAMLWithReviveKey()
	withKey := entries(true)
	for i := range withKey {
		if withKey[i].name == "backend.yaml" {
			withKey[i].body = yaml
		}
	}
	archive = writeEncryptedRestoreArchive(t, "b.tgz.enc", withKey)
	b2, cleanup2, err := InspectRestoreBackupWithPassphrase(archive, restoreTestPass)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup2()
	if p := RenderRestoreBackupPreview(b2); strings.Contains(p, "не откроются") {
		t.Fatalf("превью архива с ключом:\n%s", p)
	}

	// Открытые хранилища -- строки нет.
	archive = writeEncryptedRestoreArchive(t, "c.tgz.enc", smallArchiveEntries(validRestoreBackendYAML()))
	b3, cleanup3, err := InspectRestoreBackupWithPassphrase(archive, restoreTestPass)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup3()
	if p := RenderRestoreBackupPreview(b3); strings.Contains(p, "sealed stores") {
		t.Fatalf("превью без зашифрованных:\n%s", p)
	}
}
