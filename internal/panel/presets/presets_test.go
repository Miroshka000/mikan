package presets

import (
	"errors"
	"strings"
	"testing"

	"mikan/internal/proto"
)

// The name clients send is a host name: one given with its site's port loses the port, and
// what is no name at all is refused.
func TestSetDestTakesANameWithoutAPort(t *testing.T) {
	src, err := NewConfig("vless_reality_vision", "")
	if err != nil {
		t.Fatal(err)
	}
	tpl, _ := proto.Parse(src)
	if err := SetDest(tpl, "127.0.0.1:21355", "cdn3.example.com:443"); err != nil {
		t.Fatal(err)
	}
	if _, names := Dest(tpl); len(names) != 1 || names[0] != "cdn3.example.com" {
		t.Errorf("a name with a port: %v", names)
	}
	if err := SetDest(tpl, "www.example.org:443", ""); err != nil {
		t.Fatal(err)
	}
	if _, names := Dest(tpl); len(names) != 1 || names[0] != "www.example.org" {
		t.Errorf("the host of the site: %v", names)
	}
	for _, bad := range []string{"cdn3 example com", "1.1.1.1", "cdn3.example.com:443:1", "localhost"} {
		var pe *proto.Error
		if err := SetDest(tpl, "127.0.0.1:21355", bad); !errors.As(err, &pe) || pe.Code != "reality_sni_name" {
			t.Errorf("%q taken: %v", bad, err)
		}
	}
}

// A copy shares no secret with the original: every key a preset generates is new, the
// rest of the settings stays.
func TestRekey(t *testing.T) {
	for _, id := range []string{"vless_reality_xhttp", PresetPQ, "hysteria2", "shadowsocks_2022", "sudoku", "snell", PresetTLSWS} {
		src, err := NewConfig(id, "")
		if err != nil {
			t.Fatal(err)
		}
		orig, _ := proto.Parse(src)
		cp, _ := proto.Parse(src)
		if err := Rekey(cp); err != nil {
			t.Fatal(err)
		}
		if err := proto.Validate(cp, proto.Options{}); err != nil {
			t.Fatalf("%s: the copy is broken: %v", id, err)
		}
		changed := false
		for _, k := range []string{"obfs-password", "password", "key", "psk", "decryption"} {
			if orig[k] != nil {
				if orig[k] == cp[k] {
					t.Fatalf("%s: %s kept", id, k)
				}
				changed = true
			}
		}
		if r, ok := orig["reality-config"].(map[string]any); ok {
			c := cp["reality-config"].(map[string]any)
			if r["private-key"] == c["private-key"] || r["dest"] != c["dest"] {
				t.Fatalf("%s: reality %v / %v", id, r, c)
			}
			changed = true
		}
		if id == PresetTLSWS && changed {
			t.Fatal("VLESS TLS has no key of its own to change")
		}
		if id != PresetTLSWS && !changed {
			t.Fatalf("%s: nothing new", id)
		}
	}
}

// TLS in TLS is said of Trojan and of VLESS without Vision, and of nothing else: Vision
// hides the records of the client's own TLS, and the other protocols have no TLS of their
// own around them, or pad it (AnyTLS).
func TestNestedTLS(t *testing.T) {
	for _, p := range All {
		want := (p.Type == "trojan" || p.Type == "vless") && !strings.Contains(p.ID, "vision")
		if p.NestedTLS != want {
			t.Errorf("%s: nested TLS %v, want %v", p.ID, p.NestedTLS, want)
		}
	}
}
