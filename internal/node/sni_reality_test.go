package node

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"testing"
	"time"

	ctls "github.com/metacubex/mihomo/component/tls"

	"mikan/internal/nodeapi"
)

// realityInbound is a REALITY inbound of a test: its key pair and one short ID.
type realityInbound struct {
	priv  *ecdh.PrivateKey
	short string
}

func newRealityInbound(t *testing.T, short string) realityInbound {
	t.Helper()
	priv, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return realityInbound{priv: priv, short: short}
}

// config is the inbound's config as the panel sends it to the node.
func (r realityInbound) config(names ...string) []byte {
	key := base64.RawURLEncoding.EncodeToString(r.priv.Bytes())
	sni := ""
	for i, n := range names {
		if i > 0 {
			sni += ","
		}
		sni += fmt.Sprintf("%q", n)
	}
	return []byte(fmt.Sprintf(`{"type":"vless","network":"tcp","reality-config":{"dest":"example.com:443","private-key":%q,"short-id":[%q],"server-names":[%s]}}`, key, r.short, sni))
}

// hello is the ClientHello a REALITY client of the inbound sends for name: the TLS records
// as they leave Citrus's REALITY client, made with the inbound's public key and short ID.
func (r realityInbound) hello(t *testing.T, name string) []byte {
	t.Helper()
	var short [8]byte
	if _, err := hex.Decode(short[:], []byte(r.short)); err != nil {
		t.Fatal(err)
	}
	fp, ok := ctls.GetFingerprint("chrome")
	if !ok {
		t.Fatal("no chrome fingerprint")
	}
	return captureHello(t, func(ctx context.Context, c net.Conn) {
		_, _ = ctls.GetRealityConn(ctx, c, fp, name, &ctls.RealityConfig{PublicKey: r.priv.PublicKey(), ShortID: short})
	})
}

// plainHello is the ClientHello of a TLS client that knows nothing of REALITY.
func plainHello(t *testing.T, name string) []byte {
	t.Helper()
	return captureHello(t, func(ctx context.Context, c net.Conn) {
		_ = tls.Client(c, &tls.Config{ServerName: name, InsecureSkipVerify: true}).HandshakeContext(ctx)
	})
}

// captureHello runs a client over a pipe and returns the records of its ClientHello.
func captureHello(t *testing.T, client func(ctx context.Context, c net.Conn)) []byte {
	t.Helper()
	c, s := net.Pipe()
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		defer c.Close()
		client(ctx, c)
	}()
	_ = s.SetReadDeadline(time.Now().Add(5 * time.Second))
	var buf []byte
	tmp := make([]byte, 4096)
	for {
		if _, st := joinHello(buf); st == helloDone {
			return buf
		} else if st == helloBad {
			t.Fatalf("the client sent no ClientHello: %x", buf)
		}
		n, err := s.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			t.Fatalf("reading the ClientHello: %v", err)
		}
	}
}

// resplit puts a ClientHello's handshake message into records of at most size bytes each,
// as a client that splits its ClientHello sends it.
func resplit(t *testing.T, records []byte, size int) []byte {
	t.Helper()
	msg, st := joinHello(records)
	if st != helloDone {
		t.Fatal("no whole ClientHello to split")
	}
	var out []byte
	for len(msg) > 0 {
		n := min(size, len(msg))
		out = append(out, 0x16, 0x03, 0x01, byte(n>>8), byte(n))
		out = append(out, msg[:n]...)
		msg = msg[n:]
	}
	return out
}

func parsed(t *testing.T, records []byte) *tlsHello {
	t.Helper()
	msg, st := joinHello(records)
	if st != helloDone {
		t.Fatalf("not a whole ClientHello: %d", st)
	}
	h, ok := parseHello(msg)
	if !ok {
		t.Fatal("the ClientHello does not parse")
	}
	return h
}

func TestJoinHelloAcrossRecords(t *testing.T) {
	records := plainHello(t, "a.example.com")
	whole, _ := joinHello(records)
	split := resplit(t, records, 100)
	got, st := joinHello(split)
	if st != helloDone || string(got) != string(whole) {
		t.Fatalf("split over records: state %d, %d bytes of %d", st, len(got), len(whole))
	}
	if _, st := joinHello(split[:len(split)-10]); st != helloPart {
		t.Errorf("the last record cut short: state %d, want more to come", st)
	}
	if _, st := joinHello(append([]byte{0x17}, split[1:]...)); st != helloBad {
		t.Errorf("application data first: state %d, want no ClientHello", st)
	}
	if h := parsed(t, split); h.serverName != "a.example.com" {
		t.Errorf("the name of a split ClientHello: %q", h.serverName)
	}
}

