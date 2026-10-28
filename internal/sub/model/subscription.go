package model

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"mikan/internal/textutil"
)

// Endpoint describes how clients reach one node.
type Endpoint struct {
	Host      string // IP or domain the client connects to
	SNI       string // TLS server name for Hysteria2/TUIC; empty for IP-only nodes
	PinSHA256 string // hex SHA-256 of the node's self-signed Hysteria2/TUIC certificate
	// SNIRouter: the node shares its 443 between the inbounds marked for it, which clients
	// reach on 443.
	SNIRouter bool
}

// Node is one server in the subscription.
type Node struct {
	ID int64
	// Name is the node's country group, e.g. "🇳🇱 Нидерланды"; with several nodes its
	// leading flag (or the whole name) prefixes proxy names.
	Name     string
	Endpoint Endpoint
	// SSOneKey: the node is older than the Shadowsocks-2022 key per user (build.Input).
	SSOneKey bool
	// NoAuto keeps the node's servers out of the automatic choice: the admin's word (a
	// server for whitelist-only networks), or the node does not answer the panel.
	NoAuto bool
}

// Inbound is one inbound of a node as the subscription sees it.
type Inbound struct {
	ID   int64
	Node int64
	Pool int64 // the inbound's server group; 0: none, the main servers
	// Label is the inbound's name in the admin panel, which a log about it gives.
	Label string
	// Name is its name in the subscription: the admin's own (Named) or its protocol's.
	Name string
	// Named: the admin named it, and the name goes as typed, without the node's flag.
	Named bool
	// Port is the first port of PortSpec, the one a client dials first; 0 when PortSpec
	// reads as no port, and the inbound is left out.
	Port     int
	PortSpec string // "443", or a Hysteria2 hopping range ("20000-30000")
	Config   string // the template (proto)
	// NoAuto keeps the inbound's servers out of the automatic choice (as Node.NoAuto).
	NoAuto bool
	// Hosts are the places clients reach the inbound at, the default one first. Until the
	// build reads them it reads the template's mikan.client, which the default host repeats.
	Hosts []Host
}

// Host is one place clients reach an inbound at; an empty field (port 0) keeps what they get
// without it: the node's address, the inbound's port, the usual TLS name, the server's HTTP
// Host, the panel's fingerprint.
type Host struct {
	Address     string
	Port        int
	SNI         string
	Host        string // HTTP Host of WebSocket and XHTTP
	Fingerprint string // uTLS profile
}

// GroupNames are the proxy-group names Clash-family apps show; the admin can rename them.
type GroupNames struct {
	Main string // selector, "VPN" by default
	Auto string // url-test, "Авто" by default, "Auto" on a panel in English
}

// AliasGroup is the name Clash apps assume for the main group in the rules they inject
// themselves (Koala Clash per-app routing: "PROCESS-NAME,app.exe,PROXY").
const AliasGroup = "PROXY"

// WithDefaults fills in the names the admin left empty, in the panel's default language
// ("en", Russian otherwise).
func (g GroupNames) WithDefaults(lang string) GroupNames {
	if g.Main == "" {
		g.Main = "VPN"
	}
	if g.Auto == "" {
		g.Auto = "Авто"
		if textutil.Lang(lang).En() {
			g.Auto = "Auto"
		}
	}
	return g
}

// CheckName refuses a name the admin typed for something that shows in Clash apps next to
// the groups (a node's country group, a protocol's connection): the name must pass
// ValidName and not repeat one of the groups, or the profile does not load. The error is a
// code (name_blank, name_is_group…) the UI turns into words.
func (g GroupNames) CheckName(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	for _, x := range []string{g.Main, g.Auto, AliasGroup} {
		if strings.EqualFold(name, x) {
			return errors.New("name_is_group")
		}
	}
	return nil
}

// reserved names are mihomo's built-in policies; a group or proxy with such a name
// breaks the profile.
var reserved = []string{"DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE", "GLOBAL"}

// ValidName checks a group or proxy name the admin typed. Commas are refused because
// rules reference groups as "MATCH,<name>".
func ValidName(s string) error {
	switch {
	case s == "" || strings.TrimSpace(s) != s:
		return errors.New("name_blank")
	case utf8.RuneCountInString(s) > 48:
		return errors.New("name_too_long")
	case strings.ContainsAny(s, ",\"'\\"):
		return errors.New("name_bad_char")
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return errors.New("name_bad_char")
		}
	}
	for _, x := range reserved {
		if strings.EqualFold(s, x) {
			return errors.New("name_reserved")
		}
	}
	return nil
}

// ErrNoProxies: nothing of the profile is left for the app, or for any app.
var ErrNoProxies = errors.New("no proxies")
