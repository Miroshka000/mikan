package nodeapi

import (
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
)

// The entries of the filters' lists, read the one way both sides read them: the panel
// checks what the admin typed and keeps the canonical form, the node builds its rules
// from the canonical form only. mihomo refuses the whole config over one rule it cannot
// parse (a port "+465"), so the node must never write what these do not return.

// CanonPorts takes a port ("465") or a range ("1000-2000", "1000 - 2000") of 1–65535 and
// returns it in digits only.
func CanonPorts(s string) (string, bool) {
	lo, hi, isRange := strings.Cut(strings.TrimSpace(s), "-")
	a, okA := portNum(lo)
	b := a
	okB := true
	if isRange {
		b, okB = portNum(hi)
	}
	if !okA || !okB || a > b {
		return "", false
	}
	if a == b {
		return strconv.Itoa(a), true
	}
	return strconv.Itoa(a) + "-" + strconv.Itoa(b), true
}

// portNum is 1–65535 written in digits: strconv.Atoi alone takes a sign as well.
func portNum(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 5 || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 1 && n <= 65535
}

// CanonNetwork takes a network ("203.0.113.7/24") or one address and returns the masked
// network. An IPv4 address written as IPv6 (::ffff:203.0.113.7) is taken as IPv4: that
// is how the node sees a client's address.
func CanonNetwork(s string) (netip.Prefix, bool) {
	s = strings.TrimSpace(s)
	if p, err := netip.ParsePrefix(s); err == nil {
		if a := p.Addr(); a.Is4In6() && p.Bits() >= 96 {
			p = netip.PrefixFrom(a.Unmap(), p.Bits()-96)
		}
		return p.Masked(), true
	}
	if a, err := netip.ParseAddr(s); err == nil && a.Zone() == "" {
		a = a.Unmap()
		return netip.PrefixFrom(a, a.BitLen()), true
	}
	return netip.Prefix{}, false
}

var domainRe = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)*[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// CanonDomain takes "example.com", ".example.com" or "*.example.com" (the domain and its
// subdomains) and returns it in lower case; a national one ("пример.рф") in the form DNS
// and mihomo know it by (xn--e1afmkfd.xn--p1ai). An address is not a domain: it goes to
// the networks.
func CanonDomain(s string) (string, bool) {
	s = strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "*."), "."), "."))
	if len(s) > 1024 {
		return "", false
	}
	if !isASCII(s) {
		a, err := idna.Lookup.ToASCII(s)
		if err != nil || !utf8.ValidString(s) {
			return "", false
		}
		s = a
	}
	if len(s) > 253 || !domainRe.MatchString(s) {
		return "", false
	}
	if _, err := netip.ParseAddr(s); err == nil {
		return "", false
	}
	return s, true
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

// CanonEgress validates the value of a typed egress rule.
func CanonEgress(kind, value string) (string, bool) {
	switch kind {
	case "port":
		return CanonPorts(value)
	case "network":
		p, ok := CanonNetwork(value)
		return p.String(), ok
	case "domain":
		return CanonDomain(value)
	default:
		return "", false
	}
}
