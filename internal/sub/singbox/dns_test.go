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

// A resolver given as a bare IPv6 address is that address, with its port when it has one,
// and sing-box takes the config.
func TestDNSIPv6Resolver(t *testing.T) {
	lists(t)
	prof := profileOf(subtest.TwoNodes())
	prof.Routes = routing.Routes{DNS: routing.DNS{
		Nameserver:  []string{"2606:4700:4700::1111"},
		ProxyServer: []string{"[2606:4700:4700::1001]:5353"},
		Policy:      []routing.DNSPolicy{{Match: "+.cn", Servers: []string{"2400:3200::1#" + model.AliasGroup}}},
	}}
	raw, err := Render(context.Background(), prof, model.GroupNames{}, routing.All, model.Template{})
	if err != nil {
		t.Fatal(err)
	}
	var c sbConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	servers, _ := json.Marshal(c.DNS.Servers)
	if string(servers) != `[{"server":"2606:4700:4700::1111","tag":"remote","type":"udp"},`+
		`{"server":"2606:4700:4700::1001","server_port":5353,"tag":"local","type":"udp"},`+
		`{"detour":"`+model.AliasGroup+`","server":"2400:3200::1","tag":"policy-0","type":"udp"}]` {
		t.Fatalf("servers: %s", servers)
	}
	t.Run("sing-box", func(t *testing.T) { corecheck.SingBox(t, raw) })
}
