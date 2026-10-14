package targets

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
	"mikan/internal/scan"
)

func newDeps(t *testing.T, values map[string]any) Deps {
	t.Helper()
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	set := settings.New(st.Q)
	for k, v := range values {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	return Deps{Settings: set}
}

// A check dials from the server on the admin's word: only a public site or the panel's own
// port on loopback goes through, and the same rule holds for the API and for the CLI.
func TestCheckRefusesWhatIsNotPublic(t *testing.T) {
	d := newDeps(t, map[string]any{settings.KeyPanelPort: 1})
	ctx := context.Background()
	for _, dest := range []string{"nope", ":443", "[::1"} {
		if _, err := d.Check(ctx, dest, ""); !errors.Is(err, ErrBadDest) {
			t.Errorf("%q: %v, want ErrBadDest", dest, err)
		}
	}
	for _, dest := range []string{"127.0.0.1:443", "localhost:443", "LOCALHOST.:443", "10.0.0.1:443", "169.254.169.254:80", "intranet:443", "[::1]:443", "127.0.0.1:2"} {
		if _, err := d.Check(ctx, dest, "site.example"); !errors.Is(err, ErrPrivateDest) {
			t.Errorf("%q: %v, want ErrPrivateDest", dest, err)
		}
	}
	// The panel's own port on loopback is self-steal: dialed, and by the literal address.
	for _, dest := range []string{"127.0.0.1:1", "localhost:1"} {
		r, err := d.Check(ctx, dest, "vpn.example.com")
		if err != nil || r.Dest != "127.0.0.1:1" || r.Error != "refused" {
			t.Errorf("%q: %+v %v", dest, r, err)
		}
	}
}

// The node answers when it can; when it cannot, the panel checks from its own network.
func TestCheckAsksTheNodeFirst(t *testing.T) {
	d := newDeps(t, map[string]any{settings.KeyPanelPort: 1})
	ctx := context.Background()
	var asked []nodeapi.TargetCheckRequest
	d.CheckTarget = func(_ context.Context, req nodeapi.TargetCheckRequest) (nodeapi.TargetResult, error) {
		asked = append(asked, req)
		return nodeapi.TargetResult{Dest: req.Dest, SNI: req.SNI, OK: true, RTTms: 7}, nil
	}
	r, err := d.Check(ctx, "www.example.org:443", "www.example.org")
	if err != nil || !r.OK || r.RTTms != 7 || len(asked) != 1 || asked[0].Dest != "www.example.org:443" {
		t.Fatalf("the node's answer: %+v %v %+v", r, err, asked)
	}
	// A refused address is not even sent to the node.
	if _, err := d.Check(ctx, "10.0.0.1:443", "x.example"); !errors.Is(err, ErrPrivateDest) || len(asked) != 1 {
		t.Fatalf("a private address reached the node: %v %+v", err, asked)
	}
	d.CheckTarget = func(context.Context, nodeapi.TargetCheckRequest) (nodeapi.TargetResult, error) {
		return nodeapi.TargetResult{}, errors.New("node down")
	}
	if r, err := d.Check(ctx, "127.0.0.1:1", "vpn.example.com"); err != nil || r.Error != "refused" {
		t.Fatalf("the panel's own check: %+v %v", r, err)
	}
}

func TestScan(t *testing.T) {
	ctx := context.Background()
	local := db.Node{ID: 1}
	remote := db.Node{ID: 2, Address: "203.0.113.9:8443", PublicHost: "203.0.113.9"}
	site := scan.Result{Dest: "198.51.100.7:443", SNI: "site.example", OK: true}
	fromNode := func(context.Context, nodeapi.TargetScanRequest) (nodeapi.TargetScan, error) {
		return nodeapi.TargetScan{Scanned: 200, Results: []scan.Result{site}}, nil
	}
	nodeDown := func(context.Context, nodeapi.TargetScanRequest) (nodeapi.TargetScan, error) {
		return nodeapi.TargetScan{}, errors.New("node down")
	}
	var scanned []string
	neighbors := func(_ context.Context, ip string, limit int, o scan.Options) ([]scan.Result, int, error) {
		if o.Any || limit != ScanLimit {
			t.Errorf("scan %s: limit %d, options %+v", ip, limit, o)
		}
		scanned = append(scanned, ip)
		return []scan.Result{site}, 253, nil
	}

	t.Run("the node scans", func(t *testing.T) {
		d := newDeps(t, map[string]any{settings.KeyPublicHost: "198.51.100.1", settings.KeyPanelPort: 1})
		d.ScanTargets = fromNode
		d.Neighbors = neighbors
		scanned = nil
		got, err := d.Scan(ctx, ScanRequest{Node: local})
		if err != nil || !got.FromNode || got.IP != "198.51.100.1" || got.Scanned != 200 || len(got.Results) != 1 || scanned != nil || got.SelfSteal != nil {
			t.Fatalf("%+v %v %v", got, err, scanned)
		}
	})
	t.Run("the panel scans when the node cannot", func(t *testing.T) {
		d := newDeps(t, map[string]any{settings.KeyPublicHost: "198.51.100.1"})
		d.ScanTargets = nodeDown
		d.Neighbors = neighbors
		scanned = nil
		got, err := d.Scan(ctx, ScanRequest{Node: local})
		if err != nil || got.FromNode || got.Scanned != 253 || len(got.Results) != 1 || len(scanned) != 1 || scanned[0] != "198.51.100.1" {
			t.Fatalf("%+v %v %v", got, err, scanned)
		}
		d.ScanTargets = nil
		if got, err = d.Scan(ctx, ScanRequest{Node: local}); err != nil || got.FromNode || got.Scanned != 253 {
			t.Fatalf("without a node: %+v %v", got, err)
		}
	})
	t.Run("results are never nil", func(t *testing.T) {
		d := newDeps(t, map[string]any{settings.KeyPublicHost: "198.51.100.1"})
		d.Neighbors = func(context.Context, string, int, scan.Options) ([]scan.Result, int, error) { return nil, 253, nil }
		if got, err := d.Scan(ctx, ScanRequest{Node: local}); err != nil || got.Results == nil || len(got.Results) != 0 {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("the panel's own domain is checked when asked", func(t *testing.T) {
		d := newDeps(t, map[string]any{settings.KeyPublicHost: "198.51.100.1", settings.KeyDomain: "vpn.example.com", settings.KeyPanelPort: 1})
		d.Neighbors = neighbors
		var asked []nodeapi.TargetCheckRequest
		d.CheckTarget = func(_ context.Context, req nodeapi.TargetCheckRequest) (nodeapi.TargetResult, error) {
			asked = append(asked, req)
			return nodeapi.TargetResult{Dest: req.Dest, SNI: req.SNI, OK: true}, nil
		}
		got, err := d.Scan(ctx, ScanRequest{Node: local})
		if err != nil || got.SelfSteal != nil || len(asked) != 0 {
			t.Fatalf("not asked for: %+v %v %+v", got, err, asked)
		}
		got, err = d.Scan(ctx, ScanRequest{Node: local, SelfSteal: true})
		if err != nil || got.SelfSteal == nil || !got.SelfSteal.OK || len(asked) != 1 || asked[0].Dest != "127.0.0.1:"+strconv.Itoa(1) || asked[0].SNI != "vpn.example.com" {
			t.Fatalf("asked for: %+v %v %+v", got, err, asked)
		}
		// A remote node has no panel's domain: its own public address is scanned.
		asked = nil
		scanned = nil
		got, err = d.Scan(ctx, ScanRequest{Node: remote, SelfSteal: true})
		if err != nil || got.SelfSteal != nil || len(asked) != 0 || got.IP != "203.0.113.9" || scanned[0] != "203.0.113.9" {
			t.Fatalf("remote: %+v %v %+v %v", got, err, asked, scanned)
		}
	})
	t.Run("the domain stands in for an unset address", func(t *testing.T) {
		d := newDeps(t, map[string]any{settings.KeyDomain: "198.51.100.2"})
		d.Neighbors = neighbors
		if got, err := d.Scan(ctx, ScanRequest{Node: local}); err != nil || got.IP != "198.51.100.2" {
			t.Fatalf("%+v %v", got, err)
		}
	})
	t.Run("no address", func(t *testing.T) {
		d := newDeps(t, nil)
		d.Neighbors = neighbors
		_, err := d.Scan(ctx, ScanRequest{Node: local})
		var noIP *NoIPError
		if !errors.As(err, &noIP) || !errors.Is(err, ErrNoAddress) {
			t.Fatalf("%v", err)
		}
		_, err = d.Scan(ctx, ScanRequest{Node: db.Node{ID: 3, Address: "x:1"}})
		if !errors.Is(err, ErrNoAddress) {
			t.Fatalf("a node without a host: %v", err)
		}
	})
	t.Run("not IPv4", func(t *testing.T) {
		d := newDeps(t, map[string]any{settings.KeyPublicHost: "2001:db8::1"})
		d.Neighbors = neighbors
		_, err := d.Scan(ctx, ScanRequest{Node: local})
		var noIP *NoIPError
		if !errors.As(err, &noIP) || !errors.Is(err, scan.ErrNotIPv4) || noIP.Error() != "2001:db8::1 is not an IPv4 address" {
			t.Fatalf("%v", err)
		}
	})
}

// The panel's own scan reaches only public networks: a server whose address is internal is
// not scanned around, the way the CLI used to (Any) until it shared this code.
func TestScanRefusesAnInternalNeighborhood(t *testing.T) {
	d := newDeps(t, map[string]any{settings.KeyPublicHost: "127.0.0.1"})
	_, err := d.Scan(context.Background(), ScanRequest{Node: db.Node{ID: 1}})
	var noIP *NoIPError
	if !errors.As(err, &noIP) || noIP.Host != "127.0.0.1" {
		t.Fatalf("loopback was scanned: %v", err)
	}
}
