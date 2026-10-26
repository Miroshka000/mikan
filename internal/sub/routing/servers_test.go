package routing

import (
	"math"
	"testing"

	"mikan/internal/sub/model"
)

// Each core's own settings go over the common ones; what a core leaves at zero keeps them.
func TestBalance(t *testing.T) {
	common := Servers{Auto: "fallback", Interval: 120}
	if b := common.MihomoBalance(); b != (Balance{Type: model.Fallback, URL: ProbeURL, Interval: 120, Tolerance: 50}) {
		t.Fatalf("mihomo, common: %+v", b)
	}
	s := common
	s.Mihomo = MihomoServers{Type: "load-balance", Interval: 60, Tolerance: 150}
	s.SingBox = SingBoxServers{Tolerance: 80}
	s.Xray = XrayServers{Strategy: XrayLeastPing, ProbeURL: "https://cp.cloudflare.com/generate_204", Interval: 600}
	if b := s.MihomoBalance(); b != (Balance{Type: model.LoadBalance, URL: ProbeURL, Interval: 60, Tolerance: 150}) {
		t.Fatalf("mihomo: %+v", b)
	}
	if b := s.SingBoxBalance(); b != (Balance{Type: model.Fallback, URL: ProbeURL, Interval: 120, Tolerance: 80}) {
		t.Fatalf("sing-box: %+v", b)
	}
	if b := s.XrayBalance(); b != (Balance{Type: model.Fallback, URL: "https://cp.cloudflare.com/generate_204", Interval: 600, Tolerance: 50}) {
		t.Fatalf("xray: %+v", b)
	}
	if b := (Servers{}).SingBoxBalance(); b != (Balance{Type: model.URLTest, URL: ProbeURL, Interval: 300, Tolerance: 50}) {
		t.Fatalf("the defaults: %+v", b)
	}
}

// A core's setting no app would take is refused with the code of that core.
func TestServersCheck(t *testing.T) {
	load := XrayServers{Strategy: XrayLeastLoad, ProbeURL: "http://cp.cloudflare.com/", Interval: 60, Sampling: 3, Tolerance: 0.01, Expected: 2, MaxRTT: 1000}
	good := Servers{Mihomo: MihomoServers{Type: "fallback", Interval: 30, Tolerance: 5000}, SingBox: SingBoxServers{Interval: 86400, Tolerance: 1}, Xray: load}
	if err := good.check(); err != nil {
		t.Fatal(err)
	}
	xray := func(f func(*XrayServers)) Servers {
		x := load
		f(&x)
		return Servers{Xray: x}
	}
	for name, c := range map[string]struct {
		s    Servers
		code string
	}{
		"common interval":     {Servers{Interval: 29}, "routes_servers"},
		"mihomo type":         {Servers{Mihomo: MihomoServers{Type: "select"}}, "routes_servers_mihomo"},
		"mihomo interval":     {Servers{Mihomo: MihomoServers{Interval: 10}}, "routes_servers_mihomo"},
		"mihomo tolerance":    {Servers{Mihomo: MihomoServers{Tolerance: 5001}}, "routes_servers_mihomo"},
		"sing-box tolerance":  {Servers{SingBox: SingBoxServers{Tolerance: -1}}, "routes_servers_singbox"},
		"sing-box interval":   {Servers{SingBox: SingBoxServers{Interval: 90000}}, "routes_servers_singbox"},
		"xray strategy":       {xray(func(x *XrayServers) { x.Strategy = "fastest" }), "routes_servers_xray"},
		"xray probe scheme":   {xray(func(x *XrayServers) { x.ProbeURL = "ftp://example.com/" }), "routes_servers_xray"},
		"xray probe quote":    {xray(func(x *XrayServers) { x.ProbeURL = `https://example.com/"` }), "routes_servers_xray"},
		"xray probe user":     {xray(func(x *XrayServers) { x.ProbeURL = "https://u:p@example.com/" }), "routes_servers_xray"},
		"xray tolerance":      {xray(func(x *XrayServers) { x.Tolerance = 1.5 }), "routes_servers_xray"},
		"xray tolerance NaN":  {xray(func(x *XrayServers) { x.Tolerance = math.NaN() }), "routes_servers_xray"},
		"xray max rtt":        {xray(func(x *XrayServers) { x.MaxRTT = 60001 }), "routes_servers_xray"},
		"leastLoad only":      {xray(func(x *XrayServers) { x.Strategy = XrayLeastPing }), "routes_servers_xray"},
		"xray expected":       {xray(func(x *XrayServers) { x.Expected = -1 }), "routes_servers_xray"},
		"xray sampling":       {xray(func(x *XrayServers) { x.Sampling = 101 }), "routes_servers_xray"},
		"xray interval short": {xray(func(x *XrayServers) { x.Interval = 5 }), "routes_servers_xray"},
	} {
		if err := (Routes{Servers: c.s}).Check(func(int64) bool { return false }); err == nil || err.Error() != c.code {
			t.Errorf("%s: %v, want %s", name, err, c.code)
		}
	}
}
