package proto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/crypto/curve25519"
)

// The node runs WireGuard (and AmneziaWG) as a userspace inbound: every user is a peer
// whose private key is derived from the slot secret, so the panel stores no client key.
// The client config needs three things the panel must compute the same way the node does:
// the server's public key, the user's inner address, and the obfuscation parameters.

// WGKeySalt is the domain separation of the client key derivation. The core's
// listener/wireguard derives the same scalar; the two must never drift apart, or every
// client config stops matching the peers of the node.
const WGKeySalt = "mikan/wg/"

// WGUserKey derives a user's WireGuard private key (raw 32 bytes) from the slot secret.
func WGUserKey(secret string) [32]byte {
	sum := sha256.Sum256([]byte(WGKeySalt + secret))
	sum[0] &= 248
	sum[31] &= 127
	sum[31] |= 64
	return sum
}

// WGUserPublicKey is the peer public key of a user: what the node lists for it.
func WGUserPublicKey(secret string) ([32]byte, error) {
	private := WGUserKey(secret)
	pub, err := curve25519.X25519(private[:], curve25519.Basepoint)
	if err != nil {
		return [32]byte{}, err
	}
	var out [32]byte
	copy(out[:], pub)
	return out, nil
}

// WGServerKeys derives the server key pair from the private key the node keeps.
func WGServerPublicKey(privateB64 string) (string, error) {
	private, err := decodeWGKey(privateB64)
	if err != nil {
		return "", err
	}
	pub, err := curve25519.X25519(private[:], curve25519.Basepoint)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

// WGAddresses assigns every user of an inbound its inner address, the way the core
// does: keys sorted, a blake2b hash of the key picks the first slot, a taken slot steps
// to the next. The address is what tells the node whose connection it is, and it has to
// be the same on both sides without asking the node.
func WGAddresses(t Template, slots []Slot) (map[string]netip.Addr, error) {
	pool, err := wgPool(t)
	if err != nil {
		return nil, err
	}
	if len(pool) == 0 {
		return nil, errors.New("wireguard: no tunnel subnet")
	}
	type entry struct {
		name string
		key  [32]byte
	}
	entries := make([]entry, 0, len(slots))
	for _, s := range slots {
		key, err := WGUserPublicKey(s.Secret)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry{name: s.Name, key: key})
	}
	sort.Slice(entries, func(i, j int) bool { return lessWGKey(entries[i].key, entries[j].key) })

	out := make(map[string]netip.Addr, len(entries))
	taken := make([]bool, len(pool))
	for _, e := range entries {
		hash := blake2b.Sum256(e.key[:])
		start := (int(hash[0])<<8 | int(hash[1])) % len(pool)
		for i := 0; i < len(pool); i++ {
			slot := (start + i) % len(pool)
			if taken[slot] {
				continue
			}
			taken[slot] = true
			out[e.name] = pool[slot]
			break
		}
	}
	return out, nil
}

func lessWGKey(a, b [32]byte) bool {
	for i := 0; i < 32; i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return false
}

// wgPool lists the addresses a peer may take: the hosts of the subnets, the first of
// each belonging to the server.
func wgPool(t Template) ([]netip.Addr, error) {
	var pool []netip.Addr
	v4, v6 := wgTunnel(t)
	for _, cidr := range []string{v4, v6} {
		if cidr == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			return nil, fmt.Errorf("tunnel subnet %q: %w", cidr, err)
		}
		prefix = prefix.Masked()
		bits := prefix.Addr().BitLen()
		size := uint64(1) << (bits - prefix.Bits())
		if size > 1<<16 {
			return nil, fmt.Errorf("tunnel subnet %q: at most 65536 addresses", cidr)
		}
		addr := prefix.Addr()
		for i := uint64(0); i < size; i++ {
			if i > 0 {
				pool = append(pool, addr)
			}
			addr = addr.Next()
		}
	}
	return pool, nil
}

// defaultWGPool is the subnet a template without one gets: the core uses the same.
// defaultWGPool is the subnet a template without one gets. A panel carries every slot in
// every listener, so the pool has to hold the whole roster: a /24 (254 hosts) silently
// strands everyone past the 254th slot.
const defaultWGPool = "10.66.0.0/16"

// wgTunnel is the subnets a template actually runs on.
func wgTunnel(t Template) (v4, v6 string) {
	v4, v6 = t.str("ipv4-cidr"), t.str("ipv6-cidr")
	if v4 == "" && v6 == "" {
		v4 = defaultWGPool
	}
	return v4, v6
}