// Only the inbound whose key a REALITY client used reads its session ID, and only with a
// short ID of its own: the check the inbound's REALITY makes itself.
func TestRealityKeyKnowsItsOwnClients(t *testing.T) {
	a, b := newRealityInbound(t, "a1"), newRealityInbound(t, "0b02")
	keyA, keyB := realityKeyOf(a.config("shared.example.com")), realityKeyOf(b.config("shared.example.com"))
	if keyA == nil || keyB == nil {
		t.Fatal("the REALITY keys do not parse")
	}
	helloA, helloB := parsed(t, a.hello(t, "shared.example.com")), parsed(t, b.hello(t, "shared.example.com"))
	if !keyA.owns(helloA) || keyB.owns(helloA) {
		t.Errorf("a's client: a owns it %v, b owns it %v", keyA.owns(helloA), keyB.owns(helloA))
	}
	if !keyB.owns(helloB) || keyA.owns(helloB) {
		t.Errorf("b's client: b owns it %v, a owns it %v", keyB.owns(helloB), keyA.owns(helloB))
	}
	otherShort := realityInbound{priv: a.priv, short: "ff"}
	if realityKeyOf(otherShort.config("shared.example.com")).owns(helloA) {
		t.Error("a's key with a short ID the client did not send owns its ClientHello")
	}
	if plain := parsed(t, plainHello(t, "shared.example.com")); keyA.owns(plain) || keyB.owns(plain) {
		t.Error("a plain TLS client is taken for a REALITY one")
	}
	if realityKeyOf([]byte(`{"type":"vless","network":"tcp"}`)) != nil {
		t.Error("an inbound with no REALITY has a key")
	}
}

// The REALITY server takes the X25519 share, else the X25519 half of the hybrid one.
func TestRealityPeerPicksTheServersShare(t *testing.T) {
	x := make([]byte, 32)
	x[0] = 1
	hybrid := make([]byte, mlkem768KeySize+32)
	hybrid[mlkem768KeySize] = 2
	if got := realityPeer([]keyShare{{groupX25519MLKEM768, hybrid}, {groupX25519, x}}); got[0] != 1 {
		t.Error("both shares: the X25519 one is not taken")
	}
	if got := realityPeer([]keyShare{{groupX25519MLKEM768, hybrid}}); len(got) != 32 || got[0] != 2 {
		t.Error("the hybrid share alone: its X25519 half is not taken")
	}
	if realityPeer([]keyShare{{0x0017, make([]byte, 65)}}) != nil {
		t.Error("a P-256 share is taken")
	}
}

// With ByKey the router hands each REALITY client of a shared name to its own inbound and a
// stranger to the first; without it the name is one inbound's, the last that has it.
func TestSNIRouterTellsASharedNameByKey(t *testing.T) {
	aPort, aHits := backend(t)
	bPort, bHits := backend(t)
	a, b := newRealityInbound(t, "a1"), newRealityInbound(t, "0b02")
	const name = "shared.example.com"
	inbounds := []nodeapi.Inbound{{Name: "a", Port: aPort, Config: a.config(name)}, {Name: "b", Port: bPort, Config: b.config(name)}}
	routes := []nodeapi.SNIRoute{{Inbound: "a", Names: []string{name}}, {Inbound: "b", Names: []string{name}}}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &sniRouter{ln: ln, log: slog.New(slog.DiscardHandler)}
	r.set(&nodeapi.SNIRouter{Routes: routes, ByKey: true}, inbounds)
	go r.serve(nil)
	defer r.close()
	send := func(records []byte) net.Conn {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Write(records); err != nil {
			t.Fatal(err)
		}
		return c
	}
	for _, tc := range []struct {
		what    string
		records []byte
		hits    chan backendHit
	}{
		{"b's client", b.hello(t, name), bHits},
		{"a's client", a.hello(t, name), aHits},
		{"a's client, its ClientHello split over records", resplit(t, a.hello(t, name), 300), aHits},
		{"b's client, its ClientHello split over records", resplit(t, b.hello(t, name), 300), bHits},
		{"a stranger", plainHello(t, name), aHits},
	} {
		c := send(tc.records)
		hitBy(t, tc.hits, c, tc.what)
		c.Close()
	}

	r.set(&nodeapi.SNIRouter{Routes: routes}, inbounds)
	c := send(a.hello(t, name))
	hitBy(t, bHits, c, "without ByKey, a's client")
	c.Close()
}
