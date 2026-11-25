package node

import (
	"golang.org/x/crypto/cryptobyte"
)

// A ClientHello as the SNI router reads it to tell REALITY inbounds that share a name apart:
// the handshake message joined from its TLS records, and the parts the REALITY check reads.

// helloState is how much of a ClientHello a buffer holds.
type helloState int

const (
	helloPart helloState = iota // the message goes on in records still to come
	helloDone                   // the whole handshake message is there
	helloBad                    // it is no ClientHello
)

// tlsHello is a parsed ClientHello.
type tlsHello struct {
	serverName string // the host name of server_name; "" none
	random     []byte
	sessionID  []byte
	keyShares  []keyShare
	// aad is the message with the session ID zeroed: what a REALITY client seals the ID
	// with. nil when the ID is not the 32 bytes REALITY writes there.
	aad []byte
}

// keyShare is one entry of the key_share extension.
type keyShare struct {
	group uint16
	data  []byte
}

// Where a ClientHello keeps its session ID: after the handshake header (4), the version (2),
// the random (32) and the ID's length (1).
const helloSessionAt = 4 + 2 + 32 + 1

// joinHello joins the handshake records at the start of buf into the first handshake message.
// A client may split its ClientHello over several records; anything else before the message
// ends makes it no ClientHello.
func joinHello(buf []byte) ([]byte, helloState) {
	var msg []byte
	for {
		if len(msg) >= 4 {
			if msg[0] != 0x01 {
				return nil, helloBad
			}
			total := 4 + (int(msg[1])<<16 | int(msg[2])<<8 | int(msg[3]))
			if total > helloMax {
				return nil, helloBad
			}
			if len(msg) >= total {
				return msg[:total], helloDone
			}
		}
		if len(buf) < 5 {
			return nil, helloPart
		}
		if buf[0] != 0x16 {
			return nil, helloBad
		}
		n := int(buf[3])<<8 | int(buf[4])
		if n == 0 || n > 1<<14 {
			return nil, helloBad
		}
		if len(buf) < 5+n {
			return nil, helloPart
		}
		msg = append(msg, buf[5:5+n]...)
		buf = buf[5+n:]
	}
}

// parseHello reads a ClientHello handshake message, its header included.
func parseHello(msg []byte) (*tlsHello, bool) {
	if len(msg) < 4 || msg[0] != 0x01 {
		return nil, false
	}
	s := cryptobyte.String(msg[4:])
	var (
		vers                 uint16
		random               []byte
		sid, suites, methods cryptobyte.String
	)
	if !s.ReadUint16(&vers) || !s.ReadBytes(&random, 32) || !s.ReadUint8LengthPrefixed(&sid) ||
		!s.ReadUint16LengthPrefixed(&suites) || !s.ReadUint8LengthPrefixed(&methods) {
		return nil, false
	}
	h := &tlsHello{random: random, sessionID: sid}
	if len(sid) == 32 {
		h.aad = append([]byte(nil), msg...)
		clear(h.aad[helloSessionAt : helloSessionAt+32])
	}
	if s.Empty() {
		return h, true
	}
	var exts cryptobyte.String
	if !s.ReadUint16LengthPrefixed(&exts) || !s.Empty() {
		return nil, false
	}
	for !exts.Empty() {
		var (
			typ  uint16
			data cryptobyte.String
		)
		if !exts.ReadUint16(&typ) || !exts.ReadUint16LengthPrefixed(&data) {
			return nil, false
		}
		switch typ {
		case extServerName:
			if h.serverName == "" {
				h.serverName = hostName(data)
			}
			continue
		case extKeyShare:
		default:
			continue
		}
		var shares cryptobyte.String
		if !data.ReadUint16LengthPrefixed(&shares) {
			return nil, false
		}
		for !shares.Empty() {
			var (
				group uint16
				key   cryptobyte.String
			)
			if !shares.ReadUint16(&group) || !shares.ReadUint16LengthPrefixed(&key) {
				return nil, false
			}
			h.keyShares = append(h.keyShares, keyShare{group: group, data: key})
		}
	}
	return h, true
}

// The extensions read: server_name and the TLS 1.3 key_share.
const (
	extServerName = 0x0000
	extKeyShare   = 0x0033
)

// hostName is the first host_name of a server_name extension; "" when it has none.
func hostName(data cryptobyte.String) string {
	var list cryptobyte.String
	if !data.ReadUint16LengthPrefixed(&list) {
		return ""
	}
	for !list.Empty() {
		var (
			kind uint8
			name cryptobyte.String
		)
		if !list.ReadUint8(&kind) || !list.ReadUint16LengthPrefixed(&name) {
			return ""
		}
		if kind == 0 && len(name) > 0 {
			return string(name)
		}
	}
	return ""
}
