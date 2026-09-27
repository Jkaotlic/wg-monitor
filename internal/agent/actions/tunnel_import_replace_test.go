package actions

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Jkaotlic/wg-monitor/pkg/wire"
)

// tunnelImportReplaceFake -- awg-manager с заданным списком туннелей; пишет,
// что заменено, что создано, что удалено и в каком порядке.
type tunnelImportReplaceFake struct {
	tunnelsJSON string
	importFails bool
	calls       []string
}

func (f *tunnelImportReplaceFake) runner(t *testing.T) Runner {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":` + f.tunnelsJSON + `,"external":[],"system":[]}}`))
	})
	mux.HandleFunc("/api/tunnels/replace", func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, "replace:"+r.URL.Query().Get("id"))
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"` + r.URL.Query().Get("id") + `","name":"x","status":"running","enabled":true}}`))
	})
	mux.HandleFunc("/api/tunnels/delete", func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, "delete:"+r.URL.Query().Get("id"))
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/import/conf", func(w http.ResponseWriter, r *http.Request) {
		f.calls = append(f.calls, "import")
		if f.importFails {
			http.Error(w, "import rejected", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"data":{"id":"new-id","name":"x","status":"running","enabled":false,"backend":"nativewg"}}`))
	})
	mux.HandleFunc("/api/control/start", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{}}`))
	})
	mux.HandleFunc("/api/system/hydraroute-status", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"installed":false,"running":false}}`))
	})
	return Runner{
		AwgClient: awgmgrFake(t, mux),
		Exec:      func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
		Sleep:     func(context.Context, time.Duration) error { return nil },
		Now:       mockNow(),
	}
}

func (f *tunnelImportReplaceFake) did(prefix string) bool {
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// AGENT-09: адрес 10.x.0.2 выдают многие провайдеры. Замена «по адресу»
// переписывала чужой туннель. Теперь адрес -- только когда совпадение
// единственное и сервер тот же (или о сервере туннеля ничего не известно);
// иначе создаётся новый туннель, старый не трогается.
func TestTunnelImport_AddressFallbackOnlyForUnambiguousSameServer(t *testing.T) {
	cases := []struct {
		name, tunnels, wantReplace string
	}{
		{"two tunnels share the address", `[{"id":"a","name":"prov-a","address":"10.99.0.2/32"},{"id":"b","name":"prov-b","address":"10.99.0.2/32"}]`, ""},
		{"same address, other server", `[{"id":"a","name":"prov-a","address":"10.99.0.2/32","endpoint":"203.0.113.9:51820"}]`, ""},
		{"same address, same server", `[{"id":"a","name":"prov-a","address":"10.99.0.2/32","endpoint":"vpn.example.com:51820"}]`, "a"},
		{"same address, server unknown", `[{"id":"a","name":"prov-a","address":"10.99.0.2/32"}]`, "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &tunnelImportReplaceFake{tunnelsJSON: tc.tunnels}
			r := f.runner(t)
			res := r.Execute(context.Background(), wire.Command{
				ID: "imp", Action: "tunnel_import",
				Args: map[string]any{"conf": testConfB64, "name": "amnezia_nl", "replace": true},
			})
			if res.Status != "ok" {
				t.Fatalf("status=%q output=%q", res.Status, res.Output)
			}
			if tc.wantReplace == "" {
				if f.did("replace:") || f.did("delete:") {
					t.Fatalf("чужой туннель тронут: %v", f.calls)
				}
				if !f.did("import") {
					t.Fatalf("новый туннель не создан: %v", f.calls)
				}
				return
			}
			if !f.did("replace:" + tc.wantReplace) {
				t.Fatalf("want replace of %s, calls %v", tc.wantReplace, f.calls)
			}
		})
	}
}

// AGENT-09: пересоздание в nativeWG удаляло старый туннель ДО импорта.
// Провал импорта оставлял роутер без туннеля. Теперь сначала импорт, потом
// удаление; провал импорта старый туннель не трогает.
func TestTunnelImport_RecreateImportsBeforeDeleting(t *testing.T) {
	old := `[{"id":"old-kernel","name":"amnezia_nl","backend":"kernel","enabled":true}]`

	f := &tunnelImportReplaceFake{tunnelsJSON: old, importFails: true}
	r := f.runner(t)
	res := r.Execute(context.Background(), wire.Command{
		ID: "imp", Action: "tunnel_import",
		Args: map[string]any{"conf": testConfB64, "name": "amnezia_nl", "replace": true, "backend": "nativewg"},
	})
	if res.Status == "ok" {
		t.Fatalf("провал импорта выдан за успех: %q", res.Output)
	}
	if f.did("delete:") {
		t.Fatalf("старый туннель удалён, хотя новый не встал: %v", f.calls)
	}

	f = &tunnelImportReplaceFake{tunnelsJSON: old}
	r = f.runner(t)
	res = r.Execute(context.Background(), wire.Command{
		ID: "imp", Action: "tunnel_import",
		Args: map[string]any{"conf": testConfB64, "name": "amnezia_nl", "replace": true, "backend": "nativewg"},
	})
	if res.Status != "ok" {
		t.Fatalf("status=%q output=%q", res.Status, res.Output)
	}
	if len(f.calls) < 2 || f.calls[0] != "import" || f.calls[1] != "delete:old-kernel" {
		t.Fatalf("порядок %v, want import затем delete:old-kernel", f.calls)
	}
}
