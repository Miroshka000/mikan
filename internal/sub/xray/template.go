package xray

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
)

// An own xray template (model.CoreXray) is one xray config without the servers, the frame
// of every config of the list. The panel makes a config of it for each server the user has,
// named by the server (remarks): the server's outbound goes first, tagged proxy, for xray
// sends what no rule takes to the first outbound. A balancer of routing.balancers with
// "mikan": {} asks for servers: every one, narrowed by nodes, types and filter (model.Ask);
// a config whose rules name it carries those servers, tagged proxy-1, proxy-2… (a selector
// or an observatory subject "proxy" takes them all), and its selector lists them. One with
// "choice": true in its mikan also gets a config of its own at the head of the list, named
// by its tag, in which it stands for proxy. A balancer left with no servers selects the
// blackhole: closed, not open. The panel's and the nodes' hosts go direct ahead of the
// template's rules. The tags beginning with proxy are the panel's.

// proxyTag is the outbound of a config's own server in an own template: the user's choice.
const proxyTag = "proxy"

// Template renders the own xray template src for p: the list of configs.
func Template(p mihomo.Profile, src string) ([]byte, error) {
	cfgs, err := templateConfigs(p, src)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(cfgs, "", "  ")
}

// CheckTemplate finds what would break the configs made of src for p: JSON that does not
// parse, outbounds or balancers without a tag or with one tag twice, a tag of the panel's,
// a balancer that asks for servers in a way the panel does not read, and a rule or a
// balancer that names an outbound or a balancer that is not there.
func CheckTemplate(p mihomo.Profile, src string) error {
	_, err := templateConfigs(p, src)
	return err
}

// frame is an own template read for one user: the parts every config is made of.
type frame struct {
	cfg     map[string]any // the template, without outbounds and routing
	routing map[string]any // without rules and balancers
	rules   []any
	// balancers are the template's, in its order, their servers filled in; servers are the
	// outbounds of each one's servers, by its tag.
	balancers []map[string]any
	servers   map[string][]tagged
	choices   []string // the balancers with a config of their own
	own       []any    // the template's outbounds
	outs      []tagged // the user's servers xray carries
}

func templateConfigs(p mihomo.Profile, src string) ([]map[string]any, error) {
	f, err := readFrame(p, src)
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for _, b := range f.choices {
		out = append(out, f.config(b, nil, b))
	}
	for i := range f.outs {
		out = append(out, f.config(f.outs[i].name, &f.outs[i], ""))
	}
	return out, nil
}

