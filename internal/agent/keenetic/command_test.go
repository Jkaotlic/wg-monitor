package keenetic

import (
	"context"
	"slices"
	"testing"
)

func TestFirmwareCommandEnvironment(t *testing.T) {
	t.Setenv("LD_LIBRARY_PATH", "/opt/lib")
	t.Setenv("LD_PRELOAD", "")
	t.Setenv("KEENETIC_TEST", "kept")
	for _, name := range []string{"ndmc", "/bin/ndmc", "opkg"} {
		env := CommandContext(context.Background(), name).Environ()
		for _, variable := range []string{"LD_LIBRARY_PATH=/opt/lib", "LD_PRELOAD="} {
			if slices.Contains(env, variable) != (name == "opkg") {
				t.Errorf("%s: unexpected inheritance of %s", name, variable)
			}
		}
		if !slices.Contains(env, "KEENETIC_TEST=kept") {
			t.Errorf("%s: unrelated environment variable lost", name)
		}
	}
}
