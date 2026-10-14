package subs

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The admin's own Clash profile (settings.KeyTemplate): a mihomo config without the
// servers. The panel puts the user's proxies under proxies, and fills a group that asks
// for them:
//
//   - include-all-proxies: true (or include-all) takes every proxy of the user; filter,
//     exclude-filter and exclude-type narrow them, as mihomo itself does;
//   - mikan: {nodes: [...], types: [...]} takes the proxies of those nodes (by the name the
//     panel shows, or the id) and of those types (vless, hysteria2…).
//
// The panel lists the proxies in the group itself, so every core gets the same groups, and
// a group left with none for an app gets REJECT: closed, not open. The panel's and the
// nodes' hosts go direct ahead of the admin's rules, whatever they say.

// TemplateError is a template that would break the profiles: a code for the admin panel
// and what it is about.
type TemplateError struct {
	Code   string
	Detail string
}

func (e *TemplateError) Error() string { return e.Code + ": " + e.Detail }

func templateErr(code, detail string) error { return &TemplateError{Code: code, Detail: detail} }

func parseProfileTemplate(src string) (map[string]any, error) {
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		return nil, templateErr("template_yaml", yamlLine(err))
	}
	if cfg == nil {
		return nil, templateErr("template_empty", "")
	}
	return cfg, nil
}

// yamlLine keeps the line of a YAML error, the part an admin can act on.
func yamlLine(err error) string {
	s := err.Error()
	if m := regexp.MustCompile(`line (\d+)`).FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return s
}

// Template renders the admin's template for p.
func Template(p Profile, src string) ([]byte, error) {
	cfg, err := templateConfig(p, src)
	if err != nil {
		return nil, err
	}
	return marshalYAML(cfg)
}

// templateConfig is the profile Template renders.
func templateConfig(p Profile, src string) (map[string]any, error) {
	ps, err := build(p)
	if err != nil {
		return nil, err
	}
	if len(ps) == 0 {
		return nil, ErrNoProxies
	}
	cfg, err := parseProfileTemplate(src)
	if err != nil {
		return nil, err
	}
	// Checked again for this user: their inbounds and app may leave a group the template
	// names a member of empty, and mihomo refuses a profile with a missing member.
	if err := fillTemplate(cfg, p, ps); err != nil {
		return nil, err
	}
	if err := checkFilled(cfg, p); err != nil {
		return nil, err
	}
	return cfg, nil
}

func fillTemplate(cfg map[string]any, p Profile, ps []proxy) error {
	own, _ := cfg["proxies"].([]any)
	taken := map[string]bool{}
	for _, x := range own {
		if m, ok := x.(map[string]any); ok {
			taken[str(m["name"])] = true
		}
	}
	proxies := append([]any{}, own...)
	var names []proxy
	for _, x := range ps {
		if taken[x.name] {
			continue // the template's own proxy of that name stays
		}
		proxies = append(proxies, x.yaml)
		names = append(names, x)
	}
	cfg["proxies"] = proxies
	_, providers := cfg["proxy-providers"]
	groups, _ := cfg["proxy-groups"].([]any)
	for _, g := range groups {
		if m, ok := g.(map[string]any); ok {
			if err := fillGroup(m, p.Nodes, names, providers); err != nil {
				return err
			}
		}
	}
	rules, _ := cfg["rules"].([]any)
	out := []any{}
	for _, r := range directRules(p.Direct) {
		out = append(out, r)
	}
	out = append(out, rules...)
	// mihomo skips a rule whose group cannot carry UDP and sends what falls through to
	// DIRECT, past the tunnel: REJECT makes such apps fall back to TCP.
	if n := len(out); n > 0 {
		if last := str(out[n-1]); strings.HasPrefix(last, "MATCH,") && last != "MATCH,DIRECT" && !strings.HasPrefix(last, "MATCH,REJECT") {
			out = append(out, "MATCH,REJECT")
		}
	}
	cfg["rules"] = out
	return nil
}

