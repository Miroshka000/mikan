package mihomo

import (
	"bytes"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"
)

// keyOrder is the order people read a profile in: in a proxy or a group its name and type
// first; the general settings in the order of base.yaml, DNS the last of them; then the
// proxies, groups and rules. mihomo does not care; keys not listed follow in alphabetical
// order. The one order is for the maps of every level, so moving a top-level key of
// base.yaml moves that key in every map, the DNS's "ipv6" among them.
var keyOrder = slices.Concat([]string{"name", "type", "server", "port", "enable"}, baseKeys, []string{"proxies", "proxy-groups", "rules"})

// embeddedYAML reads a mapping of the panel's own, and its keys in the order of the file.
func embeddedYAML(src []byte) (map[string]any, []string) {
	var n yaml.Node
	var m map[string]any
	if err := yaml.Unmarshal(src, &n); err != nil {
		panic(err)
	}
	if err := n.Decode(&m); err != nil {
		panic(err)
	}
	doc := n.Content[0]
	keys := make([]string, 0, len(doc.Content)/2)
	for i := 0; i < len(doc.Content); i += 2 {
		keys = append(keys, doc.Content[i].Value)
	}
	return m, keys
}

// cloneYAML is a copy of v, a decoded YAML value, that shares no map or list with it.
func cloneYAML(v any) any {
	switch v := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(v))
		for k, x := range v {
			m[k] = cloneYAML(x)
		}
		return m
	case []any:
		s := make([]any, len(v))
		for i, x := range v {
			s[i] = cloneYAML(x)
		}
		return s
	}
	return v
}

// marshalYAML writes a profile as YAML, which the apps show to people who open it; JSON
// worked for mihomo but read as a config of another program.
func marshalYAML(v any) ([]byte, error) {
	var n yaml.Node
	if err := n.Encode(v); err != nil {
		return nil, err
	}
	orderKeys(&n)
	var b bytes.Buffer
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	if err := e.Encode(&n); err != nil {
		return nil, err
	}
	if err := e.Close(); err != nil {
		return nil, err
	}
	return astralAsText(b.Bytes()), nil
}

// astralAsText writes the characters past U+FFFF of a YAML document as themselves: the
// encoder takes them all for unprintable and escapes them, so a flag emoji in a server's
// name read "\U0001F1F3\U0001F1F1" to whoever opened the profile. Only a \U escape is
// undone, one whose backslash is not itself escaped, and only to a character YAML may
// carry as it is; \x, \u (U+FEFF among them) and \\ stay. A \U the document has as text,
// outside a double-quoted string, would become a character too: then the document no
// longer reads the same, and it is kept escaped.
func astralAsText(doc []byte) []byte {
	if !bytes.Contains(doc, []byte(`\U`)) {
		return doc
	}
	out := make([]byte, 0, len(doc))
	for i := 0; i < len(doc); i++ {
		if doc[i] != '\\' {
			out = append(out, doc[i])
			continue
		}
		if r, ok := astralEscape(doc[i:]); ok {
			out = utf8.AppendRune(out, r)
			i += len(`\UXXXXXXXX`) - 1
			continue
		}
		// any other escape is copied whole, so the backslash it may escape starts none
		out = append(out, doc[i])
		if i+1 < len(doc) {
			i++
			out = append(out, doc[i])
		}
	}
	var was, is any
	if yaml.Unmarshal(doc, &was) != nil || yaml.Unmarshal(out, &is) != nil || !reflect.DeepEqual(was, is) {
		return doc
	}
	return out
}

// astralEscape is the character of the \UXXXXXXXX escape s starts with, if it is one past
// U+FFFF and not a noncharacter.
func astralEscape(s []byte) (rune, bool) {
	if len(s) < len(`\UXXXXXXXX`) || s[1] != 'U' {
		return 0, false
	}
	v, err := strconv.ParseUint(string(s[2:10]), 16, 32)
	r := rune(v)
	if err != nil || r <= 0xFFFF || !utf8.ValidRune(r) || r&0xFFFE == 0xFFFE {
		return 0, false
	}
	return r, true
}

func orderKeys(n *yaml.Node) {
	if n.Kind == yaml.MappingNode {
		pairs := make([][2]*yaml.Node, 0, len(n.Content)/2)
		for i := 0; i+1 < len(n.Content); i += 2 {
			pairs = append(pairs, [2]*yaml.Node{n.Content[i], n.Content[i+1]})
		}
		sort.SliceStable(pairs, func(i, j int) bool { return keyRank(pairs[i][0].Value) < keyRank(pairs[j][0].Value) })
		for i, p := range pairs {
			n.Content[2*i], n.Content[2*i+1] = p[0], p[1]
		}
	}
	for _, c := range n.Content {
		orderKeys(c)
	}
}

func keyRank(k string) int {
	if i := slices.Index(keyOrder, k); i >= 0 {
		return i
	}
	return len(keyOrder)
}
