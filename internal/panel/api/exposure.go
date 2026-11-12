package api

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
	"mikan/internal/scan"
)

// What makes a node's server stand out to someone who looks at it from outside: a scanner
// that connects to its ports, a network that sees where its clients go. None of it breaks
// anything, so "Check node" only warns; each line names what to change. The list is what
// the panel can tell from its own settings, without a connection to the node.

// telling are the words that say what a server is for when they stand in its name.
var telling = []string{"vpn", "proxy", "vless", "vmess", "xray", "v2ray", "trojan", "hysteria", "wireguard", "shadowsocks", "tunnel"}

// exposure is the node's setup as the check reads it.
type exposure struct {
	node     db.Node
	inbounds []db.Inbound // the node's, enabled or not
	names    []string     // the panel's own names: its domain and the subscriptions' domain
}

// exposureItems are the lines of "Check node" about how the node looks from outside: one
// for each thing that stands out, or one that says nothing does.
func (h *handlers) exposureItems(ctx context.Context, n db.Node) []CheckItem {
	ins, err := h.d.Store.Q.ListNodeInbounds(ctx, n.ID)
	if err != nil {
		return nil
	}
	e := exposure{node: n, inbounds: ins}
	for _, key := range []string{settings.KeyDomain, settings.KeySubDomain} {
		if name, err := h.d.Settings.String(ctx, key); err == nil && name != "" {
			e.names = append(e.names, name)
		}
	}
	return e.items()
}

func (e exposure) items() []CheckItem {
	var out []CheckItem
	warn := func(code string, params map[string]string) {
		out = append(out, CheckItem{ID: "exposure", Status: nodeapi.CheckWarn, Code: code, Params: params})
	}
	var (
		sites  []string // the REALITY sites the node's ports show, each once
		weak   []string
		stock  []string // inbounds still on the site every fresh installation starts with
		ports  []string // TCP ports the node takes TLS connections on, each its own
		cdn    []string // inbounds clients reach through a CDN
		direct int      // inbounds clients reach at the node's own address
	)
	for _, in := range e.inbounds {
		if in.Enabled == 0 {
			continue
		}
		t, err := proto.Parse(in.Config)
		if err != nil {
			continue
		}
		if t.Ext().Client.Server != "" || in.Preset == presets.PresetCDNXHTTP || in.Preset == presets.PresetCDNGet {
			cdn = append(cdn, in.Name)
		} else {
			direct++
		}
		if t.Network() != "udp" && !(e.node.SniRouter != 0 && proto.SNI443(t) && proto.CanSNI443(t)) && !slices.Contains(ports, in.Port) {
			ports = append(ports, in.Port)
		}
		if !proto.Reality(t) {
			continue
		}
		dest, names := presets.Dest(t)
		if dest == presets.DefaultDest {
			stock = append(stock, in.Name)
		}
		for _, name := range names {
			name = strings.ToLower(name)
			if slices.Contains(sites, name) {
				continue
			}
			sites = append(sites, name)
			if scan.Weak(name) {
				weak = append(weak, name)
			}
		}
	}
	// Several strangers' certificates on one address: each REALITY port shows the site of
	// its own, and no ordinary server holds the certificates of three other sites.
	if len(sites) > 1 {
		warn("many_sites", map[string]string{"n": strconv.Itoa(len(sites)), "sites": strings.Join(sites, ", ")})
	}
	if len(weak) > 0 {
		warn("weak_site", map[string]string{"sites": strings.Join(weak, ", ")})
	}
	if len(stock) > 0 {
		host, _, _ := strings.Cut(presets.DefaultDest, ":")
		warn("default_site", map[string]string{"site": host, "inbounds": strings.Join(stock, ", ")})
	}
	// A row of TLS ports, each with its own protocol, where one 443 would do.
	if len(ports) > 2 {
		warn("many_ports", map[string]string{"n": strconv.Itoa(len(ports)), "ports": strings.Join(ports, ", ")})
	}
	// The server a CDN hides is found again when clients also connect to it straight.
	if len(cdn) > 0 && direct > 0 {
		warn("cdn_and_direct", map[string]string{"inbounds": strings.Join(cdn, ", ")})
	}
	if e.node.Address != "" && e.node.ApiLockdown == 0 {
		warn("api_open", nil)
	}
	names := e.names
	if e.node.Address != "" {
		names = nil // a remote node shows its own name, not the panel's
	}
	if e.node.Domain != "" {
		names = append(slices.Clone(names), e.node.Domain)
	}
	for _, name := range names {
		if word := tellingWord(name); word != "" {
			warn("telling_name", map[string]string{"name": name, "word": word})
			break
		}
	}
	if len(out) == 0 {
		return []CheckItem{{ID: "exposure", Status: nodeapi.CheckOK}}
	}
	return out
}

// tellingWord is the word of telling in a label of name, "" when it has none: the name goes
// in the clear in every connection to the server and stands in its certificate.
func tellingWord(name string) string {
	for _, label := range strings.Split(strings.ToLower(name), ".") {
		for _, w := range telling {
			if strings.Contains(label, w) {
				return w
			}
		}
	}
	return ""
}
