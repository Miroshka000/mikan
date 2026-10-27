package subsrc

import (
	"slices"
	"testing"

	submodel "mikan/internal/sub/model"
	"mikan/internal/sub/profile"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
)

// The preview of a Clash profile tells what sing-box and xray leave out of it, each core's
// group notes first, every code the panel has words for; mihomo carries it as it is and
// tells nothing.
func TestPreviewNotes(t *testing.T) {
	u := subtest.Multi()
	prof := profile.Profile{Proxies: u.Proxies(), Nodes: u.Build.Nodes, Direct: u.Direct, Rules: u.Rules, Routes: u.Routes, Lang: u.Lang}
	notes := previewNotes(prof, submodel.GroupNames{}, routing.All, subtest.Clash(subtest.DroppedTemplate))
	codes := map[string]map[string]bool{"singbox": {}, "xray": {}}
	for _, n := range notes {
		if codes[n.Core] == nil {
			t.Fatalf("a note of %s: %+v", n.Core, n)
		}
		codes[n.Core][n.Code] = true
	}
	for core, want := range map[string][]string{
		"singbox": {"proxy_unsupported", "rule_unsupported", "list_unsupported", "dns_policy_unsupported"},
		"xray":    {"select_first", "proxy_unsupported", "rule_unsupported", "list_unsupported", "dns_policy_unsupported", "category_missing"},
	} {
		for _, code := range want {
			if !codes[core][code] {
				t.Errorf("%s does not tell %s: %+v", core, code, notes)
			}
		}
	}
	// sing-box's notes come first, a core's groups before what it leaves out.
	first := slices.IndexFunc(notes, func(n submodel.Note) bool { return n.Core == "xray" })
	if first < 0 || slices.ContainsFunc(notes[first:], func(n submodel.Note) bool { return n.Core == "singbox" }) || notes[first].Code != "select_first" {
		t.Errorf("order: %+v", notes)
	}

	// The built-in profile tells what the cores leave out of the panel's routing too.
	u.Routes, u.Rules = subtest.Routes(), subtest.Rules
	prof.Routes, prof.Rules = u.Routes, u.Rules
	if n := previewNotes(prof, submodel.GroupNames{}, routing.RUDirect, submodel.Template{}); !slices.Contains(n, submodel.Note{Core: "xray", Subject: "own-wl", Code: "list_unsupported"}) {
		t.Errorf("built-in: %+v", n)
	}
}
