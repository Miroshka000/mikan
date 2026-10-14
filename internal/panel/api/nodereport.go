package api

import (
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The report for the chat: what "Check node" found, in plain words a helper can read, with
// nothing that leads to the admin's servers or opens them. The node's and the panel's
// addresses, names and API port are masked, and so is anything that looks like an
// address, a domain or a key in the raw errors.

// publicNames are names everybody's report may show: the places every mikan reaches.
var publicNames = []string{"github.com", "ghcr.io", "check-host.net", "api.telegram.org", "cloudflare.com"}

var (
	reportToken  = regexp.MustCompile(`[^\s"'(),;\[\]{}<>=]+`)
	reportDomain = regexp.MustCompile(`(?i)^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{1,62}$`)
	reportSecret = regexp.MustCompile(`^[A-Za-z0-9+/_=-]{32,}$`)
)

const masked = "***"

// maskReport hides secrets (exact strings: hosts, the API port) and whatever looks like an
// address, a domain or a key.
func maskReport(text string, secrets []string) string {
	var exact []string
	for _, s := range secrets {
		if s = strings.TrimSpace(s); len(s) >= 2 {
			exact = append(exact, s)
		}
	}
	// Longer first: a domain before the IP it may contain a part of.
	slices.SortFunc(exact, func(a, b string) int { return len(b) - len(a) })
	return reportToken.ReplaceAllStringFunc(text, func(tok string) string {
		core := strings.TrimRight(tok, ".:")
		tail := tok[len(core):]
		if core == "" {
			return tok
		}
		if slices.Contains(exact, core) || hideToken(core) {
			return masked + tail
		}
		for _, s := range exact {
			switch {
			case isNumber(s):
				// A port: only where it stands as one, never inside another number.
				if strings.HasSuffix(core, ":"+s) {
					core = strings.TrimSuffix(core, s) + masked
				}
			default:
				core = strings.ReplaceAll(core, s, masked)
			}
		}
		return core + tail
	})
}

func isNumber(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func hideToken(t string) bool {
	if strings.HasPrefix(t, "mikan1.") {
		return true
	}
	if _, err := netip.ParseAddr(strings.Trim(t, "[]")); err == nil {
		return true
	}
	if _, err := netip.ParseAddrPort(t); err == nil {
		return true
	}
	host := t
	if i := strings.Index(t, "://"); i >= 0 {
		host = t[i+3:]
	}
	host, _, _ = strings.Cut(host, "/")
	if h, p, ok := strings.Cut(host, ":"); ok && isNumber(p) {
		host = h
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return true
	}
	if reportDomain.MatchString(host) {
		h := strings.ToLower(host)
		for _, n := range publicNames {
			if h == n || strings.HasSuffix(h, "."+n) {
				return false
			}
		}
		return true
	}
	// A key or a token: long, letters and digits mixed.
	return reportSecret.MatchString(t) && strings.ContainsAny(t, "0123456789") && strings.ContainsAny(strings.ToLower(t), "abcdefghijklmnopqrstuvwxyz")
}

// reportText renders a check as lines for the chat, then masks it.
func reportText(title string, versions string, at time.Time, items []CheckItem, secrets []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n%s\n%s\n", title, versions, at.UTC().Format(time.RFC3339))
	for _, it := range items {
		fmt.Fprintf(&b, "[%s] %s", it.Status, it.ID)
		if it.Code != "" {
			fmt.Fprintf(&b, ": %s", it.Code)
		}
		if len(it.Params) > 0 {
			keys := make([]string, 0, len(it.Params))
			for k := range it.Params {
				keys = append(keys, k)
			}
			slices.Sort(keys)
			for _, k := range keys {
				fmt.Fprintf(&b, " %s=%s", k, it.Params[k])
			}
		}
		if it.Detail != "" {
			fmt.Fprintf(&b, " (%s)", strings.ReplaceAll(it.Detail, "\n", " "))
		}
		b.WriteByte('\n')
	}
	return maskReport(b.String(), secrets)
}
