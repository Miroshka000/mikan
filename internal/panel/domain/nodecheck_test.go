package domain

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mikan/internal/nodetls"
	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

// The rules of a node are one for the panel and the shell: a name is taken once and is no
// group, the host and the domain are valid and the domain leads to the host, and a change
// writes only what it names.
func TestNodeRules(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := Seed(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	panel, _ := nodetls.Generate("mikan-panel", x509.ExtKeyUsageClientAuth, now)
	records := map[string]string{"b.example.com": "198.51.100.20", "other.example.com": "198.51.100.7"}
	doh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ans := map[string]any{"Status": 3, "Answer": []any{}}
		if ip, ok := records[r.URL.Query().Get("name")]; ok {
			ans["Status"] = 0
			if r.URL.Query().Get("type") == "A" {
				ans["Answer"] = []any{map[string]any{"type": 1, "data": ip}}
			}
		}
		_ = json.NewEncoder(w).Encode(ans)
	}))
	defer doh.Close()
	rules := NodeRules{
		Name: func(name string) error {
			if strings.EqualFold(name, "VPN") {
				return errors.New("name_is_group")
			}
			return nil
		},
		DNS: &dnscheck.Checker{Resolvers: []string{doh.URL}, HTTP: http.DefaultClient},
	}
	add := func(name, host, dom string) (db.Node, error) {
		n, _, err := AddNode(ctx, st, panel, NodeInput{Name: name, Host: host, Domain: dom, APIPort: 40000, Rules: rules}, now)
		return n, err
	}

	b, err := add("B", "198.51.100.20", "b.example.com")
	if err != nil {
		t.Fatal(err)
	}
	var name *NameError
	for n, code := range map[string]string{"b": "name_in_use", "VPN": "name_is_group"} {
		if _, err := add(n, "198.51.100.30", ""); !errors.As(err, &name) || name.Code != code {
			t.Errorf("name %q: %v, want %s", n, err, code)
		}
	}
	// Both fields are named when both are wrong; the DNS is asked only about valid ones.
	if _, err := add("C", "198.51.100.30/24", "-c"); !errors.Is(err, ErrBadHost) || !errors.Is(err, ErrBadDomain) {
		t.Errorf("a bad host and domain: %v", err)
	}
	var dom *DomainError
	if _, err := add("C", "198.51.100.30", "other.example.com"); !errors.As(err, &dom) || dom.Code != "domain_elsewhere" || dom.Domain != "other.example.com" || dom.Value != "198.51.100.7 → 198.51.100.30" {
		t.Errorf("a domain that leads elsewhere: %v %+v", err, dom)
	}
	if _, err := add("C", "198.51.100.30", "nowhere.example.com"); !errors.As(err, &dom) || dom.Code != "domain_not_found" {
		t.Errorf("a domain with no records: %v", err)
	}
	if nodes, _ := st.Q.ListNodes(ctx); len(nodes) != 2 {
		t.Fatalf("a refused node was created: %d nodes", len(nodes))
	}

	str := func(s string) *string { return &s }
	yes, no := new(bool), new(bool)
	*yes = true
	update := func(p NodePatch) (db.Node, error) {
		p.Rules = rules
		return UpdateNode(ctx, st, b.ID, p, now.Add(time.Hour))
	}
	if _, err := update(NodePatch{Name: str("vpn")}); !errors.As(err, &name) || name.Code != "name_is_group" {
		t.Errorf("rename to a group: %v", err)
	}
	// A node keeps its own name; another node's it cannot take.
	c, err := add("C", "198.51.100.30", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateNode(ctx, st, c.ID, NodePatch{Name: str(" b "), Rules: rules}, now); !errors.As(err, &name) || name.Code != "name_in_use" {
		t.Errorf("rename to a taken name: %v", err)
	}
	if n, err := UpdateNode(ctx, st, c.ID, NodePatch{Name: str(" C "), Rules: rules}, now); err != nil || n.Name != "C" {
		t.Errorf("rename to itself: %+v %v", n, err)
	}

	// The domain follows the host: moving the node off the domain's address is refused.
	if _, err := update(NodePatch{Host: str("198.51.100.99")}); !errors.As(err, &dom) || dom.Code != "domain_elsewhere" {
		t.Errorf("a host the domain does not lead to: %v", err)
	}
	if _, err := update(NodePatch{Host: str("b..example.com")}); !errors.Is(err, ErrBadHost) {
		t.Errorf("a bad host: %v", err)
	}
	if _, err := update(NodePatch{Domain: str("b")}); !errors.Is(err, ErrBadDomain) {
		t.Errorf("a bad domain: %v", err)
	}
	got, err := update(NodePatch{Host: str(" 198.51.100.21 "), Domain: str("")})
	if err != nil || got.PublicHost != "198.51.100.21" || got.Address != "198.51.100.21:40000" || got.Domain != "" || got.Name != "B" {
		t.Fatalf("moved: %+v %v", got, err)
	}

	// Only what the patch names is written: the shaping needs its channel, and a refused
	// patch writes nothing.
	if _, err := update(NodePatch{FairShare: yes}); !errors.Is(err, ErrChannelRequired) {
		t.Errorf("fair share without a channel: %v", err)
	}
	mbps := int64(500)
	got, err = update(NodePatch{FairShare: yes, ChannelMbps: &mbps, Enabled: no, PublicName: str(" Public ")})
	if err != nil || got.FairShare != 1 || got.ChannelMbps.Int64 != 500 || got.Enabled != 0 || got.PublicName != "Public" || got.PublicHost != "198.51.100.21" {
		t.Fatalf("shaping: %+v %v", got, err)
	}

	// The panel's own node follows the panel's address.
	if _, err := UpdateNode(ctx, st, 1, NodePatch{Host: str("198.51.100.5"), Rules: rules}, now); !errors.Is(err, ErrLocalNode) {
		t.Errorf("host of the local node: %v", err)
	}
	if _, err := UpdateNode(ctx, st, 1, NodePatch{Name: str("Home"), Rules: rules}, now); err != nil {
		t.Errorf("rename of the local node: %v", err)
	}
	if _, err := UpdateNode(ctx, st, 999, NodePatch{Rules: rules}, now); !errors.Is(err, ErrUnknownNode) {
		t.Errorf("an unknown node: %v", err)
	}
}
