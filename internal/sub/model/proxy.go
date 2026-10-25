// Package model describes a subscription apart from the format of any core: the
// connections it carries and what they are made of. Each core's renderer (mihomo,
// singbox, xray) writes the same model its own way, so a setting reaches every app that
// can take it, and an app that cannot is told so by its own renderer.
package model

// Proxy is one connection of a subscription: a user's keys on one inbound of one node, as
// the apps dial it.
type Proxy struct {
	// Name is the proxy's name in the subscription, unique in it: mihomo refuses a profile
	// with two proxies of one name.
	Name string
	Node int64 // the node the inbound is on
	Pool int64 // the inbound's server group; 0: none, the main servers
	// Type is the protocol as the inbound's template names it: vless, trojan, hysteria2,
	// tuic, anytls, shadowsocks, trusttunnel, shadowquic, mieru, sudoku or snell.
	Type   string
	Server string
	Port   int
	// Ports is the Hysteria2 hopping range the app picks its ports from ("20000-30000");
	// "": Port alone.
	Ports string
	// The user's keys as the protocol takes them: UUID for VLESS and TUIC, Username for
	// TrustTunnel, ShadowQUIC and Mieru, Password for the others (for Shadowsocks its key).
	UUID, Username, Password string
	// TLS is how the connection is secured; nil where the protocol secures it itself.
	TLS *TLS
	// Transport is what VLESS and Trojan carry inside TLS; nil for the other protocols.
	Transport *Transport
	// UDP: the connection carries UDP too. Hysteria2 and TUIC always do.
	UDP bool
	// UDPOverTCP is the version of UDP over TCP the app wraps UDP in, 0: none. Shadowsocks
	// takes it: the node's listener has no UDP port.
	UDPOverTCP int
	Flow       string // VLESS: xtls-rprx-vision; "": none
	// Encryption is VLESS Encryption as the app sends it, with the public halves of the
	// server's keys; "": none.
	Encryption string
	// ALPN is what Hysteria2, TUIC and ShadowQUIC offer; nil: the core's own.
	ALPN       []string
	Obfs       *Obfs  // Hysteria2's obfuscation; nil: none
	Congestion string // TUIC's congestion control
	Cipher     string // Shadowsocks-2022's method
	Sudoku     *Sudoku
	Snell      *Snell
}

// TLS is a connection's TLS as the app sends it.
type TLS struct {
	// SNI is the name the app asks for; "" on a node with no domain, reached by address.
	SNI string
	// Fingerprint is the uTLS profile the app dials with; "" for the protocols without uTLS
	// (Hysteria2, TUIC, ShadowQUIC).
	Fingerprint string
	// Pin is the hex SHA-256 of the node's self-signed certificate, which the app pins in
	// place of a CA; "": the certificate is checked as any website's.
	Pin string
	// Reality is the REALITY the server runs in place of a certificate of its own; nil: TLS
	// on the node's certificate.
	Reality *Reality
}

// Reality is what a REALITY client needs of the server.
type Reality struct {
	PublicKey, ShortID string
	// MLKEM: the server lets in only clients that offer the post-quantum key exchange
	// (X25519MLKEM768), so the app must offer it.
	MLKEM bool
}

// Transport is what VLESS and Trojan carry inside TLS.
type Transport struct {
	Network string // tcp, ws, grpc or xhttp
	Path    string // ws and xhttp; "": none
	// Host is the HTTP Host the app sends over WebSocket or XHTTP; "": the server's.
	Host    string
	Service string // grpc: the service name
	XHTTP   *XHTTP // xhttp
}

// XHTTP is the client side of an XHTTP transport past its path and host.
type XHTTP struct {
	Mode string // stream-one, stream-up or packet-up; "": the core's default
	// Settings are the server's settings the app must send alike (padding, session ids,
	// upload) and the app's own over them.
	Settings []Setting
	// Reuse is the app's own connection reuse (xmux), which replaces a core's default
	// whole; nil: the inbound sets none.
	Reuse []Setting
}

// Setting is one XHTTP setting, under the name each core that speaks XHTTP gives it.
type Setting struct {
	Mihomo, Xray string
	// Value is as the inbound's template holds it: a string, a number or a bool.
	Value any
}

// Obfs is Hysteria2's obfuscation.
type Obfs struct {
	Type     string // salamander or gecko
	Password string
	// MinPacket and MaxPacket are the server's Gecko packet sizes, which the app sends
	// alike; 0: the core's default.
	MinPacket, MaxPacket int
}

// Sudoku is what a Sudoku client sends; one key for everyone. Its settings are as the
// template holds them: only mihomo speaks Sudoku.
type Sudoku struct {
	Key, AEAD, TableType string
	// PaddingMin and PaddingMax are the template's own; nil: the core's default.
	PaddingMin, PaddingMax *int
	// PureDownlink is the server's downlink mode, which the client must match; nil: the
	// default.
	PureDownlink *bool
	HTTPMask     map[string]any // nil: none
}

// Snell is what a Snell client sends; one key for everyone.
type Snell struct {
	PSK      string
	Version  int
	ObfsOpts map[string]any // as the template holds them; nil: none
}
