package actions

import (
	"context"
	"net/http"
	"strings"
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

func TestPickConnectivityTunnelIfaceSkipsStoppedDefaultRoute(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"stopped","name":"amnezia_sg","interfaceName":"opkgtun10","enabled":true,"status":"stopped","defaultRoute":true}
		]}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:youtube","name":"YouTube","enabled":true,"backend":"hydraroute","domains":["youtube.com"],"routes":[{"interface":"opkgtun10","tunnelId":"awg10"}]}
		]}`))
	})
	c := awgmgrFake(t, mux)

	iface, label := pickConnectivityTunnelIface(context.Background(), c, []connectivityTarget{
		{Name: "YouTube", URL: "https://www.youtube.com/generate_204"},
	})

	if iface != "" || label != "" {
		t.Fatalf("iface=%q label=%q, want no usable stopped tunnel", iface, label)
	}
}

func TestPickConnectivityTunnelIfaceDoesNotFallbackWhenMatchingHydraRouteUsesUnavailableIface(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"awg10","name":"amnezia_nl","interfaceName":"nwg1","enabled":true,"status":"starting","defaultRoute":true},
			{"id":"awg11","name":"amnezia_for_awg222","interfaceName":"nwg5","enabled":true,"status":"running","defaultRoute":true}
		]}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:youtube","name":"YouTube","enabled":true,"backend":"hydraroute","domains":["youtube.com"],"routes":[{"interface":"","tunnelId":"nwg1"}]}
		]}`))
	})
	c := awgmgrFake(t, mux)

	iface, label := pickConnectivityTunnelIface(context.Background(), c, []connectivityTarget{
		{Name: "YouTube", URL: "https://www.youtube.com/generate_204"},
	})

	if iface != "" || label != "" {
		t.Fatalf("iface=%q label=%q, want no fallback when matching HR route points to unavailable nwg1", iface, label)
	}
}

func TestCheckViaTunnelExplainsUnavailableHydraRouteBinding(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"awg10","name":"amnezia_nl","interfaceName":"nwg1","enabled":true,"status":"starting","defaultRoute":true},
			{"id":"awg11","name":"amnezia_for_awg222","interfaceName":"nwg5","enabled":true,"status":"running","defaultRoute":true}
		]}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:youtube","name":"YouTube","enabled":true,"backend":"hydraroute","domains":["youtube.com"],"routes":[{"interface":"","tunnelId":"nwg1"}]}
		]}`))
	})
	c := awgmgrFake(t, mux)

	status, output := CheckViaTunnel(context.Background(), c)

	if status != "err" {
		t.Fatalf("status=%q, want err; output=%q", status, output)
	}
	for _, want := range []string{"HR-Neo", "YouTube", "nwg1", "ложный OK"} {
		if !strings.Contains(output, want) {
			t.Fatalf("output=%q, want substring %q", output, want)
		}
	}
}

func TestPickConnectivityTunnelIfaceMatchesHydraRouteGeositeTarget(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"awg10","name":"amnezia_nl","interfaceName":"nwg1","enabled":true,"status":"starting","defaultRoute":true},
			{"id":"awg11","name":"amnezia_for_awg222","interfaceName":"nwg5","enabled":true,"status":"running","defaultRoute":true}
		]}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:YOUTUBE","name":"YOUTUBE","enabled":true,"backend":"hydraroute","domains":["geosite:YOUTUBE"],"routes":[{"interface":"","tunnelId":"nwg1"}]}
		]}`))
	})
	c := awgmgrFake(t, mux)

	iface, label := pickConnectivityTunnelIface(context.Background(), c, []connectivityTarget{
		{Name: "YouTube", URL: "https://www.youtube.com/generate_204"},
	})

	if iface != "" || label != "" {
		t.Fatalf("iface=%q label=%q, want no fallback when geosite:YOUTUBE points to unavailable nwg1", iface, label)
	}
}

func TestPickConnectivityTunnelIfaceDoesNotFallbackWhenMatchingHydraRoutePolicyUsesUnavailableDefault(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"awg10","name":"amnezia_nl","interfaceName":"nwg1","enabled":true,"status":"starting","defaultRoute":true},
			{"id":"awg11","name":"amnezia_for_awg222","interfaceName":"nwg5","enabled":true,"status":"running","defaultRoute":true}
		]}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:YOUTUBE","name":"YOUTUBE","enabled":true,"backend":"hydraroute","domains":["geosite:YOUTUBE"],"routes":null,"hrRouteMode":"policy","hrPolicyName":"HydraRoute"}
		]}`))
	})
	c := awgmgrFake(t, mux)

	iface, label := pickConnectivityTunnelIface(context.Background(), c, []connectivityTarget{
		{Name: "YouTube", URL: "https://www.youtube.com/generate_204"},
	})

	if iface != "" || label != "" {
		t.Fatalf("iface=%q label=%q, want no fallback when matching HR policy follows unavailable nwg1", iface, label)
	}
}

func TestPickConnectivityTunnelIfaceUsesHydraRoutePolicyInterfaceInsteadOfFirstDefaultRoute(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tunnels/all", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":{"tunnels":[
			{"id":"awg10","name":"first-default","interfaceName":"nwg1","ndmsName":"Wireguard1","enabled":true,"status":"running","defaultRoute":true},
			{"id":"awg11","name":"actual-policy","interfaceName":"nwg5","ndmsName":"Wireguard5","enabled":true,"status":"running","defaultRoute":true}
		]}}`))
	})
	mux.HandleFunc("/api/dns-routes/list", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"success":true,"data":[
			{"id":"hr:YOUTUBE","name":"YOUTUBE","enabled":true,"backend":"hydraroute","domains":["geosite:YOUTUBE"],"routes":null,"hrRouteMode":"policy","hrPolicyName":"HydraRoute","hrPolicyInterfaces":["Wireguard5"]}
		]}`))
	})
	c := awgmgrFake(t, mux)

	iface, label := pickConnectivityTunnelIface(context.Background(), c, []connectivityTarget{
		{Name: "YouTube", URL: "https://www.youtube.com/generate_204"},
	})

	if iface != "nwg5" || label != "actual-policy" {
		t.Fatalf("iface=%q label=%q, want nwg5/actual-policy", iface, label)
	}
}
