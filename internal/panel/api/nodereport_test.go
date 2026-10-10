package api

import (
	"strings"
	"testing"
	"time"
)

func TestMaskReport(t *testing.T) {
	secrets := []string{"203.0.113.5", "node.example.com", "31234", "panel.example.org"}
	cases := map[string]string{
		"dial tcp 203.0.113.5:31234: i/o timeout":                         "dial tcp ***: i/o timeout",
		"host=203.0.113.5 port=31234":                                      "host=*** port=***",
		"Get \"https://node.example.com:31234/v1/health\": EOF":           "Get \"***\": EOF",
		"other 198.51.100.7 and [2001:db8::1]:443 here":                    "other *** and [***]:443 here",
		"seen 2001:db8::5.":                                                "seen ***.",
		"from vpn.someone.net, to github.com and ghcr.io":                 "from ***, to github.com and ghcr.io",
		"key mikan1.eyJwb3J0IjozMTIzNH0abc":                                "key ***",
		"secret Zm9vYmFyYmF6cXV4MTIzNDU2Nzg5MGFiY2RlZg== end":             "secret *** end",
		"panel v0.5.0.6, node v0.5.0.4":                                    "panel v0.5.0.6, node v0.5.0.4",
		"port 443 busy, 8443 open, 312345 not a port":                      "port 443 busy, 8443 open, 312345 not a port",
		"2026-10-10T09:00:00Z":                                             "2026-10-10T09:00:00Z",
		"[fail] link: timeout":                                             "[fail] link: timeout",
		"https://panel.example.org:2053/":                                  "***",
		"address in use: listen tcp :443: bind: address already in use":   "address in use: listen tcp :443: bind: address already in use",
	}
	for in, want := range cases {
		if got := maskReport(in, secrets); got != want {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestReportText(t *testing.T) {
	items := []CheckItem{
		{ID: "link", Status: "fail", Code: "timeout", Params: map[string]string{"port": "31234", "host": "203.0.113.5"}, Detail: "dial tcp 203.0.113.5:31234: i/o timeout"},
		{ID: "port", Status: "ok", Params: map[string]string{"port": "443", "name": "vless-reality"}},
	}
	got := reportText("mikan node check", "panel v0.5.0.6, node v0.5.0.4", time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC), items, []string{"203.0.113.5", "31234"})
	for _, leak := range []string{"203.0.113.5", "31234"} {
		if strings.Contains(got, leak) {
			t.Fatalf("%s leaks:\n%s", leak, got)
		}
	}
	for _, keep := range []string{"[fail] link: timeout host=*** port=***", "[ok] port name=vless-reality port=443", "v0.5.0.4", "2026-10-10T09:00:00Z"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("missing %q:\n%s", keep, got)
		}
	}
}
