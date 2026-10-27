package apps

import (
	"regexp"
	"regexp/syntax"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Response rules answer a request by its headers before any profile is made: the admin may
// serve a client another format, hand it a page, refuse it, or close the connection on it.
// They are checked top to bottom and the first one that matches decides; the built-in rules
// and the list of blocked apps (Policy) answer what no rule of the admin's did.

// How a condition compares a header with its value. equals and contains ignore case: a
// User-Agent is written as its app likes, and "happ" must find "Happ/3.6". A regular
// expression is as written; (?i) in front ignores case.
const (
	OpEquals   = "equals"
	OpContains = "contains"
	OpRegex    = "regex"
)

// Cond tests one header of the request.
type Cond struct {
	// Header is any header's name: User-Agent, X-Device-Os, X-Hwid, a custom one.
	Header string `json:"header" minLength:"1" maxLength:"64"`
	Op     string `json:"op" enum:"equals,contains,regex"`
	// Value is what the header is compared with. Empty equals means a header that is
	// absent or empty; a regular expression is Go's RE2, so it always runs in linear time.
	Value string `json:"value" maxLength:"256"`
	// Not turns the test round: the header does not equal, contain or match.
	Not bool `json:"not,omitempty"`
}

// How a group joins what it holds.
const (
	And = "and" // every condition and group holds
	Or  = "or"  // one of them does
)

// Group joins conditions, and groups of conditions one level down: (A or B) and C is an and
// group of C and an or group of A and B. A group with nothing in it holds for every request.
type Group struct {
	Op     string  `json:"op" enum:"and,or"`
	Conds  []Cond  `json:"conds" maxItems:"20"`
	Groups []Inner `json:"groups" maxItems:"8"`
}

// Inner is a group inside a group: conditions only.
type Inner struct {
	Op    string `json:"op" enum:"and,or"`
	Conds []Cond `json:"conds" minItems:"1" maxItems:"20"`
}

// What a rule does with the request it matches.
const (
	RespondServe = "serve" // the profile, as without a rule, with what the rule changes
	RespondStub  = "stub"  // one placeholder entry that says the app is not supported
	Respond403   = "403"
	Respond404   = "404"
	RespondHTML  = "html" // the admin's own page
	RespondDrop  = "drop" // the connection is closed without a word
)

// Header is a response header a rule adds.
type Header struct {
	Name  string `json:"name" minLength:"1" maxLength:"64"`
	Value string `json:"value" maxLength:"512"`
}

// Action is what a rule does. The fields a Respond does not use are left empty (Clean).
type Action struct {
	// Respond is empty for serve.
	Respond string `json:"respond" enum:"serve,stub,403,404,html,drop"`
	// Format forces the profile's format (serve and stub): the app gets it whatever its
	// User-Agent and ?format= say. Empty: as usual.
	Format string `json:"format,omitempty" maxLength:"16"`
	// Headers are added to the answer, over the panel's own (serve, stub, 403, 404, html).
	Headers []Header `json:"headers,omitempty" maxItems:"10"`
	// SkipHWID gives a device that sends no id keys all the same, whatever the panel
	// requires of devices (serve).
	SkipHWID bool `json:"skip_hwid,omitempty"`
	// Text is the words of the stub's entry (stub); empty: the built-in ones.
	Text string `json:"text,omitempty" maxLength:"200"`
	// HTML is the page (html), shown as it is, with no scripts: the answer carries a
	// policy that forbids them, so a page cannot be turned against the subscriber.
	HTML string `json:"html,omitempty" maxLength:"16384"`
	// Template is the subscription template the profile is made from (serve), by its id: a
	// template of the core of the format the app gets; 0, or one of another core, leaves the
	// core's default (serve.Template). Check refuses one that does not exist
	// (CheckOpts.Template).
	Template int64 `json:"template,omitempty" minimum:"0"`
}

// Rule is one response rule.
type Rule struct {
	Name    string `json:"name" minLength:"1" maxLength:"80"`
	Enabled bool   `json:"enabled"`
	When    Group  `json:"when"`
	Do      Action `json:"do"`
}

// Limits of what the admin may write: every rule is run for every fetch of every app.
const (
	MaxRules  = 100
	MaxConds  = 20  // in one group, and in one rule through its groups
	MaxGroups = 8   // inner groups of one rule
	MaxValue  = 256 // a condition's value, a regular expression's source
	// MaxRegexProgram bounds the compiled program of one regular expression: a short source
	// like "[a-z]{1000}" compiles to thousands of instructions, and the rules (and the
	// tester, with a draft of them) are compiled again and again.
	MaxRegexProgram = 1000
	MaxName         = 80
	MaxHeaders      = 10
	MaxHTML         = 16 << 10
	MaxText         = 200
	// MaxHeaderValue is how much of a request header a condition reads: a client may send
	// a megabyte, and the rules must not be a way to make the panel read it again and again.
	MaxHeaderValue = 2048
)

// CheckOpts tells Check what only its caller knows.
type CheckOpts struct {
	// Formats are the formats a rule may force (serve.Formats).
	Formats []string
	// Template says whether a template exists; nil: none does, so a rule may name none.
	Template func(id int64) bool
}

// CheckError is the first thing wrong with the rules: where, and a code the admin panel
// turns into words.
type CheckError struct {
	Path   string // "rules.2.when.conds.0.value"
	Code   string
	Detail string // the value or the compiler's words
}

func (e *CheckError) Error() string { return e.Path + ": " + e.Code }

func bad(path, code, detail string) error { return &CheckError{Path: path, Code: code, Detail: detail} }

// CheckRules checks the admin's rules as a list, as they are written. A rule's name is what
// the tester and the log say decided, next to the panel's own labels (RuleApps, RuleSSR,
// RuleOld): a rule may not take one of them, or a builtin:* one the panel may add later.
// Check leaves names alone, so a rule saved before still loads and runs.
func CheckRules(rules []Rule, opts CheckOpts) error {
	if len(rules) > MaxRules {
		return bad("rules", "resp_rules_many", strconv.Itoa(MaxRules))
	}
	for i, r := range rules {
		path := "rules." + strconv.Itoa(i)
		if err := r.Check(path, opts); err != nil {
			return err
		}
		if name := strings.TrimSpace(r.Name); name == RuleApps || strings.HasPrefix(name, "builtin:") {
			return bad(path+".name", "resp_name_reserved", name)
		}
	}
	return nil
}

var headerName = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]{1,64}$")

