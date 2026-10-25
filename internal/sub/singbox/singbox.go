// Package singbox renders subscriptions for the sing-box apps that run a whole config
// (the official SFA, SFI, SFM and SFT). Their links-only subscription was no profile for
// them.
//
// The config is of sing-box 1.12 and later: type-based DNS servers and rule actions, the
// forms that version brought in and 1.14 requires. It is made of the Clash profile: the
// same proxies, groups, rules and DNS, so Settings → Routing and an own profile reach them
// too. What sing-box has no counterpart for is left out: XHTTP and the mihomo-only
// protocols (build.ForApp leaves them out already), a self-signed certificate pinned by its
// hash (sing-box pins a public key, from 1.13 on), .mrs lists, logic rules but AND of
// network and port.
package singbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"mikan/internal/sub/apps"
	"mikan/internal/sub/mihomo"
	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
)

// Since is the first sing-box with the config's shape.
var Since = apps.Version{1, 12, 0}

// The keepalive of a gRPC transport: an idle connection is pinged after grpcIdleTimeout and
// given up when no answer comes within grpcPingTimeout, so that the app does not go on
// sending into one a middlebox cut while it was silent.
const (
	grpcIdleTimeout = "15s"
	grpcPingTimeout = "10s"
)

// Render is the sing-box config of p: of the own Clash profile src when there is one (and
// it renders for p), of the built-in one otherwise.
func Render(ctx context.Context, p mihomo.Profile, g model.GroupNames, m routing.Mode, src string) ([]byte, error) {
	// The Clash profile as a current mihomo would get it: the rules of every list, for the
	// lists to be converted (a list of a younger core has no sing-box counterpart anyway).
	p.App = apps.App{}
	var cfg map[string]any
	var err error
	if src != "" {
		cfg, _, err = mihomo.TemplateConfig(p, src)
	}
	if src == "" || err != nil && !errors.Is(err, model.ErrNoProxies) {
		cfg, err = mihomo.Config(p, g.WithDefaults(p.Lang), m)
	}
	if err != nil {
		return nil, err
	}
	out := config(ctx, cfg, g.WithDefaults(p.Lang).Main)
	if out == nil {
		return nil, model.ErrNoProxies // nothing of the profile sing-box can carry
	}
	return json.MarshalIndent(out, "", "  ")
}

// Stub is a config of one placeholder outbound named after why there are no others: the
// app shows it where the servers would be. main is the main group's name.
func Stub(name, main string) []byte {
	body, _ := json.MarshalIndent(map[string]any{
		"outbounds": []map[string]any{{"type": "selector", "tag": main, "outbounds": []string{name}},
			{"type": "socks", "tag": name, "server": "127.0.0.1", "server_port": 1}},
		"route": map[string]any{"final": main},
	}, "", "  ")
	return body
}

func config(ctx context.Context, cfg map[string]any, main string) map[string]any {
	var outbounds []map[string]any
	tags := map[string]bool{"direct": true}
	for _, x := range anyList(cfg["proxies"]) {
		m, _ := x.(map[string]any)
		if o, ok := singboxOutbound(m); ok {
			outbounds = append(outbounds, o)
			tags[str(o["tag"])] = true
		}
	}
	if len(outbounds) == 0 {
		return nil
	}
	groups := singboxGroups(anyList(cfg["proxy-groups"]), tags)
	for _, gr := range groups {
		tags[str(gr["tag"])] = true
	}
	if !tags[main] {
		main = ""
		if len(groups) > 0 {
			main = str(groups[0]["tag"])
		}
	}
	// The lists download through the tunnel: GitHub is slow or cut off where it is needed.
	detour := main
	if detour == "" {
		detour = str(outbounds[0]["tag"])
	}
	sets := singboxSets{detour: detour, providers: anyMap(cfg["rule-providers"]), ctx: ctx, used: map[string]map[string]any{}}
	rules, final := singboxRules(anyList(cfg["rules"]), tags, &sets)
	if final == "" {
		final = main
	}
	dns, resolver := singboxDNS(anyMap(cfg["dns"]), tags, &sets)
	all := append(append([]map[string]any{}, groups...), outbounds...)
	all = append(all, map[string]any{"type": "direct", "tag": "direct"})
	route := map[string]any{
		"rules":                 append([]map[string]any{{"action": "sniff"}, {"protocol": "dns", "action": "hijack-dns"}}, rules...),
		"auto_detect_interface": true, "default_domain_resolver": resolver,
	}
	if final != "" {
		route["final"] = final
	}
	if len(sets.used) > 0 {
		var list []map[string]any
		for _, s := range sets.order {
			list = append(list, sets.used[s])
		}
		route["rule_set"] = list
	}
	return map[string]any{
		"log": map[string]any{"level": "warn"},
		"dns": dns,
		"inbounds": []map[string]any{{"type": "tun", "tag": "tun-in", "address": []string{"172.19.0.1/30"},
			"auto_route": true, "strict_route": true}},
		"outbounds":    all,
		"route":        route,
		"experimental": map[string]any{"cache_file": map[string]any{"enabled": true}},
	}
}