// fillGroup puts the user's proxies the group asks for into it.
func fillGroup(g map[string]any, nodes []Node, ps []proxy, providers bool) error {
	all := g["include-all-proxies"] == true || g["include-all"] == true && !providers
	ext, _ := g["mikan"].(map[string]any)
	if !all && ext == nil {
		return nil
	}
	pick := ps
	if ext != nil {
		pick = byNodeAndType(ps, nodes, ext)
	}
	re := func(key string) ([]*regexp.Regexp, bool) {
		s := str(g[key])
		if s == "" {
			return nil, true
		}
		var out []*regexp.Regexp
		// mihomo takes several patterns separated by backticks.
		for _, part := range strings.Split(s, "`") {
			r, err := regexp.Compile(part)
			if err != nil {
				return nil, false
			}
			out = append(out, r)
		}
		return out, true
	}
	keep, ok1 := re("filter")
	drop, ok2 := re("exclude-filter")
	if !ok1 || !ok2 {
		// A pattern Go cannot read (mihomo's regexp2 knows more): mihomo applies it itself,
		// but then it cannot know which nodes "mikan" picked, and the group would take every
		// server of the user.
		if ext != nil {
			return templateErr("template_filter", str(g["name"]))
		}
		return nil
	}
	dropTypes := map[string]bool{}
	for _, t := range strings.Split(str(g["exclude-type"]), "|") {
		if t = strings.ToLower(strings.TrimSpace(t)); t != "" {
			dropTypes[t] = true
		}
	}
	members, _ := g["proxies"].([]any)
	list := append([]any{}, members...)
	for _, x := range pick {
		if len(keep) > 0 && !slices.ContainsFunc(keep, func(r *regexp.Regexp) bool { return r.MatchString(x.name) }) {
			continue
		}
		if slices.ContainsFunc(drop, func(r *regexp.Regexp) bool { return r.MatchString(x.name) }) || dropTypes[strings.ToLower(str(x.yaml["type"]))] {
			continue
		}
		list = append(list, x.name)
	}
	if len(list) == 0 && g["use"] == nil {
		list = []any{"REJECT"}
	}
	g["proxies"] = list
	for _, k := range []string{"mikan", "include-all-proxies", "filter", "exclude-filter", "exclude-type"} {
		delete(g, k)
	}
	if !providers {
		delete(g, "include-all")
	}
	return nil
}

func byNodeAndType(ps []proxy, nodes []Node, ext map[string]any) []proxy {
	wantNode := map[int64]bool{}
	for _, v := range list(ext["nodes"]) {
		for _, n := range nodes {
			if v == n.Name || v == strconv.FormatInt(n.ID, 10) {
				wantNode[n.ID] = true
			}
		}
	}
	wantType := map[string]bool{}
	for _, v := range list(ext["types"]) {
		wantType[strings.ToLower(v)] = true
	}
	var out []proxy
	for _, x := range ps {
		if len(list(ext["nodes"])) > 0 && !wantNode[x.node] {
			continue
		}
		if len(wantType) > 0 && !wantType[strings.ToLower(str(x.yaml["type"]))] {
			continue
		}
		out = append(out, x)
	}
	return out
}

func list(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			out = append(out, str(e))
		}
	case string:
		out = append(out, x)
	case int:
		out = append(out, strconv.Itoa(x))
	}
	return out
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

// policies are mihomo's own targets.
var policies = []string{"DIRECT", "REJECT", "REJECT-DROP", "PASS", "COMPATIBLE"}

// CheckTemplate finds what would break the profiles made of src for p: YAML that does
// not parse, a group that lists a member nobody defines, a rule that sends traffic to a
// group that does not exist or uses a rule list that is not there.
func CheckTemplate(p Profile, src string) error {
	cfg, err := parseProfileTemplate(src)
	if err != nil {
		return err
	}
	ps, err := build(p)
	if err != nil {
		return err
	}
	if err := fillTemplate(cfg, p, ps); err != nil {
		return err
	}
	return checkFilled(cfg, p)
}

// checkFilled checks a template with the user's proxies in it: on saving, against a user
// given every inbound; on every profile, against the user it is for.
func checkFilled(cfg map[string]any, p Profile) error {
	known := map[string]bool{}
	for _, x := range policies {
		known[x] = true
	}
	proxies, _ := cfg["proxies"].([]any)
	for _, x := range proxies {
		if m, ok := x.(map[string]any); ok {
			known[str(m["name"])] = true
		}
	}
	rawGroups, ok := cfg["proxy-groups"].([]any)
	if !ok && cfg["proxy-groups"] != nil {
		return templateErr("template_groups", "")
	}
	var groups []map[string]any
	for i, x := range rawGroups {
		m, ok := x.(map[string]any)
		if !ok || str(m["name"]) == "" {
			return templateErr("template_group_name", strconv.Itoa(i+1))
		}
		if known[str(m["name"])] {
			return templateErr("template_group_dup", str(m["name"]))
		}
		known[str(m["name"])] = true
		groups = append(groups, m)
	}
	for _, g := range groups {
		members, _ := g["proxies"].([]any)
		for _, x := range members {
			if !known[str(x)] {
				return templateErr("template_group_member", str(g["name"])+" → "+str(x))
			}
		}
	}
	providers, _ := cfg["rule-providers"].(map[string]any)
	subRules, _ := cfg["sub-rules"].(map[string]any)
	rules, _ := cfg["rules"].([]any)
	for i, x := range rules[len(directRules(p.Direct)):] {
		r := str(x)
		typ, target, set := ruleParts(r)
		switch {
		case typ == "":
			return templateErr("template_rule", strconv.Itoa(i+1))
		case typ == "SUB-RULE":
			if _, ok := subRules[target]; !ok {
				return templateErr("template_rule_target", strconv.Itoa(i+1)+" ("+target+")")
			}
		case !known[target]:
			return templateErr("template_rule_target", strconv.Itoa(i+1)+" ("+target+")")
		}
		if set != "" {
			if _, ok := providers[set]; !ok {
				return templateErr("template_rule_set", strconv.Itoa(i+1)+" ("+set+")")
			}
		}
	}
	return nil
}

