package nodesync

import (
	"context"
	"database/sql"
	"errors"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/turn"
)

// turn is the node's way in through VK calls, nil when it has none, when it is off, or
// when the inbound it hands traffic to is gone or off: a server with nothing to hand
// traffic to would only look like it works.
func (s *Syncer) turn(ctx context.Context, n db.Node, inbounds []db.Inbound) (*nodeapi.Turn, error) {
	row, err := s.m.st.Q.GetNodeTurn(ctx, n.ID)
	if errors.Is(err, sql.ErrNoRows) || err == nil && row.Enabled == 0 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	cfg := turn.FromRow(row)
	for _, in := range inbounds {
		if in.ID != cfg.InboundID || in.NodeID != n.ID || in.Enabled == 0 {
			continue
		}
		server, err := cfg.Server(in)
		if err != nil {
			s.log.Warn("turn: the inbound it points at cannot be served", "inbound", in.Name, "err", err)
			return nil, nil
		}
		return server, nil
	}
	return nil, nil
}
