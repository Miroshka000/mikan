package mihomo

import (
	"testing"

	"github.com/metacubex/mihomo/config"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
)

// The profile is what an app loads: mihomo's own parser must take it whole. The tests alone
// link the core; the module's code never does (internal/sub's dependency test).
func TestMihomoParsesTheProfile(t *testing.T) {
	prof := profileOf(subtest.Defaults(""))
	for _, m := range []routing.Mode{routing.All, routing.RUDirect} {
		raw, err := Render(prof, model.GroupNames{Main: "🚀 Мой VPN", Auto: "on"}, m)
		if err != nil {
			t.Fatal(err)
		}
		coreAccepts(t, raw)
		rc, err := config.UnmarshalRawConfig(raw)
		if err != nil {
			t.Fatalf("%s: %v\n%s", m, err, raw)
		}
		if len(rc.Proxy) == 0 || len(rc.ProxyGroup) == 0 || len(rc.Rule) == 0 {
			t.Fatalf("%s: an empty section:\n%s", m, raw)
		}
		if g := rc.ProxyGroup[1]["name"]; g != "on" {
			t.Fatalf("a group named %q came back as %v", "on", g)
		}
	}
}
