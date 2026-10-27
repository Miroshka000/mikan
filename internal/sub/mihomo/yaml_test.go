package mihomo

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

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
		{`name: "\U0001F1G3"` + "\n", `name: "\U0001F1G3"` + "\n"},
	} {
		if got := string(astralAsText([]byte(c.doc))); got != c.want {
			t.Errorf("%s: %s, want %s", c.doc, got, c.want)
		}
	}
}

// marshalsBack is the document marshalYAML writes for in, failed unless it reads back as in.
func marshalsBack(t *testing.T, in map[string]any) string {
	t.Helper()
	raw, err := marshalYAML(in)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := yaml.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%v\n%s", err, raw)
	}
	if !reflect.DeepEqual(out, in) {
		t.Fatalf("read back\n%q\nwant\n%q\nfrom\n%s", out, in, raw)
	}
	return string(raw)
}

func proxiesNamed(names ...string) map[string]any {
	ps := []any{}
	for _, n := range names {
		ps = append(ps, map[string]any{"name": n})
	}
	return map[string]any{"proxies": ps}
}

// Whatever a name holds, the profile reads it back as it was, and an emoji in it is written
// as itself.
func TestMarshalYAMLNames(t *testing.T) {
	for _, c := range []struct {
		in     map[string]any
		asText bool // the emoji is written as itself
	}{
		{proxiesNamed("🇳🇱 VLESS", "🇺🇸 Hysteria2", "plain"), true},
		{proxiesNamed("🇳🇱 VLESS", `back\slash\🇺🇸`, "quote \" 🇺🇸", "\ufeffbom", "tab\tand\x01"), true},
		{proxiesNamed("🇳🇱 " + strings.Repeat("long name ", 12)), true},
		// a plain C:\U0001F1F3 would turn into C:🇳: the profile keeps its escapes
		{proxiesNamed("🇳🇱 VLESS", `C:\U0001F1F3`), false},
		{map[string]any{"proxies": []any{map[string]any{"name": "🇳🇱 VLESS"}}, `C:\U0001F1F3`: "x"}, false},
	} {
		if raw := marshalsBack(t, c.in); strings.Contains(raw, "🇳🇱") != c.asText {
			t.Errorf("emoji as text %v, want %v:\n%s", !c.asText, c.asText, raw)
		}
	}
}

// Any name, and any key, reads back as it was.
func FuzzMarshalYAMLNames(f *testing.F) {
	for _, s := range []string{"🇳🇱 VLESS", `C:\U0001F1F3`, `\\U0001F1F3`, `\🇳🇱`, `\\🇳🇱`, `\U0001F1F3`, "\ufeff🇳🇱", "a\\\n🇳🇱 \"x\"", "'🇳🇱'", "- 🇳🇱: x", "🇳🇱" + strings.Repeat(` \`, 50)} {
		f.Add(s, "🇺🇸 США")
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		if !utf8.ValidString(a) || !utf8.ValidString(b) {
			t.Skip() // the encoder writes such a string as !!binary
		}
		in := proxiesNamed(a, b)
		in["k"+a] = b
		var enc map[string]any
		if raw, _, err := encodeYAML(in); err != nil || yaml.Unmarshal(raw, &enc) != nil || !reflect.DeepEqual(enc, in) {
			t.Skip() // the encoder's own: it writes a "\nА" in a list as a block it cannot read back
		}
		marshalsBack(t, in)
	})
}
