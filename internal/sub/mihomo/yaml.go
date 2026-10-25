package mihomo

import (
	"bytes"
	"slices"
	"sort"

	"go.yaml.in/yaml/v3"
)

// keyOrder is the order people read a profile in: the general settings, DNS, then the
// proxies, groups and rules, and in a proxy or a group its name and type first. mihomo
// does not care; keys not listed follow in alphabetical order.
var keyOrder = []string{
	"name", "type", "server", "port", "enable",
	"mixed-port", "allow-lan", "mode", "log-level", "ipv6", "unified-delay", "tcp-concurrent",
	"geodata-mode", "geox-url", "dns", "proxies", "proxy-groups", "rules",
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
