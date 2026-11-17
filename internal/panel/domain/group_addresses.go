package domain

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"mikan/internal/panel/store/db"
)

// GroupAddress is where a group's clients reach an inbound in place of its default address
// (#290): each field set goes over what the default address gives them, and the rest stays.
// The inbound's other addresses for clients are left as they are: they say themselves who
// they are for (Host.GroupMode).
type GroupAddress struct {
	InboundID int64
	Address   string // "": the node's address
	Port      int    // 0: the inbound's port
	SNI       string // "": the usual TLS name
	Host      string // "": the server's HTTP Host (WebSocket, XHTTP)
	// Name is the proxy's name in the group's subscriptions, the variables of the profile
	// title in it as in a host's; "": the inbound's.
	Name string
	// CDN: a CDN in front shows clients its own certificate: the node's is not pinned.
	CDN bool
}

// MaxGroupAddresses bounds the addresses of one group: one an inbound, and a list an admin
// still reads.
const MaxGroupAddresses = 200

// Empty: the address changes nothing.
func (a GroupAddress) Empty() bool {
	return a.Address == "" && a.Port == 0 && a.SNI == "" && a.Host == "" && a.Name == "" && !a.CDN
}

func (a GroupAddress) trimmed() GroupAddress {
	a.Address, a.SNI, a.Host, a.Name = strings.TrimSpace(a.Address), strings.TrimSpace(a.SNI), strings.TrimSpace(a.Host), strings.TrimSpace(a.Name)
	return a
}

// LoadGroupAddresses is every group's addresses by group id, by inbound in each.
func LoadGroupAddresses(ctx context.Context, q *db.Queries) (map[int64][]GroupAddress, error) {
	rows, err := q.ListGroupHosts(ctx)
	if err != nil {
		return nil, err
	}
	out := map[int64][]GroupAddress{}
	for _, r := range rows {
		out[r.GroupID] = append(out[r.GroupID], GroupAddress{InboundID: r.InboundID, Address: r.Address, Port: int(r.Port), SNI: r.Sni, Host: r.Host, Name: r.Name, CDN: r.Cdn})
	}
	return out, nil
}

// SetGroupAddresses gives group id the addresses list on q's transaction, in place of the ones
// it had. Each is of an inbound there is, once, changes something and holds to the rules of the
// inbound's own client endpoint, as an address for clients does (checkHost). A refusal is a
// *FieldError at addresses.<i>.<field>.
func SetGroupAddresses(ctx context.Context, q *db.Queries, id int64, list []GroupAddress) error {
	if len(list) > MaxGroupAddresses {
		return &FieldError{Field: "addresses", Code: "addresses_many"}
	}
	seen := map[int64]bool{}
	clean := make([]GroupAddress, len(list))
	for i, a := range list {
		at := "addresses." + strconv.Itoa(i) + "."
		a = a.trimmed()
		if seen[a.InboundID] {
			return &FieldError{Field: at + "inbound_id", Code: "address_inbound_twice"}
		}
		seen[a.InboundID] = true
		if a.Empty() {
			return &FieldError{Field: at + "address", Code: "address_empty"}
		}
		in, err := q.GetInbound(ctx, a.InboundID)
		if errors.Is(err, sql.ErrNoRows) {
			return &FieldError{Field: at + "inbound_id", Code: "inbound_not_found"}
		}
		if err != nil {
			return err
		}
		if err := checkHost(in.Config, Host{Address: a.Address, Port: a.Port, SNI: a.SNI, Host: a.Host}); err != nil {
			var e *EditError
			if errors.As(err, &e) {
				return &FieldError{Field: at + e.Field, Code: e.Err.Code}
			}
			return err
		}
		clean[i] = a
	}
	if err := q.ClearGroupHosts(ctx, id); err != nil {
		return err
	}
	for _, a := range clean {
		if err := q.AddGroupHost(ctx, db.AddGroupHostParams{GroupID: id, InboundID: a.InboundID, Address: a.Address, Port: int64(a.Port), Sni: a.SNI, Host: a.Host, Name: a.Name, Cdn: a.CDN}); err != nil {
			return err
		}
	}
	return nil
}
