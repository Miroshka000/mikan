package mihomo

import (
	"errors"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/config"

	"mikan/internal/sub/model"
	"mikan/internal/sub/subtest"
)

// The template's own proxies, which the chains are made of, as in Citrus's own tests.
const chainProxies = `proxies:
  - {name: entry, type: socks5, server: 127.0.0.1, port: 1081}
  - {name: mid, type: socks5, server: 127.0.0.1, port: 1082}
  - {name: exit-a, type: ss, server: 127.0.0.1, port: 1083, cipher: aes-128-gcm, password: pass}
  - {name: exit-b, type: ss, server: 127.0.0.1, port: 1084, cipher: aes-128-gcm, password: pass}
  - {name: other, type: socks5, server: 127.0.0.1, port: 1085}
`

// coreRefuses is the error of the core's parser on the profile, nil when it loads it.
func coreRefuses(t *testing.T, raw []byte) error {
	t.Helper()
	rc, err := config.UnmarshalRawConfig(raw)
	if err != nil {
		t.Fatalf("the profile does not read: %v\n%s", err, raw)
	}
	_, err = config.ParseRawConfig(rc)
	return err
}

// What Citrus loads is saved: a chain of proxies, a chain as a hop, a group as a hop (the
// first one too), DIRECT as the first hop, two chains that wire a proxy the same way, a
// group hop of the user's proxies.
func TestCheckTemplateChainsCitrusLoads(t *testing.T) {
	prof := profileOf(subtest.Defaults(""))
	for name, groups := range map[string]string{
		"plain":  `  - {name: C, type: chain, proxies: [entry, mid, exit-a]}`,
		"nested": "  - {name: tail, type: chain, proxies: [mid, exit-a]}\n  - {name: C, type: chain, proxies: [entry, tail]}",
		"group hops": "  - {name: entries, type: fallback, proxies: [entry, other]}\n  - {name: exits, type: url-test, proxies: [exit-a, exit-b]}\n" +
			"  - {name: C, type: chain, proxies: [entries, exits]}",
		"direct first": `  - {name: C, type: chain, proxies: [DIRECT, exit-a]}`,
		"same twice":   "  - {name: C, type: chain, proxies: [entry, exit-a]}\n  - {name: D, type: chain, proxies: [entry, exit-a]}",
		"the user's":   "  - {name: mine, type: select, include-all-proxies: true}\n  - {name: C, type: chain, proxies: [entry, mine]}",
	} {
		src := chainProxies + "proxy-groups:\n" + groups + "\nrules:\n  - MATCH,C\n"
		if err := CheckTemplate(prof, src); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		raw, _, err := Template(prof, src)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := coreRefuses(t, raw); err != nil {
			t.Errorf("%s: Citrus refuses it: %v", name, err)
		}
	}
}

