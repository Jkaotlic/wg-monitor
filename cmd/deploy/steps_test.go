package main

import "testing"

func TestBackendHealthURLDefaultsToLocalListenerForPublicDomain(t *testing.T) {
	if got := backendHealthURL("wgmonitor.example.test"); got != "http://127.0.0.1:8080/healthz" {
		t.Fatalf("backendHealthURL(public)=%q, want local listener healthz", got)
	}
}

func TestBackendHealthURLUsesLoopbackDomainPortWhenExplicit(t *testing.T) {
	if got := backendHealthURL("http://127.0.0.1:9090"); got != "http://127.0.0.1:9090/healthz" {
		t.Fatalf("backendHealthURL(loopback)=%q, want explicit loopback port", got)
	}
}
