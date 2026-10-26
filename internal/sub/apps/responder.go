package apps

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// Policy is what the admin decided about the requests for subscriptions.
type Policy struct {
	// Blocked are ids of Catalog's apps that get a stub in place of the servers.
	Blocked []string
	// Text is the stub's words for a blocked app; empty: the built-in ones.
	Text  string
	Rules []Rule
	// NoBuiltin turns the built-in rules off.
	NoBuiltin bool
}

// What decided a request, when it was not a rule of the admin's: Decision.Rule.
const (
	// RuleApps: the app is on the list of blocked ones.
	RuleApps = "apps"
	// RuleSSR and RuleOld are the built-in rules, refusing what cannot use a profile of the
	// panel at all: ShadowsocksR clients, which know none of its protocols, and the Clash
	// apps that were given up on and whose core knows neither VLESS nor Hysteria2.
	RuleSSR = "builtin:ssr"
	RuleOld = "builtin:old"
)

// builtin are the rules that run after the admin's. They are rules like any other, so
// the tester and the log show them the same way; the admin turns them off all at once.
var builtin = []struct {
	label string
	rule  Rule
}{
	{RuleSSR, Rule{Name: "ShadowsocksR", Enabled: true,
		When: Group{Op: Or, Conds: []Cond{{Header: "User-Agent", Op: OpRegex, Value: `(?i)shadowsocksr|^ssrr?([/ ]|$)`}}},
		Do:   Action{Respond: RespondStub}}},
	// ClashX before its Meta fork: both are "ClashX", the fork is a current core. ClashX is a
	// word of its own, so FlClashX, a current mihomo app, is not taken for it.
	{RuleOld, Rule{Name: "Discontinued Clash apps", Enabled: true,
		When: Group{Op: Or,
			Conds: []Cond{{Header: "User-Agent", Op: OpRegex, Value: `(?i)clash ?for ?windows|clashforandroid`}},
			Groups: []Inner{{Op: And, Conds: []Cond{
				{Header: "User-Agent", Op: OpRegex, Value: `(?i)(^|[^a-z])clashx`},
				{Header: "User-Agent", Op: OpContains, Value: "meta", Not: true},
			}}}},
		Do: Action{Respond: RespondStub}}},
}

// Decision is what the rules came to for one request.
type Decision struct {
	// App is the known app that asks; its ID is empty for one the list does not know.
	App Known
	// Rule is what decided: the admin's rule by its name, a built-in one (RuleSSR, RuleOld),
	// RuleApps, or empty when nothing did and the request is served as usual.
	Rule string
	// Position is the 1-based place in the admin's list of the rule that decided; 0 when
	// no rule of the admin's did.
	Position int
	// Action is what to do; its zero value serves as usual.
	Action Action
	// Install names the apps a stub sends the visitor to: the recommended ones the admin
	// has not blocked.
	Install []string
}

// Responds is how the request is answered, never empty.
func (d Decision) Responds() string {
	if d.Action.Respond == "" {
		return RespondServe
	}
	return d.Action.Respond
}

// Responder decides requests by a Policy. Its regular expressions are compiled once, so
// it is built when the settings are read and used for every request. The zero value and
// nil serve every request.
type Responder struct {
	blocked map[string]bool
	text    string
	rules   []compiled
	builtin []compiled
}

type compiled struct {
	label  string
	pos    int // 1-based place in the admin's list; 0 for a built-in rule
	when   group
	action Action
}

// NewResponder compiles p. A rule Check refused is an error here too.
func NewResponder(p Policy) (*Responder, error) {
	r := &Responder{blocked: map[string]bool{}, text: p.Text}
	for _, id := range p.Blocked {
		r.blocked[id] = true
	}
	for i, rule := range p.Rules {
		c, err := compileRule(rule.Name, rule)
		if err != nil {
			return nil, err
		}
		if rule.Enabled {
			c.pos = i + 1
			r.rules = append(r.rules, c)
		}
	}
	if !p.NoBuiltin {
		for _, b := range builtin {
			c, err := compileRule(b.label, b.rule)
			if err != nil {
				return nil, err
			}
			r.builtin = append(r.builtin, c)
		}
	}
	return r, nil
}

// Decide says what to do with a request by its headers: the first rule of the admin's that
// matches, else the first built-in one, else the list of blocked apps. A request nothing
// matches is served as usual.
func (r *Responder) Decide(h http.Header) Decision {
	app, _ := Identify(h.Get("User-Agent"))
	d := Decision{App: app}
	if r == nil {
		return d
	}
	switch {
	case r.first(r.rules, h, &d), r.first(r.builtin, h, &d):
	case app.ID != "" && r.blocked[app.ID]:
		d.Rule, d.Action = RuleApps, Action{Respond: RespondStub, Text: r.text}
	}
	if d.Action.Respond == RespondStub {
		d.Install = Alternatives(r.blocked, 3)
	}
	return d
}

func (r *Responder) first(rules []compiled, h http.Header, d *Decision) bool {
	for _, c := range rules {
		if c.when.holds(h) {
			d.Rule, d.Position, d.Action = c.label, c.pos, c.action
			return true
		}
	}
	return false
}

// group, cond: Group and Cond with their regular expressions compiled.
type group struct {
	or     bool
	conds  []cond
	groups []group
}

type cond struct {
	header string // canonical
	op     string
	value  string // lowercase for equals and contains
	re     *regexp.Regexp
	not    bool
}

func compileRule(label string, r Rule) (compiled, error) {
	g, err := compileGroup(r.When.Op, r.When.Conds)
	if err != nil {
		return compiled{}, fmt.Errorf("rule %q: %w", r.Name, err)
	}
	for _, in := range r.When.Groups {
		ig, err := compileGroup(in.Op, in.Conds)
		if err != nil {
			return compiled{}, fmt.Errorf("rule %q: %w", r.Name, err)
		}
		g.groups = append(g.groups, ig)
	}
	return compiled{label: label, when: g, action: r.Clean().Do}, nil
}

func compileGroup(op string, conds []Cond) (group, error) {
	g := group{or: op == Or}
	for _, c := range conds {
		k := cond{header: canonical(c.Header), op: c.Op, value: strings.ToLower(c.Value), not: c.Not}
		if c.Op == OpRegex {
			re, err := regexp.Compile(c.Value)
			if err != nil {
				return group{}, err
			}
			k.re = re
		}
		g.conds = append(g.conds, k)
	}
	return g, nil
}

func canonical(name string) string { return http.CanonicalHeaderKey(name) }

func (g group) holds(h http.Header) bool {
	if len(g.conds)+len(g.groups) == 0 {
		return true
	}
	for _, c := range g.conds {
		if c.holds(h) == g.or {
			return g.or
		}
	}
	for _, in := range g.groups {
		if in.holds(h) == g.or {
			return g.or
		}
	}
	return !g.or
}

func (c cond) holds(h http.Header) bool {
	v := strings.Join(h.Values(c.header), ", ")
	if len(v) > MaxHeaderValue {
		v = v[:MaxHeaderValue]
	}
	var ok bool
	switch c.op {
	case OpEquals:
		ok = strings.EqualFold(v, c.value)
	case OpContains:
		ok = strings.Contains(strings.ToLower(v), c.value)
	case OpRegex:
		ok = c.re.MatchString(v)
	}
	return ok != c.not
}

// BlockedIDs are the ids of the blocked apps, in the catalog's order.
func (r *Responder) BlockedIDs() []string {
	var out []string
	if r == nil {
		return out
	}
	for _, k := range Catalog {
		if r.blocked[k.ID] {
			out = append(out, k.ID)
		}
	}
	return out
}
