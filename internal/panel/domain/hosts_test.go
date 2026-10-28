package domain

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
	"mikan/internal/sub/build"
	"mikan/internal/sub/model"
)

// One truth while subscriptions read the template: whichever way the template changes (the
// inbound's form, its YAML, the default host itself), its mikan.client and the default host
// say the same, and the subscription's proxy is made of that value.
func TestDefaultHostIsTheTemplates(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, s := inbounds(t, &now, nil)
	ctx := context.Background()
	same := func(when string, id int64) db.InboundHost {
		t.Helper()
		in, err := st.Q.GetInbound(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		hosts, err := s.Hosts(ctx, id)
		if err != nil || len(hosts) == 0 || hosts[0].Position != 0 {
			t.Fatalf("%s: hosts %+v %v", when, hosts, err)
		}
		w := store.DefaultHost(id, in.Config)
		got := db.SetDefaultInboundHostParams{InboundID: hosts[0].InboundID, Address: hosts[0].Address, Port: hosts[0].Port, Sni: hosts[0].Sni, Host: hosts[0].Host, Fingerprint: hosts[0].Fingerprint}
		if got != w {
			t.Fatalf("%s: default host %+v, template %+v", when, got, w)
		}
		return hosts[0]
	}

	// The seeded inbounds have theirs.
	all, err := st.Q.ListInbounds(ctx)
	if err != nil || len(all) == 0 {
		t.Fatal(all, err)
	}
	for _, in := range all {
		same("seeded "+in.Name, in.ID)
	}

	in, err := s.Create(ctx, NewInbound{Preset: "vless_tls_ws"})
	if err != nil {
		t.Fatal(err)
	}
	if h := same("created", in.ID); h.Address != "" || h.Port != 0 {
		t.Fatalf("a new inbound's default host: %+v", h)
	}
	if _, _, err := s.Update(ctx, in.ID, InboundPatch{Client: &ClientEndpoint{Server: "proxy.example.com", Port: 8443, SNI: "front.example.com", Host: "h.example.com"}}); err != nil {
		t.Fatal(err)
	}
	if h := same("the form's client", in.ID); h.Address != "proxy.example.com" || h.Port != 8443 || h.Sni != "front.example.com" || h.Host != "h.example.com" {
		t.Fatalf("after the form: %+v", h)
	}
	fp := "firefox"
	if _, _, err := s.Update(ctx, in.ID, InboundPatch{Fingerprint: &fp}); err != nil {
		t.Fatal(err)
	}
	if h := same("the form's fingerprint", in.ID); h.Fingerprint != "firefox" {
		t.Fatalf("after the fingerprint: %+v", h)
	}
	cur, _ := st.Q.GetInbound(ctx, in.ID)
	yaml := strings.Replace(cur.Config, "proxy.example.com", "yaml.example.com", 1)
	if yaml == cur.Config {
		t.Fatalf("no address in %q", cur.Config)
	}
	if _, _, err := s.Update(ctx, in.ID, InboundPatch{Config: &yaml}); err != nil {
		t.Fatal(err)
	}
	def := same("the YAML", in.ID)
	if def.Address != "yaml.example.com" {
		t.Fatalf("after the YAML: %+v", def)
	}

	// The default host changed through the hosts goes into the template, and the proxy is
	// made of it.
	if _, err := s.UpdateHost(ctx, in.ID, def.ID, Host{Address: " edge.example.com ", Port: 2053, SNI: "edge.example.com", Fingerprint: "safari"}); err != nil {
		t.Fatal(err)
	}
	def = same("the default host", in.ID)
	if def.Address != "edge.example.com" || def.Port != 2053 || def.Host != "" || def.Fingerprint != "safari" {
		t.Fatalf("after the default host: %+v", def)
	}
	cur, _ = st.Q.GetInbound(ctx, in.ID)
	ps := build.Proxies(build.Profile{Keys: proto.Slot{Name: "u", UUID: "00000000-0000-0000-0000-000000000001", Secret: "s"},
		Inbounds: []model.Inbound{{ID: cur.ID, Node: 1, Name: "ws", Port: 443, PortSpec: cur.Port, Config: cur.Config}},
		Nodes:    []model.Node{{ID: 1, Name: "n", Endpoint: model.Endpoint{Host: "198.51.100.1"}}}})
	if len(ps) != 1 || ps[0].Server != "edge.example.com" || ps[0].Port != 2053 || ps[0].TLS == nil || ps[0].TLS.SNI != "edge.example.com" || ps[0].TLS.Fingerprint != "safari" {
		t.Fatalf("the proxy of the default host: %+v", ps)
	}

	// A refusal of the template is named by the host's field, and changes nothing.
	reality, err := s.Create(ctx, NewInbound{Preset: "trojan_reality"})
	if err != nil {
		t.Fatal(err)
	}
	rdef := same("reality", reality.ID)
	var edit *EditError
	if _, err := s.UpdateHost(ctx, reality.ID, rdef.ID, Host{SNI: "other.example.com"}); !errors.As(err, &edit) || edit.Field != "sni" || edit.Err.Code != "config_client_sni" {
		t.Fatalf("REALITY's default host with an SNI: %v", err)
	}
	same("reality after the refusal", reality.ID)
}

// The other hosts follow the default one in the order they came, are held to what the
// inbound takes, and a copy of the inbound takes them; the default host stays.
func TestOtherHosts(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	_, s := inbounds(t, &now, nil)
	ctx := context.Background()
	in, err := s.Create(ctx, NewInbound{Preset: "trojan_reality"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.AddHost(ctx, in.ID, Host{Address: "a.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.AddHost(ctx, in.ID, Host{Address: "b.example.com", Port: 8443, Fingerprint: "ios"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Position != 1 || b.Position != 2 {
		t.Fatalf("positions %d %d", a.Position, b.Position)
	}
	var edit *EditError
	for _, c := range []struct {
		h     Host
		field string
	}{
		{Host{SNI: "x.example.com"}, "sni"}, // REALITY's name is the target's
		{Host{Host: "x.example.com"}, "host"},
		{Host{Address: "bad host"}, "address"},
		{Host{Port: 70000}, "port"},
		{Host{Fingerprint: "Not One"}, "fingerprint"},
	} {
		if _, err := s.AddHost(ctx, in.ID, c.h); !errors.As(err, &edit) || edit.Field != c.field {
			t.Errorf("%+v: %v, want a refusal of %s", c.h, err, c.field)
		}
	}
	if _, err := s.AddHost(ctx, 999, Host{}); !errors.Is(err, ErrUnknownInbound) {
		t.Fatalf("a host of no inbound: %v", err)
	}
	if got, err := s.UpdateHost(ctx, in.ID, a.ID, Host{Address: "a2.example.com", Port: 2083}); err != nil || got.Address != "a2.example.com" || got.Port != 2083 || got.Position != 1 {
		t.Fatalf("update: %+v %v", got, err)
	}
	if _, err := s.UpdateHost(ctx, in.ID, 999, Host{}); !errors.Is(err, ErrUnknownHost) {
		t.Fatalf("an unknown host: %v", err)
	}

	copied, err := s.Copy(ctx, in.ID, CopyInbound{})
	if err != nil {
		t.Fatal(err)
	}
	hosts, err := s.Hosts(ctx, copied.ID)
	if err != nil || len(hosts) != 3 || hosts[1].Address != "a2.example.com" || hosts[2].Address != "b.example.com" || hosts[2].Fingerprint != "ios" {
		t.Fatalf("the copy's hosts: %+v %v", hosts, err)
	}

	hosts, _ = s.Hosts(ctx, in.ID)
	if _, err := s.DeleteHost(ctx, in.ID, hosts[0].ID); !errors.Is(err, ErrDefaultHost) {
		t.Fatalf("deleting the default host: %v", err)
	}
	if _, err := s.DeleteHost(ctx, in.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	// A host is its inbound's: another inbound's id does not reach it.
	if _, err := s.DeleteHost(ctx, copied.ID, b.ID); !errors.Is(err, ErrUnknownHost) {
		t.Fatalf("another inbound's host: %v", err)
	}
	c, err := s.AddHost(ctx, in.ID, Host{Address: "c.example.com"})
	if err != nil || c.Position != 3 {
		t.Fatalf("a host after a deleted one: %+v %v", c, err)
	}
	hosts, _ = s.Hosts(ctx, in.ID)
	var order []string
	for _, h := range hosts {
		order = append(order, h.Address)
	}
	if strings.Join(order, ",") != ",b.example.com,c.example.com" {
		t.Fatalf("order %q", order)
	}
}
