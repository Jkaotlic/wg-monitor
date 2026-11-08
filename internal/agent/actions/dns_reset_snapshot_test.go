package actions

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDNSSnapshotKeepsOriginalAcrossImmediateRetry(t *testing.T) {
	dir := t.TempDir()
	first, err := writeDNSSnapshot(dir, "before addition")
	if err != nil {
		t.Fatal(err)
	}
	second, err := writeDNSSnapshot(dir, "after failed save")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("retry overwrote original snapshot path")
	}
	b, err := os.ReadFile(first)
	if err != nil || string(b) != "before addition" {
		t.Fatalf("original snapshot lost: %q %v", b, err)
	}
	info, err := os.Stat(second)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions %v", info.Mode())
	}
}

func TestDNSSnapshotPrunesByAgeAcrossLegacyAndUniqueNames(t *testing.T) {
	dir := t.TempDir()
	// Name ordering deliberately disagrees with age, as mixed formats can.
	names := []string{"dns-before-999.txt", "dns-before-100.999-random.txt", "dns-before-100.txt", "dns-before-90.001-random.txt", "dns-before-80.txt", "dns-before-70.001-random.txt"}
	for i, name := range names {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(time.Duration(i-10) * time.Hour)
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
	fresh, err := writeDNSSnapshot(dir, "latest")
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "dns-before-*.txt"))
	if len(files) != 5 {
		t.Fatalf("retained %d snapshots", len(files))
	}
	for i, name := range names {
		_, err := os.Stat(filepath.Join(dir, name))
		if (i < 2) != os.IsNotExist(err) {
			t.Errorf("age pruning wrong for %s: %v", name, err)
		}
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("latest removed: %v", err)
	}
}
