package acme

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCA is an ACME server enough for lego: one account, orders of one identifier, an
// http-01 challenge it checks through validate, certificates from its own root.
type fakeCA struct {
	t   *testing.T
	srv *httptest.Server
	// validate gets the token and the key authorization lego computed and says whether the
	// challenge passed: it fetches the token as a CA would, or asks the fake node.
	validate func(host, token, keyAuth string) bool
	// requireEAB: newAccount without an external account binding is refused.
	requireEAB bool

	mu      sync.Mutex
	eabKID  string // the kid of the last binding
	host    string
	token   string
	valid   bool
	csr     *x509.CertificateRequest
	key     *ecdsa.PrivateKey
	root    *x509.Certificate
	orders  int
	account bool
}

const fakeToken = "evaGxfADs6pSRb2LAv9IZf17Dt3juxGJ-PCt92wr-oA"

func newFakeCA(t *testing.T, validate func(host, token, keyAuth string) bool) *fakeCA {
	t.Helper()
	f := &fakeCA{t: t, validate: validate, token: fakeToken}
	f.key, _ = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Fake Root"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &f.key.PublicKey, f.key)
	if err != nil {
		t.Fatal(err)
	}
	f.root, _ = x509.ParseCertificate(der)
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LEGO_CA_CERTIFICATES", ca)
	t.Setenv("MIKAN_ACME_DIRECTORY", f.srv.URL+"/directory")
	return f
}

func (f *fakeCA) url(p string) string { return f.srv.URL + p }

// payload is the JWS payload of a request.
func (f *fakeCA) payload(r *http.Request) map[string]any {
	raw, _ := io.ReadAll(r.Body)
	var jws struct {
		Payload string `json:"payload"`
	}
	_ = json.Unmarshal(raw, &jws)
	out := map[string]any{}
	if p, err := base64.RawURLEncoding.DecodeString(jws.Payload); err == nil && len(p) > 0 {
		_ = json.Unmarshal(p, &out)
	}
	return out
}

func (f *fakeCA) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Replay-Nonce", base64.RawURLEncoding.EncodeToString([]byte(time.Now().String())))
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	problem := func(status int, typ, detail string) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"type": "urn:ietf:params:acme:error:" + typ, "detail": detail, "status": status})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/directory":
		reply(200, map[string]any{"newNonce": f.url("/nonce"), "newAccount": f.url("/account"), "newOrder": f.url("/order"),
			"revokeCert": f.url("/revoke"), "keyChange": f.url("/key"), "meta": map[string]any{"externalAccountRequired": f.requireEAB}})
	case "/nonce":
		w.WriteHeader(http.StatusOK)
	case "/account":
		p := f.payload(r)
		if only, _ := p["onlyReturnExisting"].(bool); only && !f.account {
			problem(400, "accountDoesNotExist", "no account")
			return
		}
		if !f.account {
			eab, _ := p["externalAccountBinding"].(map[string]any)
			if f.requireEAB && eab == nil {
				problem(400, "externalAccountRequired", "an external account binding is required")
				return
			}
			if eab != nil {
				var prot struct {
					KID string `json:"kid"`
				}
				raw, _ := base64.RawURLEncoding.DecodeString(eab["protected"].(string))
				_ = json.Unmarshal(raw, &prot)
				f.eabKID = prot.KID
			}
			f.account = true
		}
		w.Header().Set("Location", f.url("/acct/1"))
		reply(201, map[string]any{"status": "valid"})
	case "/order":
		p := f.payload(r)
		ids, _ := p["identifiers"].([]any)
		if len(ids) == 1 {
			f.host, _ = ids[0].(map[string]any)["value"].(string)
		}
		f.orders++
		f.valid, f.csr = false, nil
		w.Header().Set("Location", f.url("/order/1"))
		reply(201, f.order())
	case "/order/1":
		reply(200, f.order())
	case "/authz/1":
		status := "pending"
		if f.valid {
			status = "valid"
		}
		reply(200, map[string]any{"status": status, "identifier": map[string]any{"type": "dns", "value": f.host},
			"challenges": []any{map[string]any{"type": "http-01", "url": f.url("/chall/1"), "token": f.token, "status": status}}})
	case "/chall/1":
		// The key authorization lego computed is token.thumbprint: the validator gets it to
		// compare with what the CA fetches.
		thumb := "unknown"
		host, token := f.host, f.token
		f.mu.Unlock()
		ok := f.validate(host, token, thumb)
		f.mu.Lock()
		if !ok {
			problem(403, "unauthorized", "203.0.113.9: Invalid response from http://"+host+"/.well-known/acme-challenge/"+token+": 404")
			return
		}
		f.valid = true
		reply(200, map[string]any{"type": "http-01", "url": f.url("/chall/1"), "token": f.token, "status": "valid"})
	case "/finalize":
		p := f.payload(r)
		raw, _ := base64.RawURLEncoding.DecodeString(p["csr"].(string))
		f.csr, _ = x509.ParseCertificateRequest(raw)
		reply(200, f.order())
	case "/cert":
		leafKey := f.csr.PublicKey
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: f.host}, DNSNames: f.csr.DNSNames,
			IPAddresses: f.csr.IPAddresses, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(90 * 24 * time.Hour),
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, f.root, leafKey, f.key)
		if err != nil {
			f.t.Error(err)
		}
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_ = pem.Encode(w, &pem.Block{Type: "CERTIFICATE", Bytes: der})
		_ = pem.Encode(w, &pem.Block{Type: "CERTIFICATE", Bytes: f.root.Raw})
	default:
		if strings.HasPrefix(r.URL.Path, "/acct/") {
			reply(200, map[string]any{"status": "valid"})
			return
		}
		http.NotFound(w, r)
	}
}

// order is the order's state: ready once the challenge passed, valid once finalized.
func (f *fakeCA) order() map[string]any {
	o := map[string]any{"status": "pending", "identifiers": []any{map[string]any{"type": "dns", "value": f.host}},
		"authorizations": []any{f.url("/authz/1")}, "finalize": f.url("/finalize")}
	if f.valid {
		o["status"] = "ready"
	}
	if f.csr != nil {
		o["status"], o["certificate"] = "valid", f.url("/cert")
	}
	return o
}