// framing headers carry how the body is delimited and how the connection lives: set by a
// rule they would break the answer, or the connection under it.
var framing = map[string]bool{
	"Content-Length": true, "Transfer-Encoding": true, "Connection": true, "Keep-Alive": true, "Upgrade": true,
	"Te": true, "Trailer": true, "Proxy-Connection": true, "Proxy-Authenticate": true, "Content-Encoding": true,
}

// Check says what is wrong with the rule at path, or nil. A rule that passes compiles.
func (r Rule) Check(path string, opts CheckOpts) error {
	if n := utf8.RuneCountInString(strings.TrimSpace(r.Name)); n == 0 || n > MaxName || hasControl(r.Name) {
		return bad(path+".name", "resp_name", r.Name)
	}
	total := len(r.When.Conds)
	if err := checkGroup(path+".when", r.When.Op, r.When.Conds, opts, true); err != nil {
		return err
	}
	if len(r.When.Groups) > MaxGroups {
		return bad(path+".when.groups", "resp_groups", strconv.Itoa(MaxGroups))
	}
	for i, g := range r.When.Groups {
		total += len(g.Conds)
		if err := checkGroup(path+".when.groups."+strconv.Itoa(i), g.Op, g.Conds, opts, false); err != nil {
			return err
		}
	}
	if total > MaxConds {
		return bad(path+".when", "resp_conds", strconv.Itoa(MaxConds))
	}
	return r.Do.check(path+".do", opts)
}

func checkGroup(path, op string, conds []Cond, opts CheckOpts, outer bool) error {
	if op != And && op != Or {
		return bad(path+".op", "resp_group_op", op)
	}
	if len(conds) > MaxConds {
		return bad(path+".conds", "resp_conds", strconv.Itoa(MaxConds))
	}
	if !outer && len(conds) == 0 {
		return bad(path+".conds", "resp_group_empty", "")
	}
	for i, c := range conds {
		if err := c.check(path + ".conds." + strconv.Itoa(i)); err != nil {
			return err
		}
	}
	return nil
}

