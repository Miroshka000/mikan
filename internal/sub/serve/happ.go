package serve

import (
	"mikan/internal/sub/profile"
	"mikan/internal/sub/xray"
)

// happAutoRouting is the happ://routing/onadd/ link of cfg's routing, or of the own Clash
// profile clash when there is one: the same for every user it is made for
// (xray.HappRoutingAuto).
func happAutoRouting(cfg Config, clash string) string {
	name, p := autoRouting(cfg)
	return xray.HappRoutingOf(name, p, cfg.Mode, clash)
}

// incyAutoRouting is happAutoRouting for INCY (xray.IncyRoutingOf).
func incyAutoRouting(cfg Config, clash string) string {
	name, p := autoRouting(cfg)
	return xray.IncyRoutingOf(name, p, cfg.Mode, clash)
}

// autoRouting is the name and the settings of the profile the panel makes.
func autoRouting(cfg Config) (string, profile.Profile) {
	name := cfg.Brand
	if name == "" {
		name = "VPN"
	}
	return name, cfg.Profile(nil)
}
