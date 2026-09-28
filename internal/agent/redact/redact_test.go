package redact

import "testing"

func TestText(t *testing.T) {
	cases := map[string]string{
		"peer 198.51.100.7:51820 up":     "peer 198.*.*.7:51820 up",
		"resolve vpn.example.com failed": "resolve v***.com failed",
		"addr 2001:db8::1 set":           "addr <IPv6> set",
		"fe80:0:0:0:1:2:3:4":             "<IPv6>",
		"at 15:04:05 ok":                 "at 15:04:05 ok",
		"уже замаскировано 12*****.1":    "уже замаскировано 12*****.1",
		"": "",
	}
	for in, want := range cases {
		if got := Text(in); got != want {
			t.Errorf("Text(%q) = %q, want %q", in, got, want)
		}
	}
}
