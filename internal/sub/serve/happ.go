package serve

import (
	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/xray"
)

// happAutoRouting is the happ://routing/onadd/ link of cfg's Clash profile, the same for
// every user (xray.HappRoutingAuto).
func happAutoRouting(cfg Config) string {
	name := cfg.Brand
	if name == "" {
		name = "VPN"
	}
	p := mihomo.Profile{Nodes: cfg.Nodes, Direct: cfg.Direct, Rules: cfg.Rules, Routes: cfg.Routes, Lang: cfg.Lang}
	return xray.HappRoutingOf(name, mihomo.Rules(p, cfg.Groups.WithDefaults(cfg.Lang), cfg.Mode, cfg.Template))
}
