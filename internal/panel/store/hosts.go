package store

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"

	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// hostsMigration is migration 63, the one in Go: the default host of every inbound is its
// template's mikan.client (DefaultHost), and only Go reads a template. Migration 0062 made
// the table, so no SQL file takes the number 63. Run again, it writes the same rows; going
// down takes the default hosts away and leaves the others, which 0062's own down drops.
func hostsMigration() *goose.Migration {
	return goose.NewGoMigration(63,
		&goose.GoFunc{RunTx: func(ctx context.Context, tx *sql.Tx) error {
			rows, err := tx.QueryContext(ctx, "SELECT id, config FROM inbounds")
			if err != nil {
				return err
			}
			var hosts []db.SetDefaultInboundHostParams
			for rows.Next() {
				var id int64
				var config string
				if err := rows.Scan(&id, &config); err != nil {
					rows.Close()
					return err
				}
				hosts = append(hosts, DefaultHost(id, config))
			}
			if err := rows.Close(); err != nil {
				return err
			}
			if err := rows.Err(); err != nil {
				return err
			}
			q := db.New(tx)
			for _, h := range hosts {
				if err := q.SetDefaultInboundHost(ctx, h); err != nil {
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

// DefaultHost is the default host of inbound id with template config: what its
// mikan.client sets of the five host fields, read the way subscriptions read it
// (proto.Template.Ext), so the row and the template say the same while both exist. A
// template that does not parse, or sets none of them, gives an empty host.
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
