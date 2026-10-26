package model

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
)

// A group's ask narrows the user's servers by node (name or id), by protocol (mihomo's ss
// is shadowsocks) and by name; what it leaves empty narrows nothing.
func TestAskTake(t *testing.T) {
	nodes := []Node{{ID: 1, Name: "🇳🇱 Нидерланды"}, {ID: 2, Name: "🇺🇸 США"}}
	ps := []Proxy{
		{Name: "🇳🇱 VLESS", Node: 1, Type: "vless"},
		{Name: "🇳🇱 Shadowsocks", Node: 1, Type: "shadowsocks"},
		{Name: "🇺🇸 VLESS", Node: 2, Type: "vless"},
		{Name: "🇺🇸 Hysteria2", Node: 2, Type: "hysteria2"},
	}
	names := func(a Ask) []string {
		t.Helper()
		got, err := a.Take(ps, nodes)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, x := range got {
			out = append(out, x.Name)
		}
		return out
	}
	for _, c := range []struct {
		name string
		ask  Ask
		want []string
	}{
		{"every server", Ask{}, []string{"🇳🇱 VLESS", "🇳🇱 Shadowsocks", "🇺🇸 VLESS", "🇺🇸 Hysteria2"}},
		{"a node by name", Ask{Nodes: []string{"🇺🇸 США"}}, []string{"🇺🇸 VLESS", "🇺🇸 Hysteria2"}},
		{"a node by id", Ask{Nodes: []string{"1"}}, []string{"🇳🇱 VLESS", "🇳🇱 Shadowsocks"}},
		{"a node nobody has", Ask{Nodes: []string{"🇩🇪 Германия"}}, nil},
		{"ss is shadowsocks", Ask{Types: []string{"SS"}}, []string{"🇳🇱 Shadowsocks"}},
		{"node and type", Ask{Nodes: []string{"2"}, Types: []string{"vless"}}, []string{"🇺🇸 VLESS"}},
		{"by name", Ask{Filter: "Hysteria|Shadow"}, []string{"🇳🇱 Shadowsocks", "🇺🇸 Hysteria2"}},
	} {
		if got := names(c.ask); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	if _, err := (Ask{Filter: "("}).Take(ps, nodes); err == nil {
		t.Fatal("a filter that is no regular expression is taken")
	}
}

// A JSON template is one object: what does not parse is named by its line, and so is a
// second value after it.
func TestParseJSONTemplate(t *testing.T) {
	for _, c := range []struct {
		src, code, detail string
	}{
		{"  ", "template_empty", ""},
		{"{\n  \"log\": {},\n  \"dns\" {}\n}", "template_json", "3"},
		{"{}\n{}", "template_json", "2"},
		{"[1]", "template_object", ""},
		{"{\"a\":", "template_json", "1"},
	} {
		_, err := ParseJSONTemplate(c.src)
		var te *TemplateError
		if !errors.As(err, &te) || te.Code != c.code || te.Detail != c.detail {
			t.Errorf("%q: %v, want %s %s", c.src, err, c.code, c.detail)
		}
	}
	m, err := ParseJSONTemplate(`{"port": 10808, "ratio": 1.50}`)
	if err != nil || m["port"] != json.Number("10808") || m["ratio"] != json.Number("1.50") {
		t.Fatalf("numbers as written: %v %v", m, err)
	}
}

// A group's "mikan" is read whole: a key nobody knows refuses it, so that a typo does not
// widen the group to every server.
func TestReadAsk(t *testing.T) {
	a, ok := ReadAsk(map[string]any{"nodes": []any{"🇺🇸 США", json.Number("2")}, "types": "vless", "filter": "🇺🇸"}, nil)
	if !ok || !slices.Equal(a.Nodes, []string{"🇺🇸 США", "2"}) || !slices.Equal(a.Types, []string{"vless"}) || a.Filter != "🇺🇸" {
		t.Fatalf("%+v %v", a, ok)
	}
	if _, ok := ReadAsk(map[string]any{"node": "1"}, nil); ok {
		t.Fatal("a typo is taken")
	}
	if _, ok := ReadAsk("all", nil); ok {
		t.Fatal("no object is taken")
	}
	choice := false
	more := func(k string, v any) bool {
		b, isBool := v.(bool)
		choice = b
		return k == "choice" && isBool
	}
	if _, ok := ReadAsk(map[string]any{"choice": true}, more); !ok || !choice {
		t.Fatal("a key of the core's own is not handed over")
	}
}