// singboxOutbound is a mihomo proxy as a sing-box outbound; false for one sing-box cannot
// carry.
func singboxOutbound(m map[string]any) (map[string]any, bool) {
	typ := str(m["type"])
	o := map[string]any{"tag": str(m["name"]), "server": str(m["server"]), "server_port": m["port"]}
	tls, ok := singboxTLS(m)
	if !ok {
		return nil, false
	}
	switch typ {
	case "vless":
		o["type"], o["uuid"], o["packet_encoding"] = "vless", str(m["uuid"]), "xudp"
		if f := str(m["flow"]); f != "" {
			o["flow"] = f
		}
		if m["encryption"] != nil && str(m["encryption"]) != "none" {
			return nil, false
		}
	case "trojan":
		o["type"], o["password"] = "trojan", str(m["password"])
	case "hysteria2":
		o["type"], o["password"] = "hysteria2", str(m["password"])
		if ports := str(m["ports"]); ports != "" {
			var list []string
			for _, p := range strings.Split(ports, ",") {
				list = append(list, strings.ReplaceAll(strings.TrimSpace(p), "-", ":"))
			}
			o["server_ports"] = list
			delete(o, "server_port")
		}
		switch str(m["obfs"]) {
		case "":
		case "salamander":
			o["obfs"] = map[string]any{"type": "salamander", "password": str(m["obfs-password"])}
		default:
			return nil, false // Gecko is mihomo's own
		}
	case "tuic":
		o["type"], o["uuid"], o["password"] = "tuic", str(m["uuid"]), str(m["password"])
		o["congestion_control"], o["udp_relay_mode"] = str(m["congestion-controller"]), str(m["udp-relay-mode"])
	case "anytls":
		o["type"], o["password"] = "anytls", str(m["password"])
	case "ss":
		o["type"], o["method"], o["password"] = "shadowsocks", str(m["cipher"]), str(m["password"])
	default:
		return nil, false
	}
	if tls != nil {
		o["tls"] = tls
	}
	switch str(m["network"]) {
	case "", "tcp":
	case "ws":
		w := anyMap(m["ws-opts"])
		t := map[string]any{"type": "ws", "path": str(w["path"])}
		if h := anyMap(w["headers"]); len(h) > 0 {
			t["headers"] = h
		}
		o["transport"] = t
	case "grpc":
		o["transport"] = map[string]any{"type": "grpc", "service_name": str(anyMap(m["grpc-opts"])["grpc-service-name"]), "idle_timeout": grpcIdleTimeout, "ping_timeout": grpcPingTimeout}
	default:
		return nil, false // XHTTP, HTTP/2 and the like
	}
	if m["udp"] == false && typ != "tuic" && typ != "hysteria2" {
		o["network"] = "tcp"
	}
	return o, true
}

