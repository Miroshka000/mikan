package node

import (
	"encoding/json"
	"testing"

	"mikan/internal/nodeapi"
	"mikan/internal/proto"
)

// The panel's rendering and the core's listener must agree on every key the client needs:
// this is the end-to-end check that a template turns into a config the core accepts and a
// client profile that points at the peer the core created.
func TestWireGuardListenerAndClientAgree(t *testing.T) {
	key, err := proto.NewWGServerKey()
	if err != nil {
		t.Fatal(err)
	}
	src := "type: wireguard\nmtu: 1420\nipv4-cidr: 10.66.66.0/24\nmikan:\n  wg-key: " + key + "\n"
	tpl, err := proto.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := proto.Validate(tpl, proto.Options{}); err != nil {
		t.Fatalf("validate: %v", err)
	}
	slots := []nodeapi.Slot{{Name: "alice", UUID: "u1", Secret: "s1"}, {Name: "bob", UUID: "u2", Secret: "s2"}}
	raw, err := json.Marshal(tpl)
	if err != nil {
		t.Fatal(err)
	}
	l, err := listenerFor(nodeapi.Inbound{Name: "wg", Listen: "0.0.0.0", Port: "51820", Config: raw}, slots, proto.Cert{}, proto.Options{})
	if err != nil {
		t.Fatalf("listener: %v", err)
	}
	if l["type"] != "wireguard" || l["port"] != "51820" || l["listen"] != "0.0.0.0" {
		t.Fatalf("listener = %v", l)
	}
	if l["private-key"] != key {
		t.Fatal("the node did not get the private key")
	}
	users, ok := l["users"].(map[string]string)
	if !ok || len(users) != 2 || users["alice"] != "s1" {
		t.Fatalf("users = %v", l["users"])
	}

	// Every user gets a distinct address, and the client profile carries the one the node
	// put in the peer's allowed ips.
	addrs, err := proto.WGAddresses(tpl, slots)
	if err != nil {
		t.Fatal(err)
	}
	if addrs["alice"] == addrs["bob"] {
		t.Fatal("two users got the same address")
	}
	pub, err := proto.WGServerPublicKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alice", "bob"} {
		c, err := proto.ClientConfig(tpl, proto.ClientInput{Name: name, Host: "203.0.113.7", Port: 51820,
			Slot: nodeapi.Slot{Name: name, Secret: users[name]}, WGAddress: addrs[name]})
		if err != nil {
			t.Fatalf("%s client: %v", name, err)
		}
		if c.Mihomo["public-key"] != pub {
			t.Fatalf("%s: client verifies %v, node's public key is %s", name, c.Mihomo["public-key"], pub)
		}
		if c.Mihomo["ip"] != addrs[name].String() {
			t.Fatalf("%s: client ip %v, node assigned %s", name, c.Mihomo["ip"], addrs[name])
		}
	}
}
