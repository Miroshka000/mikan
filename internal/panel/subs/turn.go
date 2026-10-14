package subs

import (
	"fmt"
	"strings"

	"mikan/internal/panel/store/db"
)

// TurnSubscription renders the other half of the WL servers: what the program running
// next to the user's apps needs (the vk-turn-proxy client). For every node with the
// VK-call way in on it names the node, the VK call to join, where the program listens and
// how. The subscription proper carries the matching entries: the same inbound listed at
// the client's own address, named by the admin.
//
// The format is a plain text of "## node" sections with key: value lines: no client reads
// it, a person copies it into the fields of the client app.
func TurnSubscription(p Profile) string {
	var b strings.Builder
	b.WriteString("# vk turn\n")
	any := false
	for _, n := range p.Nodes {
		tc, ok := p.Turn[n.ID]
		if !ok {
			continue
		}
		in, ok := mirrorOf(p.Inbounds, tc.InboundID)
		if !ok {
			continue
		}
		mode, err := tc.Mode(in)
		if err != nil {
			continue
		}
		any = true
		fmt.Fprintf(&b, "\n## %s\n", n.Name)
		fmt.Fprintf(&b, "peer: %s\n", tc.Peer(n.Endpoint.Host))
		if link := tc.Link(p.UserID); link != "" {
			fmt.Fprintf(&b, "vk: %s\n", link)
		}
		fmt.Fprintf(&b, "listen: %s\n", tc.Listen())
		fmt.Fprintf(&b, "mode: %s\n", mode)
		fmt.Fprintf(&b, "n: %d\n", tc.Streams)
		if tc.UDP {
			b.WriteString("udp: on\n")
		}
	}
	if !any {
		return ""
	}
	return b.String()
}

// mirrorOf is the inbound a node's turn config hands traffic to, if the user may use it.
func mirrorOf(ins []db.Inbound, id int64) (db.Inbound, bool) {
	for _, in := range ins {
		if in.ID == id {
			return in, true
		}
	}
	return db.Inbound{}, false
}
