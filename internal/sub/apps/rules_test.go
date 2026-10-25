package apps

import (
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

// samples are the User-Agents each app of the catalog sends, as far as they are known.
var samples = map[string]string{
	"mikan":        "mihomo/1.19.32 Mikan/0.1.3",
	"kabosu":       "kabosu/1.0.0",
	"koala":        "koala-clash/1.4.1",
	"clashfest":    "mihomo/1.19.32 ClashFest/1.2.0",
	"slothclash":   "clash.meta/v1.19.32 SlothClash/0.9.4",
	"flclash":      "FlClashX/v0.9.1 Platform/android",
	"verge":        "clash-verge/v2.2.3",
	"clashmeta":    "ClashMetaForAndroid/2.11.19.Meta",
	"mihomo":       "Flowvy/0.9 mihomo/1.19.31",
	"happ":         "Happ/3.4.1",
	"incy":         "INCY/2.1.0",
	"v2raytun":     "v2RayTun/Android",
	"v2rayng":      "v2rayNG/1.10.20",
	"v2rayn":       "v2rayN/7.0.0",
	"xray":         "OneXray/1.0",
	"hiddify":      "HiddifyNext/4.1.1 (android) like ClashMeta v2ray sing-box",
	"karing":       "Karing/1.2.26 platform/tvos sing-box 1.14.0",
	"nekobox":      "NekoBox/1.3.7",
	"husi":         "husi/0.9.1 (sing-box 1.15.0-alpha.10)",
	"singbox":      "SFA (sing-box 1.14.2; language en_US)",
	"stash":        "Stash/2.8.0 Clash/1.11.0",
	"streisand":    "Streisand/1.6.44",
	"shadowrocket": "Shadowrocket/2592 CFNetwork/1568.100.1",
	"foxray":       "FoxRay/1.0",
}

// Every app of the list is told from its User-Agent, and DetectApp agrees with the list on
// the family: the two read the same header and must never disagree about an app.
func TestCatalogIdentifiesAndAgreesWithDetectApp(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range Catalog {
		if seen[k.ID] || k.ID == "" || k.Name == "" {
			t.Errorf("%+v: an id that repeats or is empty, or no name", k)
		}
		seen[k.ID] = true
		ua, ok := samples[k.ID]
		if !ok {
			t.Errorf("%s: no sample User-Agent in the test", k.ID)
			continue
		}
		got, ok := Identify(ua)
		if !ok || got.ID != k.ID {
			t.Errorf("%q is %q, want %s", ua, got.ID, k.ID)
		}
		if f := DetectApp(ua).Family; f != k.Family {
			t.Errorf("%s: DetectApp says %s, the catalog %s", k.ID, f, k.Family)
		}
	}
	if len(samples) != len(Catalog) {
		t.Errorf("%d samples for %d apps", len(samples), len(Catalog))
	}
	for _, ua := range []string{"", "curl/8.9", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X)", "okhttp/4.12.0"} {
		if k, ok := Identify(ua); ok {
			t.Errorf("%q is %s: a browser or a tool is no app of the list", ua, k.ID)
		}
	}
}

func TestIdentifyTellsTheAppBeforeItsCore(t *testing.T) {
	for ua, want := range map[string]string{
		"mihomo/1.19.32 ClashFest/1.2.0":                            "clashfest",
		"HiddifyNext/2.5.7 (android) like ClashMeta v2ray sing-box": "hiddify",
		"v2rayNG/1.10.2":                        "v2rayng",
		"clash.meta/v1.19.12":                   "mihomo",
		"Prizrak-Box/1.4.2 (windows)":           "mihomo",
		"SFI (sing-box 1.11.4; language ru_RU)": "singbox",
		"Stash/3.1.1 Clash/1.9.0":               "stash",
		"HAPP/3.4.1":                            "happ",
	} {
		if k, _ := Identify(ua); k.ID != want {
			t.Errorf("%q is %q, want %q", ua, k.ID, want)
		}
	}
}

