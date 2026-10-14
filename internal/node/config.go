package node

import (
	"encoding/json"

	"github.com/metacubex/mihomo/listener"

	"mikan/internal/nodeapi"
	"mikan/internal/proto"
)

// Traffic to the node's own networks is refused: without these rules any VPN user could
// reach the host's loopback services or the cloud metadata endpoint (169.254.169.254).
// IP rules without no-resolve also catch domains that resolve to private addresses.
var privateRules = []string{
	"IP-CIDR,0.0.0.0/8,REJECT",
	"IP-CIDR,10.0.0.0/8,REJECT",
	"IP-CIDR,100.64.0.0/10,REJECT",
	"IP-CIDR,127.0.0.0/8,REJECT",
	"IP-CIDR,169.254.0.0/16,REJECT",
	"IP-CIDR,172.16.0.0/12,REJECT",
	"IP-CIDR,192.168.0.0/16,REJECT",
	"IP-CIDR,224.0.0.0/3,REJECT",
	"IP-CIDR6,::/128,REJECT", // "any address": a connection to it reaches the host itself
	"IP-CIDR6,::1/128,REJECT",
	// IPv4 written as IPv6 (::ffff:127.0.0.1) does not match the IPv4 ranges above.
	"IP-CIDR6,::ffff:0:0/96,REJECT",
	"IP-CIDR6,fc00::/7,REJECT",
	"IP-CIDR6,fe80::/10,REJECT",
}

func rules(st nodeapi.DesiredState, allowPrivate bool) []string {
	var r []string
	if !allowPrivate {
		r = append(r, privateRules...)
	}
	// Outbound SMTP from a shared VPN IP gets the address blacklisted within hours.
	r = append(r, "DST-PORT,25,REJECT")
	r = append(r, egressRules(st.Filters)...)
	r = append(r, exitRules(st)...)
	r = append(r, warpRules(st)...)
	return append(r, "MATCH,DIRECT")
}

// outbounds are the proxies besides DIRECT: WARP and the other nodes used as exits.
func outbounds(st nodeapi.DesiredState) ([]any, error) {
	out := []any{}
	if st.Warp != nil {
		p, err := warpProxyConfig(st.Warp)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	for _, e := range st.Exits {
		p, err := exitProxyConfig(e)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// buildConfig renders the mihomo config as JSON, which mihomo's YAML parser accepts.
// log-level warning keeps per-connection lines out of mihomo's own output; the ones that
// still come through are dropped by pumpLogs.
//
// A listener that does not pass the checks is left out and reported in rejected (not OK,
// with the reason): one broken inbound must not stop the node from taking every other
// change of the state, new users and policies included. What the whole state needs, the
// outbounds, is an error as before.
func buildConfig(st nodeapi.DesiredState, cert proto.Cert, allowPrivate bool) (raw []byte, rejected []nodeapi.ListenerStatus, err error) {
	listeners := make([]map[string]any, 0, len(st.Inbounds)+1)
	keep := func(name string, l map[string]any, err error) {
		if err == nil {
			// The parser mihomo applies the config with; it opens nothing.
			_, err = listener.ParseListener(l)
		}
		if err != nil {
			rejected = append(rejected, nodeapi.ListenerStatus{Name: name, Error: err.Error()})
			return
		}
		listeners = append(listeners, l)
	}
	for _, in := range st.Inbounds {
		l, err := listenerFor(in, st.Slots, cert, proto.Options{SelfStealPort: st.SelfStealPort})
		keep(in.Name, l, err)
	}
	if st.Relay != nil {
		l, err := relayListener(st.Relay, cert)
		keep(nodeapi.RelayListener, l, err)
	}
	proxies, err := outbounds(st)
	if err != nil {
		return nil, nil, err
	}
	cfg := map[string]any{
		"mode":              "rule",
		"log-level":         "warning",
		"ipv6":              true,
		"allow-lan":         false,
		"mixed-port":        0,
		"find-process-mode": "off",
		"profile":           map[string]any{"store-selected": false, "store-fake-ip": false},
		"dns":               map[string]any{"enable": false},
		"proxies":           proxies,
		"rules":             rules(st, allowPrivate),
		"listeners":         listeners,
	}
	raw, err = json.Marshal(cfg)
	return raw, rejected, err
}

func listenerFor(in nodeapi.Inbound, slots []nodeapi.Slot, cert proto.Cert, o proto.Options) (map[string]any, error) {
	t, err := template(in)
	if err != nil {
		return nil, err
	}
	return proto.Listener(t, in.Name, in.Listen, in.Port, slots, cert, o)
}

// sharedListeners are the inbounds with one key for everyone.
func sharedListeners(st nodeapi.DesiredState) []string {
	var out []string
	for _, in := range st.Inbounds {
		if t, err := template(in); err == nil && proto.Shared(t.Type()) {
			out = append(out, in.Name)
		}
	}
	return out
}

// template reads the inbound's listener template; states saved by mikan ≤ 0.1.2 carry
// a preset with its settings instead.
func template(in nodeapi.Inbound) (proto.Template, error) {
	if len(in.Config) > 0 {
		return proto.FromJSON(in.Config)
	}
	return proto.FromPreset(in.Preset, in.Settings)
}
