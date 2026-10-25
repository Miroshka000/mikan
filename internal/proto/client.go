package proto

import (
	"regexp"
	"slices"

	"mikan/internal/hostname"
)

// What the apps of an inbound get past the node's own settings: their uTLS profile, and
// where they connect when it is not the node itself. Subscriptions turn a template into the
// apps' connections (internal/sub/build); here is what the admin sets of them.

// Fingerprints are the uTLS profiles that mihomo (component/tls/utls.go, v1.19.31), Xray
// and sing-box all accept, so a share link and a Clash profile mimic the same browser.
// mihomo also knows chrome120, firefox120, safari16 and deprecated ones Xray refuses.
var Fingerprints = []string{"chrome", "firefox", "safari", "ios", "android", "edge", "360", "qq", "random", "randomized"}

// DefaultFingerprint is what clients get when neither the inbound nor the panel picks one.
const DefaultFingerprint = "chrome"

// KnownFingerprint says whether s is one of Fingerprints.
func KnownFingerprint(s string) bool { return slices.Contains(Fingerprints, s) }

// fingerprintPattern is the shape of an own fingerprint the admin types: a uTLS profile
// name of mihomo or Xray that the list leaves out (chrome120, randomizednoalpn, …).
// Nothing else fits, so the value cannot break a link or a profile.
var fingerprintPattern = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// ValidFingerprint says whether s may be used: one of Fingerprints or an own one of the
// right shape. An own one is the admin's call: an app that does not know it may refuse
// the link (Xray) or connect without uTLS (mihomo).
func ValidFingerprint(s string) bool { return KnownFingerprint(s) || fingerprintPattern.MatchString(s) }

// UsesFingerprint says whether clients of t dial through uTLS, so a fingerprint applies.
func UsesFingerprint(t Template) bool {
	switch t.Type() {
	case "vless", "trojan": // REALITY or the node certificate, never plain
		return t.section("reality-config") != nil || t.Ext().TLS == "node"
	case "anytls", "trusttunnel":
		return true
	}
	return false
}

// SetFingerprint writes the inbound's own fingerprint into mikan.client; "" removes it,
// so the panel's default applies again.
func SetFingerprint(t Template, fp string) error {
	if fp != "" && !ValidFingerprint(fp) {
		return fail("config_fingerprint", extKey+".client.fingerprint")
	}
	setClient(t, "fingerprint", fp)
	return nil
}

// ClientSNI says whether clients of t send a TLS name the admin may change: the node
// certificate's protocols. A REALITY client sends the target's name, which the node
// checks against server-names, so another name would not get through.
func ClientSNI(t Template) bool {
	return t.section("reality-config") == nil && (rules[t.Type()].cert || t.Ext().TLS == "node")
}

// SetClientEndpoint writes where clients connect when it is not the node itself, e.g. a
// TCP proxy (nginx stream, HAProxy) in front of an inbound on 127.0.0.1, or a CDN. An
// empty server, SNI or host, or port 0, removes that override: the node's address, the
// inbound's port, the usual TLS name and the server's HTTP host apply again.
func SetClientEndpoint(t Template, server string, port int, sni, host string) error {
	switch {
	case server != "" && !hostname.Valid(server):
		return fail("config_client_server", extKey+".client.server")
	case port < 0 || port > 65535:
		return fail("config_client_port", extKey+".client.port")
	case sni != "" && (!hostname.Name(sni) || !ClientSNI(t)):
		return fail("config_client_sni", extKey+".client.sni")
	case host != "" && (!hostname.Name(host) || !ClientHost(t)):
		return fail("config_client_host", extKey+".client.host")
	}
	setClient(t, "server", server)
	setClient(t, "port", port)
	setClient(t, "sni", sni)
	setClient(t, "host", host)
	return nil
}

// ClientHost says whether clients of t send an HTTP Host the admin may change: VLESS and
// Trojan over WebSocket or XHTTP.
func ClientHost(t Template) bool {
	switch t.Type() {
	case "vless", "trojan":
		tr := Transport(t)
		return tr == "ws" || tr == "xhttp"
	}
	return false
}

// setClient writes one key of mikan.client; an empty value removes it, and with it the
// sections it leaves empty.
func setClient(t Template, key string, v any) {
	unset := v == "" || v == 0
	ext, _ := t[extKey].(map[string]any)
	if ext == nil {
		if unset {
			return
		}
		ext = map[string]any{}
		t[extKey] = ext
	}
	client, _ := ext["client"].(map[string]any)
	if client == nil {
		client = map[string]any{}
	}
	if unset {
		delete(client, key)
	} else {
		client[key] = v
	}
	switch {
	case len(client) > 0:
		ext["client"] = client
	default:
		delete(ext, "client")
	}
	if len(ext) == 0 {
		delete(t, extKey)
	}
}
