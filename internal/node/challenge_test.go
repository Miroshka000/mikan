package node

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mikan/internal/acmechallenge"
	"mikan/internal/nodeapi"
)

const (
	acmeToken   = "evaGxfADs6pSRb2LAv9IZf17Dt3juxGJ-PCt92wr-oA"
	acmeKeyAuth = acmeToken + ".9jg46WB3rR_AHD-EBXdN7cBkH1WOu0tA3M9fm21mqTI"
)

func challengeNode(t *testing.T, addr string) *nodeapi.Client {
	t.Helper()
	e := &Engine{log: quiet(), Reg: NewRegistry("e1", 0, time.Minute, time.Now), sys: newSysSampler(), Challenge: acmechallenge.New(addr)}
	t.Cleanup(e.Challenge.Close)
	sock := t.TempDir() + "/node.sock"
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("no unix sockets here")
	}
	srv := httptest.NewUnstartedServer(Handler(e, quiet()))
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	return nodeapi.NewUnixClient(sock)
}

// The panel puts a token on the node; the CA reads it on the node's port, which is given
// back once the panel takes the token away.
func TestNodeAnswersTheChallenge(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.Addr().String()
	probe.Close()
	c := challengeNode(t, addr)
	ctx := context.Background()
	if err := c.PresentChallenge(ctx, acmeToken, acmeKeyAuth); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get("http://" + addr + acmechallenge.Prefix + acmeToken)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != acmeKeyAuth {
		t.Fatalf("the CA got %d %q", resp.StatusCode, body)
	}
	if err := c.CleanUpChallenge(ctx, acmeToken); err != nil {
		t.Fatal(err)
	}
	if conn, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		conn.Close()
		t.Fatal("port still held after the challenge")
	}
}

func TestNodeSaysWhoHoldsPort80(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	c := challengeNode(t, held.Addr().String())
	err = c.PresentChallenge(context.Background(), acmeToken, acmeKeyAuth)
	var ne *nodeapi.Error
	if !errors.As(err, &ne) || ne.Code != nodeapi.CodePort80Busy {
		t.Fatalf("busy port: %v", err)
	}
	if err := c.PresentChallenge(context.Background(), "../../etc", "x"); err == nil {
		t.Fatal("a path for a token")
	}
}
