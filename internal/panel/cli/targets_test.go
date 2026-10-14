package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/config"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/storetest"
	"mikan/internal/panel/targets"
	"mikan/internal/scan"
)

// The installer's SNI step: `targets scan --json` lists sites next to the server and the
// current targets, `targets apply --all` points every REALITY inbound at the chosen one.
func TestTargetsScanAndApply(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := domain.Seed(ctx, st, time.Now()); err != nil {
		t.Fatal(err)
	}
	set := settings.New(st.Q)
	// Nothing listens around 127.0.0.1 here: the scan finds no sites and the panel's own
	// domain is not ready. No node runs either, so the panel scans by itself. The real scan
	// refuses a loopback address and would open 250 connections to a public one, so a fake
	// stands in.
	was := scanNeighbors
	scanNeighbors = func(_ context.Context, ip string, limit int, o scan.Options) ([]scan.Result, int, error) {
		if ip != "127.0.0.1" || limit != targets.ScanLimit || o.Any {
			t.Errorf("scan of %s, limit %d, options %+v", ip, limit, o)
		}
		return nil, 253, nil
	}
	defer func() { scanNeighbors = was }()
	for k, v := range map[string]any{settings.KeyPublicHost: "127.0.0.1", settings.KeyPanelPort: 1, settings.KeyDomain: "vpn.example.com"} {
		if err := settings.Set(ctx, set, k, v); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) (string, string, error) {
		var out, errOut bytes.Buffer
		err := targetsCmd(ctx, st, set, config.Config{}, args, &out, &errOut)
		return out.String(), errOut.String(), err
	}

	out, _, err := run("scan", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var res TargetScan
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if res.Node != 1 || res.IP != "127.0.0.1" || res.FromNode || res.Scanned != 253 || res.Results == nil || len(res.Results) != 0 {
		t.Fatalf("scan: %+v", res)
	}
	if s := res.SelfSteal; s == nil || s.OK || s.Dest != "127.0.0.1:1" || s.SNI != "vpn.example.com" || s.Error != "refused" {
		t.Fatalf("self-steal: %+v", s)
	}
	if len(res.Current) != 2 || res.Current[0].Inbound != "vless-xhttp" || res.Current[0].Dest != presets.DefaultDest {
		t.Fatalf("the seeded REALITY inbounds: %+v", res.Current)
	}
	if out, _, err := run("scan"); err != nil || !strings.Contains(out, "vpn.example.com on 127.0.0.1:1: not ready: connection refused") {
		t.Fatalf("table: %v\n%s", err, out)
	}

	// The installer polls the panel's own domain until its certificate is there.
	if out, _, err := run("check", "--dest", "127.0.0.1:1", "--sni", "vpn.example.com", "--json"); err != nil || !strings.Contains(out, `"error":"refused"`) {
		t.Fatalf("check --json: %v %s", err, out)
	}
	if _, _, err := run("check", "--dest", "127.0.0.1:1", "--sni", "vpn.example.com"); err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("check: %v", err)
	}

	// The site is checked before anything changes.
	if _, _, err := run("apply", "--all", "--dest", "127.0.0.1:9", "--sni", "www.example.org"); err == nil || !strings.Contains(err.Error(), "does not suit REALITY: an internal address") {
		t.Fatalf("a dead site: %v", err)
	}
	out, errOut, err := run("apply", "--all", "--dest", "203.0.113.20:443", "--sni", "www.example.org", "--force")
	if err != nil || out != "" || !strings.Contains(errOut, "vless-xhttp: www.microsoft.com:443 → 203.0.113.20:443 (www.example.org)") || !strings.Contains(errOut, "vless-vision:") {
		t.Fatalf("apply: %v\nstdout %q\nstderr %s", err, out, errOut)
	}
	now, err := realityTargets(ctx, st, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range now {
		if x.Dest != "203.0.113.20:443" || x.SNI != "www.example.org" {
			t.Fatalf("not applied: %+v", now)
		}
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"apply", "--all", "--dest", "203.0.113.30:443", "--force"}, "--sni"},
		{[]string{"apply", "--inbound", "hysteria2", "--dest", "www.example.org:443", "--force"}, "no REALITY"},
		{[]string{"apply", "--inbound", "nope", "--dest", "www.example.org:443", "--force"}, `"nope"`},
		{[]string{"apply", "--all", "--inbound", "vless-xhttp", "--dest", "www.example.org:443"}, "usage"},
		{[]string{"apply", "--all", "--dest", "www.example.org:443", "--node", "9"}, "no node 9"},
	} {
		if _, _, err := run(c.args...); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: %v, want %q", c.args, err, c.want)
		}
	}
}
