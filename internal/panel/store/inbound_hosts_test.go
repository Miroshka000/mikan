package store

import (
	"context"
	"io/fs"
	"strconv"
	"strings"
	"testing"

	"mikan/internal/panel/store/db"
)

// Migrations 0063 and 64 give every inbound a default host: its template's mikan.client, the
// five fields subscriptions read of it, and an empty one when the template sets none or does
// not parse. Run again, the move writes the same rows; going down takes the default hosts
// away and then the table, and up again brings the same hosts back.
func TestInboundHostsMigration(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := postgresProvider(ctx, s.DB, postgresFS)
	if err != nil {
		t.Fatal(err)
	}
	const table, move = 63, 64
	before := int64(0)
	for _, src := range p.ListSources() {
		if src.Version < table {
			before = src.Version
		}
	}
	if _, err := p.DownTo(ctx, before); err != nil {
		t.Fatal(err)
	}
	exists := func() bool {
		t.Helper()
		var ok bool
		if err := s.DB.QueryRowContext(ctx, "SELECT to_regclass('inbound_hosts') IS NOT NULL").Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if exists() {
		t.Fatal("inbound_hosts after the rollback")
	}
	if _, err := s.DB.ExecContext(ctx, "INSERT INTO nodes(id,name,address,created_at,updated_at) VALUES(7,'n','198.51.100.7:40000',1,1)"); err != nil {
		t.Fatal(err)
	}
	const (
		client = "type: vless\nws-path: /ws\nmikan:\n    tls: node\n    client:\n        server: cdn.example.com\n        port: 8443\n        sni: front.example.com\n        host: h.example.com\n        fingerprint: firefox\n"
		none   = "type: vless\nws-path: /ws\nmikan:\n    tls: node\n"
		// Written by hand, in flow style: what subscriptions read of it counts, not its look.
		flow   = "{type: trojan, ws-path: /t, mikan: {tls: node, client: {server: 203.0.113.9}}}"
		broken = "type: [vless"
	)
	for id, config := range map[int]string{71: client, 72: none, 73: flow, 74: broken} {
		if _, err := s.DB.ExecContext(ctx, "INSERT INTO inbounds(id,node_id,name,preset,port,config,created_at,updated_at) VALUES($1,7,$2,'custom','443',$3,1,1)", id, "in"+string(rune('0'+id-70)), config); err != nil {
			t.Fatal(err)
		}
	}
	want := map[int64]db.InboundHost{
		71: {InboundID: 71, Address: "cdn.example.com", Port: 8443, Sni: "front.example.com", Host: "h.example.com", Fingerprint: "firefox"},
		72: {InboundID: 72},
		73: {InboundID: 73, Address: "203.0.113.9"},
		74: {InboundID: 74},
	}
	check := func(when string) {
		t.Helper()
		q := db.New(s.DB)
		got, err := q.ListHostsOfInbounds(ctx, []int64{71, 72, 73, 74})
		if err != nil {
			t.Fatal(err)
		}
		var defaults []db.InboundHost
		for _, h := range got {
			if h.Position == 0 {
				defaults = append(defaults, h)
			}
		}
		if len(defaults) != len(want) {
			t.Fatalf("%s: %d default hosts, want %d: %+v", when, len(defaults), len(want), got)
		}
		for _, h := range defaults {
			w := want[h.InboundID]
			w.ID = h.ID
			if h != w {
				t.Errorf("%s: inbound %d: %+v, want %+v", when, h.InboundID, h, w)
			}
		}
	}
	if _, err := p.UpTo(ctx, move); err != nil {
		t.Fatal(err)
	}
	check("after the move")

	// The move run again over the same rows changes nothing.
	m := hostsMigration()
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.UpFnContext(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	check("after the move ran twice")

	// Going down past the move leaves the table and its other hosts; up again, with the
	// other host still there, the same default hosts come back beside it.
	if _, err := s.DB.ExecContext(ctx, "INSERT INTO inbound_hosts(inbound_id,position,address) VALUES(71,1,'second.example.com')"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.DownTo(ctx, table); err != nil {
		t.Fatal(err)
	}
	var left []string
	if err := func() error {
		rows, err := s.DB.QueryContext(ctx, "SELECT address FROM inbound_hosts ORDER BY id")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a string
			if err := rows.Scan(&a); err != nil {
				return err
			}
			left = append(left, a)
		}
		return rows.Err()
	}(); err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0] != "second.example.com" {
		t.Fatalf("below the move: %q, want only the other host", left)
	}
	if _, err := p.UpTo(ctx, move); err != nil {
		t.Fatal(err)
	}
	check("up again")
	var second string
	if err := s.DB.QueryRowContext(ctx, "SELECT address FROM inbound_hosts WHERE inbound_id = 71 AND position = 1").Scan(&second); err != nil || second != "second.example.com" {
		t.Fatalf("the other host up again: %q %v", second, err)
	}

	if _, err := p.DownTo(ctx, before); err != nil {
		t.Fatal(err)
	}
	if exists() {
		t.Fatal("inbound_hosts after going down")
	}
	var config string
	if err := s.DB.QueryRowContext(ctx, "SELECT config FROM inbounds WHERE id = 71").Scan(&config); err != nil || config != client {
		t.Fatalf("the template after going down: %q %v", config, err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
}

// The frozen reader of migration 64 reads a template as proto did when it was written: the
// same five fields as DefaultHost for every shape of mikan.client the panel has seen.
func TestFrozenClientReadsAsProto(t *testing.T) {
	for _, config := range []string{
		"type: vless\nmikan:\n    client:\n        server: a.example.com\n        port: 8443\n        sni: s.example.com\n        host: h.example.com\n        fingerprint: ios\n",
		"{type: trojan, mikan: {client: {server: 203.0.113.9, port: 2053.0}}}",
		"type: vless\nmikan:\n    client:\n        port: 70000\n        server: 7\n",
		"type: vless\nmikan:\n    client:\n        port: \"443\"\n        sni: s.example.com\n",
		"type: vless\nmikan:\n    tls: node\n",
		"type: vless\nmikan: on\n",
		"type: [vless",
		"",
	} {
		w := DefaultHost(1, config)
		address, port, sni, host, fp := frozenClient(config)
		got := db.SetDefaultInboundHostParams{InboundID: 1, Address: address, Port: port, Sni: sni, Host: host, Fingerprint: fp}
		if got != w {
			t.Errorf("%q: frozen %+v, proto %+v", config, got, w)
		}
	}
}

// The binary's latest migration counts the Go ones beside the SQL files: a database migrated
// up stands at LatestMigration, which is past the last SQL file while a Go migration is the
// newest, and a check that counted the files alone would take the schema for a newer one.
func TestLatestMigrationCountsGoMigrations(t *testing.T) {
	ctx := context.Background()
	s, err := OpenTest(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	latest, err := LatestMigration(ctx, s.DB)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := schemaVersion(ctx, s.DB)
	if err != nil || applied != latest {
		t.Fatalf("migrated to %d (%v), latest migration %d", applied, err, latest)
	}
	files, err := fs.Glob(postgresFS, "*.sql")
	if err != nil || len(files) == 0 {
		t.Fatal(files, err)
	}
	var lastFile int64
	for _, f := range files {
		num, _, _ := strings.Cut(f, "_")
		v, err := strconv.ParseInt(num, 10, 64)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		lastFile = max(lastFile, v)
	}
	want := lastFile
	for _, m := range goMigrations() {
		want = max(want, m.Version)
	}
	if latest != want {
		t.Fatalf("latest migration %d, want %d (last SQL file %d)", latest, want, lastFile)
	}
}
