// Package nodehello is the node's hello to its panel. A node that starts tells its panel
// so (POST PanelURL+Path, signed with the node's own key); the panel finds the node by its
// certificate's pin, dials it back at once and answers how that went, signed with the
// panel's key the join key pins. The installer prints the answer, so the admin learns in
// the terminal whether the panel reaches the node, and if not, why.
//
// Anything that is not a valid hello gets the panel's plain 404, like any other path: the
// route reveals nothing, the admin path least of all.
package nodehello

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mikan/internal/fsutil"
	"mikan/internal/nodetls"
)

// Path is the panel's route for hellos, the same on every panel.
const Path = "/.well-known/mikan-node-hello"

// Window is how far a hello's time may be from the panel's clock.
const Window = 5 * time.Minute

// MaxBody bounds a hello and an answer: a certificate and a few words.
const MaxBody = 16 << 10

// Codes of the node's side, beside the panel's link codes (nodeapi.Link*).
const (
	// NoPanelURL: the join key comes from a panel before hellos.
	NoPanelURL = "no_panel_url"
	// PanelUnreachable: the node cannot reach the panel's address. The panel dials the
	// node, not the other way round, so this alone breaks nothing.
	PanelUnreachable = "panel_unreachable"
	// PanelRejected: the panel does not know the node's certificate: the key was replaced
	// or the node removed.
	PanelRejected = "panel_rejected"
	// PanelUnverified: the answer is not signed by the panel the key names.
	PanelUnverified = "panel_unverified"
	// PanelUntrusted: the panel's HTTPS certificate is not a public one yet (still its
	// self-signed one): the node does not talk to it. The panel still dials the node.
	PanelUntrusted = "panel_untrusted"
)

// IPDiffers is the Result.Params flag of a node whose address in the panel is not the one
// its hello came from.
const IPDiffers = "ip_differs"

// Request is a hello.
type Request struct {
	Cert  string `json:"cert"`  // the node's certificate, PEM
	Time  int64  `json:"time"`  // unix seconds
	Nonce string `json:"nonce"` // 32 hex digits
	Sig   string `json:"sig"`   // base64 Ed25519 over requestMessage
}

// Result is how the panel's dial back went.
type Result struct {
	OK     bool              `json:"ok"`
	Code   string            `json:"code,omitempty"`
	Params map[string]string `json:"params,omitempty"`
	// SeenIP is where the hello came from; Host the node's address in the panel.
	SeenIP string `json:"seen_ip,omitempty"`
	Host   string `json:"host,omitempty"`
	// Error is the dial's own words.
	Error string `json:"error,omitempty"`
}

// Response is the panel's signed answer: Result as the JSON that was signed.
type Response struct {
	Result string `json:"result"`
	Cert   string `json:"cert"` // the panel's client certificate, PEM
	Sig    string `json:"sig"`
}

func requestMessage(t int64, nonce string) []byte {
	return []byte("mikan-hello/1\n" + strconv.FormatInt(t, 10) + "\n" + nonce)
}

func replyMessage(nonce, result string) []byte {
	return []byte("mikan-hello-reply/1\n" + nonce + "\n" + result)
}

// NewRequest signs a hello with the node's key.
func NewRequest(key nodetls.Key, now time.Time) (Request, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Request{}, err
	}
	nonce := hex.EncodeToString(b[:])
	t := now.Unix()
	sig, err := nodetls.Sign(key.KeyPEM, requestMessage(t, nonce))
	if err != nil {
		return Request{}, err
	}
	return Request{Cert: key.CertPEM, Time: t, Nonce: nonce, Sig: base64.StdEncoding.EncodeToString(sig)}, nil
}

// Errors of Verify; the panel answers each with its plain 404.
var (
	ErrMalformed = errors.New("hello: malformed")
	ErrExpired   = errors.New("hello: time out of the window")
	ErrSignature = errors.New("hello: bad signature")
)

// Verify checks a hello's form, time and signature, and returns the pin of the
// certificate that signed it. Whether that pin is one of the panel's nodes is the caller's.
func Verify(req Request, now time.Time) (string, error) {
	if len(req.Nonce) != 32 || len(req.Cert) > 4096 || len(req.Sig) > 200 {
		return "", ErrMalformed
	}
	if _, err := hex.DecodeString(req.Nonce); err != nil {
		return "", ErrMalformed
	}
	if d := now.Sub(time.Unix(req.Time, 0)); d > Window || d < -Window {
		return "", ErrExpired
	}
	sig, err := base64.StdEncoding.DecodeString(req.Sig)
	if err != nil {
		return "", ErrMalformed
	}
	pin, err := nodetls.Verify(req.Cert, requestMessage(req.Time, req.Nonce), sig)
	if err != nil {
		return "", ErrSignature
	}
	return pin, nil
}