// singboxTLS is the tls block of a proxy, nil for none; false for a pinned self-signed
// certificate, which sing-box 1.12 cannot pin.
func singboxTLS(m map[string]any) (map[string]any, bool) {
	typ := str(m["type"])
	reality := anyMap(m["reality-opts"])
	if m["tls"] != true && reality == nil && typ != "hysteria2" && typ != "tuic" && typ != "anytls" {
		return nil, true
	}
	if str(m["fingerprint"]) != "" {
		return nil, false
	}
	t := map[string]any{"enabled": true}
	sni := str(m["servername"])
	if sni == "" {
		sni = str(m["sni"])
	}
	if sni != "" {
		t["server_name"] = sni
	}
	if alpn := anyList(m["alpn"]); len(alpn) > 0 {
		t["alpn"] = alpn
	}
	if m["skip-cert-verify"] == true {
		t["insecure"] = true
	}
	fp := str(m["client-fingerprint"])
	if reality != nil {
		t["reality"] = map[string]any{"enabled": true, "public_key": str(reality["public-key"]), "short_id": str(reality["short-id"])}
		if fp == "" {
			fp = "chrome" // REALITY needs uTLS in sing-box
		}
	}
	if fp != "" && typ != "hysteria2" && typ != "tuic" {
		t["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
	}
	return t, true
}

// singboxGroups are the Clash groups as selectors and url tests. A member sing-box has no
// outbound for goes; a group left with none goes too, and so do its mentions.
func singboxGroups(groups []any, tags map[string]bool) []map[string]any {
	type grp struct {
		m       map[string]any
		members []string
	}
	var gs []grp
	names := map[string]bool{}
	for _, x := range groups {
		m := anyMap(x)
		if m == nil || str(m["name"]) == "" {
			continue
		}
		var members []string
		for _, y := range anyList(m["proxies"]) {
			members = append(members, str(y))
		}
		gs = append(gs, grp{m, members})
		names[str(m["name"])] = true
	}
	alive := map[string]bool{}
	for n := range names {
		alive[n] = true
	}
	member := func(s string) (string, bool) {
		switch {
		case s == "DIRECT":
			return "direct", true
		case tags[s] || alive[s]:
			return s, true
		}
		return "", false // REJECT, PASS, a proxy sing-box cannot carry, a closed group
	}
	for changed := true; changed; {
		changed = false
		for _, gr := range gs {
			name := str(gr.m["name"])
			if !alive[name] {
				continue
			}
			ok := false
			for _, s := range gr.members {
				if _, y := member(s); y && s != name {
					ok = true
					break
				}
			}
			if !ok {
				alive[name], changed = false, true
			}
		}
	}
	var out []map[string]any
	for _, gr := range gs {
		name := str(gr.m["name"])
		if !alive[name] {
			continue
		}
		var list []string
		seen := map[string]bool{}
		for _, s := range gr.members {
			if t, ok := member(s); ok && !seen[t] {
				seen[t] = true
				list = append(list, t)
			}
		}
		o := map[string]any{"tag": name, "outbounds": list}
		switch str(gr.m["type"]) {
		case "url-test", "fallback", "load-balance":
			o["type"] = "urltest"
			if u := str(gr.m["url"]); u != "" {
				o["url"] = u
			}
			if n, ok := toSeconds(gr.m["interval"]); ok {
				o["interval"] = strconv.Itoa(n) + "s"
			}
			if n, ok := gr.m["tolerance"].(int); ok {
				o["tolerance"] = n
			}
		default:
			o["type"] = "selector"
		}
		out = append(out, o)
	}
	return out
}

func toSeconds(v any) (int, bool) {
	switch x := v.(type) {
	case int:
		return x, x > 0
	case string:
		n, err := strconv.Atoi(x)
		return n, err == nil && n > 0
	}
	return 0, false
}

// singboxTarget is where a rule to target sends traffic: an outbound or a reject action.
func singboxTarget(target string, tags map[string]bool) (map[string]any, bool) {
	switch {
	case target == "DIRECT":
		return map[string]any{"outbound": "direct"}, true
	case strings.HasPrefix(target, "REJECT"):
		return map[string]any{"action": "reject"}, true
	case tags[target]:
		return map[string]any{"outbound": target}, true
	}
	return nil, false
}

// singboxRules are the Clash rules as route rules, and the outbound MATCH sends the rest
// to. The first MATCH ends them, as in mihomo.
func singboxRules(rules []any, tags map[string]bool, sets *singboxSets) ([]map[string]any, string) {
	var out []map[string]any
	for _, x := range rules {
		rule := str(x)
		typ, target, _ := routing.RuleParts(rule)
		to, ok := singboxTarget(target, tags)
		if typ == "MATCH" {
			if ok && to["outbound"] != nil {
				return out, str(to["outbound"])
			}
			return out, ""
		}
		if !ok {
			continue // a group sing-box has none of
		}
		for _, match := range sets.match(rule) {
			for k, v := range to {
				match[k] = v
			}
			out = append(out, match)
		}
	}
	return out, ""
}

// match is a Clash rule's condition as route rules (several for a list of both domains
// and processes); none for a rule sing-box has no counterpart for.
func (s *singboxSets) match(rule string) []map[string]any {
	f := routing.SplitRule(rule)
	typ := strings.TrimSpace(f[0])
	if len(f) < 3 {
		return nil
	}
	value := strings.TrimSpace(f[1])
	one := func(k string, v any) []map[string]any { return []map[string]any{{k: v}} }
	switch typ {
	case "DOMAIN":
		return one("domain", []string{value})
	case "DOMAIN-SUFFIX":
		return one("domain_suffix", []string{value})
	case "DOMAIN-KEYWORD":
		return one("domain_keyword", []string{value})
	case "DOMAIN-REGEX":
		return one("domain_regex", []string{value})
	case "IP-CIDR", "IP-CIDR6":
		return one("ip_cidr", []string{value})
	case "GEOIP":
		if strings.EqualFold(value, "lan") {
			return one("ip_is_private", true)
		}
		return one("rule_set", []string{s.geo("geoip", value)})
	case "GEOSITE":
		return one("rule_set", []string{s.geo("geosite", value)})
	case "PROCESS-NAME":
		return []map[string]any{processRule(value)}
	case "PROCESS-NAME-REGEX":
		return one("process_path_regex", []string{value})
	case "DST-PORT":
		if n, err := strconv.Atoi(value); err == nil {
			return one("port", []int{n})
		}
	case "NETWORK":
		return one("network", []string{strings.ToLower(value)})
	case "RULE-SET":
		if tag, ok := s.list(value); ok {
			return one("rule_set", []string{tag})
		}
	case "AND":
		// AND,((NETWORK,UDP),(DST-PORT,443)),… : each part a plain condition.
		var parts []map[string]any
		for _, part := range routing.SplitRule(strings.TrimSuffix(strings.TrimPrefix(value, "("), ")")) {
			p := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(part), "("), ")")
			m := s.match(p + ",x")
			if len(m) != 1 {
				return nil
			}
			parts = append(parts, m[0])
		}
		if len(parts) > 0 {
			return []map[string]any{{"type": "logical", "mode": "and", "rules": parts}}
		}
	}
	return nil
}

