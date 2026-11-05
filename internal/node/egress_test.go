package node

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"testing"
	"time"

	"github.com/metacubex/mihomo/adapter/outbound"
	C "github.com/metacubex/mihomo/constant"

	"mikan/internal/nodeapi"
)

// What a user reaches through a node is the word of the node's rules alone. Citrus has a
// guard of its own, on unless the config says otherwise, that refuses the server's own
// addresses whatever the rules say: with it a client going through the node beside the panel
// could not open the panel or refresh its subscription. The target here is on the loopback,
// the one address of this host a test can count on: with private addresses allowed only that
// guard could refuse it, and with them closed the rules must.
func TestRulesDecideWhatAUserReaches(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	port := uint16(target.Addr().(*net.TCPAddr).Port)

	for _, tc := range []struct {
		name         string
		allowPrivate bool
	}{
		{"private addresses allowed", true},
		{"private addresses closed", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := echoesThroughNode(t, tc.allowPrivate, port); got != tc.allowPrivate {
				t.Fatalf("the target answered: %v, want %v", got, tc.allowPrivate)
			}
		})
	}
}

// echoesThroughNode says whether what a user of a running node sends to the port on the
// loopback comes back.
func echoesThroughNode(t *testing.T, allowPrivate bool, port uint16) bool {
	t.Helper()
	e, err := Start(Options{DataDir: t.TempDir(), Version: "test", AllowPrivate: allowPrivate, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = e.Apply(nodeapi.DesiredState{Revision: 99}) })

	// Shadowsocks-2022 with ChaCha20: one key for the inbound, no users to set up.
	const cipher = "2022-blake3-chacha20-poly1305"
	password := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	inbound := freePort(t)
	res, err := e.Apply(nodeapi.DesiredState{Revision: 1, Inbounds: []nodeapi.Inbound{{
		Name: "ss", Listen: "127.0.0.1", Port: inbound,
		Config: []byte(`{"type":"shadowsocks","cipher":"` + cipher + `","password":"` + password + `"}`),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if st := statusOf(res.Listeners)["ss"]; !st.OK {
		t.Fatalf("the inbound does not run: %+v", st)
	}

	inboundPort, err := strconv.Atoi(inbound)
	if err != nil {
		t.Fatal(err)
	}
	client, err := outbound.NewShadowSocks(outbound.ShadowSocksOption{Name: "client", Server: "127.0.0.1", Port: inboundPort, Cipher: cipher, Password: password})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := client.DialContext(ctx, &C.Metadata{NetWork: C.TCP, DstIP: netip.MustParseAddr("127.0.0.1"), DstPort: port})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}

	const hello = "through the node"
	if _, err := conn.Write([]byte(hello)); err != nil {
		return false
	}
	got := make([]byte, len(hello))
	if _, err := io.ReadFull(conn, got); err != nil {
		return false
	}
	return string(got) == hello
}
