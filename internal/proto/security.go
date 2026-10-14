package proto

import (
	"crypto/ecdh"
	"encoding/base64"
	"encoding/hex"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
)

// Options relax checks for one installation.
type Options struct {
	// SelfStealPort allows REALITY dest 127.0.0.1:<port>: the panel's own HTTPS with a
	// real certificate for the server's domain, so the SNI matches the IP.
	SelfStealPort int
	// AnyDest skips the dest check (rendering clients of an already accepted template).
	AnyDest bool
}

func validateReality(r map[string]any, o Options) error {
	dest, _ := r["dest"].(string)
	host, port, err := net.SplitHostPort(dest)
	if err != nil || host == "" {
		return fail("reality_dest", "reality-config.dest")
	}
	if p, err := strconv.Atoi(port); err != nil || p < 1 || p > 65535 {
		return fail("reality_dest", "reality-config.dest")
	}
	// The node dials dest for every probe of the port, so an internal address here would
	// publish that service to the internet.
	if !o.AnyDest && !PublicHost(host) && !(o.SelfStealPort > 0 && (host == "127.0.0.1" || host == "localhost") && port == strconv.Itoa(o.SelfStealPort)) {
		return fail("reality_dest_private", "reality-config.dest")
	}
	if _, err := RealityPublicKey(str(r["private-key"])); err != nil {
		return fail("reality_key", "reality-config.private-key")
	}
	names := strings1(r["server-names"])
	if len(names) == 0 {
		return fail("reality_sni", "reality-config.server-names")
	}
	sids := strings1(r["short-id"])
	if len(sids) == 0 {
		return fail("reality_sid", "reality-config.short-id")
	}
	for _, s := range sids {
		if len(s) > 16 || len(s)%2 != 0 {
			return fail("reality_sid", "reality-config.short-id")
		}
		if _, err := hex.DecodeString(s); err != nil {
			return fail("reality_sid", "reality-config.short-id")
		}
	}
	if _, ok := r["proxy"]; ok {
		return fail("config_key", "reality-config.proxy")
	}
	return nil
}

// realityKeys remembers the public key of each private key asked about (a panel has a
// handful): deriving it is an x25519 scalar multiplication, and every subscription fetch
// and every validation of a REALITY template does it.
var realityKeys sync.Map

// RealityPublicKey derives the client's public key from the server's private key.
func RealityPublicKey(private string) (string, error) {
	if pub, ok := realityKeys.Load(private); ok {
		return pub.(string), nil
	}
	pub, err := realityPublicKey(private)
	if err == nil {
		realityKeys.Store(private, pub)
	}
	return pub, err
}

func realityPublicKey(private string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(private, "="))
	if err != nil {
		return "", err
	}
	k, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()), nil
}

// PublicHost is the syntax of a host that may be dialed on behalf of the admin: DNS names
// and public addresses pass, literal private addresses, localhost in any spelling and bare
// names do not. It does not resolve: a name that leads to a private address passes here,
// and whoever dials it checks the address it gets (scan.Check, Inbounds.CheckTemplate).
func PublicHost(h string) bool {
	// "localhost." and "LOCALHOST" are the same name; so is anything under .localhost.
	h = strings.ToLower(strings.TrimSuffix(strings.Trim(h, "[]"), "."))
	if h == "localhost" || strings.HasSuffix(h, ".localhost") || !strings.Contains(h, ".") && !strings.Contains(h, ":") {
		return false
	}
	ip, err := netip.ParseAddr(h)
	if err != nil {
		return true
	}
	return PublicAddr(ip)
}

// nat64 is the well-known NAT64 prefix.
var nat64 = netip.MustParsePrefix("64:ff9b::/96")

// Unwrap4 is the address a dial judges: an IPv4-mapped one as IPv4, and an IPv4 address inside
// the NAT64 prefix as itself, so 64:ff9b::a9fe:a9fe does not reach 169.254.169.254.
func Unwrap4(ip netip.Addr) netip.Addr {
	ip = ip.Unmap()
	if nat64.Contains(ip) {
		b := ip.As16()
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
	}
	return ip
}

// PublicAddr: an address on the internet, not one of this host's or its network's.
func PublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return !(ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() ||
		netip.MustParsePrefix("100.64.0.0/10").Contains(ip))
}

func publicHTTPS(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.User == nil && PublicHost(u.Hostname())
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// strings1 accepts a YAML list or a single string.
func strings1(v any) []string {
	switch x := v.(type) {
	case string:
		if x == "" {
			return nil
		}
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return x
	}
	return nil
}
