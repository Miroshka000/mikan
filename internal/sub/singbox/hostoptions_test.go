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