func (c Cond) check(path string) error {
	if !headerName.MatchString(c.Header) {
		return bad(path+".header", "resp_header", c.Header)
	}
	if len(c.Value) > MaxValue || strings.ContainsAny(c.Value, "\r\n\x00") {
		return bad(path+".value", "resp_value", "")
	}
	switch c.Op {
	case OpEquals:
	case OpContains:
		if c.Value == "" {
			return bad(path+".value", "resp_value_empty", "")
		}
	case OpRegex:
		if c.Value == "" {
			return bad(path+".value", "resp_value_empty", "")
		}
		if _, err := regexp.Compile(c.Value); err != nil {
			return bad(path+".value", "resp_regex", err.Error())
		}
		// regexp compiles with the Perl flags; the same parse tells the program's size.
		re, err := syntax.Parse(c.Value, syntax.Perl)
		if err != nil {
			return bad(path+".value", "resp_regex", err.Error())
		}
		if prog, err := syntax.Compile(re.Simplify()); err != nil || len(prog.Inst) > MaxRegexProgram {
			return bad(path+".value", "resp_regex_big", "")
		}
	default:
		return bad(path+".op", "resp_op", c.Op)
	}
	return nil
}

func (a Action) check(path string, opts CheckOpts) error {
	switch a.Respond {
	case "", RespondServe, RespondStub, Respond403, Respond404, RespondHTML, RespondDrop:
	default:
		return bad(path+".respond", "resp_respond", a.Respond)
	}
	if a.takes(RespondServe, RespondStub) && a.Format != "" {
		ok := false
		for _, f := range opts.Formats {
			ok = ok || f == a.Format
		}
		if !ok {
			return bad(path+".format", "resp_format", a.Format)
		}
	}
	if a.takes(RespondServe) && a.Template != 0 && (opts.Template == nil || !opts.Template(a.Template)) {
		return bad(path+".template", "resp_template", strconv.FormatInt(a.Template, 10))
	}
	if a.takes(RespondStub) && (utf8.RuneCountInString(a.Text) > MaxText || hasControl(a.Text)) {
		return bad(path+".text", "resp_text", "")
	}
	if a.takes(RespondHTML) && (strings.TrimSpace(a.HTML) == "" || utf8.RuneCountInString(a.HTML) > MaxHTML) {
		return bad(path+".html", "resp_html", "")
	}
	if !a.takes(RespondServe, RespondStub, Respond403, Respond404, RespondHTML) {
		return nil
	}
	if len(a.Headers) > MaxHeaders {
		return bad(path+".headers", "resp_headers_many", strconv.Itoa(MaxHeaders))
	}
	for i, h := range a.Headers {
		at := path + ".headers." + strconv.Itoa(i)
		if !headerName.MatchString(h.Name) || framing[canonical(h.Name)] {
			return bad(at+".name", "resp_header_name", h.Name)
		}
		if len(h.Value) > 512 || !printable(h.Value) {
			return bad(at+".value", "resp_header_value", "")
		}
	}
	return nil
}

// takes says whether the action's Respond is one of these; an empty one is serve.
func (a Action) takes(responds ...string) bool {
	got := a.Respond
	if got == "" {
		got = RespondServe
	}
	for _, r := range responds {
		if r == got {
			return true
		}
	}
	return false
}

// Clean is the rule with what its Respond does not use left empty, so a rule that was
// changed from one answer to another keeps nothing of the old one.
func (r Rule) Clean() Rule {
	r.Name = strings.TrimSpace(r.Name)
	if r.When.Conds == nil {
		r.When.Conds = []Cond{}
	}
	if r.When.Groups == nil {
		r.When.Groups = []Inner{}
	}
	a := r.Do
	if a.Respond == "" {
		a.Respond = RespondServe
	}
	if !a.takes(RespondServe, RespondStub) {
		a.Format = ""
	}
	if !a.takes(RespondServe) {
		a.SkipHWID, a.Template = false, 0
	}
	if !a.takes(RespondStub) {
		a.Text = ""
	}
	if !a.takes(RespondHTML) {
		a.HTML = ""
	}
	if !a.takes(RespondServe, RespondStub, Respond403, Respond404, RespondHTML) {
		a.Headers = nil
	}
	if len(a.Headers) == 0 {
		a.Headers = nil
	}
	r.Do = a
	return r
}

func hasControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f }) >= 0
}

// printable: visible ASCII, spaces and tabs, what a header value may safely hold.
func printable(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c != '\t' && (c < ' ' || c > '~') {
			return false
		}
	}
	return true
}

// ValidHeaderName says whether s can name a header: what a condition and a rule's header
// may be called.
func ValidHeaderName(s string) bool { return headerName.MatchString(s) }
