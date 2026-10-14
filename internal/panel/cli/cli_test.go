package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/store/storetest"
)

// The host script opens the new inbound's port in ufw from stdout, so it must be bare.
func TestInboundAddPrintsPortForHostScript(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := domain.Seed(ctx, st, time.Now()); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := inboundCmd(ctx, st, []string{"add", "anytls"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if out.String() != "2083/tcp\n" {
		t.Fatalf("stdout must be port/network, got %q", out.String())
	}
	if err := inboundCmd(ctx, st, []string{"add", "anytls", "--port", "2083"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "anytls") {
		t.Fatalf("a taken port named by the admin must name the owner: %v", err)
	}
	// With no port named, the second one takes a free port of the pool, and the host
	// script opens that one.
	out.Reset()
	if err := inboundCmd(ctx, st, []string{"add", "anytls"}, &out, &errOut); err != nil || out.String() == "2083/tcp\n" || !strings.HasSuffix(out.String(), "/tcp\n") {
		t.Fatalf("the second anytls: %v %q", err, out.String())
	}
	out.Reset()
	if err := inboundCmd(ctx, st, []string{"list"}, &out, &errOut); err != nil || !strings.Contains(out.String(), "2083/tcp") {
		t.Fatalf("list: %v %q", err, out.String())
	}
}

// A moved inbound's port goes to stdout for ufw on this server only; a remote node's port
// is opened on that node's server, so the panel's host script must not open it here.
func TestInboundSetPrintsPortOnlyForOwnNode(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	if err := domain.Seed(ctx, st, now); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := inboundCmd(ctx, st, []string{"set", "vless-xhttp", "--port", "2443"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if out.String() != "2443/tcp\n" || !strings.Contains(errOut.String(), "443 → 2443/tcp") {
		t.Fatalf("own node: stdout %q, stderr %q", out.String(), errOut.String())
	}

	n, err := st.Q.CreateNode(ctx, db.CreateNodeParams{Name: "🇺🇸 США", Address: "203.0.113.7:25305", PublicHost: "203.0.113.7", CreatedAt: now.Unix(), UpdatedAt: now.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	node := strconv.FormatInt(n.ID, 10)
	for _, args := range [][]string{
		{"add", "hysteria2", "--node", node, "--port", "2443"},
		{"set", "hysteria2", "--node", node, "--port", "3443"},
	} {
		out.Reset()
		errOut.Reset()
		if err := inboundCmd(ctx, st, args, &out, &errOut); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if out.Len() != 0 || !strings.Contains(errOut.String(), "ufw allow "+args[len(args)-1]+"/udp") {
			t.Fatalf("%v: remote node must not print a rule for this server: stdout %q, stderr %q", args, out.String(), errOut.String())
		}
	}
	if err := inboundCmd(ctx, st, []string{"set", "nope", "--port", "3000"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown inbound: %v", err)
	}
}

// The host `mikan` script of every installed version waits for the panel after an update
// with path=$(admin url | sed -E 's#https?://[^/]+/##'); 0.1.2 broke it by printing
// "Адрес: ..." and the login on stdout, and every update rolled back.
func TestURLStdoutParsesInHostScript(t *testing.T) {
	var out, errOut bytes.Buffer
	printURL(&out, &errOut, "https://vpn.example.com:21355/Abc123secret/", "k7x2m9qfa4tw")
	if out.String() != "https://vpn.example.com:21355/Abc123secret/\n" {
		t.Fatalf("stdout must be the bare link, got %q", out.String())
	}
	path := regexp.MustCompile(`https?://[^/]+/`).ReplaceAllString(strings.TrimSpace(out.String()), "")
	if path != "Abc123secret/" {
		t.Fatalf("host script would request %q", path)
	}
	if !strings.Contains(errOut.String(), "k7x2m9qfa4tw") {
		t.Fatalf("the login still shows in the terminal: %q", errOut.String())
	}
}

// The installer bootstraps the panel in the language picked first; the panel then opens
// in it and names its defaults in it.
func TestBootstrapStoresDefaultLang(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	set := settings.New(st.Q)
	args := []string{"--public-host", "203.0.113.10", "--port", "21355", "--password-stdin"}
	pw := strings.NewReader("correct-horse-battery\n")
	var out bytes.Buffer
	if err := bootstrap(ctx, st, set, append(args, "--lang", "de"), pw, &out); err == nil || !strings.Contains(err.Error(), "--lang") {
		t.Fatalf("an unknown language: %v", err)
	}
	if err := bootstrap(ctx, st, set, append(args, "--lang", "en"), pw, &out); err != nil {
		t.Fatal(err)
	}
	if lang, err := set.Lang(ctx); err != nil || lang != "en" {
		t.Fatalf("default language %q %v", lang, err)
	}
	if !strings.Contains(out.String(), "https://203.0.113.10:21355/") || strings.Contains(out.String(), "correct-horse-battery") {
		t.Fatalf("output: %q", out.String())
	}
}

// The shell takes the same hosts as the admin panel (internal/hostname): bootstrap, node
// add and node set alike.
func TestHostsAreChecked(t *testing.T) {
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
	var out, errOut bytes.Buffer
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--public-host", "vpn example.com", "--port", "21355"}, "--public-host"},
		{[]string{"--public-host", "203.0.113.10", "--port", "21355", "--domain", "-vpn.example.com"}, "--domain"},
	} {
		if err := bootstrap(ctx, st, set, append(c.args, "--password-stdin"), strings.NewReader("correct-horse-battery\n"), &out); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("bootstrap %v: %v", c.args, err)
		}
	}
	// The shell asks public DNS where a node's domain leads, as the panel does.
	records := map[string]string{"b.example.com": "198.51.100.20", "other.example.com": "198.51.100.7"}
	doh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ans := map[string]any{"Status": 3, "Answer": []any{}}
		if ip, ok := records[r.URL.Query().Get("name")]; ok {
			ans["Status"] = 0
			if r.URL.Query().Get("type") == "A" {
				ans["Answer"] = []any{map[string]any{"type": 1, "data": ip}}
			}
		}
		_ = json.NewEncoder(w).Encode(ans)
	}))
	defer doh.Close()
	prev := nodeDNS
	nodeDNS = &dnscheck.Checker{Resolvers: []string{doh.URL}, HTTP: http.DefaultClient}
	defer func() { nodeDNS = prev }()
	data := t.TempDir()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"add", "--name", "B", "--host", "198.51.100.20/24"}, "--host"},
		{[]string{"add", "--name", "B", "--host", "198.51.100.20", "--domain", "b"}, "--domain"},
		{[]string{"add", "--name", "C", "--host", "198.51.100.30", "--domain", "other.example.com"}, "does not lead to the node"},
		{[]string{"add", "--name", "proxy", "--host", "198.51.100.30"}, "--name: this is the name of a group"},
	} {
		if err := nodeCmd(ctx, st, data, c.args, &out, &errOut); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("node %v: %v", c.args, err)
		}
	}
	if err := nodeCmd(ctx, st, data, []string{"add", "--name", "B", "--host", "198.51.100.20", "--api-port", "40000", "--domain", "b.example.com"}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	// A name is one node's: the panel compares them without regard to case.
	if err := nodeCmd(ctx, st, data, []string{"add", "--name", "b", "--host", "198.51.100.30"}, &out, &errOut); err == nil || !strings.Contains(err.Error(), "--name: another node has this name") {
		t.Errorf("a second B: %v", err)
	}
	nodes, err := st.Q.ListNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b := nodes[len(nodes)-1]
	id := strconv.FormatInt(b.ID, 10)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"set", id, "--host", "b..example.com"}, "--host"},
		{[]string{"set", id, "--domain", "b.example.123"}, "--domain"},
		{[]string{"set", id, "--domain", "other.example.com"}, "does not lead to the node (198.51.100.7 → 198.51.100.20)"},
		{[]string{"set", id, "--domain", "nowhere.example.com"}, "no A or AAAA record"},
		{[]string{"set", id, "--name", "PROXY"}, "--name: this is the name of a group"},
		{[]string{"set", id, "--host", "198.51.100.99"}, "does not lead to the node"},
	} {
		if err := nodeCmd(ctx, st, data, c.args, &out, &errOut); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("node %v: %v", c.args, err)
		}
	}
	if err := nodeCmd(ctx, st, data, []string{"set", id, "--host", " 198.51.100.21 ", "--domain", ""}, &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if n, err := st.Q.GetNode(ctx, b.ID); err != nil || n.PublicHost != "198.51.100.21" || n.Address != "198.51.100.21:40000" {
		t.Fatalf("moved: %+v %v", n, err)
	}
}

