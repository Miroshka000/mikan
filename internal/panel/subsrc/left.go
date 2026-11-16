package subsrc

import (
	"mikan/internal/panel/store/db"
)

// Why a server of the panel is not in a client's subscription (Left.Reason): what reach and
// the hosts of the inbounds it gives leave out, as a preview of the subscription tells it.
const (
	LeftOff       = "inbound_off"   // the protocol is switched off
	LeftNodeOff   = "node_off"      // its node is switched off
	LeftOwn       = "not_own"       // the card's own list of protocols leaves it out
	LeftGroup     = "not_in_group"  // the client's group's own servers leave it out
	LeftTariff    = "not_in_tariff" // the client's tariff's servers leave it out
	LeftPool      = "pool_shut"     // its server group is closed for the client, or its traffic ran out
	LeftHidden    = "hidden"        // the client is shown no address of its node
	LeftBalance   = "balance"       // its node's balance group keeps the client on another node
	LeftHostState = "host_state"    // the address goes to other states of a subscription
	LeftHostTrial = "host_trial"    // the address goes to the trial alone, or to everyone but it
	LeftHostGroup = "host_group"    // the address goes to other groups
)

// Left is a server of the panel a client's subscription leaves out, with why (Left*): an
// inbound whole (Host 0) or one of its addresses for clients, with that address and its own
// name ("" for the node's address, the inbound's name).
type Left struct {
	Inbound  db.Inbound
	Host     int64
	Address  string
	HostName string
	Reason   string
}

// leftWhy is why reach leaves in out of r: the first of its checks in turns down. on are the
// enabled nodes, spent the server groups shut for the client, dark the nodes where it is
// shown no address.
func leftWhy(in db.Inbound, r reach, on, spent, dark map[int64]bool) string {
	switch {
	case in.Enabled == 0:
		return LeftOff
	case !on[in.NodeID]:
		return LeftNodeOff
	case !r.access.Allows(in) && r.access.Own():
		return LeftOwn
	case !r.access.Allows(in) && r.byGroup:
		return LeftGroup
	case !r.access.Allows(in):
		return LeftTariff
	case in.PoolID.Valid && spent[in.PoolID.Int64]:
		return LeftPool
	case dark[in.NodeID]:
		return LeftHidden
	}
	return LeftBalance
}