func TestAlternativesSkipBlockedApps(t *testing.T) {
	if got := Alternatives(nil, 3); !reflect.DeepEqual(got, []string{"Happ", "INCY", "Koala Clash"}) {
		t.Errorf("%v", got)
	}
	if got := Alternatives(map[string]bool{"happ": true, "koala": true}, 3); !reflect.DeepEqual(got, []string{"INCY", "Mikan", "Kabosu"}) {
		t.Errorf("%v", got)
	}
	all := map[string]bool{}
	for _, id := range Recommended {
		all[id] = true
	}
	if got := Alternatives(all, 3); len(got) != 0 {
		t.Errorf("%v: nothing is left to recommend", got)
	}
}

func hdr(kv ...string) http.Header {
	h := http.Header{}
	for i := 0; i+1 < len(kv); i += 2 {
		h.Add(kv[i], kv[i+1])
	}
	return h
}

func uaIs(op, value string, not bool) Cond {
	return Cond{Header: "User-Agent", Op: op, Value: value, Not: not}
}

func rule(name string, g Group, do Action) Rule {
	return Rule{Name: name, Enabled: true, When: g, Do: do}
}

func allOf(conds ...Cond) Group { return Group{Op: And, Conds: conds} }
func anyOf(conds ...Cond) Group { return Group{Op: Or, Conds: conds} }