// The backup holds secrets: it is never readable by others, not even for a moment.
func TestBackupIsPrivateAndConsistent(t *testing.T) {
	ctx := context.Background()
	st, err := storetest.Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := domain.Seed(ctx, st, time.Now()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "backup.db")
	dsn := schemaDSN(t, st)
	if err := databaseBackup(ctx, dsn, path); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Size() == 0 || fi.Mode().Perm() != 0o600 {
		t.Fatalf("backup file: %v %v", fi, err)
	}
	// An existing backup is not overwritten, and is left as it was.
	before, _ := os.ReadFile(path)
	if err := databaseBackup(ctx, dsn, path); err == nil {
		t.Fatal("a second backup over the first")
	}
	if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
		t.Fatal("the existing backup was changed")
	}
	// A bad path leaves nothing behind.
	if err := databaseBackup(ctx, dsn, filepath.Join(t.TempDir(), "no", "dir", "x.db")); err == nil {
		t.Fatal("a path that cannot be written")
	}
}

// The health check takes any answer of the panel but a server error: the root is a 404, or
// the admin's own site with its 200, and a panel with a site was called unhealthy.
func TestHealthProbeTakesTheOwnSite(t *testing.T) {
	for status, ok := range map[int]bool{http.StatusNotFound: true, http.StatusOK: true, http.StatusBadGateway: false} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) }))
		err := probe(srv.Client(), srv.URL+"/")
		srv.Close()
		if (err == nil) != ok {
			t.Errorf("status %d: %v", status, err)
		}
	}
}
