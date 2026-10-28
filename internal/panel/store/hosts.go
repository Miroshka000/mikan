package store

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
	"go.yaml.in/yaml/v3"

	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// hostsMigration is migration 63, the one in Go: the default host of every inbound is its
// template's mikan.client, and only Go reads a template. Migration 0062 made the table, so
// no SQL file takes the number 63. Run again, it writes the same rows; going down takes the
// default hosts away and leaves the others, which 0062's own down drops.
//
// Frozen: it runs on old databases and on restored backups long after the code around it
// has moved on, so it uses nothing that may change with the code (the sqlc queries, proto):
// its SQL is written out and it reads the template with frozenClient. Leave it as it is; a
// later change of hosts is a migration of its own.
func hostsMigration() *goose.Migration {
	return goose.NewGoMigration(63,
		&goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error {
			rows, err := tx.QueryContext(ctx, "SELECT id, config FROM inbounds")
			if err != nil {
				return err
			}
			type host struct {
				inbound                       int64
				address, sni, hostname, print string
				port                          int64
			}
			var hosts []host
			for rows.Next() {
				var id int64
				var config string
				if err := rows.Scan(&id, &config); err != nil {
					rows.Close()
					return err
				}
				h := host{inbound: id}
				h.address, h.port, h.sni, h.hostname, h.print = frozenClient(config)
				hosts = append(hosts, h)
			}
			if err := rows.Close(); err != nil {
				return err
			}
			if err := rows.Err(); err != nil {
				return err
			}
			for _, h := range hosts {
				if _, err := tx.ExecContext(ctx, `INSERT INTO inbound_hosts (inbound_id, position, address, port, sni, host, fingerprint)
VALUES ($1, 0, $2, $3, $4, $5, $6)
ON CONFLICT (inbound_id) WHERE position = 0 DO UPDATE
SET address = excluded.address, port = excluded.port, sni = excluded.sni, host = excluded.host, fingerprint = excluded.fingerprint`,
					h.inbound, h.address, h.port, h.sni, h.hostname, h.print); err != nil {
					return err
				}
			}
			return nil
		}},
		&goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "DELETE FROM inbound_hosts WHERE position = 0")
			return err
		}})
}

// frozenClient reads the five host fields of mikan.client from a template as subscriptions
// read them when migration 63 was written (proto.Template.Ext then): a value of another
// type than the field's is left out, a port outside 1..65535 is none, and a template that
// does not parse has no client. Frozen with hostsMigration.
func frozenClient(config string) (address string, port int64, sni, host, fingerprint string) {
	var t map[string]any
	if yaml.Unmarshal([]byte(config), &t) != nil {
		return
	}
	ext, _ := t["mikan"].(map[string]any)
	c, _ := ext["client"].(map[string]any)
	str := func(k string) string {
		s, _ := c[k].(string)
		return s
	}
	p, ok := c["port"].(int)
	if f, isFloat := c["port"].(float64); isFloat && f == float64(int(f)) {
		p, ok = int(f), true // 8443.0: a number all the same to JSON
	}
	if ok && p > 0 && p <= 65535 {
		port = int64(p)
	}
	return str("server"), port, str("sni"), str("host"), str("fingerprint")
}

// DefaultHost is the default host of inbound id with template config: what its
// mikan.client sets of the five host fields, read the way subscriptions read it
// (proto.Template.Ext), so the row and the template say the same while both exist. A
// template that does not parse, or sets none of them, gives an empty host. The panel keeps
// the row with it (domain.syncDefaultHost); migration 63 has its own frozen copy.
func DefaultHost(id int64, config string) db.SetDefaultInboundHostParams {
	h := db.SetDefaultInboundHostParams{InboundID: id}
	t, err := proto.Parse(config)
	if err != nil {
		return h
	}
	c := t.Ext().Client
	h.Address, h.Sni, h.Host, h.Fingerprint = c.Server, c.SNI, c.Host, c.Fingerprint
	if c.Port > 0 && c.Port <= 65535 {
		h.Port = int64(c.Port)
	}
	return h
}
