package node

import (
	"encoding/json"
	"fmt"
	"strings"

	"mikan/internal/nodeapi"
	"mikan/internal/proto"
)

// listenerNames are the listeners the state runs, the relay included: rules may name
// only these.
func listenerNames(st nodeapi.DesiredState) map[string]bool {
	present := map[string]bool{}
	for _, in := range st.Inbounds {
		present[in.Name] = true
	}
	if st.Relay != nil {
		present[nodeapi.RelayListener] = true
	}
	return present
}

// relayListener is the hidden VLESS REALITY door other nodes of the panel come in by. On the
// panel's own node it may show the panel's own HTTPS, as the node's protocols may
// (selfSteal, the state's SelfStealPort).
func relayListener(r *nodeapi.Relay, cert proto.Cert, selfSteal int) (map[string]any, error) {
	t, err := proto.FromJSON(r.Config)
	if err != nil {
		return nil, err
	}
	if t.Type() != "vless" || t["reality-config"] == nil {
		return nil, fmt.Errorf("the relay is VLESS REALITY")
	}
	return proto.Listener(t, nodeapi.RelayListener, "", r.Port, r.Users, cert, proto.Options{SelfStealPort: selfSteal})
}

// exitProxyConfig is the outbound to another node's relay. The panel builds it; the node
// only names it and keeps it to the protocol relays speak.
func exitProxyConfig(e nodeapi.Exit) (map[string]any, error) {
	if !strings.HasPrefix(e.Name, "NODE-") || !safeRuleValue(e.Name) {
		return nil, fmt.Errorf("exit name %q", e.Name)
	}
	var p map[string]any
	if err := json.Unmarshal(e.Proxy, &p); err != nil {
		return nil, fmt.Errorf("exit %s: %w", e.Name, err)
	}
	if p["type"] != "vless" {
		return nil, fmt.Errorf("exit %s: the relay is VLESS", e.Name)
	}
	p["name"] = e.Name
	return p, nil
}

// exitRules send the inbounds of each exit to that node. An inbound named by several
// exits goes to the first.
func exitRules(st nodeapi.DesiredState) []string {
	present := listenerNames(st)
	taken := map[string]bool{}
	var r []string
	for _, e := range st.Exits {
		if !strings.HasPrefix(e.Name, "NODE-") || !safeRuleValue(e.Name) {
			continue
		}
		for _, name := range e.Inbounds {
			if present[name] && !taken[name] && safeRuleValue(name) {
				taken[name] = true
				r = append(r, "IN-NAME,"+name+","+e.Name)
			}
		}
	}
	return r
}
