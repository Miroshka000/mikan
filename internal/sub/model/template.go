package model

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
)

// Core is a core the apps run, as a subscription template is written for it.
type Core string

const (
	CoreMihomo  Core = "mihomo"  // the Clash profile, YAML (mihomo.Template)
	CoreSingBox Core = "singbox" // the sing-box config, JSON (singbox.Template)
	CoreXray    Core = "xray"    // the list of xray configs, JSON (xray.Template)
)

// Cores are the cores a template is written for, in the order the admin panel lists them.
var Cores = []Core{CoreMihomo, CoreSingBox, CoreXray}

// Known says whether c is one of Cores.
func (c Core) Known() bool { return c == CoreMihomo || c == CoreSingBox || c == CoreXray }

// Template is an admin's own profile for one core, a subscription template: the core's
// config without the servers, which the panel puts the user's servers into. The zero
// Template is none: the built-in profile.
type Template struct {
	ID   int64
	Core Core
	Body string
	// Default: the core's apps get it when no response rule names another.
	Default bool
}

// TemplateError is a template that would break the profiles: a code for the admin panel
// and what it is about.
type TemplateError struct {
	Code   string
	Detail string
}

func (e *TemplateError) Error() string { return e.Code + ": " + e.Detail }

// TemplateErr is the *TemplateError of code and detail.
func TemplateErr(code, detail string) error { return &TemplateError{Code: code, Detail: detail} }

// Ask is what a group of a JSON template asks of the user's servers (its "mikan"): every
// server, narrowed to the nodes named (by the name the panel shows, or the id), to the
// protocols named (vless, hysteria2, ss or shadowsocks…) and to the names Filter matches. A
// list left empty narrows nothing.
type Ask struct {
	Nodes  []string `json:"nodes,omitempty"`
	Types  []string `json:"types,omitempty"`
	Filter string   `json:"filter,omitempty"`
}

// Take are the proxies of ps the ask takes, in their order; an error when Filter is no
// regular expression.
func (a Ask) Take(ps []Proxy, nodes []Node) ([]Proxy, error) {
	var re *regexp.Regexp
	if a.Filter != "" {
		var err error
		if re, err = regexp.Compile(a.Filter); err != nil {
			return nil, err
		}
	}
	wantNode := map[int64]bool{}
	for _, v := range a.Nodes {
		for _, n := range nodes {
			if v == n.Name || v == strconv.FormatInt(n.ID, 10) {
				wantNode[n.ID] = true
			}
		}
	}
	wantType := map[string]bool{}
	for _, v := range a.Types {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "ss" {
			v = "shadowsocks" // mihomo's name of it
		}
		wantType[v] = true
	}
	var out []Proxy
	for _, x := range ps {
		if len(a.Nodes) > 0 && !wantNode[x.Node] || len(wantType) > 0 && !wantType[strings.ToLower(x.Type)] || re != nil && !re.MatchString(x.Name) {
			continue
		}
		out = append(out, x)
	}
	return out, nil
}

// ReadAsk reads the "mikan" of a group of a JSON template: an object of nodes, types (each a
// list, or one word) and filter. more takes a key of a core's own and says whether it is
// good; false for anything else, and for a key nobody knows: a typo must not widen a group
// to every server.
func ReadAsk(v any, more func(key string, v any) bool) (Ask, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return Ask{}, false
	}
	var a Ask
	for k, x := range m {
		switch k {
		case "nodes":
			a.Nodes, ok = words(x)
		case "types":
			a.Types, ok = words(x)
		case "filter":
			a.Filter, ok = x.(string)
		default:
			ok = more != nil && more(k, x)
		}
		if !ok {
			return Ask{}, false
		}
	}
	return a, true
}

// words are a list of words and numbers (a node's id), or one of them.
func words(v any) ([]string, bool) {
	switch x := v.(type) {
	case string:
		return []string{x}, true
	case json.Number:
		return []string{x.String()}, true
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			w, ok := words(e)
			if !ok || len(w) != 1 {
				return nil, false
			}
			out = append(out, w[0])
		}
		return out, true
	}
	return nil, false
}

// ParseJSONTemplate reads a JSON template: one object, its numbers as they are written
// (json.Number). template_json with the line of what does not parse, template_object for
// JSON of another kind, template_empty for nothing.
func ParseJSONTemplate(src string) (map[string]any, error) {
	if strings.TrimSpace(src) == "" {
		return nil, TemplateErr("template_empty", "")
	}
	dec := json.NewDecoder(strings.NewReader(src))
	dec.UseNumber()
	var v any
	err := dec.Decode(&v)
	if err == nil {
		// One object and nothing after it: a second one would be dropped unseen.
		if _, next := dec.Token(); next != io.EOF {
			err = &json.SyntaxError{Offset: dec.InputOffset()}
		}
	}
	if err != nil {
		var se *json.SyntaxError
		offset := int64(len(src))
		if errors.As(err, &se) {
			offset = se.Offset
		}
		return nil, TemplateErr("template_json", strconv.Itoa(strings.Count(src[:min(int(offset), len(src))], "\n")+1))
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, TemplateErr("template_object", "")
	}
	return m, nil
}