// ruleParts reads a rule's type, its target and the rule list it uses ("" when none).
// Logic rules (AND, OR, NOT) hold commas in brackets; the target is the last field but a
// trailing no-resolve or src.
func ruleParts(r string) (typ, target, set string) {
	fields := splitRule(r)
	for len(fields) > 2 && (fields[len(fields)-1] == "no-resolve" || fields[len(fields)-1] == "src") {
		fields = fields[:len(fields)-1]
	}
	if len(fields) < 2 {
		return "", "", ""
	}
	typ, target = strings.TrimSpace(fields[0]), strings.TrimSpace(fields[len(fields)-1])
	if typ == "RULE-SET" && len(fields) == 3 {
		set = strings.TrimSpace(fields[1])
	}
	if typ == "MATCH" && len(fields) != 2 || typ != "MATCH" && len(fields) < 3 {
		return "", "", ""
	}
	return typ, target, set
}

// splitRule splits on the commas outside brackets.
func splitRule(r string) []string {
	var out []string
	depth, start := 0, 0
	for i, c := range r {
		switch c {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, r[start:i])
				start = i + 1
			}
		}
	}
	return append(out, r[start:])
}

// Starter is the template of the profile Mihomo makes for p with g and r: the start of an
// admin's own. The proxies go, and the groups ask for them: every proxy of the user
// (include-all-proxies), or those of some nodes (mikan: nodes).
func Starter(p Profile, g Groups, r Routing) ([]byte, error) {
	cfg, ps, err := mihomoConfig(p, g.WithDefaults(p.Lang), r)
	if err != nil {
		return nil, err
	}
	delete(cfg, "proxies")
	byName := map[string]proxy{}
	perNode := map[int64]int{}
	for _, x := range ps {
		byName[x.name] = x
		perNode[x.node]++
	}
	nodeRef := func(id int64) any {
		for _, n := range p.Nodes {
			if n.ID == id && n.Name != "" {
				dup := slices.IndexFunc(p.Nodes, func(o Node) bool { return o.ID != id && o.Name == n.Name }) >= 0
				if !dup {
					return n.Name
				}
			}
		}
		return id
	}
	groups, _ := cfg["proxy-groups"].([]map[string]any)
	for _, grp := range groups {
		members, _ := grp["proxies"].([]string)
		var others []string
		nodes := map[int64]int{}
		count := 0
		for _, m := range members {
			if x, ok := byName[m]; ok {
				nodes[x.node]++
				count++
			} else {
				others = append(others, m)
			}
		}
		if count == 0 {
			continue
		}
		whole := true // every proxy of the nodes it has some of
		for id, n := range nodes {
			whole = whole && n == perNode[id]
		}
		switch {
		case count == len(ps):
			grp["proxies"], grp["include-all-proxies"] = others, true
		case whole:
			var refs []any
			for _, n := range p.Nodes {
				if nodes[n.ID] > 0 {
					refs = append(refs, nodeRef(n.ID))
				}
			}
			grp["proxies"], grp["mikan"] = others, map[string]any{"nodes": refs}
		}
		if len(others) == 0 {
			if _, asks := grp["proxies"].([]string); asks && (grp["include-all-proxies"] == true || grp["mikan"] != nil) {
				delete(grp, "proxies")
			}
		}
	}
	// The panel adds the rules for its own hosts to every profile.
	if rules, ok := cfg["rules"].([]string); ok {
		cfg["rules"] = rules[len(directRules(p.Direct)):]
	}
	return marshalYAML(cfg)
}
