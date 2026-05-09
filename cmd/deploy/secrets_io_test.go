package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExportImportRoundTrip(t *testing.T) {
	// Source side: pretend secretsCachePath() and statePath both live under
	// a per-test directory by overriding WG_SECRETS_FILE.
	srcDir := t.TempDir()
	srcSecrets := filepath.Join(srcDir, "secrets.env")
	srcState := filepath.Join(srcDir, "wizard.toml")
	wantSecrets := []byte("WG_VPS_PASS=hunter2\nWG_AGENT_TOKEN_TESTKEEN=deadbeef\n")
	wantState := []byte("schema_version = 1\n[backend]\nhost = \"vps.example.com\"\n")
	if err := os.WriteFile(srcSecrets, wantSecrets, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(srcState, wantState, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WG_SECRETS_FILE", srcSecrets)
	t.Setenv("WG_NO_SECRET_CACHE", "")

	// Export -> archive in a separate dir.
	archDir := t.TempDir()
	archive := filepath.Join(archDir, "wizard-backup.tgz")
	if err := ExportSecrets(archive, srcState); err != nil {
		t.Fatalf("export: %v", err)
	}
	st, err := os.Stat(archive)
	if err != nil {
		t.Fatalf("stat archive: %v", err)
	}
	if st.Size() == 0 {
		t.Fatal("archive is empty")
	}

	// Import side: point WG_SECRETS_FILE and statePath at a fresh dir.
	dstDir := t.TempDir()
	dstSecrets := filepath.Join(dstDir, "secrets.env")
	dstState := filepath.Join(dstDir, "wizard.toml")
	t.Setenv("WG_SECRETS_FILE", dstSecrets)

	if err := ImportSecrets(archive, dstState, false); err != nil {
		t.Fatalf("import: %v", err)
	}

	gotSecrets, err := os.ReadFile(dstSecrets)
	if err != nil {
		t.Fatalf("read dst secrets: %v", err)
	}
	if string(gotSecrets) != string(wantSecrets) {
		t.Errorf("secrets content mismatch:\n got: %q\nwant: %q", gotSecrets, wantSecrets)
	}
	gotState, err := os.ReadFile(dstState)
	if err != nil {
		t.Fatalf("read dst state: %v", err)
	}
	if string(gotState) != string(wantState) {
		t.Errorf("state content mismatch:\n got: %q\nwant: %q", gotState, wantState)
	}
}

func TestImportSecrets_RefusesOverwriteWithoutForce(t *testing.T) {
	// Build an archive from one dir.
	srcDir := t.TempDir()
	srcSecrets := filepath.Join(srcDir, "secrets.env")
	srcState := filepath.Join(srcDir, "wizard.toml")
	os.WriteFile(srcSecrets, []byte("WG_VPS_PASS=fresh\n"), 0o600)
	os.WriteFile(srcState, []byte("schema_version = 1\n"), 0o600)
	t.Setenv("WG_SECRETS_FILE", srcSecrets)
	t.Setenv("WG_NO_SECRET_CACHE", "")
	archDir := t.TempDir()
	archive := filepath.Join(archDir, "x.tgz")
	if err := ExportSecrets(archive, srcState); err != nil {
		t.Fatalf("export: %v", err)
	}

	// Destination already populated.
	dstDir := t.TempDir()
	dstSecrets := filepath.Join(dstDir, "secrets.env")
	dstState := filepath.Join(dstDir, "wizard.toml")
	os.WriteFile(dstSecrets, []byte("WG_VPS_PASS=existing\n"), 0o600)
	os.WriteFile(dstState, []byte("schema_version = 1\n"), 0o600)
	t.Setenv("WG_SECRETS_FILE", dstSecrets)

	if err := ImportSecrets(archive, dstState, false); err == nil {
		t.Fatal("expected refusal to overwrite without force")
	}
	// Existing file untouched.
	got, _ := os.ReadFile(dstSecrets)
	if string(got) != "WG_VPS_PASS=existing\n" {
		t.Errorf("file was overwritten despite force=false: %q", got)
	}

	// With force=true it must succeed.
	if err := ImportSecrets(archive, dstState, true); err != nil {
		t.Fatalf("import force=true: %v", err)
	}
	got, _ = os.ReadFile(dstSecrets)
	if string(got) != "WG_VPS_PASS=fresh\n" {
		t.Errorf("force overwrite did not apply: %q", got)
	}
}

func TestExportSecrets_RejectsNonTgzExtension(t *testing.T) {
	srcDir := t.TempDir()
	src := filepath.Join(srcDir, "secrets.env")
	os.WriteFile(src, []byte("k=v\n"), 0o600)
	t.Setenv("WG_SECRETS_FILE", src)
	t.Setenv("WG_NO_SECRET_CACHE", "")
	if err := ExportSecrets(filepath.Join(srcDir, "out.zip"), filepath.Join(srcDir, "wizard.toml")); err == nil {
		t.Fatal("expected error for non-.tgz extension")
	}
}
