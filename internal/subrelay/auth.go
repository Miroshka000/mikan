package subrelay

import (
	"context"
	"crypto/rand"
	"net/http"
	"strings"
	"time"
)

// credentialsRejected accepts only a nonce-bound revocation signal authenticated by
// a ticket issued after a prior successful login. Unmarked WAF/OwnSite responses,
// redirects and outages are inconclusive and cannot empty everybody's cache.
func (p *Proxy) credentialsRejected(parent context.Context) bool {
	p.mu.Lock()
	proof := p.relayProof
	p.mu.Unlock()
	if proof == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	u := *p.upstream
	u.Path, u.RawPath, u.RawQuery = strings.TrimRight(u.Path, "/")+AuthPath, "", ""
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, u.String(), nil)
	if err != nil {
		return false
	}
	nonce := rand.Text()
	req.Header.Set("Authorization", "Bearer "+p.token)
	req.Header.Set(RelayNonceHeader, nonce)
	req.Header.Set(RelayProofHeader, proof)
	req.Header.Set("User-Agent", "mikan-sub")
	p.stats.upstream.Add(1)
	resp, err := p.client.Do(req)
	if err != nil {
		p.stats.failures.Add(1)
		return false
	}
	defer resp.Body.Close()
	stripHopHeaders(resp.Header)
	return resp.StatusCode == http.StatusNoContent && resp.Header.Get(RelayAuthHeader) == "revoked:"+nonce
}
