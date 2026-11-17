package proto

import (
	"net/netip"
	"regexp"
	"strings"
)

// CDN says that apps reach an inbound through a CDN (Cloudflare, Yandex Cloud CDN, VK,
// Selectel and the like): they connect to the CDN's domain, and the CDN to the node on the
// node's own domain and certificate. Their address is set like any other client endpoint
// (SetClientEndpoint); what this adds is what a CDN changes:
//
//   - the apps see the CDN's certificate, never the node's, so a self-signed one is not
//     pinned for them;
//   - the node sees the CDN's addresses, so device limits and the ingress filter would
//     count edge servers: with Header set, the node takes the client's address from the
//     header the CDN adds, from the CDN's networks (Trusted) only.
//
// It needs VLESS or Trojan on the node certificate over XHTTP, WebSocket or gRPC: a CDN
// carries HTTP, not REALITY or a bare TLS stream.
type CDN struct {
	// Header is where the CDN puts the client's address: CF-Connecting-IP (Cloudflare),
	// X-Forwarded-For, X-Real-IP. Empty: the node keeps the CDN's addresses.
	Header string `json:"header,omitempty" yaml:"header,omitempty"`
	// Trusted are the networks whose header is believed: CIDRs, addresses and the names
	// cloudflare, private and loopback.
	Trusted []string `json:"trusted,omitempty" yaml:"trusted,omitempty"`
}

// TrustedCloudflare names Cloudflare's networks in CDN.Trusted.
const TrustedCloudflare = "cloudflare"

// cloudflareNets are Cloudflare's published edge networks (www.cloudflare.com/ips, as of
// 2026-10). They change rarely; an admin may list new ones next to the name.
var cloudflareNets = []string{
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18", "108.162.192.0/18",
	"190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
	"104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
}

var headerName = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

// CDNOf is the inbound's CDN section, nil when it is not behind one.
func CDNOf(t Template) *CDN { return t.Ext().CDN }

// SetCDN puts the inbound behind a CDN (c) or takes it out (nil).
func SetCDN(t Template, c *CDN) error {
	if c == nil {
		setExt(t, "cdn", nil)
		return nil
	}
	n := CDN{Header: strings.TrimSpace(c.Header)}
	for _, s := range c.Trusted {
		if s = strings.TrimSpace(s); s != "" {
			n.Trusted = append(n.Trusted, s)
		}
	}
	if err := n.check(t); err != nil {
		return err
	}
	m := map[string]any{}
	if n.Header != "" {
		m["header"] = n.Header
	}
	if len(n.Trusted) > 0 {
		list := make([]any, len(n.Trusted))
		for i, s := range n.Trusted {
			list[i] = s
		}
		m["trusted"] = list
	}
	setExt(t, "cdn", m)
	return nil
}

// CanCDN says whether t may be put behind a CDN.
func CanCDN(t Template) bool { return CDN{}.check(t) == nil }

// check refuses a CDN section the template cannot have.
func (c CDN) check(t Template) error {
	const field = extKey + ".cdn"
	switch typ := t.Type(); {
	case typ != "vless" && typ != "trojan", t.Ext().TLS != "node":
		return fail("cdn_type", field)
	}
	switch Transport(t) {
	case "xhttp", "ws", "grpc":
	default:
		return fail("cdn_transport", field)
	}
	if t.Ext().Flow != "" {
		return fail("cdn_transport", field)
	}
	if c.Header != "" && !headerName.MatchString(c.Header) {
		return fail("cdn_header", field+".header")
	}
	if c.Header != "" && len(c.Trusted) == 0 {
		// The core believes the header from nobody without them, and from anybody it
		// must not: a client reaching the node directly could name any address.
		return fail("cdn_trusted_needed", field+".trusted")
	}
	for _, s := range c.Trusted {
		if !validTrusted(s) {
			return fail("cdn_trusted", field+".trusted")
		}
	}
	return nil
}

func validTrusted(s string) bool {
	switch s {
	case TrustedCloudflare, "private", "loopback":
		return true
	}
	if _, err := netip.ParsePrefix(s); err == nil {
		return true
	}
	_, err := netip.ParseAddr(s)
	return err == nil
}

// cdnListener gives the listener the client's address from the CDN's header.
func cdnListener(t Template, l map[string]any) {
	c := CDNOf(t)
	if c == nil || c.Header == "" || len(c.Trusted) == 0 {
		return
	}
	var trusted []any
	for _, s := range c.Trusted {
		if s == TrustedCloudflare {
			for _, n := range cloudflareNets {
				trusted = append(trusted, n)
			}
			continue
		}
		trusted = append(trusted, s)
	}
	l["trusted-proxy-header"], l["trusted-proxies"] = c.Header, trusted
}

// setExt writes one key of the mikan section; nil removes it, and the section when it is
// left empty.
func setExt(t Template, key string, v any) {
	ext, _ := t[extKey].(map[string]any)
	if v == nil {
		if ext == nil {
			return
		}
		delete(ext, key)
		if len(ext) == 0 {
			delete(t, extKey)
		}
		return
	}
	if ext == nil {
		ext = map[string]any{}
		t[extKey] = ext
	}
	ext[key] = v
}