// processRule: an Android package ("com.example.app") or a program's name.
func processRule(name string) map[string]any {
	if strings.Contains(name, ".") && !strings.HasSuffix(strings.ToLower(name), ".exe") {
		return map[string]any{"package_name": []string{name}}
	}
	return map[string]any{"process_name": []string{name}}
}

// singboxSets collects the rule sets the rules use: mihomo's geodata as MetaCubeX's .srs,
// and the profile's Clash lists converted (singboxList).
type singboxSets struct {
	ctx       context.Context
	detour    string
	providers map[string]any
	used      map[string]map[string]any
	order     []string
}

const singboxGeo = "https://raw.githubusercontent.com/MetaCubeX/meta-rules-dat/sing/geo/"

func (s *singboxSets) geo(kind, name string) string {
	name = strings.ToLower(name)
	tag := kind + "-" + name
	if _, ok := s.used[tag]; !ok {
		s.add(tag, map[string]any{"type": "remote", "tag": tag, "format": "binary",
			"url": singboxGeo + kind + "/" + url.PathEscape(name) + ".srs", "download_detour": s.detour})
	}
	return tag
}

func (s *singboxSets) add(tag string, set map[string]any) {
	s.used[tag] = set
	s.order = append(s.order, tag)
}

// list is a Clash rule-provider as an inline rule set: the classical and domain lists in
// YAML or text a sing-box rule can hold; false for the rest (.mrs).
func (s *singboxSets) list(name string) (string, bool) {
	tag := "list-" + name
	if _, ok := s.used[tag]; ok {
		return tag, true
	}
	p := anyMap(s.providers[name])
	if p == nil || str(p["format"]) == "mrs" || str(p["url"]) == "" {
		return "", false
	}
	payload, err := singboxList(s.ctx, str(p["url"]))
	if err != nil {
		return "", false
	}
	var rules []map[string]any
	switch str(p["behavior"]) {
	case "domain":
		var plain, suffix []string
		for _, d := range payload {
			if rest, ok := strings.CutPrefix(d, "+."); ok {
				suffix = append(suffix, rest)
			} else {
				plain = append(plain, d)
			}
		}
		rules = headless(map[string]any{"domain": plain, "domain_suffix": suffix})
	case "ipcidr":
		rules = headless(map[string]any{"ip_cidr": payload})
	default:
		byField := map[string][]string{}
		for _, line := range payload {
			for _, m := range s.match(line + ",x") {
				for k, v := range m {
					if l, ok := v.([]string); ok && k != "rule_set" {
						byField[k] = append(byField[k], l...)
					}
				}
			}
		}
		// Fields of one kind are OR'ed in a rule; kinds are AND'ed, so each kind is a
		// rule of its own.
		for _, kinds := range [][]string{{"domain", "domain_suffix", "domain_keyword", "domain_regex"}, {"ip_cidr"}, {"process_name"}, {"package_name"}, {"process_path_regex"}} {
			r := map[string]any{}
			for _, k := range kinds {
				if len(byField[k]) > 0 {
					r[k] = byField[k]
				}
			}
			if len(r) > 0 {
				rules = append(rules, r)
			}
		}
	}
	if len(rules) == 0 {
		return "", false
	}
	s.add(tag, map[string]any{"type": "inline", "tag": tag, "rules": rules})
	return tag, true
}

