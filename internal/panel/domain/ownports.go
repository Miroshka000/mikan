package domain

import (
	"errors"
	"math"
	"slices"
	"strconv"
)

// The core's egress guard keeps a node's users off the server's own addresses, where a
// firewall that closes a port from outside does not stop them (internal/node/guard.go). The
// panel keeps the ports of its clients open there by itself (nodesync), and the admin adds
// the ones of a service of their own on that server (nodes.own_ports): a DNS, a monitoring.

// MaxOwnPorts is how many ports the admin may add for a node's server. The guard goes through
// them, each on every address of the server, for every connection and UDP packet.
const MaxOwnPorts = 16

// ErrManyOwnPorts: more ports than MaxOwnPorts.
var ErrManyOwnPorts = errors.New("own_ports_many")

// OwnPortError is an entry of the list that is no port.
type OwnPortError struct{ Value int }

func (e *OwnPortError) Error() string { return "own_port_invalid: " + strconv.Itoa(e.Value) }

// NormalizeOwnPorts checks the ports the admin added and writes them the one way: sorted,
// each one there once.
func NormalizeOwnPorts(in []int) ([]int64, error) {
	out := make([]int64, 0, len(in))
	for _, p := range in {
		if p < 1 || p > math.MaxUint16 {
			return nil, &OwnPortError{Value: p}
		}
		out = append(out, int64(p))
	}
	slices.Sort(out)
	out = slices.Compact(out)
	if len(out) > MaxOwnPorts {
		return nil, ErrManyOwnPorts
	}
	return out, nil
}
