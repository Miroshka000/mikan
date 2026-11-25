package proto

// Behind the node's SNI router: the inbound shares the node's TCP 443 with others, told
// apart by the TLS name the client asks for, and listens on loopback with PROXY protocol
// (the node's sni.go). Clients connect to 443. It takes a TCP protocol over TLS or REALITY:
// the router reads a ClientHello.

// CanSNI443 says whether the template can go behind the router.
func CanSNI443(t Template) bool {
	switch t.Type() {
	case "vless", "trojan", "anytls":
	default:
		return false
	}
	if t.Network() != "tcp" {
		return false
	}
	_, reality := t["reality-config"].(map[string]any)
	return reality || t.Ext().TLS == "node" || t.Type() == "anytls"
}

// SNI443 says whether the inbound is behind the router.
func SNI443(t Template) bool { return t.Ext().SNI443 }

// SetSNI443 puts the inbound behind the router or takes it out.
func SetSNI443(t Template, on bool) error {
	if !on {
		setExt(t, "sni443", nil)
		return nil
	}
	if !CanSNI443(t) {
		return fail("sni443_type", extKey+".sni443")
	}
	setExt(t, "sni443", true)
	return nil
}

// SNINames are the names the router sends to the inbound: a REALITY inbound's server
// names, else the name its clients send (the client SNI set for it, or the node's domain).
func SNINames(t Template, nodeDomain string) []string {
	if rc, ok := t["reality-config"].(map[string]any); ok {
		return strings1(rc["server-names"])
	}
	if sni := t.Ext().Client.SNI; sni != "" {
		return []string{sni}
	}
	if nodeDomain != "" {
		return []string{nodeDomain}
	}
	return nil
}

// Reality says whether the template is a REALITY inbound.
func Reality(t Template) bool {
	_, ok := t["reality-config"].(map[string]any)
	return ok
}

// Silent says whether the template is a protocol with no handshake a stranger could read:
// its TCP port takes a connection and answers nothing to whatever is sent. Nothing makes
// such a port look like a site's, and it cannot go behind the router, which tells its
// inbounds apart by a TLS name.
func Silent(t Template) bool {
	if t.Network() != "tcp" || CanSNI443(t) {
		return false
	}
	switch t.Type() {
	case "shadowsocks", "mieru", "sudoku", "snell":
		return true
	}
	return false
}
