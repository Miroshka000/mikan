package mihomo

import (
	"reflect"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func TestAstralAsText(t *testing.T) {
	for _, c := range []struct{ doc, want string }{
		{`name: "\U0001F1F3\U0001F1F1 VLESS"` + "\n", `name: "🇳🇱 VLESS"` + "\n"},
		{`name: "\U0001f1f3x"` + "\n", `name: "🇳x"` + "\n"},
		// a backslash the document has as text, then U and hex digits
		{`name: "\\U0001F1F3"` + "\n", `name: "\\U0001F1F3"` + "\n"},
		{`name: "\\\U0001F1F3"` + "\n", `name: "\\🇳"` + "\n"},
		{`name: "\\U0001F1F3 \U0001F1F3"` + "\n", `name: "\\U0001F1F3 🇳"` + "\n"},
		{`name: "\\\\U0001F1F3"` + "\n", `name: "\\\\U0001F1F3"` + "\n"},
		// what YAML wants escaped stays so
		{`name: "\uFEFF\x01\U0001FFFE\U0010FFFF"` + "\n", `name: "\uFEFF\x01\U0001FFFE\U0010FFFF"` + "\n"},
		{`name: "\U0000E000"` + "\n", `name: "\U0000E000"` + "\n"},
		{`name: "\U0001F1F"` + "\n", `name: "\U0001F1F"` + "\n"},
		// \U outside a double-quoted string is text: the document is kept as it is
		{`path: C:\U0001F1F3` + "\n" + `name: "\U0001F1F3"` + "\n", `path: C:\U0001F1F3` + "\n" + `name: "\U0001F1F3"` + "\n"},
		{`path: 'C:\U0001F1F3'` + "\n", `path: 'C:\U0001F1F3'` + "\n"},
	} {
		if got := string(astralAsText([]byte(c.doc))); got != c.want {
			t.Errorf("%s: %s, want %s", c.doc, got, c.want)
		}
	}
}

// Whatever a name holds, the profile reads it back as it was, and an emoji in it is written
// as itself.
func TestMarshalYAMLNames(t *testing.T) {
	for _, c := range []struct {
		names  []string
		asText bool // the emoji is written as itself
	}{
		{[]string{"🇳🇱 VLESS", "🇺🇸 Hysteria2", "plain"}, true},
		{[]string{"🇳🇱 VLESS", `🇳🇱 back\slash \U0001F1F3`, "quote \" 🇺🇸", "\ufeffbom", "tab\tand\x01"}, true},
		{[]string{"🇳🇱 " + strings.Repeat("long name ", 12)}, true},
		// a plain C:\U0001F1F3 would turn into C:🇳: the profile keeps its escapes
		{[]string{"🇳🇱 VLESS", `C:\U0001F1F3`}, false},
	} {
		in := map[string]any{"proxies": []any{}}
		for _, n := range c.names {
			in["proxies"] = append(in["proxies"].([]any), map[string]any{"name": n})
		}
		raw, err := marshalYAML(in)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := yaml.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(out, in) {
			t.Errorf("read back\n%v\nwant\n%v\nfrom\n%s", out, in, raw)
		}
		if strings.Contains(string(raw), "🇳🇱") != c.asText {
			t.Errorf("emoji as text %v, want %v:\n%s", !c.asText, c.asText, raw)
		}
	}
}