func headless(fields map[string]any) []map[string]any {
	r := map[string]any{}
	for k, v := range fields {
		if l, ok := v.([]string); ok && len(l) > 0 {
			r[k] = l
		}
	}
	if len(r) == 0 {
		return nil
	}
	return []map[string]any{r}
}

// singboxList is the payload of a Clash rule list. The panel's lists are pinned to a commit,
// so what is fetched once stays; a failure is tried again a few minutes later.
var singboxList = func(ctx context.Context, src string) ([]string, error) {
	listCache.Lock()
	e, ok := listCache.m[src]
	listCache.Unlock()
	if ok && (e.err == nil || time.Since(e.at) < 5*time.Minute) {
		return e.payload, e.err
	}
	payload, err := fetchList(ctx, src)
	listCache.Lock()
	listCache.m[src] = listEntry{payload: payload, err: err, at: time.Now()}
	listCache.Unlock()
	return payload, err
}

type listEntry struct {
	payload []string
	err     error
	at      time.Time
}

var listCache = struct {
	sync.Mutex
	m map[string]listEntry
}{m: map[string]listEntry{}}

func fetchList(ctx context.Context, src string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", src, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return parseList(raw), nil
}

// parseList reads a Clash rule list: YAML with a payload, or a line per entry.
func parseList(raw []byte) []string {
	var doc struct {
		Payload []string `yaml:"payload"`
	}
	if yaml.Unmarshal(raw, &doc) == nil && len(doc.Payload) > 0 {
		return doc.Payload
	}
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// singboxDNS is the Clash profile's DNS as sing-box servers: the first nameserver for
// everything, a plain one for the proxies' own names (the default resolver), and the
// policy's servers for their domains. Fake IP is mihomo's: sing-box resolves for real.
func singboxDNS(d map[string]any, tags map[string]bool, sets *singboxSets) (map[string]any, string) {
	servers := []map[string]any{}
	add := func(tag, s string) bool {
		srv, ok := singboxDNSServer(tag, s, tags)
		if ok {
			servers = append(servers, srv)
		}
		return ok
	}
	remote := ""
	for _, s := range anyList(d["nameserver"]) {
		if add("remote", str(s)) {
			remote = "remote"
			break
		}
	}
	local := ""
	for _, key := range []string{"proxy-server-nameserver", "default-nameserver"} {
		for _, s := range anyList(d[key]) {
			if local == "" && add("local", str(s)) {
				local = "local"
			}
		}
	}
	if local == "" {
		servers = append(servers, map[string]any{"type": "local", "tag": "local"})
		local = "local"
	}
	if remote == "" {
		remote = local
	}
	var rules []map[string]any
	i := 0
	policy := anyMap(d["nameserver-policy"])
	keys := make([]string, 0, len(policy))
	for k := range policy {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		v := policy[key]
		list := anyList(v)
		if len(list) == 0 {
			if s := str(v); s != "" {
				list = []any{s}
			}
		}
		tag := "policy-" + strconv.Itoa(i)
		if len(list) == 0 || !add(tag, str(list[0])) {
			continue
		}
		i++
		var match map[string]any
		switch k := strings.TrimSpace(key); {
		case strings.HasPrefix(k, "geosite:"):
			match = map[string]any{"rule_set": []string{sets.geo("geosite", strings.TrimPrefix(k, "geosite:"))}}
		case strings.HasPrefix(k, "+."):
			match = map[string]any{"domain_suffix": []string{strings.TrimPrefix(k, "+.")}}
		case !strings.ContainsAny(k, ":*"):
			match = map[string]any{"domain": []string{k}}
		default:
			continue
		}
		match["server"] = tag
		rules = append(rules, match)
	}
	dns := map[string]any{"servers": servers, "final": remote}
	if len(rules) > 0 {
		dns["rules"] = rules
	}
	if d["ipv6"] == false {
		dns["strategy"] = "ipv4_only"
	}
	return dns, local
}

// singboxDNSServer is a mihomo nameserver as a sing-box one: "https://1.1.1.1/dns-query#VPN",
// "tls://dns.google", "77.88.8.8", "system".
func singboxDNSServer(tag, s string, tags map[string]bool) (map[string]any, bool) {
	s, group, _ := strings.Cut(s, "#")
	srv := map[string]any{"tag": tag}
	if group != "" && tags[group] {
		srv["detour"] = group
	}
	if s == "system" {
		srv["type"] = "local"
		delete(srv, "detour")
		return srv, true
	}
	if !strings.Contains(s, "://") {
		s = "udp://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Hostname() == "" {
		return nil, false
	}
	switch u.Scheme {
	case "udp", "tcp", "tls", "quic", "https", "h3":
	default:
		return nil, false
	}
	srv["type"], srv["server"] = u.Scheme, u.Hostname()
	if p := u.Port(); p != "" {
		n, _ := strconv.Atoi(p)
		srv["server_port"] = n
	}
	if (u.Scheme == "https" || u.Scheme == "h3") && u.Path != "" && u.Path != "/dns-query" {
		srv["path"] = u.Path
	}
	return srv, true
}

func anyList(v any) []any {
	switch x := v.(type) {
	case []any:
		return x
	case []string:
		out := make([]any, len(x))
		for i, s := range x {
			out[i] = s
		}
		return out
	case []map[string]any:
		out := make([]any, len(x))
		for i, m := range x {
			out[i] = m
		}
		return out
	}
	return nil
}

func anyMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func str(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case nil:
		return ""
	}
	return fmt.Sprint(v)
}
