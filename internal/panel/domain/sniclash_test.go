package domain

import (
	"slices"
	"testing"

	"mikan/internal/panel/presets"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// routedInbound is an inbound of a preset behind the node's router, asking for names.
func routedInbound(t *testing.T, id int64, name, preset string, names ...string) db.Inbound {
	t.Helper()
	cfg, err := presets.NewConfig(preset, "www.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := proto.Parse(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.SetSNI443(tpl, true); err != nil {
		t.Fatal(err)
	}
	if rc, ok := tpl["reality-config"].(map[string]any); ok {
		rc["server-names"] = names
	} else if err := proto.SetClientEndpoint(tpl, "", 0, names[0], ""); err != nil {
		t.Fatal(err)
	}
	return db.Inbound{ID: id, Name: name, Preset: preset, Port: "1000", Enabled: 1, Config: string(tpl.JSON())}
}

func TestSNIClashes(t *testing.T) {
	xhttp := routedInbound(t, 1, "vless-xhttp", "vless_reality_xhttp", "cdn3.example.com")
	pq := routedInbound(t, 2, "vless-pq", "vless_reality_grpc", "cdn3.example.com")
	vision := routedInbound(t, 3, "vless-vision", "vless_reality_vision", "cdn2.example.com")
	any1 := routedInbound(t, 4, "anytls", "anytls", "cdn3.example.com")
	any2 := routedInbound(t, 5, "anytls-2", "anytls", "cdn3.example.com")

	got := SNIClashes([]db.Inbound{xhttp, pq, vision}, "node.example.com", false)
	if c := got[1]; !slices.Equal(c.With, []string{"vless-pq"}) || c.Fix != "by_key" {
		t.Errorf("two REALITY ones on one name, no key: %+v", c)
	}
	if c := got[2]; !slices.Equal(c.With, []string{"vless-xhttp"}) || c.Fix != "by_key" {
		t.Errorf("the other of them: %+v", c)
	}
	if _, ok := got[3]; ok {
		t.Error("a name of its own is no clash")
	}
	if got := SNIClashes([]db.Inbound{xhttp, pq, vision, any1}, "node.example.com", true); len(got) != 0 {
		t.Errorf("REALITY ones and one other on a name, with the key: %+v", got)
	}
	got = SNIClashes([]db.Inbound{xhttp, pq, any1, any2}, "node.example.com", true)
	if c := got[4]; !slices.Equal(c.With, []string{"anytls-2"}) || c.Fix != "name" {
		t.Errorf("two that are no REALITY ones, with the key: %+v", c)
	}
	if _, ok := got[1]; ok {
		t.Error("a REALITY one is in the way with the key on")
	}
	got = SNIClashes([]db.Inbound{xhttp, any1, any2}, "node.example.com", false)
	if c := got[1]; !slices.Equal(c.With, []string{"anytls", "anytls-2"}) || c.Fix != "name" {
		t.Errorf("no key, two that are no REALITY ones among them: %+v", c)
	}
	off := pq
	off.Enabled = 0
	if got := SNIClashes([]db.Inbound{xhttp, off}, "node.example.com", false); len(got) != 0 {
		t.Errorf("a switched off inbound asks for nothing: %+v", got)
	}
}
