package domain

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"mikan/internal/panel/presets"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// CopyInbound is what may differ in a copy of an inbound; zero values keep the original's.
type CopyInbound struct {
	NodeID     int64  // 0: the original's node
	Port       string // "": the original's, or the first free one when it is taken
	Dest       string // REALITY: another target, host:port
	ServerName string // the name clients send with Dest when it is an IP
}

// Copy makes a new inbound from a set-up one: the same protocol and settings, on the same
// or another node, with keys of its own (presets.Rekey) and, when asked, another port or
// REALITY target. It is checked like any new inbound before it is saved. The copy starts
// direct to the internet, on all addresses, in the original's traffic pool, with its labels.
func (s *Inbounds) Copy(ctx context.Context, id int64, in CopyInbound) (db.Inbound, error) {
	src, err := s.st.Q.GetInbound(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return db.Inbound{}, ErrUnknownInbound
	}
	if err != nil {
		return db.Inbound{}, err
	}
	nodeID := in.NodeID
	if nodeID == 0 {
		nodeID = src.NodeID
	}
	node, err := s.node(ctx, nodeID)
	if err != nil {
		return db.Inbound{}, err
	}
	t, err := proto.Parse(src.Config)
	if err != nil {
		return db.Inbound{}, err
	}
	if err := presets.Rekey(t); err != nil {
		return db.Inbound{}, err
	}
	if dest := strings.TrimSpace(in.Dest); dest != "" {
		if err := presets.SetDest(t, dest, strings.TrimSpace(in.ServerName)); err != nil {
			var pe *proto.Error
			if errors.As(err, &pe) {
				return db.Inbound{}, &EditError{Field: "dest", Err: pe}
			}
			return db.Inbound{}, err
		}
	}
	port := strings.TrimSpace(in.Port)
	if port == "" {
		if port, err = s.defaultPort(ctx, node, src.Port, t.Network()); err != nil {
			return db.Inbound{}, err
		}
	}
	if !ValidPort(port) {
		return db.Inbound{}, ErrBadPort
	}
	config := proto.Marshal(t)
	if _, err := s.CheckTemplate(ctx, node, config, port); err != nil {
		return db.Inbound{}, err
	}
	var row db.Inbound
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		if err := CheckPort(ctx, q, node, port, t.Network(), PortHolder{}); err != nil {
			return err
		}
		existing, err := q.ListNodeInbounds(ctx, node.ID)
		if err != nil {
			return err
		}
		now := s.now().Unix()
		row, err = q.CreateInbound(ctx, db.CreateInboundParams{NodeID: node.ID, Name: FreeName(existing, src.Name), Preset: src.Preset, Port: port,
			Config: config, CreatedAt: now, UpdatedAt: now})
		if err != nil {
			return err
		}
		if src.PoolID.Valid {
			if err := q.SetInboundPool(ctx, db.SetInboundPoolParams{PoolID: src.PoolID, ID: row.ID}); err != nil {
				return err
			}
			row.PoolID = src.PoolID
		}
		if src.Tags != row.Tags {
			if err := q.SetInboundTags(ctx, db.SetInboundTagsParams{Tags: src.Tags, ID: row.ID}); err != nil {
				return err
			}
			row.Tags = src.Tags
		}
		return nil
	})
	return row, err
}