// validateWireGuard checks a wireguard template: the subnets, the MTU and the
// obfuscation parameters whose mistakes are silent on the wire.
func validateWireGuard(t Template, o Options) error {
	v4, v6 := wgTunnel(t)
	for _, cidr := range []string{v4, v6} {
		if cidr == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil {
			return fail("wg_cidr", "ipv4-cidr")
		}
		if prefix.Bits() > prefix.Addr().BitLen()-2 {
			return fail("wg_cidr_small", "ipv4-cidr")
		}
	}
	// The peer pool is a slice of addresses in memory, and the node holds the same
	// bound: a subnet that cannot be a pool is refused before either side starts.
	if _, err := wgPool(t); err != nil {
		return fail("wg_cidr_large", "ipv4-cidr")
	}
	if key := t.Ext().WGKey; key != "" {
		if _, err := decodeWGKey(key); err != nil {
			return fail("wg_key", extKey+".wg-key")
		}
	}
	if mtu, ok := toInt(t["mtu"]); ok && (mtu < 1280 || mtu > 1500) {
		return fail("wg_mtu", "mtu")
	}
	if workers, ok := toInt(t["workers"]); ok && (workers < 0 || workers > 64) {
		return fail("wg_workers", "workers")
	}
	awg, _ := t["amnezia-wg-option"].(map[string]any)
	if awg == nil {
		return nil
	}
	if err := validateAmneziaWG(awg); err != nil {
		return err
	}
	return nil
}

// validateAmneziaWG mirrors the checks of the core: a header protection key with a
// padding below 12 never converges, and no error on either end says why.
func validateAmneziaWG(awg map[string]any) error {
	version, _ := toInt(awg["version"])
	for _, key := range []string{"jc", "jmin", "jmax", "s1", "s2", "s3", "s4"} {
		if n, ok := toInt(awg[key]); ok && n < 0 {
			return fail("wg_awg_range", "amnezia-wg-option."+key)
		}
	}
	jmin, _ := toInt(awg["jmin"])
	jmax, _ := toInt(awg["jmax"])
	if jmin > jmax && jmax != 0 {
		return fail("wg_awg_junk", "amnezia-wg-option.jmin")
	}
	// A handshake initiation is 148 bytes and a response 92: s2 = s1+56 makes them the same
	// length, so the obfuscation separates nothing while both ends still connect.
	s1, _ := toInt(awg["s1"])
	s2, _ := toInt(awg["s2"])
	if s1 != 0 && s2 != 0 && s2 == s1+56 {
		return fail("wg_awg_s2_collision", "amnezia-wg-option.s2")
	}
	for _, key := range []string{"h1", "h2", "h3", "h4"} {
		if v := str(awg[key]); v != "" && !validUintRange(v) {
			return fail("wg_awg_range", "amnezia-wg-option."+key)
		}
	}
	for _, key := range []string{"content-padding-addition", "rekey-after-time", "rekey-timeout",
		"reject-after-time", "keepalive-timeout", "max-handshake-attempts"} {
		if v := str(awg[key]); v != "" && !validUintRange(v) {
			return fail("wg_awg_range", "amnezia-wg-option."+key)
		}
	}
	if version != 3 {
		return nil
	}
	if str(awg["header-protection-key"]) == "" {
		return fail("wg_awg_hpkey", "amnezia-wg-option.header-protection-key")
	}
	if _, err := decodeWGKey(str(awg["header-protection-key"])); err != nil {
		return fail("wg_awg_hpkey", "amnezia-wg-option.header-protection-key")
	}
	for _, key := range []string{"s1", "s2", "s3", "s4"} {
		n, _ := toInt(awg[key])
		if n < 12 {
			return fail("wg_awg_padding", "amnezia-wg-option."+key)
		}
	}
	return nil
}

// validUintRange accepts the UAPI "a" or "a-b" form.
func validUintRange(s string) bool {
	lo, hi, isRange := strings.Cut(s, "-")
	if _, err := strconv.ParseUint(strings.TrimSpace(lo), 10, 32); err != nil {
		return false
	}
	if isRange {
		if _, err := strconv.ParseUint(strings.TrimSpace(hi), 10, 32); err != nil {
			return false
		}
	}
	return true
}

// decodeWGKey reads a 32-byte key written in hex or base64.
func decodeWGKey(s string) ([32]byte, error) {
	var out [32]byte
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
	} {
		if raw, err := enc.DecodeString(s); err == nil {
			if len(raw) != 32 {
				return out, errors.New("expected 32 bytes")
			}
			copy(out[:], raw)
			return out, nil
		}
	}
	return out, errors.New("not a WireGuard key")
}

// AmneziaWGPreset is the obfuscation a fresh AmneziaWG inbound starts with: the v3
// parameters with a random header protection key and paddings above 12, which is the one
// combination that converges on both ends without further tuning.
func AmneziaWGPreset() map[string]any {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	return map[string]any{
		"version":                  3,
		"jc":                       4,
		"jmin":                     50,
		"jmax":                     1000,
		"s1":                       20,
		"s2":                       24,
		"s3":                       28,
		"s4":                       32,
		"h1":                       "1234567-1234667",
		"h2":                       "2234567-2234667",
		"h3":                       "3234567-3234667",
		"h4":                       "4234567-4234667",
		"header-protection-key":    base64.StdEncoding.EncodeToString(key),
		"content-padding-addition": "20-100",
		"random-trailers":          true,
		"disable-cookies":          true,
	}
}

// NewWGServerKey makes the server private key of a new inbound.
func NewWGServerKey() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key), nil
}

// WGServerKey is the server private key of a template, base64 ("" when none is set).
func WGServerKey(t Template) string { return t.Ext().WGKey }
