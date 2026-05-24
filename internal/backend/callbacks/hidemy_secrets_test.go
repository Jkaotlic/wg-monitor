package callbacks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestHideMySecretsStoreMultipleCodesAndActive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hidemyname.json")
	r := &Router{cfg: Config{HideMySecretsPath: path}}

	if got, err := r.getHideMyCode(7); err != nil || got != "" {
		t.Fatalf("empty get = %q, %v", got, err)
	}
	first, err := r.addHideMyCode(7, "123456789012345")
	if err != nil {
		t.Fatal(err)
	}
	second, err := r.addHideMyCode(7, "987654321098765")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatalf("ids should differ: %q", first.ID)
	}
	codes, err := r.listHideMyCodes(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes.Codes) != 2 || codes.ActiveID != second.ID {
		t.Fatalf("codes = %+v", codes)
	}
	got, err := r.getHideMyCodeByID(7, first.ID)
	if err != nil || got != "123456789012345" {
		t.Fatalf("get first = %q, %v", got, err)
	}
	if err := r.deleteHideMyCode(7, second.ID); err != nil {
		t.Fatal(err)
	}
	codes, err = r.listHideMyCodes(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes.Codes) != 1 || codes.ActiveID != first.ID {
		t.Fatalf("after delete = %+v", codes)
	}
}

func TestHideMySecretsRejectsNonNumericCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hidemyname.json")
	r := &Router{cfg: Config{HideMySecretsPath: path}}
	if _, err := r.addHideMyCode(7, "vpn://not-hidemy"); err == nil {
		t.Fatal("expected invalid code error")
	}
}

func TestHideMySecretsWriteStructuredJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hidemyname.json")
	r := &Router{cfg: Config{HideMySecretsPath: path}}
	if _, err := r.addHideMyCode(7, "123456789012345"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var env hideMySecretFile
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	if env.Version != 1 || len(env.Routers["7"].Codes) != 1 {
		t.Fatalf("env = %+v", env)
	}
}
