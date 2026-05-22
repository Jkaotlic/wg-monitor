package actions

import (
	"context"
	"net/http"
	"testing"
)

func TestPickConnectivityTunnelIfacePrefersMatchingHydraRoute(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"old","name":"old-default","interfaceName":"nwg1","enabled":true,"defaultRoute":true},
			{"id":"actual","name":"hydra-exit","interfaceName":"nwg2","enabled":true,"defaultRoute":false}
		]}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:youtube","name":"YouTube","enabled":true,"backend":"hydraroute","domains":["youtube.com"],"routes":[{"interface":"nwg2","tunnelId":"nwg2"}]}
		]}`))
	})
	c := awgmgrFake(t, mux)

	iface, label := pickConnectivityTunnelIface(context.Background(), c, []connectivityTarget{
		{Name: "YouTube", URL: "https://www.youtube.com/generate_204"},
	})

	if iface != "nwg2" || label != "hydra-exit" {
		t.Fatalf("iface=%q label=%q, want nwg2/hydra-exit", iface, label)
	}
}

func TestPickConnectivityTunnelIfaceFallsBackToDefaultRoute(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"old","name":"old-default","interfaceName":"nwg1","enabled":true,"defaultRoute":true},
			{"id":"other","name":"other","interfaceName":"nwg2","enabled":true,"defaultRoute":false}
		]}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[]}`))
	})
	c := awgmgrFake(t, mux)

	iface, label := pickConnectivityTunnelIface(context.Background(), c, []connectivityTarget{
		{Name: "YouTube", URL: "https://www.youtube.com/generate_204"},
	})

	if iface != "nwg1" || label != "old-default" {
		t.Fatalf("iface=%q label=%q, want nwg1/old-default", iface, label)
	}
}