// What Citrus refuses on loading is refused on saving, with a code of its own; Citrus does
// refuse each of these profiles, which a template saved before still renders as they are.
func TestCheckTemplateChainsCitrusRefuses(t *testing.T) {
	prof := profileOf(subtest.Defaults(""))
	for name, tc := range map[string]struct{ extra, groups, code, detail string }{
		"one hop":  {groups: `  - {name: C, type: chain, proxies: [entry]}`, code: "template_chain_hops", detail: "C"},
		"self":     {groups: `  - {name: C, type: chain, proxies: [entry, C]}`, code: "template_chain_hops", detail: "C"},
		"twice":    {groups: `  - {name: C, type: chain, proxies: [entry, mid, entry]}`, code: "template_chain_hops", detail: "C"},
		"use":      {groups: `  - {name: C, type: chain, proxies: [entry, mid], use: [subs]}`, code: "template_chain_keys", detail: "C"},
		"no hops":  {groups: "  - {name: E, type: chain, proxies: []}\n  - {name: C, type: chain, proxies: [entry, E]}", code: "template_chain_hops", detail: "E"},
		"DIRECT 2": {groups: `  - {name: C, type: chain, proxies: [entry, DIRECT]}`, code: "template_chain_builtin", detail: "C → DIRECT"},
		"DIRECT in a group past the first": {groups: "  - {name: exits, type: select, proxies: [exit-a, DIRECT]}\n  - {name: C, type: chain, proxies: [entry, exits]}",
			code: "template_chain_builtin", detail: "C → DIRECT"},
		"DIRECT first in a nested chain": {groups: "  - {name: tail, type: chain, proxies: [DIRECT, exit-a]}\n  - {name: C, type: chain, proxies: [entry, tail]}",
			code: "template_chain_builtin", detail: "C → DIRECT"},
		"include-all group": {extra: "proxy-providers:\n  subs: {type: inline, payload: [{name: sub-1, type: ss, server: 127.0.0.1, port: 1090, cipher: aes-128-gcm, password: pass}]}\n",
			groups: "  - {name: all, type: url-test, include-all: true}\n  - {name: C, type: chain, proxies: [entry, all]}", code: "template_chain_group", detail: "C → all"},
		"no provider": {groups: "  - {name: exits, type: url-test, use: [nope], proxies: [exit-a]}\n  - {name: C, type: chain, proxies: [entry, exits]}",
			code: "template_group_member", detail: "exits → nope"},
		"two chains disagree": {groups: "  - {name: C1, type: chain, proxies: [entry, exit-a]}\n  - {name: C2, type: chain, proxies: [mid, exit-a]}",
			code: "template_chain_dialer", detail: "C2 → exit-a"},
		"through a group too": {groups: "  - {name: exits, type: url-test, proxies: [exit-a, exit-b]}\n  - {name: C1, type: chain, proxies: [entry, exits]}\n" +
			"  - {name: C2, type: chain, proxies: [mid, exit-b]}", code: "template_chain_dialer", detail: "C2 → exit-b"},
		"a nested chain's entry": {groups: "  - {name: tail, type: chain, proxies: [mid, exit-a]}\n  - {name: C1, type: chain, proxies: [entry, tail]}\n" +
			"  - {name: C2, type: chain, proxies: [other, mid]}", code: "template_chain_dialer", detail: "C2 → mid"},
		"past DIRECT": {groups: "  - {name: C1, type: chain, proxies: [DIRECT, exit-a]}\n  - {name: C2, type: chain, proxies: [mid, exit-a]}",
			code: "template_chain_dialer", detail: "C2 → exit-a"},
		"own dialer-proxy": {extra: "  - {name: pinned, type: ss, server: 127.0.0.1, port: 1086, cipher: aes-128-gcm, password: pass, dialer-proxy: other}\n",
			groups: `  - {name: C, type: chain, proxies: [entry, pinned]}`, code: "template_chain_dialer", detail: "C → pinned"},
		"provider's override": {extra: "proxy-providers:\n  subs: {type: inline, override: {dialer-proxy: other}, payload: [{name: sub-1, type: ss, server: 127.0.0.1, port: 1090, cipher: aes-128-gcm, password: pass}]}\n",
			groups: "  - {name: exits, type: url-test, use: [subs]}\n  - {name: C, type: chain, proxies: [entry, exits]}", code: "template_chain_dialer", detail: "C → subs"},
		"loop through a group": {groups: "  - {name: g, type: select, proxies: [C, other]}\n  - {name: C, type: chain, proxies: [g, exit-a]}",
			code: "template_chain_loop", detail: "C → g → C"},
		"loop made by the wiring": {groups: "  - {name: exits, type: fallback, proxies: [exit-a, entry]}\n  - {name: C, type: chain, proxies: [entry, exits]}",
			code: "template_chain_loop", detail: "entry → entry"},
		"two chains cross": {groups: "  - {name: C1, type: chain, proxies: [mid, exit-a]}\n  - {name: C2, type: chain, proxies: [exit-a, mid]}",
			code: "template_chain_loop"},
	} {
		src := chainProxies + tc.extra + "proxy-groups:\n" + tc.groups + "\n"
		err := CheckTemplate(prof, src)
		var te *model.TemplateError
		if !errors.As(err, &te) || te.Code != tc.code || tc.detail != "" && te.Detail != tc.detail {
			t.Errorf("%s: %v, want %s (%s)", name, err, tc.code, tc.detail)
			continue
		}
		// Saved before, it still renders, as it is: Citrus wires the chains itself.
		raw, _, err := Template(prof, src)
		if err != nil {
			t.Fatalf("%s: a saved template: %v", name, err)
		}
		if n := strings.Count(string(raw), "dialer-proxy"); n != strings.Count(tc.extra, "dialer-proxy") {
			t.Errorf("%s: the profile has %d dialer-proxy:\n%s", name, n, raw)
		}
		if err := coreRefuses(t, raw); err == nil {
			t.Errorf("%s: Citrus loads it", name)
		}
	}
}
