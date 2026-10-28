package domain

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// The hosts of an inbound (#01) are the places its clients reach it at: an address, a port,
// a TLS name, an HTTP Host and a fingerprint, each empty (port 0) for what clients get
// without it. They go in the order of their position. Position 0 is the default host: until
// subscriptions read the hosts, they read the template's mikan.client, so that host is the
// template's, written only through it. Every write of a template writes it too, in the same
// transaction (syncDefaultHost); a change of the default host is a change of the template
// (UpdateHost). The other hosts are kept for the subscriptions to come and change nothing yet.

var (
	ErrUnknownHost = errors.New("host_not_found")
	// ErrDefaultHost: the default host goes only with its inbound.
	ErrDefaultHost = errors.New("default_host")
)

// Host is a host as the admin sets it.
type Host struct {
	Address     string // "": the node's address
	Port        int    // 0: the inbound's port
	SNI         string // "": the usual TLS name
	Host        string // "": the server's HTTP Host (WebSocket, XHTTP)
	Fingerprint string // "": the panel's default uTLS profile
}

// createInbound adds an inbound with its default host.
func createInbound(ctx context.Context, q *db.Queries, p db.CreateInboundParams) (db.Inbound, error) {
	row, err := q.CreateInbound(ctx, p)
	if err != nil {
		return row, err
	}
	return row, syncDefaultHost(ctx, q, row)
}

// syncDefaultHost makes the default host of in what its template says.
func syncDefaultHost(ctx context.Context, q *db.Queries, in db.Inbound) error {
	return q.SetDefaultInboundHost(ctx, store.DefaultHost(in.ID, in.Config))
}

// Hosts lists the hosts of inbound id, the default one first.
func (s *Inbounds) Hosts(ctx context.Context, id int64) ([]db.InboundHost, error) {
	if _, err := s.inbound(ctx, s.st.Q, id); err != nil {
		return nil, err
	}
	return s.st.Q.ListInboundHosts(ctx, id)
}

// AddHost adds a host after the others of inbound id.
func (s *Inbounds) AddHost(ctx context.Context, id int64, h Host) (db.InboundHost, error) {
	h = h.trimmed()
	var row db.InboundHost
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		in, err := s.inbound(ctx, q, id)
		if err != nil {
			return err
		}
		if err := checkHost(in.Config, h); err != nil {
			return err
		}
		if _, err := q.LockInbound(ctx, id); err != nil {
			return err
		}
		row, err = q.AddInboundHost(ctx, db.AddInboundHostParams{InboundID: id, Address: h.Address, Port: int64(h.Port), Sni: h.SNI, Host: h.Host, Fingerprint: h.Fingerprint})
		return err
	})
	return row, err
}

// UpdateHost replaces host hostID of inbound id. The default host changes as the template
// does, through Update: the inbound's form and this one check and write the same values.
func (s *Inbounds) UpdateHost(ctx context.Context, id, hostID int64, h Host) (db.InboundHost, error) {
	h = h.trimmed()
	cur, err := s.inboundHost(ctx, s.st.Q, id, hostID)
	if err != nil {
		return db.InboundHost{}, err
	}
	if cur.Position == 0 {
		in, err := s.inbound(ctx, s.st.Q, id)
		if err != nil {
			return db.InboundHost{}, err
		}
		var cdn *proto.CDN
		if t, err := proto.Parse(in.Config); err == nil {
			cdn = proto.CDNOf(t)
		}
		p := InboundPatch{Client: &ClientEndpoint{Server: h.Address, Port: h.Port, SNI: h.SNI, Host: h.Host, CDN: cdn}}
		if h.Fingerprint != cur.Fingerprint {
			p.Fingerprint = &h.Fingerprint
		}
		if _, _, err := s.Update(ctx, id, p); err != nil {
			return db.InboundHost{}, hostError(err)
		}
		return s.inboundHost(ctx, s.st.Q, id, hostID)
	}
	var row db.InboundHost
	err = s.st.Tx(ctx, func(q *db.Queries) error {
		in, err := s.inbound(ctx, q, id)
		if err != nil {
			return err
		}
		if err := checkHost(in.Config, h); err != nil {
			return err
		}
		row, err = q.UpdateInboundHost(ctx, db.UpdateInboundHostParams{ID: hostID, InboundID: id, Address: h.Address, Port: int64(h.Port), Sni: h.SNI, Host: h.Host, Fingerprint: h.Fingerprint})
		if errors.Is(err, sql.ErrNoRows) {
			return ErrUnknownHost
		}
		return err
	})
	return row, err
}

// DeleteHost removes host hostID of inbound id; the default host stays (ErrDefaultHost).
func (s *Inbounds) DeleteHost(ctx context.Context, id, hostID int64) (db.InboundHost, error) {
	var row db.InboundHost
	err := s.st.Tx(ctx, func(q *db.Queries) error {
		var err error
		if row, err = s.inboundHost(ctx, q, id, hostID); err != nil {
			return err
		}
		if row.Position == 0 {
			return ErrDefaultHost
		}
		_, err = q.DeleteInboundHost(ctx, db.DeleteInboundHostParams{ID: hostID, InboundID: id})
		return err
	})
	return row, err
}

func (s *Inbounds) inbound(ctx context.Context, q *db.Queries, id int64) (db.Inbound, error) {
	in, err := q.GetInbound(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return in, ErrUnknownInbound
	}
	return in, err
}

func (s *Inbounds) inboundHost(ctx context.Context, q *db.Queries, id, hostID int64) (db.InboundHost, error) {
	if _, err := s.inbound(ctx, q, id); err != nil {
		return db.InboundHost{}, err
	}
	h, err := q.GetInboundHost(ctx, db.GetInboundHostParams{ID: hostID, InboundID: id})
	if errors.Is(err, sql.ErrNoRows) {
		return h, ErrUnknownHost
	}
	return h, err
}

func (h Host) trimmed() Host {
	h.Address, h.SNI, h.Host, h.Fingerprint = strings.TrimSpace(h.Address), strings.TrimSpace(h.SNI), strings.TrimSpace(h.Host), strings.TrimSpace(h.Fingerprint)
	return h
}

// checkHost holds a host to the rules of the inbound's own client endpoint (config is its
// template): what the template's mikan.client could not hold, no host holds. A refusal is an
// *EditError named by the host's field.
func checkHost(config string, h Host) error {
	t, err := proto.Parse(config)
	if err != nil {
		return err
	}
	err = proto.SetClientEndpoint(t, h.Address, h.Port, h.SNI, h.Host)
	if err == nil && h.Fingerprint != "" {
		if !proto.UsesFingerprint(t) {
			err = &proto.Error{Code: "fingerprint_no_tls", Field: "mikan.client.fingerprint"}
		} else {
			err = proto.SetFingerprint(t, h.Fingerprint)
		}
	}
	var pe *proto.Error
	if errors.As(err, &pe) {
		return &EditError{Field: hostField(pe.Field), Err: pe}
	}
	return err
}

// hostError names a refusal of the template's client endpoint by the host's field.
func hostError(err error) error {
	var e *EditError
	if errors.As(err, &e) && (e.Field == "client" || e.Field == "fingerprint") {
		return &EditError{Field: hostField(e.Err.Field), Err: e.Err}
	}
	return err
}

// hostField is the host's field for a key of mikan.client.
func hostField(key string) string {
	switch k := strings.TrimPrefix(key, "mikan.client."); k {
	case "port", "sni", "host", "fingerprint":
		return k
	}
	return "address"
}