func readFrame(p mihomo.Profile, src string) (frame, error) {
	cfg, err := model.ParseJSONTemplate(src)
	if err != nil {
		return frame{}, err
	}
	f := frame{cfg: cfg, outs: outboundsOf(p.Proxies), servers: map[string][]tagged{}}
	if len(f.outs) == 0 {
		return frame{}, model.ErrNoProxies
	}
	raw, ok := cfg["outbounds"].([]any)
	if !ok && cfg["outbounds"] != nil {
		return frame{}, model.TemplateErr("template_shape", "outbounds")
	}
	tags := map[string]bool{}
	freedom, blackhole := "", ""
	for i, x := range raw {
		o, _ := x.(map[string]any)
		tag := text(o["tag"])
		switch {
		case tag == "":
			return frame{}, model.TemplateErr("template_tag", strconv.Itoa(i+1))
		case strings.HasPrefix(tag, proxyTag):
			return frame{}, model.TemplateErr("template_proxy_tag", tag)
		case tags[tag]:
			return frame{}, model.TemplateErr("template_tag_dup", tag)
		}
		tags[tag] = true
		switch text(o["protocol"]) {
		case "freedom":
			freedom = firstOf(freedom, tag)
		case "blackhole":
			blackhole = firstOf(blackhole, tag)
		}
		f.own = append(f.own, o)
	}
	rt, ok := cfg["routing"].(map[string]any)
	if !ok && cfg["routing"] != nil {
		return frame{}, model.TemplateErr("template_shape", "routing")
	}
	if f.rules, ok = rt["rules"].([]any); !ok && rt["rules"] != nil {
		return frame{}, model.TemplateErr("template_shape", "routing.rules")
	}
	rawBalancers, ok := rt["balancers"].([]any)
	if !ok && rt["balancers"] != nil {
		return frame{}, model.TemplateErr("template_shape", "routing.balancers")
	}
	var carried []model.Proxy
	for _, x := range p.Proxies {
		if slices.ContainsFunc(f.outs, func(t tagged) bool { return t.name == x.Name }) {
			carried = append(carried, x)
		}
	}
	balancers := map[string]bool{}
	var closed []map[string]any // left with no servers: they select the blackhole
	for i, x := range rawBalancers {
		b, _ := x.(map[string]any)
		tag := text(b["tag"])
		switch {
		case tag == "":
			return frame{}, model.TemplateErr("template_balancer_tag", strconv.Itoa(i+1))
		case balancers[tag]:
			return frame{}, model.TemplateErr("template_tag_dup", tag)
		}
		balancers[tag] = true
		b = maps.Clone(b)
		if fb := text(b["fallbackTag"]); fb != "" && !tags[fb] {
			return frame{}, model.TemplateErr("template_detour", tag+" → "+fb)
		}
		if v, asks := b["mikan"]; asks {
			choice := false
			ask, ok := model.ReadAsk(v, func(k string, v any) bool {
				on, isBool := v.(bool)
				choice = on
				return k == "choice" && isBool
			})
			picked, err := ask.Take(carried, p.Nodes)
			if !ok || err != nil {
				return frame{}, model.TemplateErr("template_ask", tag)
			}
			selector, ok := words(b["selector"])
			if !ok {
				return frame{}, model.TemplateErr("template_shape", tag+".selector")
			}
			for _, o := range f.outs {
				if slices.ContainsFunc(picked, func(x model.Proxy) bool { return x.Name == o.name }) {
					f.servers[tag] = append(f.servers[tag], o)
					selector = append(selector, o.tag)
				}
			}
			switch {
			case len(selector) == 0:
				closed = append(closed, b)
			case choice && len(f.servers[tag]) > 0:
				f.choices = append(f.choices, tag)
			}
			b["selector"] = selector
			delete(b, "mikan")
		}
		f.balancers = append(f.balancers, b)
	}
	if len(closed) > 0 && blackhole == "" {
		blackhole = freeTag("block", tags)
		f.own = append(f.own, map[string]any{"tag": blackhole, "protocol": "blackhole"})
	}
	for _, b := range closed {
		b["selector"] = []string{blackhole}
	}
	for i, x := range f.rules {
		r, ok := x.(map[string]any)
		if !ok {
			return frame{}, model.TemplateErr("template_rule_object", strconv.Itoa(i+1))
		}
		out, bal := text(r["outboundTag"]), text(r["balancerTag"])
		switch {
		case out == "" && bal == "":
			return frame{}, model.TemplateErr("template_rule_to", strconv.Itoa(i+1))
		case out != "" && out != proxyTag && !tags[out]:
			return frame{}, model.TemplateErr("template_rule_target", fmt.Sprintf("%d (%s)", i+1, out))
		case bal != "" && !balancers[bal]:
			return frame{}, model.TemplateErr("template_rule_target", fmt.Sprintf("%d (%s)", i+1, bal))
		}
	}
	// The panel's and the nodes' hosts go direct before anything the admin wrote.
	var head []any
	for _, r := range routing.HostRules(p.Direct) {
		for _, fl := range fields(r.When, routing.Common) {
			if freedom == "" {
				freedom = freeTag(directTag, tags)
				f.own = append(f.own, map[string]any{"tag": freedom, "protocol": "freedom"})
			}
			head = append(head, withRef(fl, Ref{Tag: freedom}))
		}
	}
	f.rules = append(head, f.rules...)
	f.routing = maps.Clone(rt)
	if f.routing == nil {
		f.routing = map[string]any{}
	}
	delete(f.routing, "rules")
	delete(f.routing, "balancers")
	f.cfg = maps.Clone(cfg)
	delete(f.cfg, "outbounds")
	delete(f.cfg, "routing")
	return f, nil
}

