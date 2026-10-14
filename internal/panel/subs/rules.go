package subs

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"
)

// The admin's own Clash rules: one per line, TYPE,VALUE,TARGET with no-resolve where mihomo
// takes it, "#" comments and blank lines allowed. They go before the built-in routing in
// every Clash profile. One rule mihomo cannot parse fails the whole profile in every
// app, so each line is checked when saved and again before it is served.

const (
	MaxRules    = 500
	MaxRuleLine = 512
)

// RuleError is the first bad line, numbered from 1.
type RuleError struct {
	Line int
	Code string
}

func (e *RuleError) Error() string { return e.Code + " on line " + strconv.Itoa(e.Line) }

type ruleKind int

const (
	kindDomain ruleKind = iota
	kindWildcard
	kindKeyword
	kindRegex
	kindGeo
	kindCIDR
	kindCIDR6
	kindASN
	kindPort
	kindNetwork
	kindProcess
)

var ruleKinds = map[string]ruleKind{
	"DOMAIN": kindDomain, "DOMAIN-SUFFIX": kindDomain, "DOMAIN-KEYWORD": kindKeyword, "DOMAIN-WILDCARD": kindWildcard, "DOMAIN-REGEX": kindRegex,
	"GEOSITE": kindGeo, "GEOIP": kindGeo, "SRC-GEOIP": kindGeo,
	"IP-CIDR": kindCIDR, "IP-CIDR6": kindCIDR6, "IP-SUFFIX": kindCIDR, "SRC-IP-CIDR": kindCIDR, "IP-ASN": kindASN,
	"DST-PORT": kindPort, "SRC-PORT": kindPort, "NETWORK": kindNetwork, "PROCESS-NAME": kindProcess, "PROCESS-PATH": kindProcess,
}

// noResolve: the types that resolve a domain to match unless told not to.
var noResolve = map[string]bool{"GEOIP": true, "IP-CIDR": true, "IP-CIDR6": true, "IP-SUFFIX": true, "IP-ASN": true}

var builtinTargets = []string{"DIRECT", "REJECT", "REJECT-DROP"}

var (
	domainChars = regexp.MustCompile(`^[A-Za-z0-9.\-_]+$`)
	wildChars   = regexp.MustCompile(`^[A-Za-z0-9.\-_*?]+$`)
	geoCode     = regexp.MustCompile(`^!?[A-Za-z0-9_.\-]+(@[A-Za-z0-9_.\-!]+)?$`)
)

// RuleTargets are where a rule may send traffic: the built-in policies, the main and the
// automatic groups, and PROXY, the alias of the main group that never changes its name.
func RuleTargets(g Groups) []string {
	return append(append([]string{}, builtinTargets...), AliasGroup, g.Main, g.Auto)
}

// ParseRules checks the admin's text line by line and returns the rules as the profile
// writes them: types and built-in policies in upper case, spaces around commas gone.
func ParseRules(text string, g Groups) ([]string, error) {
	var out []string
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if len(line) > MaxRuleLine {
			return nil, &RuleError{Line: i + 1, Code: "rule_too_long"}
		}
		if len(out) == MaxRules {
			return nil, &RuleError{Line: i + 1, Code: "rules_too_many"}
		}
		rule, code := parseRule(line, g)
		if code != "" {
			return nil, &RuleError{Line: i + 1, Code: code}
		}
		out = append(out, rule)
	}
	return out, nil
}

func parseRule(line string, g Groups) (string, string) {
	f := strings.Split(line, ",")
	for i := range f {
		f[i] = strings.TrimSpace(f[i])
	}
	if len(f) < 3 || len(f) > 4 {
		return "", "rule_format"
	}
	typ := strings.ToUpper(f[0])
	kind, ok := ruleKinds[typ]
	if !ok {
		return "", "rule_type"
	}
	if !validValue(kind, typ, f[1]) {
		return "", "rule_value"
	}
	target := ""
	for _, t := range RuleTargets(g) {
		if f[2] == t || slicesContainsFold(builtinTargets, t) && strings.EqualFold(f[2], t) {
			target = t
			break
		}
	}
	if target == "" {
		return "", "rule_target"
	}
	out := typ + "," + f[1] + "," + target
	if len(f) == 4 {
		if !strings.EqualFold(f[3], "no-resolve") || !noResolve[typ] {
			return "", "rule_option"
		}
		out += ",no-resolve"
	}
	return out, ""
}

func slicesContainsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

func validValue(kind ruleKind, typ, v string) bool {
	if v == "" {
		return false
	}
	switch kind {
	case kindDomain:
		return domainChars.MatchString(v) && !strings.Contains(v, "..")
	case kindWildcard:
		return wildChars.MatchString(v)
	case kindKeyword:
		return printable(v)
	case kindRegex:
		// mihomo's engine takes more than Go's; what Go compiles it compiles too.
		_, err := regexp.Compile(v)
		return err == nil && printable(v)
	case kindGeo:
		return geoCode.MatchString(v)
	case kindCIDR, kindCIDR6:
		p, err := netip.ParsePrefix(v)
		return err == nil && (kind == kindCIDR || p.Addr().Is6())
	case kindASN:
		n, err := strconv.ParseUint(v, 10, 32)
		return err == nil && n > 0
	case kindPort:
		return validPorts(v)
	case kindNetwork:
		return strings.EqualFold(v, "tcp") || strings.EqualFold(v, "udp")
	case kindProcess:
		return printable(v)
	}
	return false
}

// validPorts: "443", "8000-9000" or several joined with "/".
func validPorts(v string) bool {
	for _, part := range strings.Split(v, "/") {
		lo, hi, isRange := strings.Cut(part, "-")
		a, err := strconv.Atoi(lo)
		if err != nil || a < 1 || a > 65535 {
			return false
		}
		if isRange {
			b, err := strconv.Atoi(hi)
			if err != nil || b < a || b > 65535 {
				return false
			}
		}
	}
	return true
}

func printable(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || r == '"' || r == '\'' {
			return false
		}
	}
	return true
}

// ruleCache keeps the rules of the text last served: every Clash profile needs them, and
// the text changes only when the admin saves it.
var ruleCache struct {
	sync.Mutex
	key   string
	rules []string
}

// ServedRules are the saved rules as profiles carry them. Lines that no longer parse (a
// group renamed since) are left out rather than failing every profile.
func ServedRules(text string, g Groups) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	key := g.Main + "\x00" + g.Auto + "\x00" + text
	ruleCache.Lock()
	defer ruleCache.Unlock()
	if ruleCache.key == key {
		return ruleCache.rules
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if rules, err := ParseRules(line, g); err == nil && len(out)+len(rules) <= MaxRules {
			out = append(out, rules...)
		}
	}
	ruleCache.key, ruleCache.rules = key, out
	return out
}

// ruleSince is the first mihomo with a rule type younger than the cores still around.
var ruleSince = map[string]Version{"DOMAIN-REGEX": {1, 18, 2}, "IP-ASN": {1, 18, 2}, "SRC-GEOIP": {1, 18, 4}, "DOMAIN-WILDCARD": {1, 19, 12}}

// RulesFor are the rules an app's core parses. A rule it does not know fails the whole
// profile, so a younger type goes only to a mihomo app that names a core with it.
func RulesFor(rules []string, app App) []string {
	ok := func(r string) bool {
		typ, _, _ := strings.Cut(r, ",")
		since, young := ruleSince[typ]
		return !young || app.Family == FamilyMihomo && app.Core.Known() && app.Core.AtLeast(since)
	}
	for i, r := range rules {
		if !ok(r) {
			out := append([]string{}, rules[:i]...)
			for _, r := range rules[i+1:] {
				if ok(r) {
					out = append(out, r)
				}
			}
			return out
		}
	}
	return rules
}
