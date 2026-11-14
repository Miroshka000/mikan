package node

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"

	"mikan/internal/proto"
)

// The REALITY check of the SNI router: several REALITY inbounds may share a name behind it
// (SNIRouter.ByKey), and a client is handed to the one whose key it made its ClientHello
// with. The check is the one the inbound's REALITY makes itself: the client seals its short
// ID in the session ID with a key from X25519 between its key share and the inbound's private
// key, so only the inbound it was made for opens it.

// The TLS groups a REALITY client takes its key from, and the size of the ML-KEM-768 half of
// the hybrid one (its X25519 half follows it).
const (
	groupX25519         = 0x001d
	groupX25519MLKEM768 = 0x11ec
	mlkem768KeySize     = 1184
)

// realityKey is what tells one REALITY inbound's clients from everyone else's.
type realityKey struct {
	priv   *ecdh.PrivateKey
	shorts map[[8]byte]bool
}

// realityKeyOf reads an inbound's REALITY private key and short IDs from its config, as the
// core reads them; nil for an inbound that is no REALITY one or whose key does not parse.
func realityKeyOf(config []byte) *realityKey {
	t, err := proto.Parse(string(config))
	if err != nil {
		return nil
	}
	rc, ok := t["reality-config"].(map[string]any)
	if !ok {
		return nil
	}
	s, _ := rc["private-key"].(string)
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil
	}
	priv, err := ecdh.X25519().NewPrivateKey(raw)
	if err != nil {
		return nil
	}
	k := &realityKey{priv: priv, shorts: map[[8]byte]bool{}}
	var ids []string
	switch v := rc["short-id"].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				ids = append(ids, s)
			}
		}
	case []string:
		ids = v
	}
	for _, s := range ids {
		var id [8]byte
		if len(s) > 2*len(id) {
			continue
		}
		if _, err := hex.Decode(id[:], []byte(s)); err != nil {
			continue
		}
		k.shorts[id] = true
	}
	return k
}

// owns says whether the ClientHello was made with this inbound's key: its session ID opens
// with the key and carries one of the inbound's short IDs.
func (k *realityKey) owns(h *tlsHello) bool {
	if h.aad == nil || len(h.random) != 32 {
		return false
	}
	peer := realityPeer(h.keyShares)
	if peer == nil {
		return false
	}
	pub, err := ecdh.X25519().NewPublicKey(peer)
	if err != nil {
		return false
	}
	secret, err := k.priv.ECDH(pub)
	if err != nil {
		return false
	}
	key, err := hkdf.Key(sha256.New, secret, h.random[:20], "REALITY", 32)
	if err != nil {
		return false
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return false
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return false
	}
	plain, err := aead.Open(nil, h.random[20:], h.sessionID, h.aad)
	if err != nil || len(plain) < 16 {
		return false
	}
	var id [8]byte
	copy(id[:], plain[8:16])
	return k.shorts[id]
}

// realityPeer is the client's X25519 key the way the REALITY server picks it: the X25519
// share, else the X25519 half of the first X25519MLKEM768 share.
func realityPeer(shares []keyShare) []byte {
	var hybrid []byte
	for _, s := range shares {
		switch {
		case s.group == groupX25519 && len(s.data) == 32:
			return s.data
		case s.group == groupX25519MLKEM768 && len(s.data) == mlkem768KeySize+32 && hybrid == nil:
			hybrid = s.data[mlkem768KeySize:]
		}
	}
	return hybrid
}