// config is the config of the list named name: of the server choice (tagged proxy in it),
// or of the balancer that stands for proxy.
func (f frame) config(name string, choice *tagged, balancer string) map[string]any {
	rules := make([]map[string]any, 0, len(f.rules)+1)
	for _, x := range f.rules {
		r := x.(map[string]any)
		if balancer != "" && text(r["outboundTag"]) == proxyTag {
			r = maps.Clone(r)
			delete(r, "outboundTag")
			r["balancerTag"] = balancer
		}
		rules = append(rules, r)
	}
	if balancer != "" {
		// There is no proxy outbound to fall to: what no rule takes goes to the balancer.
		rules = append(rules, map[string]any{"network": "tcp,udp", "balancerTag": balancer})
	}
	rules = once(rules)
	named := map[string]bool{}
	for _, r := range rules {
		if t := text(r["balancerTag"]); t != "" {
			named[t] = true
		}
	}
	var outbounds []any
	if choice != nil {
		o := maps.Clone(choice.out)
		o["tag"] = proxyTag
		outbounds = append(outbounds, o)
	}
	var balancers []any
	carry := map[string]bool{}
	for _, b := range f.balancers {
		tag := text(b["tag"])
		if !named[tag] {
			continue // no rule of this config reaches it
		}
		balancers = append(balancers, b)
		for _, o := range f.servers[tag] {
			carry[o.tag] = true
		}
	}
	for _, o := range f.outs {
		if carry[o.tag] {
			outbounds = append(outbounds, o.out)
		}
	}
	outbounds = append(outbounds, f.own...)
	rt := maps.Clone(f.routing)
	rt["rules"] = rules
	if len(balancers) > 0 {
		rt["balancers"] = balancers
	}
	cfg := maps.Clone(f.cfg)
	cfg["remarks"], cfg["outbounds"], cfg["routing"] = name, outbounds, rt
	return cfg
}

// firstOf is have, or v when there is none yet: the first of its kind.
func firstOf(have, v string) string {
	if have != "" {
		return have
	}
	return v
}

// freeTag is base, or base with a number when the template took it.
func freeTag(base string, tags map[string]bool) string {
	tag := base
	for i := 2; tags[tag]; i++ {
		tag = base + "-" + strconv.Itoa(i)
	}
	return tag
}

// text is a string of the template; "" for anything else.
func text(v any) string {
	s, _ := v.(string)
	return s
}

// words are a list of strings; false for anything else.
func words(v any) ([]string, bool) {
	if v == nil {
		return nil, true
	}
	l, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(l))
	for _, x := range l {
		s, ok := x.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// Example is an own template to start from, with the panel's name of the automatic choice:
// it heads the list, then a config per server; DNS goes through the tunnel and the local
// networks direct. It takes the place of the panel's routing, so it has none of it.
func Example(g model.GroupNames) []byte {
	auto, _ := json.Marshal(g.Auto)
	return fmt.Appendf(nil, `{
  "log": {"loglevel": "warning"},
  "dns": {"tag": "dns", "servers": ["https://1.1.1.1/dns-query"], "queryStrategy": "UseIPv4"},
  "inbounds": [
    {"tag": "socks", "protocol": "socks", "listen": "127.0.0.1", "port": %[2]d, "settings": {"auth": "noauth", "udp": true},
     "sniffing": {"enabled": true, "destOverride": ["http", "tls", "quic"]}},
    {"tag": "http", "protocol": "http", "listen": "127.0.0.1", "port": %[3]d,
     "sniffing": {"enabled": true, "destOverride": ["http", "tls", "quic"]}}
  ],
  "outbounds": [
    {"tag": "direct", "protocol": "freedom"},
    {"tag": "block", "protocol": "blackhole"},
    {"tag": "dns-out", "protocol": "dns"}
  ],
  "routing": {
    "domainStrategy": "AsIs",
    "rules": [
      {"port": "53", "outboundTag": "dns-out"},
      {"ip": ["geoip:private"], "outboundTag": "direct"},
      {"network": "tcp,udp", "outboundTag": "proxy"}
    ],
    "balancers": [
      {"tag": %[1]s, "mikan": {"choice": true}, "strategy": {"type": "leastPing"}}
    ]
  },
  "observatory": {"subjectSelector": ["proxy"], "probeUrl": "https://www.gstatic.com/generate_204", "probeInterval": "5m", "enableConcurrency": true}
}
`, auto, socksPort, httpPort)
}
