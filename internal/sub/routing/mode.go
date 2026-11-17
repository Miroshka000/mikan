// Package routing is the panel's routing of subscriptions, apart from any core: the mode,
// where each service goes, the lists and the apps past the tunnel, the admin's own rules
// and DNS, the catalogs of services and lists, and the checks that keep a setting from
// breaking the profiles. Intent is all of it as every core reads it, and BuildGroups the
// groups of the built-in profile; the geodata each core's apps download is in catalog.go.
package routing

// Mode is how the apps split traffic between the tunnel and the direct path.
type Mode string

const (
	// RUDirect sends Russian sites and IPs past the tunnel by mihomo's own geodata
	// (MetaCubeX geosite category-ru and GeoIP ru): banks and state services refuse
	// foreign IPs, and the node does not carry traffic that needs no VPN.
	RUDirect Mode = "ru_direct"
	// All sends everything but the LAN through the tunnel and needs no geodata.
	All Mode = "all"
	// Blocked sends only what is blocked or slowed down in Russia through the tunnel
	// (BlockedLists, and the services sent to it), the rest goes direct. Apps that cannot
	// take rule lists get RUDirect.
	Blocked     Mode = "blocked"
	DefaultMode      = RUDirect
)

// ParseMode maps a stored setting to a mode; empty and unknown values get the default.
func ParseMode(s string) Mode {
	if m := Mode(s); m == RUDirect || m == All || m == Blocked {
		return m
	}
	return DefaultMode
}
