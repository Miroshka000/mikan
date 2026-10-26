package serve

import (
	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/xray"
)

// happAutoRouting is the happ://routing/onadd/ link of cfg's routing, or of the own Clash
// profile clash when there is one: the same for every user it is made for
// (xray.HappRoutingAuto).
func happAutoRouting(cfg Config, clash string) string {
	name := cfg.Brand
	if name == "" {
		name = "VPN"
	}
	p := mihomo.Profile{Nodes: cfg.Nodes, Direct: cfg.Direct, Rules: cfg.Rules, Routes: cfg.Routes, Lang: cfg.Lang}
	return xray.HappRoutingOf(name, p, cfg.Mode, clash)
}
