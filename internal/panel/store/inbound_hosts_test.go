package store

import (
	"context"
	"testing"

	"mikan/internal/panel/store/db"
)

// Migrations 0062 and 63 give every inbound a default host: its template's mikan.client, the
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
	const table, move = 62, 63
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
		got, err := q.ListAllInboundHosts(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("%s: %d hosts, want %d: %+v", when, len(got), len(want), got)
		}
		for _, h := range got {
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

	// Going down past the move leaves the table and its other hosts; up again, the same
	// default hosts come back.
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
	if _, err := s.DB.ExecContext(ctx, "DELETE FROM inbound_hosts"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, move); err != nil {
		t.Fatal(err)
	}
	check("up again")

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