func responder(t *testing.T, p Policy) *Responder {
	t.Helper()
	r, err := NewResponder(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestConditions(t *testing.T) {
	for _, c := range []struct {
		name string
		cond Cond
		h    http.Header
		want bool
	}{
		{"equals ignores case", uaIs(OpEquals, "happ/3.6", false), hdr("User-Agent", "HAPP/3.6"), true},
		{"equals is the whole value", uaIs(OpEquals, "happ", false), hdr("User-Agent", "Happ/3.6"), false},
		{"contains ignores case", uaIs(OpContains, "HAPP", false), hdr("User-Agent", "Happ/3.6"), true},
		{"contains", uaIs(OpContains, "incy", false), hdr("User-Agent", "Happ/3.6"), false},
		{"regex is as written", uaIs(OpRegex, `^happ/\d`, false), hdr("User-Agent", "Happ/3.6"), false},
		{"regex with the flag", uaIs(OpRegex, `(?i)^happ/\d`, false), hdr("User-Agent", "Happ/3.6"), true},
		{"not", uaIs(OpContains, "happ", true), hdr("User-Agent", "INCY/2"), true},
		{"not of a match", uaIs(OpContains, "happ", true), hdr("User-Agent", "Happ/3"), false},
		{"any header", Cond{Header: "x-device-os", Op: OpEquals, Value: "iOS"}, hdr("X-Device-Os", "ios"), true},
		{"an absent header equals nothing", Cond{Header: "X-Hwid", Op: OpEquals, Value: ""}, hdr("User-Agent", "Happ"), true},
		{"a present one does not", Cond{Header: "X-Hwid", Op: OpEquals, Value: ""}, hdr("X-Hwid", "abc"), false},
		{"an absent header contains nothing", Cond{Header: "X-Hwid", Op: OpContains, Value: "a"}, hdr(), false},
		{"values of a repeated header are one", Cond{Header: "X-Forwarded-For", Op: OpContains, Value: "10.0.0.2"}, hdr("X-Forwarded-For", "10.0.0.1", "X-Forwarded-For", "10.0.0.2"), true},
		{"only the start of a long value is read", uaIs(OpContains, "needle", false), hdr("User-Agent", strings.Repeat("a", MaxHeaderValue)+"needle"), false},
	} {
		r := responder(t, Policy{NoBuiltin: true, Rules: []Rule{rule("r", allOf(c.cond), Action{Respond: Respond403})}})
		if got := r.Decide(c.h).Rule == "r"; got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestGroups(t *testing.T) {
	happ, ios := uaIs(OpContains, "happ", false), Cond{Header: "X-Device-Os", Op: OpEquals, Value: "ios"}
	incy := uaIs(OpContains, "incy", false)
	// (Happ or INCY) and iOS.
	nested := Group{Op: And, Conds: []Cond{ios}, Groups: []Inner{{Op: Or, Conds: []Cond{happ, incy}}}}
	for _, c := range []struct {
		name string
		g    Group
		h    http.Header
		want bool
	}{
		{"and needs both", allOf(happ, ios), hdr("User-Agent", "Happ", "X-Device-Os", "android"), false},
		{"and has both", allOf(happ, ios), hdr("User-Agent", "Happ", "X-Device-Os", "ios"), true},
		{"or needs one", anyOf(happ, ios), hdr("User-Agent", "curl", "X-Device-Os", "ios"), true},
		{"or has none", anyOf(happ, ios), hdr("User-Agent", "curl"), false},
		{"nested: the group and the condition", nested, hdr("User-Agent", "INCY/2", "X-Device-Os", "ios"), true},
		{"nested: the group alone", nested, hdr("User-Agent", "INCY/2", "X-Device-Os", "android"), false},
		{"nested: the condition alone", nested, hdr("User-Agent", "curl", "X-Device-Os", "ios"), false},
		{"an or of groups", Group{Op: Or, Groups: []Inner{{Op: And, Conds: []Cond{happ, ios}}, {Op: And, Conds: []Cond{incy}}}}, hdr("User-Agent", "INCY"), true},
		{"a group of nothing holds for every request", Group{Op: And, Conds: []Cond{}}, hdr(), true},
		{"so does an or of nothing", Group{Op: Or}, hdr(), true},
	} {
		r := responder(t, Policy{NoBuiltin: true, Rules: []Rule{rule("r", c.g, Action{Respond: Respond403})}})
		if got := r.Decide(c.h).Rule == "r"; got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestFirstMatchDecides(t *testing.T) {
	r := responder(t, Policy{NoBuiltin: true, Rules: []Rule{
		{Name: "off", Enabled: false, When: allOf(uaIs(OpContains, "happ", false)), Do: Action{Respond: Respond404}},
		rule("first", allOf(uaIs(OpContains, "happ", false)), Action{Respond: Respond403}),
		rule("second", allOf(uaIs(OpContains, "happ", false)), Action{Respond: RespondDrop}),
	}})
	d := r.Decide(hdr("User-Agent", "Happ/3.4.1"))
	if d.Rule != "first" || d.Responds() != Respond403 || d.App.ID != "happ" {
		t.Errorf("%+v", d)
	}
	if d := r.Decide(hdr("User-Agent", "INCY/2")); d.Rule != "" || d.Responds() != RespondServe || d.App.ID != "incy" {
		t.Errorf("a request no rule matches: %+v", d)
	}
}

func TestBuiltinRules(t *testing.T) {
	r := responder(t, Policy{})
	for ua, want := range map[string]string{
		"ShadowsocksR/4.9.2":               RuleSSR,
		"ShadowsocksRR/1.2":                RuleSSR,
		"SSR/1.0":                          RuleSSR,
		"ssrr":                             RuleSSR,
		"ClashforWindows/0.20.39":          RuleOld,
		"Clash for Windows/0.20.39":        RuleOld,
		"ClashX/1.118.0 (com.west2online)": RuleOld,
		"ClashX Pro/1.118.0.5":             RuleOld,
		"ClashForAndroid/2.5.12":           RuleOld,
		"ClashX Meta/1.4.15":               "",
		"ClashMetaForAndroid/2.11.19.Meta": "",
		"clash.meta/v1.19.12":              "",
		"clash-verge/v2.2.3":               "",
		"Stash/2.8.0 Clash/1.11.0":         "",
		"Shadowrocket/2592 CFNetwork/1568": "",
		"Happ/3.4.1":                       "",
		"v2rayNG/1.10.20":                  "",
		"Mozilla/5.0 (Windows NT 10.0)":    "",
		"":                                 "",
	} {
		d := r.Decide(hdr("User-Agent", ua))
		if d.Rule != want {
			t.Errorf("%q: %q, want %q", ua, d.Rule, want)
		}
		if want != "" && (d.Responds() != RespondStub || len(d.Install) != 3) {
			t.Errorf("%q: %s with %v to install, want a stub that names three apps", ua, d.Responds(), d.Install)
		}
	}
	// The admin turns them off all at once, or answers the client otherwise before they run.
	off := responder(t, Policy{NoBuiltin: true})
	if d := off.Decide(hdr("User-Agent", "ClashforWindows/0.20.39")); d.Rule != "" {
		t.Errorf("built-in rules are off: %+v", d)
	}
	own := responder(t, Policy{Rules: []Rule{rule("cfw", allOf(uaIs(OpContains, "clashforwindows", false)), Action{Respond: RespondServe})}})
	if d := own.Decide(hdr("User-Agent", "ClashforWindows/0.20.39")); d.Rule != "cfw" || d.Responds() != RespondServe {
		t.Errorf("a rule of the admin's goes first: %+v", d)
	}
}

func TestBlockedApps(t *testing.T) {
	r := responder(t, Policy{Blocked: []string{"shadowrocket", "happ"}, Text: "Use INCY"})
	d := r.Decide(hdr("User-Agent", "Shadowrocket/2592 CFNetwork/1568"))
	if d.Rule != RuleApps || d.Responds() != RespondStub || d.Action.Text != "Use INCY" || d.App.ID != "shadowrocket" {
		t.Errorf("%+v", d)
	}
	if !reflect.DeepEqual(d.Install, []string{"INCY", "Koala Clash", "Mikan"}) {
		t.Errorf("the blocked Happ is not recommended: %v", d.Install)
	}
	if d := r.Decide(hdr("User-Agent", "INCY/2.1.0")); d.Rule != "" || d.Responds() != RespondServe {
		t.Errorf("an allowed app: %+v", d)
	}
	if !r.Blocked("happ") || r.Blocked("incy") || (*Responder)(nil).Blocked("happ") {
		t.Error("Blocked")
	}
	// A rule of the admin's answers before the list does: it is the more particular.
	over := responder(t, Policy{Blocked: []string{"happ"}, Rules: []Rule{rule("happ ios", allOf(uaIs(OpContains, "happ", false)), Action{Respond: RespondServe, SkipHWID: true})}})
	if d := over.Decide(hdr("User-Agent", "Happ/3.4.1")); d.Rule != "happ ios" || !d.Action.SkipHWID {
		t.Errorf("%+v", d)
	}
}

func TestNilResponderServesAndNamesTheApp(t *testing.T) {
	var r *Responder
	d := r.Decide(hdr("User-Agent", "Happ/3.4.1"))
	if d.App.ID != "happ" || d.Rule != "" || d.Responds() != RespondServe {
		t.Errorf("%+v", d)
	}
}

func TestCleanDropsWhatTheAnswerDoesNotUse(t *testing.T) {
	r := Rule{Name: "  x  ", Do: Action{Respond: Respond403, Format: "clash", Text: "t", HTML: "<p>", SkipHWID: true, Template: 3, Headers: []Header{{Name: "X-A", Value: "1"}}}}.Clean()
	if r.Name != "x" || r.Do.Format != "" || r.Do.Text != "" || r.Do.HTML != "" || r.Do.SkipHWID || r.Do.Template != 0 || len(r.Do.Headers) != 1 {
		t.Errorf("403 keeps its headers only: %+v", r)
	}
	if r.When.Conds == nil || r.When.Groups == nil {
		t.Error("a group must serialize as empty lists, not null")
	}
	d := Rule{Do: Action{Respond: RespondDrop, Headers: []Header{{Name: "X-A", Value: "1"}}}}.Clean().Do
	if d.Headers != nil {
		t.Error("a dropped connection carries no header")
	}
	s := Rule{Do: Action{Format: "uri", SkipHWID: true, HTML: "<p>"}}.Clean().Do
	if s.Respond != RespondServe || s.Format != "uri" || !s.SkipHWID || s.HTML != "" {
		t.Errorf("an empty respond is serve: %+v", s)
	}
}

func okRule() Rule {
	return rule("ok", allOf(uaIs(OpContains, "happ", false)), Action{Respond: RespondServe})
}

func TestCheck(t *testing.T) {
	opts := CheckOpts{Formats: []string{"clash", "singbox", "uri"}}
	long := strings.Repeat("a", MaxValue+1)
	tooMany := make([]Cond, MaxConds+1)
	for i := range tooMany {
		tooMany[i] = uaIs(OpContains, "a", false)
	}
	with := func(f func(r *Rule)) Rule {
		r := okRule()
		f(&r)
		return r
	}
	for _, c := range []struct {
		name string
		rule Rule
		path string
		code string
	}{
		{"a name", with(func(r *Rule) { r.Name = "  " }), "r.name", "rule_name"},
		{"a long name", with(func(r *Rule) { r.Name = strings.Repeat("я", MaxName+1) }), "r.name", "rule_name"},
		{"a group's op", with(func(r *Rule) { r.When.Op = "xor" }), "r.when.op", "rule_group_op"},
		{"a condition's op", with(func(r *Rule) { r.When.Conds[0].Op = "starts" }), "r.when.conds.0.op", "rule_op"},
		{"a header name with a space", with(func(r *Rule) { r.When.Conds[0].Header = "User Agent" }), "r.when.conds.0.header", "rule_header"},
		{"a long value", with(func(r *Rule) { r.When.Conds[0].Value = long }), "r.when.conds.0.value", "rule_value"},
		{"a value with a line break", with(func(r *Rule) { r.When.Conds[0].Value = "a\nb" }), "r.when.conds.0.value", "rule_value"},
		{"an empty contains", with(func(r *Rule) { r.When.Conds[0].Value = "" }), "r.when.conds.0.value", "rule_value_empty"},
		{"a regular expression that does not compile", with(func(r *Rule) { r.When.Conds[0] = uaIs(OpRegex, "(", false) }), "r.when.conds.0.value", "rule_regex"},
		{"an empty regular expression", with(func(r *Rule) { r.When.Conds[0] = uaIs(OpRegex, "", false) }), "r.when.conds.0.value", "rule_value_empty"},
		{"too many conditions", with(func(r *Rule) { r.When = Group{Op: Or, Conds: tooMany} }), "r.when.conds", "rule_conds"},
		{"too many through the groups", with(func(r *Rule) {
			r.When = Group{Op: And, Conds: tooMany[:MaxConds-1], Groups: []Inner{{Op: Or, Conds: tooMany[:2]}}}
		}), "r.when", "rule_conds"},
		{"an empty inner group", with(func(r *Rule) { r.When.Groups = []Inner{{Op: Or}} }), "r.when.groups.0.conds", "rule_group_empty"},
		{"an inner group's op", with(func(r *Rule) { r.When.Groups = []Inner{{Op: "", Conds: []Cond{uaIs(OpContains, "a", false)}}} }), "r.when.groups.0.op", "rule_group_op"},
		{"too many groups", with(func(r *Rule) {
			for range MaxGroups + 1 {
				r.When.Groups = append(r.When.Groups, Inner{Op: Or, Conds: []Cond{uaIs(OpContains, "a", false)}})
			}
		}), "r.when.groups", "rule_groups"},
		{"a respond", with(func(r *Rule) { r.Do.Respond = "redirect" }), "r.do.respond", "rule_respond"},
		{"a format the panel has not", with(func(r *Rule) { r.Do.Format = "yaml" }), "r.do.format", "rule_format"},
		{"a template that does not exist", with(func(r *Rule) { r.Do.Template = 7 }), "r.do.template", "rule_template"},
		{"a page with nothing in it", with(func(r *Rule) { r.Do = Action{Respond: RespondHTML, HTML: " \n"} }), "r.do.html", "rule_html"},
		{"a page that is too long", with(func(r *Rule) { r.Do = Action{Respond: RespondHTML, HTML: strings.Repeat("a", MaxHTML+1)} }), "r.do.html", "rule_html"},
		{"a stub's text with a line break", with(func(r *Rule) { r.Do = Action{Respond: RespondStub, Text: "a\nb"} }), "r.do.text", "rule_text"},
		{"too many headers", with(func(r *Rule) {
			r.Do.Headers = make([]Header, MaxHeaders+1)
			for i := range r.Do.Headers {
				r.Do.Headers[i] = Header{Name: "X-A", Value: "1"}
			}
		}), "r.do.headers", "rule_headers_many"},
		{"a header that frames the body", with(func(r *Rule) { r.Do.Headers = []Header{{Name: "Content-Length", Value: "0"}} }), "r.do.headers.0.name", "rule_header_name"},
		{"a header that frames, in any case", with(func(r *Rule) { r.Do.Headers = []Header{{Name: "transfer-encoding", Value: "chunked"}} }), "r.do.headers.0.name", "rule_header_name"},
		{"a header name with a line break", with(func(r *Rule) { r.Do.Headers = []Header{{Name: "X-A\r\nX-B", Value: "1"}} }), "r.do.headers.0.name", "rule_header_name"},
		{"a header value with a line break", with(func(r *Rule) { r.Do.Headers = []Header{{Name: "X-A", Value: "1\r\nX-B: 2"}} }), "r.do.headers.0.value", "rule_header_value"},
		{"a header value that is not ASCII", with(func(r *Rule) { r.Do.Headers = []Header{{Name: "X-A", Value: "привет"}} }), "r.do.headers.0.value", "rule_header_value"},
	} {
		err := c.rule.Check("r", opts)
		var ce *CheckError
		if !errors.As(err, &ce) || ce.Path != c.path || ce.Code != c.code {
			t.Errorf("%s: %v, want %s at %s", c.name, err, c.code, c.path)
		}
	}
	if err := okRule().Check("r", opts); err != nil {
		t.Errorf("a good rule: %v", err)
	}
	// What the answer does not use is not checked: Clean drops it.
	drop := with(func(r *Rule) {
		r.Do = Action{Respond: RespondDrop, Format: "yaml", Template: 9, Headers: []Header{{Name: "Content-Length", Value: "x"}}}
	})
	if err := drop.Check("r", opts); err != nil {
		t.Errorf("a dropped connection: %v", err)
	}
	// A template that exists is named: the hook the templates plug into.
	opts.Template = func(id int64) bool { return id == 7 }
	if err := with(func(r *Rule) { r.Do.Template = 7 }).Check("r", opts); err != nil {
		t.Errorf("a template that exists: %v", err)
	}
	if err := with(func(r *Rule) { r.Do.Template = 8 }).Check("r", opts); err == nil {
		t.Error("a template that does not")
	}
	// The inner groups hold what the outer ones can: a rule at the limits passes.
	edge := with(func(r *Rule) {
		r.When = Group{Op: And, Conds: tooMany[:MaxConds-2], Groups: []Inner{{Op: Or, Conds: tooMany[:2]}}}
	})
	if err := edge.Check("r", opts); err != nil {
		t.Errorf("a rule at the limit: %v", err)
	}
}

func TestCheckRules(t *testing.T) {
	rules := make([]Rule, MaxRules+1)
	for i := range rules {
		rules[i] = okRule()
	}
	var ce *CheckError
	if err := CheckRules(rules, CheckOpts{}); !errors.As(err, &ce) || ce.Code != "rules_many" {
		t.Errorf("%v", err)
	}
	rules = rules[:2]
	rules[1].When.Conds[0].Op = "x"
	if err := CheckRules(rules, CheckOpts{}); !errors.As(err, &ce) || ce.Path != "rules.1.when.conds.0.op" {
		t.Errorf("the path names the rule: %v", err)
	}
}
