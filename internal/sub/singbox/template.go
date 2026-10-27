package singbox

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
)

// An own sing-box template (model.CoreSingBox) is a sing-box config without the servers:
// the apps get what the admin wrote, with the user's servers in it. The panel adds them to
// outbounds after the template's own, tagged by their names (an own outbound of a server's
// name stays in its place), and fills the groups that ask for them: a selector or an
// urltest with "mikan": {} takes every server, narrowed by nodes, types and filter
// (model.Ask), after the outbounds it lists itself. A group left with none gets REJECT, an
// outbound that refuses every connection: closed, not open. The panel's and the nodes'
// hosts go direct ahead of the template's route rules, after its sniff and DNS hijack.

// rejectTag is the outbound of a group left without servers. sing-box has had no outbound
// that blocks since 1.13, so it is a SOCKS server on a port of the device nobody listens
// on: every connection through it is refused at once.
const rejectTag = "REJECT"

// Template renders the own sing-box template src for p.
func Template(p mihomo.Profile, src string) ([]byte, error) {
	cfg, err := templateConfig(p, src)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(cfg, "", "  ")
}

// CheckTemplate finds what would break the configs made of src for p: JSON that does not
// parse, outbounds without a tag or with one tag twice, a group that asks for servers in a
// way the panel does not read, and what names an outbound or a rule set that is not there.
func CheckTemplate(p mihomo.Profile, src string) error {
	_, err := templateConfig(p, src)
	return err
}

// templateConfig is the config src makes for p, checked.
func templateConfig(p mihomo.Profile, src string) (map[string]any, error) {
	if len(p.Proxies) == 0 {
		return nil, model.ErrNoProxies
	}
	cfg, err := model.ParseJSONTemplate(src)
	if err != nil {
		return nil, err
	}
	raw, ok := cfg["outbounds"].([]any)
	if !ok && cfg["outbounds"] != nil {
		return nil, model.TemplateErr("template_shape", "outbounds")
	}
	tags := map[string]bool{}
	own := make([]any, 0, len(raw))
	direct := ""
	for i, x := range raw {
		o, _ := x.(map[string]any)
		tag := str(o["tag"])
		switch {
		case tag == "":
			return nil, model.TemplateErr("template_tag", strconv.Itoa(i+1))
		case tags[tag]:
			return nil, model.TemplateErr("template_tag_dup", tag)
		}
		tags[tag] = true
		if direct == "" && str(o["type"]) == "direct" {
			direct = tag
		}
		own = append(own, o)
	}
	var servers []any
	var carried []model.Proxy
	for _, x := range p.Proxies {
		if tags[x.Name] {
			continue // the template's own outbound of that name stays
		}
		if o, ok := singboxOutbound(mihomo.Proxy(x)); ok {
			servers = append(servers, o)
			carried = append(carried, x)
		}
	}
	if len(servers) == 0 {
		return nil, model.ErrNoProxies // nothing of the user's sing-box can carry
	}
	reject := false
	for _, x := range own {
		o := x.(map[string]any)
		v, asks := o["mikan"]
		if !asks {
			continue
		}
		tag := str(o["tag"])
		if typ := str(o["type"]); typ != "selector" && typ != "urltest" {
			return nil, model.TemplateErr("template_ask", tag)
		}
		ask, ok := model.ReadAsk(v, nil)
		picked, err := ask.Take(carried, p.Nodes)
		if !ok || err != nil {
			return nil, model.TemplateErr("template_ask", tag)
		}
		members, ok := o["outbounds"].([]any)
		if !ok && o["outbounds"] != nil {
			return nil, model.TemplateErr("template_shape", tag+".outbounds")
		}
		members = slices.Clone(members)
		for _, x := range picked {
			members = append(members, x.Name)
		}
		if len(members) == 0 {
			members, reject = []any{rejectTag}, true
		}
		o["outbounds"] = members
		delete(o, "mikan")
	}
	outs := append(own, servers...)
	if reject && !tags[rejectTag] {
		outs = append(outs, map[string]any{"type": "socks", "tag": rejectTag, "server": "127.0.0.1", "server_port": 1})
	}
	if direct == "" {
		direct = model.FreeTag("direct", tags)
		outs = append(outs, map[string]any{"type": "direct", "tag": direct})
	}
	cfg["outbounds"] = outs
	// Checked before the panel's own rule goes in, for a refusal to name the admin's rules
	// by their places.
	if err := checkConfig(cfg); err != nil {
		return nil, err
	}
	hostsDirect(cfg, p.Direct, direct)
	return cfg, nil
}

// hostsDirect sends the panel's and the nodes' hosts to the outbound direct, ahead of the
// template's route rules but after the ones that only look at a connection (sniff, the DNS
// hijack, resolve): a host is matched by the name sniffing finds. cfg is checked.
func hostsDirect(cfg map[string]any, hosts []string, direct string) {
	var domains, cidrs []string
	for _, r := range routing.HostRules(hosts) {
		for _, m := range r.When {
			if m.Type == "DOMAIN" {
				domains = append(domains, m.Value)
			} else {
				cidrs = append(cidrs, m.Value)
			}
		}
	}
	if len(domains)+len(cidrs) == 0 {
		return
	}
	// sing-box matches a rule's domains and addresses one or another.
	rule := map[string]any{"outbound": direct}
	if len(domains) > 0 {
		rule["domain"] = domains
	}
	if len(cidrs) > 0 {
		rule["ip_cidr"] = cidrs
	}
	route, _ := cfg["route"].(map[string]any)
	if route == nil {
		route = map[string]any{}
		cfg["route"] = route
	}
	rules, _ := route["rules"].([]any)
	at := 0
	for at < len(rules) {
		switch str(anyMap(rules[at])["action"]) {
		case "sniff", "hijack-dns", "resolve":
			at++
			continue
		}
		break
	}
	route["rules"] = slices.Insert(slices.Clone(rules), at, any(rule))
}

