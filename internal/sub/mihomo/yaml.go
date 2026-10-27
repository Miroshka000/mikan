package mihomo

import (
	"bytes"
	"slices"
	"sort"

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
	return b.Bytes(), nil
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
