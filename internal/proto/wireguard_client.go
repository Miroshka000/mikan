package proto

import (
	"encoding/base64"
)

// wireGuardClient builds the client side of a wireguard inbound: a Clash profile entry
// (mihomo, and the apps on its core) with the user's own key, inner address and the
// server's public key.
//
// AmneziaWG parameters ride along in amnezia-wg-option, which mihomo's wireguard
// outbound and Rawline both read whole. There is no share link: sing-box takes WG only as
// endpoints and Xray has no WG link scheme, so the profile is the way to clients.
func (c *clientBuilder) wireGuardClient() Client {
	s := c.in.Slot
	private := WGUserKey(s.Secret)
	c.y["type"] = "wireguard"
	c.y["private-key"] = base64.StdEncoding.EncodeToString(private[:])

	if server := c.serverPublicKey(); server != "" {
		c.y["public-key"] = server
	}
	if addr := c.userAddress(); addr != "" {
		c.y["ip"] = addr
	}
	c.y["allowed-ips"] = []string{"0.0.0.0/0", "::/0"}
	if v6 := c.t.str("ipv6-cidr"); v6 != "" {
		// A user has an /128 of the v6 subnet; without it only the v4 address goes in.
		c.y["allowed-ips"] = []string{"0.0.0.0/0", "::/0"}
	}
	mtu, _ := toInt(c.t["mtu"])
	if mtu == 0 {
		mtu = 1420
	}
	c.y["mtu"] = mtu
	c.y["udp"] = true
	if awg, ok := c.t["amnezia-wg-option"].(map[string]any); ok && len(awg) > 0 {
		c.y["amnezia-wg-option"] = clientAmneziaWG(awg)
	}
	return Client{Mihomo: c.y}
}

// awgV3Only are the AmneziaWG 3.x parameters a 2.0 client refuses the whole config over.
var awgV3Only = []string{"header-protection-key", "content-padding-addition", "rekey-after-time",
	"rekey-timeout", "reject-after-time", "keepalive-timeout", "max-handshake-attempts",
	"random-trailers", "disable-cookies"}

// clientAmneziaWG is the obfuscation a client gets.
//
// A 2.0 app refuses the whole config over a key it does not know, and a one-sided server
// setting is exactly that: the 3.x parameters go to the client only when the inbound is a
// 3.x one. The server keeps them either way.
func clientAmneziaWG(awg map[string]any) map[string]any {
	out := clone(awg).(map[string]any)
	version, _ := toInt(out["version"])
	if version != 3 {
		for _, key := range awgV3Only {
			delete(out, key)
		}
	}
	return out
}

// serverPublicKey derives the server key clients verify the peer with. The node keeps
// the private key, so the panel renders it from the template the node holds: the panel's
// own copy is filled in when the inbound is saved (see ServerPrivateKey).
func (c *clientBuilder) serverPublicKey() string {
	private := c.t.Ext().WGKey
	if private == "" {
		private = str(c.t["private-key"])
	}
	if private == "" {
		return ""
	}
	pub, err := WGServerPublicKey(private)
	if err != nil {
		return ""
	}
	return pub
}

// userAddress is the inner address of this user, computed exactly as the core does.
//
// The address depends on the whole set of users (a hash of the key picks a slot, a taken
// slot steps on), so a subscription renders all of an inbound's users and passes this
// user's address in. A single test render computes the set from the one slot it has.
func (c *clientBuilder) userAddress() string {
	addr := c.in.WGAddress
	if !addr.IsValid() {
		tunnel := c.t.Clone()
		if v4, _ := wgTunnel(tunnel); v4 != "" {
			tunnel["ipv4-cidr"] = v4
		}
		if _, v6 := wgTunnel(tunnel); v6 != "" {
			tunnel["ipv6-cidr"] = v6
		}
		addrs, err := WGAddresses(tunnel, []Slot{c.in.Slot})
		if err != nil {
			return ""
		}
		addr = addrs[c.in.Slot.Name]
	}
	if !addr.IsValid() {
		return ""
	}
	return addr.String()
}