// checkConfig checks a filled template: what the groups, the rules, the rule sets, the DNS
// servers and the outbounds dialing through others name must be there.
func checkConfig(cfg map[string]any) error {
	outs := cfg["outbounds"].([]any)
	tags := map[string]bool{}
	for _, x := range outs {
		tags[str(x.(map[string]any)["tag"])] = true
	}
	known := func(tag string) bool { return tag == "" || tags[tag] }
	for _, x := range outs {
		o := x.(map[string]any)
		tag := str(o["tag"])
		switch str(o["type"]) {
		case "selector", "urltest":
			members, ok := o["outbounds"].([]any)
			if !ok || len(members) == 0 {
				return model.TemplateErr("template_group_empty", tag)
			}
			for _, m := range members {
				if s, ok := m.(string); !ok || !tags[s] {
					return model.TemplateErr("template_group_member", tag+" → "+str(m))
				}
			}
			if d := str(o["default"]); !known(d) {
				return model.TemplateErr("template_group_member", tag+" → "+d)
			}
		}
		if d := str(o["detour"]); !known(d) {
			return model.TemplateErr("template_detour", tag+" → "+d)
		}
	}
	route, ok := cfg["route"].(map[string]any)
	if !ok && cfg["route"] != nil {
		return model.TemplateErr("template_shape", "route")
	}
	sets := map[string]bool{}
	setList, ok := route["rule_set"].([]any)
	if !ok && route["rule_set"] != nil {
		return model.TemplateErr("template_shape", "route.rule_set")
	}
	for i, x := range setList {
		s, _ := x.(map[string]any)
		tag := str(s["tag"])
		if tag == "" {
			return model.TemplateErr("template_rule_set_tag", strconv.Itoa(i+1))
		}
		sets[tag] = true
		if d := str(s["download_detour"]); !known(d) {
			return model.TemplateErr("template_detour", tag+" → "+d)
		}
	}
	rules, ok := route["rules"].([]any)
	if !ok && route["rules"] != nil {
		return model.TemplateErr("template_shape", "route.rules")
	}
	for i, x := range rules {
		if err := checkRule(x, strconv.Itoa(i+1), tags, sets); err != nil {
			return err
		}
	}
	if f := str(route["final"]); !known(f) {
		return model.TemplateErr("template_final", f)
	}
	dns, ok := cfg["dns"].(map[string]any)
	if !ok && cfg["dns"] != nil {
		return model.TemplateErr("template_shape", "dns")
	}
	servers, ok := dns["servers"].([]any)
	if !ok && dns["servers"] != nil {
		return model.TemplateErr("template_shape", "dns.servers")
	}
	for _, x := range servers {
		s, _ := x.(map[string]any)
		if d := str(s["detour"]); !known(d) {
			return model.TemplateErr("template_detour", str(s["tag"])+" → "+d)
		}
	}
	return nil
}

// checkRule checks a route rule at its place n: the outbound it sends to and the rule sets
// it reads, of the rules inside a logical one too.
func checkRule(x any, n string, tags, sets map[string]bool) error {
	r, ok := x.(map[string]any)
	if !ok {
		return model.TemplateErr("template_rule_object", n)
	}
	if o := str(r["outbound"]); o != "" && !tags[o] {
		return model.TemplateErr("template_rule_target", fmt.Sprintf("%s (%s)", n, o))
	}
	var names []any
	switch v := r["rule_set"].(type) {
	case string:
		names = []any{v}
	case []any:
		names = v
	}
	for _, s := range names {
		if !sets[str(s)] {
			return model.TemplateErr("template_rule_set", fmt.Sprintf("%s (%s)", n, str(s)))
		}
	}
	inner, _ := r["rules"].([]any)
	for _, x := range inner {
		if err := checkRule(x, n, tags, sets); err != nil {
			return err
		}
	}
	return nil
}

// Example is an own template to start from, with the panel's group names: the main group
// to choose a server in and the automatic choice over every server, a TUN, DNS through the
// tunnel, the local networks direct. It takes the place of the panel's routing, so it has
// none of it.
func Example(g model.GroupNames) []byte {
	name := func(s string) string {
		b, _ := json.Marshal(s)
		return string(b)
	}
	return fmt.Appendf(nil, `{
  "log": {"level": "warn"},
  "dns": {
    "servers": [
      {"type": "https", "tag": "remote", "server": "1.1.1.1", "detour": %[1]s},
      {"type": "udp", "tag": "local", "server": "1.1.1.1"}
    ],
    "final": "remote"
  },
  "inbounds": [
    {"type": "tun", "tag": "tun-in", "address": ["172.19.0.1/30"], "auto_route": true, "strict_route": true}
  ],
  "outbounds": [
    {"type": "selector", "tag": %[1]s, "outbounds": [%[2]s], "mikan": {}},
    {"type": "urltest", "tag": %[2]s, "url": "https://www.gstatic.com/generate_204", "interval": "5m", "mikan": {}},
    {"type": "direct", "tag": "direct"}
  ],
  "route": {
    "rules": [
      {"action": "sniff"},
      {"protocol": "dns", "action": "hijack-dns"},
      {"ip_is_private": true, "outbound": "direct"}
    ],
    "final": %[1]s,
    "auto_detect_interface": true,
    "default_domain_resolver": "local"
  },
  "experimental": {"cache_file": {"enabled": true}}
}
`, name(g.Main), name(g.Auto))
}