// Reply signs a result for the hello with nonce, with the panel's client key.
func Reply(panel nodetls.Pair, nonce string, r Result) (Response, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return Response{}, err
	}
	sig, err := nodetls.Sign(panel.KeyPEM, replyMessage(nonce, string(raw)))
	if err != nil {
		return Response{}, err
	}
	return Response{Result: string(raw), Cert: panel.CertPEM, Sig: base64.StdEncoding.EncodeToString(sig)}, nil
}

// VerifyReply checks that the answer comes from the panel of the key and is for this hello.
func VerifyReply(key nodetls.Key, nonce string, resp Response) (Result, error) {
	sig, err := base64.StdEncoding.DecodeString(resp.Sig)
	if err != nil {
		return Result{}, ErrSignature
	}
	pin, err := nodetls.Verify(resp.Cert, replyMessage(nonce, resp.Result), sig)
	if err != nil || !nodetls.SamePin(pin, key.PanelPin) {
		return Result{}, ErrSignature
	}
	var r Result
	if err := json.Unmarshal([]byte(resp.Result), &r); err != nil {
		return Result{}, ErrSignature
	}
	return r, nil
}

// Send says hello to the panel of the key and returns how the panel's dial back went, or
// why there is no answer (a Result with one of the node's codes).
func Send(ctx context.Context, key nodetls.Key, hc *http.Client, now time.Time) Result {
	if key.PanelURL == "" {
		return Result{Code: NoPanelURL}
	}
	req, err := NewRequest(key, now)
	if err != nil {
		return Result{Code: PanelUnreachable, Error: err.Error()}
	}
	body, _ := json.Marshal(req)
	url := strings.TrimSuffix(key.PanelURL, "/") + Path
	hr, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return Result{Code: PanelUnreachable, Error: err.Error(), Params: map[string]string{"url": key.PanelURL}}
	}
	hr.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(hr)
	if err != nil {
		var bad *tls.CertificateVerificationError
		if errors.As(err, &bad) {
			return Result{Code: PanelUntrusted, Error: err.Error(), Params: map[string]string{"url": key.PanelURL}}
		}
		return Result{Code: PanelUnreachable, Error: err.Error(), Params: map[string]string{"url": key.PanelURL}}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// A panel that does not know the node says 404 to it, as to anybody.
		return Result{Code: PanelRejected, Params: map[string]string{"url": key.PanelURL, "status": strconv.Itoa(resp.StatusCode)}}
	}
	var out Response
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxBody)).Decode(&out); err != nil {
		return Result{Code: PanelUnverified, Error: err.Error()}
	}
	r, err := VerifyReply(key, req.Nonce, out)
	if err != nil {
		return Result{Code: PanelUnverified, Error: err.Error()}
	}
	return r
}

// Client is the HTTP client for hellos. It checks the panel's certificate as any client
// would: a panel on its self-signed one gets no hello (PanelUntrusted) until its public
// certificate comes, which it gets by itself. The answer's signature proves the panel
// on top of that.
func Client() *http.Client {
	return &http.Client{Timeout: 25 * time.Second, Transport: &http.Transport{
		Proxy:               nil,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 10 * time.Second,
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Final says whether a result ends the hellos of a start: the panel reached the node, or
// trying again cannot change the answer.
func Final(r Result) bool {
	switch r.Code {
	case "", NoPanelURL, PanelRejected, PanelUnverified, PanelUntrusted, "pin_mismatch":
		return true
	}
	return r.OK
}

// File is hello.json in the node's data directory: the last hello and how it went, for the
// installer and `mikan doctor` on the node's server.
const File = "hello.json"

// Status is what File holds.
type Status struct {
	Result
	At      time.Time `json:"at"`
	Started time.Time `json:"started"`
	Attempt int       `json:"attempt"`
	// Final: no more hellos in this series.
	Final bool `json:"final"`
}

// Write saves a status in dir; root reads it on the host, so it is not secret.
func Write(dir string, s Status) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(dir, File), append(raw, '\n'), 0o644)
}

// Read loads the status in dir.
func Read(dir string) (Status, error) {
	var s Status
	raw, err := os.ReadFile(filepath.Join(dir, File))
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return s, fmt.Errorf("%s: %w", File, err)
	}
	return s, nil
}
