package keenetic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const tunnelsLive = `{"success":true,"data":{"tunnels":[
  {"id":"awg11","ndmsName":"Wireguard1","interfaceName":"nwg1","status":"running"},
  {"id":"awg12","ndmsName":"Wireguard0","interfaceName":"nwg0","status":"running"}
]}}`

func TestFetchIfaceMap_FromAwgManager(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Requested-With") != "XMLHttpRequest" {
			http.Error(w, "missing XHR header", http.StatusBadRequest)
			return
		}
		if r.URL.Path != "/api/tunnels/all" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tunnelsLive))
	}))
	defer srv.Close()

	m, err := FetchIfaceMap(context.Background(), IfaceMapOptions{
		AwgManagerURL: srv.URL,
		Client:        srv.Client(),
	})
	if err != nil {
		t.Fatalf("FetchIfaceMap: %v", err)
	}
	if m["Wireguard0"] != "nwg0" {
		t.Errorf("Wireguard0 → %q, want nwg0", m["Wireguard0"])
	}
	if m["Wireguard1"] != "nwg1" {
		t.Errorf("Wireguard1 → %q, want nwg1", m["Wireguard1"])
	}
}

func TestFetchIfaceMap_AwgManagerDown(t *testing.T) {
	// Reach an obviously-dead URL (RFC 5737 documentation IP, port 1).
	_, err := FetchIfaceMap(context.Background(), IfaceMapOptions{
		AwgManagerURL: "http://192.0.2.1:1",
		Client:        &http.Client{},
	})
	if err == nil {
		t.Fatalf("expected error on unreachable awg-manager")
	}
}

func TestFetchIfaceMap_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>not json</html>`))
	}))
	defer srv.Close()
	_, err := FetchIfaceMap(context.Background(), IfaceMapOptions{
		AwgManagerURL: srv.URL,
		Client:        srv.Client(),
	})
	if err == nil {
		t.Fatalf("expected error on bad JSON")
	}
}
