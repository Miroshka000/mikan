package proto

import (
	"testing"
)

// Hysteria2 obfuscation: Salamander or Gecko with a password; Gecko's packet sizes only
// with Gecko, within what one fragment can carry.
func TestObfsValidation(t *testing.T) {
	for src, want := range map[string]string{
		"type: hysteria2\nobfs: salamander\nobfs-password: p\n":                                                   "",
		"type: hysteria2\nobfs: gecko\nobfs-password: p\n":                                                        "",
		"type: hysteria2\nobfs: gecko\nobfs-password: p\nobfs-min-packet-size: 600\nobfs-max-packet-size: 1100\n": "",
		"type: hysteria2\nobfs: gecko\nobfs-password: p\nobfs-max-packet-size: 2048\n":                            "",
		"type: hysteria2\nobfs: gecko\n":                                                                          "config_obfs",
		"type: hysteria2\nobfs: brutal\nobfs-password: p\n":                                                       "config_obfs",
		"type: hysteria2\nobfs: \"\"\nobfs-password: p\n":                                                         "config_obfs",
		"type: hysteria2\nobfs-min-packet-size: 600\n":                                                            "config_obfs",
		"type: hysteria2\nobfs: salamander\nobfs-password: p\nobfs-min-packet-size: 600\n":                        "config_obfs_sizes",
		"type: hysteria2\nobfs: gecko\nobfs-password: p\nobfs-min-packet-size: 100\n":                             "config_obfs_sizes",
		"type: hysteria2\nobfs: gecko\nobfs-password: p\nobfs-max-packet-size: 4096\n":                            "config_obfs_sizes",
		"type: hysteria2\nobfs: gecko\nobfs-password: p\nobfs-min-packet-size: 1300\n":                            "config_obfs_sizes",
		"type: hysteria2\nobfs: gecko\nobfs-password: p\nobfs-min-packet-size: 900\nobfs-max-packet-size: 800\n":  "config_obfs_sizes",
		"type: hysteria2\nobfs: gecko\nobfs-password: p\nobfs-min-packet-size: big\n":                             "config_obfs_sizes",
		"type: hysteria2\nobfs: gecko\nobfs-password: p\nobfs-min-packet-size: 512.5\n":                           "config_obfs_sizes",
	} {
		if got := code(Validate(mustParse(t, src), Options{})); got != want && !(want == "" && got == "") {
			t.Errorf("%q: %q, want %q", src, got, want)
		}
	}
}

// Needs marks Gecko so only the apps that know it get the inbound.
func TestGeckoNeeds(t *testing.T) {
	tpl := mustParse(t, "type: hysteria2\nobfs: gecko\nobfs-password: pw\nobfs-min-packet-size: 600\nobfs-max-packet-size: 1100\n")
	if n := NeedsOf(tpl); !n.Gecko {
		t.Fatal("Gecko not in the needs")
	}
	if n := NeedsOf(mustParse(t, "type: hysteria2\nobfs: salamander\nobfs-password: pw\n")); n.Gecko {
		t.Fatal("Salamander marked as Gecko")
	}
}

// Switching obfuscation keeps the password, fills one in, and drops Gecko's sizes with Gecko.
func TestSetObfs(t *testing.T) {
	tpl := mustParse(t, "type: hysteria2\nobfs: gecko\nobfs-password: keep\nobfs-min-packet-size: 600\n")
	if err := SetObfs(tpl, ObfsSalamander, "new"); err != nil {
		t.Fatal(err)
	}
	if tpl["obfs"] != "salamander" || tpl["obfs-password"] != "keep" || tpl["obfs-min-packet-size"] != nil {
		t.Fatalf("to salamander: %v", tpl)
	}
	bare := mustParse(t, "type: hysteria2\n")
	if err := SetObfs(bare, ObfsGecko, "new"); err != nil || bare["obfs-password"] != "new" || Obfs(bare) != "gecko" {
		t.Fatalf("to gecko: %v %v", bare, err)
	}
	if err := SetObfs(mustParse(t, "type: tuic\n"), ObfsGecko, "x"); code(err) != "config_obfs" {
		t.Fatalf("tuic: %v", err)
	}
}
