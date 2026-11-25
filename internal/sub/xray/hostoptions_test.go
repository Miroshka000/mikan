package xray

import (
	"strings"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
)

// An address's IP version reaches the configs as xray's domainStrategy of the dial, and
// its ALPN the TLS of the protocols on the node's certificate; xray checks them.
func TestHostOptions(t *testing.T) {
	p := profileOf(subtest.WithHostOptions(subtest.EveryType()))
	raw, err := Render(p, model.GroupNames{}, routing.All, model.Template{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"UseIPv6v4"`) {
		t.Errorf("no IP version in the configs:\n%s", raw)
	}
	render(t, p, routing.All, model.Template{})
}

// A split TLS hello: the servers on the node's certificate dial through a freedom outbound
// of xray's fragment, which a config carries once and only when a server of it dials
// through it; the records go at once, or each in a write of its own. REALITY, Hysteria2 and
// Shadowsocks dial as they did. xray checks the configs.
func TestHostFragment(t *testing.T) {
	for _, tc := range []struct{ fragment, interval string }{{model.FragmentRecord, "0"}, {model.FragmentTCP, "10-20"}} {
		t.Run(tc.fragment, func(t *testing.T) {
			cs := render(t, profileOf(subtest.WithHostFragment(subtest.EveryType(), tc.fragment)), routing.All, model.Template{})
			tag := fragments[tc.fragment].tag
			split := 0
			for _, c := range cs {
				dialed, splitters := false, 0
				for _, o := range c.Outbounds {
					if get(o, "streamSettings", "sockopt", "dialerProxy") == tag {
						dialed = true
						if get(o, "streamSettings", "security") != "tls" {
							t.Errorf("%s: %v splits a hello of no TLS of the node's", c.Remarks, o["tag"])
						}
					}
					if o["tag"] == tag {
						splitters++
						f := get(o, "settings", "fragment")
						if o["protocol"] != "freedom" || get(o, "settings", "fragment", "packets") != "tlshello" || get(o, "settings", "fragment", "length") != "100-200" ||
							get(o, "settings", "fragment", "interval") != tc.interval {
							t.Errorf("%s: the splitter %v", c.Remarks, f)
						}
					}
				}
				if splitters > 1 || dialed != (splitters == 1) {
					t.Errorf("%s: %d splitters, a server dials through one: %v", c.Remarks, splitters, dialed)
				}
				if dialed {
					split++
				}
			}
			if split == 0 {
				t.Fatalf("no config splits the hello: %+v", cs)
			}
		})
	}
}

// An own template's outbound of a splitter's tag stays the admin's: the servers dial through
// one of a free tag.
func TestHostFragmentOwnTemplate(t *testing.T) {
	const src = `{
  "inbounds": [{"tag": "socks", "protocol": "socks", "listen": "127.0.0.1", "port": 10808, "settings": {"udp": true}}],
  "outbounds": [{"tag": "fragment-tcp", "protocol": "freedom"}],
  "routing": {"rules": [{"network": "tcp,udp", "outboundTag": "proxy"}]}
}`
	cs := render(t, profileOf(subtest.WithHostFragment(subtest.EveryType(), model.FragmentTCP)), routing.All, own(src))
	split := 0
	for _, c := range cs {
		if get(c.outbound(t, "fragment-tcp"), "settings", "fragment") != nil {
			t.Fatalf("%s: the template's outbound became a splitter", c.Remarks)
		}
		via := get(c.outbound(t, proxyTag), "streamSettings", "sockopt", "dialerProxy")
		if via == nil {
			continue
		}
		if via != "fragment-tcp-2" || get(c.outbound(t, "fragment-tcp-2"), "settings", "fragment", "interval") != "10-20" {
			t.Fatalf("%s: the server dials through %v: %v", c.Remarks, via, c.Outbounds)
		}
		split++
	}
	if split == 0 {
		t.Fatal("no server splits the hello")
	}
}
