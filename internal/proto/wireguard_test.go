package proto

import (
	"crypto/sha256"
	"encoding/base64"
	"net/netip"
	"testing"

	"golang.org/x/crypto/curve25519"
)

// The core derives the client key from the same secret with the same salt; a drift here
// would hand every client a peer the node does not have.
func TestWGUserKeyMatchesTheCore(t *testing.T) {
	sum := sha256.Sum256([]byte("mikan/wg/" + "slot-secret"))
	sum[0] &= 248
	sum[31] &= 127
	sum[31] |= 64
	if got := WGUserKey("slot-secret"); got != sum {
		t.Fatalf("private key = %x, want %x", got, sum)
	}
	// The public key must be the X25519 of the scalar.
	pub, err := WGUserPublicKey("slot-secret")
	if err != nil {
		t.Fatal(err)
	}
	want, err := curve25519.X25519(sum[:], curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	if string(pub[:]) != string(want) {
		t.Fatal("the public key is not the X25519 of the scalar")
	}
}

func TestWGServerPublicKey(t *testing.T) {
	private := make([]byte, 32)
	for i := range private {
		private[i] = byte(i + 1)
	}
	pub, err := WGServerPublicKey(base64.StdEncoding.EncodeToString(private))
	if err != nil {
		t.Fatal(err)
	}
	want, err := curve25519.X25519(private, curve25519.Basepoint)
	if err != nil {
		t.Fatal(err)
	}
	if pub != base64.StdEncoding.EncodeToString(want) {
		t.Fatalf("server public key = %s", pub)
	}
	if _, err := WGServerPublicKey("not-a-key"); err == nil {
		t.Fatal("a broken key must be rejected")
	}
}

func TestWGAddressesAreUniqueAndStable(t *testing.T) {
	tpl := mustParse(t, "type: wireguard\nipv4-cidr: 10.66.66.0/24\n")
	slots := []Slot{
		{Name: "a", Secret: "sa"}, {Name: "b", Secret: "sb"}, {Name: "c", Secret: "sc"},
	}
	first, err := WGAddresses(tpl, slots)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 3 {
		t.Fatalf("assigned %d addresses", len(first))
	}
	seen := map[netip.Addr]bool{}
	for name, addr := range first {
		if addr == netip.MustParseAddr("10.66.66.0") {
			t.Fatalf("%s got the network address", name)
		}
		if addr == netip.MustParseAddr("10.66.66.1") {
			t.Fatalf("%s got the server address", name)
		}
		if seen[addr] {
			t.Fatalf("address %s handed out twice", addr)
		}
		seen[addr] = true
	}

	// The order of the slots must not move anyone: a subscription lists what the panel
	// gave, and the node computes from its own order.
	second, err := WGAddresses(tpl, []Slot{slots[2], slots[0], slots[1]})
	if err != nil {
		t.Fatal(err)
	}
	for name, addr := range first {
		if second[name] != addr {
			t.Fatalf("%s moved from %s to %s", name, addr, second[name])
		}
	}
}

func TestWGAddressesWithoutSubnetUseTheDefault(t *testing.T) {
	tpl := mustParse(t, "type: wireguard\n")
	addrs, err := WGAddresses(tpl, []Slot{{Name: "a", Secret: "sa"}})
	if err != nil {
		t.Fatal(err)
	}
	addr := addrs["a"]
	if !addr.IsValid() || !netip.MustParsePrefix("10.66.0.0/16").Contains(addr) {
		t.Fatalf("address = %s, want one of the default subnet", addr)
	}
}

func TestValidateWireGuardSubnets(t *testing.T) {
	cases := []string{
		"type: wireguard\nipv4-cidr: 10.0.0.0/31\n",
		"type: wireguard\nipv4-cidr: 10.0.0.0/8\n",
		"type: wireguard\nipv4-cidr: nonsense\n",
		"type: wireguard\nmtu: 9000\n",
		"type: wireguard\nworkers: 999\n",
	}
	for _, src := range cases {
		if err := Validate(mustParse(t, src), Options{}); err == nil {
			t.Fatalf("%q must be rejected", src)
		}
	}
	if err := Validate(mustParse(t, "type: wireguard\nmtu: 1420\nipv4-cidr: 10.66.66.0/24\n"), Options{}); err != nil {
		t.Fatalf("a valid template rejected: %v", err)
	}
}

func TestValidateAmneziaWGNeedsPaddingWithHeaderKey(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	src := "type: wireguard\namnezia-wg-option:\n  version: 3\n  s1: 11\n  s2: 20\n  s3: 20\n  s4: 20\n  header-protection-key: " + key + "\n"
	err := Validate(mustParse(t, src), Options{})
	if err == nil {
		t.Fatal("s1 below 12 must be rejected")
	}
	if e, ok := err.(*Error); !ok || e.Code != "wg_awg_padding" {
		t.Fatalf("error = %v, want wg_awg_padding", err)
	}
}

func TestValidateAmneziaWGRejectsBrokenRanges(t *testing.T) {
	for _, src := range []string{
		"type: wireguard\namnezia-wg-option:\n  version: 1\n  h1: abc\n",
		"type: wireguard\namnezia-wg-option:\n  version: 1\n  jmin: 500\n  jmax: 100\n",
		"type: wireguard\namnezia-wg-option:\n  version: 3\n",
		"type: wireguard\namnezia-wg-option:\n  version: 1\n  s1: 20\n  s2: 76\n",
	} {
		if err := Validate(mustParse(t, src), Options{}); err == nil {
			t.Fatalf("%q must be rejected", src)
		}
	}
}

// A template may not set what the node owns: the private key, the users and the address
// are the node's, and a template that could set them would let the admin read another
// inbound's keys.
func TestWireGuardTemplateCannotSetManagedKeys(t *testing.T) {
	for _, key := range []string{"private-key", "users", "name", "port", "listen", "certificate"} {
		src := "type: wireguard\n" + key + ": x\n"
		if err := Validate(mustParse(t, src), Options{}); err == nil {
			t.Fatalf("%s must be rejected", key)
		}
	}
}

func TestWireGuardClientConfig(t *testing.T) {
	serverPrivate := make([]byte, 32)
	for i := range serverPrivate {
		serverPrivate[i] = byte(i + 7)
	}
	tpl := mustParse(t, "type: wireguard\nmtu: 1420\nipv4-cidr: 10.66.66.0/24\nmikan:\n  wg-key: "+base64.StdEncoding.EncodeToString(serverPrivate)+"\n")

	slot := Slot{Name: "alice", UUID: "uuid", Secret: "alice-secret"}
	addr, err := WGAddresses(tpl, []Slot{slot})
	if err != nil {
		t.Fatal(err)
	}
	c, err := ClientConfig(tpl, ClientInput{Name: "wg", Host: "203.0.113.7", Port: 51820, Slot: slot, WGAddress: addr["alice"]})
	if err != nil {
		t.Fatal(err)
	}
	if c.URI != "" {
		t.Fatalf("WireGuard has no share link, got %q", c.URI)
	}
	y := c.Mihomo
	if y["type"] != "wireguard" || y["server"] != "203.0.113.7" || y["port"] != 51820 {
		t.Fatalf("profile = %v", y)
	}
	private := WGUserKey(slot.Secret)
	if y["private-key"] != base64.StdEncoding.EncodeToString(private[:]) {
		t.Fatal("the private key is not derived from the slot secret")
	}
	want, err := WGServerPublicKey(base64.StdEncoding.EncodeToString(serverPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if y["public-key"] != want {
		t.Fatalf("public-key = %v, want %s", y["public-key"], want)
	}
	if y["ip"] != addr["alice"].String() {
		t.Fatalf("ip = %v, want %s", y["ip"], addr["alice"])
	}
	if y["mtu"] != 1420 || y["udp"] != true {
		t.Fatalf("profile = %v", y)
	}
}

func TestWireGuardClientCarriesAmneziaWG(t *testing.T) {
	tpl := mustParse(t, "type: wireguard\namnezia-wg-option:\n  version: 3\n  s1: 20\n  s2: 20\n  s3: 20\n  s4: 20\n  header-protection-key: "+
		base64.StdEncoding.EncodeToString(make([]byte, 32))+"\n  random-trailers: true\n")
	slot := Slot{Name: "a", Secret: "s"}
	c, err := ClientConfig(tpl, ClientInput{Name: "awg", Host: "1.2.3.4", Port: 51821, Slot: slot})
	if err != nil {
		t.Fatal(err)
	}
	awg, ok := c.Mihomo["amnezia-wg-option"].(map[string]any)
	if !ok || toIntMust(awg["version"]) != 3 || awg["random-trailers"] != true {
		t.Fatalf("amnezia-wg-option = %v", c.Mihomo["amnezia-wg-option"])
	}
}

// toIntMust reads a number that YAML may have decoded as int or float.
func toIntMust(v any) int {
	n, _ := toInt(v)
	return n
}

// A 2.0 client refuses the whole config over a key it does not know, so a 3.x parameter of
// a server that only uses it on its side must not reach it.
func TestClientAmneziaWGStripsV3KeysFromALegacyInbound(t *testing.T) {
	tpl := mustParse(t, "type: wireguard\namnezia-wg-option:\n  version: 1\n  jc: 4\n  s1: 20\n  s2: 24\n  random-trailers: true\n  disable-cookies: true\n")
	slot := Slot{Name: "a", Secret: "s"}
	c, err := ClientConfig(tpl, ClientInput{Name: "wg", Host: "1.2.3.4", Port: 51820, Slot: slot})
	if err != nil {
		t.Fatal(err)
	}
	awg := c.Mihomo["amnezia-wg-option"].(map[string]any)
	if awg["random-trailers"] != nil || awg["disable-cookies"] != nil {
		t.Fatalf("3.x keys reached a legacy client: %v", awg)
	}
	if toIntMust(awg["jc"]) != 4 || toIntMust(awg["s1"]) != 20 {
		t.Fatalf("2.0 keys are missing: %v", awg)
	}
}
