package api

import (
	"context"
	cryptohmac "crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"mikan/internal/panel/store/db"
)

const relayProofSetting = "sub_relay_proof_key"

// A key which previously authenticated may ask whether it was revoked. The ticket
// grants only that diagnostic, not subscription access, and survives key deletion.
// Random/other-scope tokens cannot obtain it or fingerprint the endpoint with it.
type relayProofSigner struct {
	q   *db.Queries
	mu  sync.Mutex
	key []byte
}

func (s *relayProofSigner) proof(ctx context.Context, token string, create bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.key) == 0 {
		raw, err := s.q.GetSetting(ctx, relayProofSetting)
		if errors.Is(err, sql.ErrNoRows) && create {
			candidate, _ := json.Marshal(rand.Text() + rand.Text())
			// Concurrent first requests keep the same winner; no secret can be overwritten.
			raw, err = s.q.GetOrCreateSetting(ctx, db.GetOrCreateSettingParams{Key: relayProofSetting, Value: string(candidate)})
		}
		if errors.Is(err, sql.ErrNoRows) && !create {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		var secret string
		if err := json.Unmarshal([]byte(raw), &secret); err != nil {
			return "", err
		}
		if len(secret) < 32 {
			return "", fmt.Errorf("invalid subscription relay signing key")
		}
		s.key = []byte(secret)
	}
	mac := cryptohmac.New(sha256.New, s.key)
	mac.Write([]byte("mikan subscription relay diagnostic v1\x00"))
	hash := sha256.Sum256([]byte(token))
	mac.Write(hash[:])
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
