package singbox

import (
	"context"
	"encoding/json"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
	"mikan/internal/sub/subtest/corecheck"
)

// An address's own fields reach the config as sing-box names them: the IP version as the
// strategy of the config's default DNS server, which sing-box requires. sing-box checks it.
func TestHostOptions(t *testing.T) {
	lists(t)
	raw, err := Render(context.Background(), profileOf(subtest.WithHostOptions(subtest.EveryType())), model.GroupNames{}, routing.All, model.Template{})
	if err != nil {
		t.Fatal(err)
	}
	var c sbConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	resolved, muxed := 0, 0
	for _, o := range c.Outbounds {
		if r, ok := o["domain_resolver"].(map[string]any); ok {
			if r["server"] != c.Route.Resolver || r["strategy"] != "prefer_ipv6" {
				t.Errorf("%v: %v, want the server %q", o["tag"], r, c.Route.Resolver)
			}
			resolved++
		}
		if m, ok := o["multiplex"].(map[string]any); ok && m["protocol"] == "h2mux" && m["padding"] == true {
			muxed++
		}
	}
	if resolved == 0 || muxed == 0 {
		t.Fatalf("resolved %d, multiplexed %d:\n%s", resolved, muxed, raw)
	}
	corecheck.SingBox(t, raw)
}

// A split TLS hello reaches the TLS of the node's certificate: in records alone, or in
// records each in a packet of its own, which sing-box 1.12 does only with both set.
func TestHostFragment(t *testing.T) {
	lists(t)
	for _, tc := range []struct {
		fragment string
		want     map[string]any
	}{
		{model.FragmentRecord, map[string]any{"record_fragment": true}},
		{model.FragmentTCP, map[string]any{"record_fragment": true, "fragment": true}},
	} {
		t.Run(tc.fragment, func(t *testing.T) {
			raw, err := Render(context.Background(), profileOf(subtest.WithHostFragment(subtest.EveryType(), tc.fragment)), model.GroupNames{}, routing.All, model.Template{})
			if err != nil {
				t.Fatal(err)
			}
			var c sbConfig
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Fatal(err)
			}
			split := 0
			for _, o := range c.Outbounds {
				tls, _ := o["tls"].(map[string]any)
				if tls["record_fragment"] == nil && tls["fragment"] == nil {
					continue
				}
				if o["type"] != "vless" || tls["reality"] != nil {
					t.Errorf("%v: a split hello of %v", o["tag"], o["type"])
				}
				for _, k := range []string{"record_fragment", "fragment"} {
					if tls[k] != tc.want[k] {
						t.Errorf("%v: %s %v, want %v", o["tag"], k, tls[k], tc.want[k])
					}
				}
				split++
			}
			// VLESS over TLS and over WebSocket, on the node's certificate.
			if split != 2 {
				t.Fatalf("%d outbounds split the hello, want 2:\n%s", split, raw)
			}
			corecheck.SingBox(t, raw)
		})
	}
}
