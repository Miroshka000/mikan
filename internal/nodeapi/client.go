package nodeapi

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Client talks to a node: the local one over its unix socket, a remote one over TLS.
type Client struct {
	hc   *http.Client
	base string
	// dial opens a bare connection to the Node API, for Tunnel.
	dial func(ctx context.Context) (net.Conn, error)
}

func NewUnixClient(socket string) *Client {
	dial := func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
	}
	return &Client{base: "http://node", dial: dial, hc: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "unix", socket)
		},
		MaxIdleConns: 4,
	}}}
}

// NewTLSClient reaches a remote node at host:port; cfg pins both sides (see nodetls).
func NewTLSClient(address string, cfg *tls.Config) *Client {
	// A tunnel takes over the connection, which HTTP/2 does not allow.
	h1 := cfg.Clone()
	h1.NextProtos = []string{"http/1.1"}
	dial := func(ctx context.Context) (net.Conn, error) {
		d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}, Config: h1}
		return d.DialContext(ctx, "tcp", address)
	}
	return &Client{base: "https://" + address, dial: dial, hc: &http.Client{Transport: &http.Transport{
		DialContext:         (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     cfg,
		TLSHandshakeTimeout: 5 * time.Second,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        4,
		IdleConnTimeout:     90 * time.Second,
	}}}
}

var ErrUnavailable = errors.New("node unavailable")

// StatusError is a node's failure answer without an Error body, such as the 404 of a node
// that predates an endpoint.
type StatusError struct {
	Method, Path string
	Status       int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("node %s %s: status %d", e.Method, e.Path, e.Status)
}

func (c *Client) do(ctx context.Context, method, path string, in, out any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e Error
		if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&e) == nil && e.Code != "" {
			return &e
		}
		return &StatusError{Method: method, Path: path, Status: resp.StatusCode}
	}
	if out != nil {
		return json.NewDecoder(&cappedReader{r: resp.Body, left: MaxResponse}).Decode(out)
	}
	return nil
}

// MaxResponse bounds what the panel reads from a node in one answer: the biggest real one
// (the counters of thousands of slots) is a few MiB. A node is a server somebody else may
// run, and one that streams JSON for ever must not take the panel's memory.
const MaxResponse = 8 << 20

// ErrTooLarge is a node's answer past MaxResponse.
var ErrTooLarge = errors.New("node answer too large")

type cappedReader struct {
	r    io.Reader
	left int64
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, ErrTooLarge
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

func (c *Client) Apply(ctx context.Context, s DesiredState) (ApplyResult, error) {
	var r ApplyResult
	err := c.do(ctx, http.MethodPut, "/v1/state", s, &r, 60*time.Second)
	return r, err
}

// Validate returns *Error{Code: "invalid_config"} when mihomo refuses the inbound.
func (c *Client) Validate(ctx context.Context, req ValidateRequest) error {
	return c.do(ctx, http.MethodPost, "/v1/validate", req, nil, 10*time.Second)
}

func (c *Client) SetPolicies(ctx context.Context, epoch string, p []Policy) error {
	return c.do(ctx, http.MethodPut, "/v1/policies", PoliciesRequest{Epoch: epoch, Policies: p}, nil, 30*time.Second)
}

func (c *Client) Counters(ctx context.Context) (Counters, error) {
	var r Counters
	err := c.do(ctx, http.MethodGet, "/v1/counters", nil, &r, 10*time.Second)
	return r, err
}

func (c *Client) Ack(ctx context.Context, epoch string, seq int64) error {
	return c.do(ctx, http.MethodPost, "/v1/counters/ack", AckRequest{Epoch: epoch, Seq: seq}, nil, 10*time.Second)
}

func (c *Client) Health(ctx context.Context) (Health, error) {
	var r Health
	err := c.do(ctx, http.MethodGet, "/v1/health", nil, &r, 5*time.Second)
	return r, err
}

// RequestUpdate asks the node to update to a release; the updater on its server takes
// the request. Nodes before 0.5.0.2 answer 404.
func (c *Client) RequestUpdate(ctx context.Context, version string) error {
	return c.do(ctx, http.MethodPost, "/v1/update", UpdateRequest{Version: version}, nil, 10*time.Second)
}

// Torrents returns the torrent blocker's hits after seq of epoch. Nodes that predate the
// blocker answer 404.
func (c *Client) Torrents(ctx context.Context, epoch string, after int64) (TorrentHits, error) {
	var r TorrentHits
	q := url.Values{"epoch": {epoch}, "after": {strconv.FormatInt(after, 10)}}
	err := c.do(ctx, http.MethodGet, "/v1/torrents?"+q.Encode(), nil, &r, 10*time.Second)
	return r, err
}

// SpeedTest runs the node's speed test, about twenty seconds. Nodes that predate it
// answer 404 (a *StatusError); one running a test answers 409 speed_test_busy.
func (c *Client) SpeedTest(ctx context.Context) (SpeedTest, error) {
	var r SpeedTest
	err := c.do(ctx, http.MethodPost, "/v1/speedtest", nil, &r, 90*time.Second)
	return r, err
}

func (c *Client) Activity(ctx context.Context) (Activity, error) {
	var r Activity
	err := c.do(ctx, http.MethodGet, "/v1/activity", nil, &r, 10*time.Second)
	return r, err
}

func (c *Client) CheckTarget(ctx context.Context, req TargetCheckRequest) (TargetResult, error) {
	var r TargetResult
	err := c.do(ctx, http.MethodPost, "/v1/targets/check", req, &r, 20*time.Second)
	return r, err
}

// ScanTargets opens ~250 connections on the node; it runs one scan at a time.
func (c *Client) ScanTargets(ctx context.Context, req TargetScanRequest) (TargetScan, error) {
	var r TargetScan
	err := c.do(ctx, http.MethodPost, "/v1/targets/scan", req, &r, 45*time.Second)
	return r, err
}

// Warp checks the node's way out through WARP (a request to Cloudflare through it). The
// node answers from a minute-old check unless force asks for a new one; it may still
// answer from the cache when forced too often, and old nodes ignore force.
func (c *Client) Warp(ctx context.Context, force bool) (WarpStatus, error) {
	var r WarpStatus
	path := "/v1/warp"
	if force {
		path += "?force=1"
	}
	err := c.do(ctx, http.MethodGet, path, nil, &r, 20*time.Second)
	return r, err
}

// Probe checks the internet through one outbound of the node: WARP or NODE-<id>.
func (c *Client) Probe(ctx context.Context, proxy string) (ProbeResult, error) {
	var r ProbeResult
	err := c.do(ctx, http.MethodGet, "/v1/probe?proxy="+url.QueryEscape(proxy), nil, &r, 20*time.Second)
	return r, err
}

// TunnelHosts are what a node opens a tunnel to: the Bot API, for a panel whose own
// server cannot reach Telegram. Nothing else, so a node is no open proxy.
var TunnelHosts = []string{"api.telegram.org:443"}

// Tunnel opens a TCP stream to addr through the node (HTTP CONNECT on the Node API).
func (c *Client) Tunnel(ctx context.Context, addr string) (net.Conn, error) {
	conn, err := c.dial(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if d, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(d)
	} else {
		_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	}
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Host: addr}, Host: addr, Header: http.Header{}}
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if resp.StatusCode != http.StatusOK {
		defer conn.Close()
		var e Error
		if json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e) == nil && e.Code != "" {
			return nil, &e
		}
		return nil, fmt.Errorf("node tunnel: status %d", resp.StatusCode)
	}
	_ = conn.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

// bufConn reads what the CONNECT response left buffered before the connection itself.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) { return c.r.Read(p) }

// Diagnose asks the node how its server is: DNS, the internet, GitHub and GHCR for
// updates, its clock, disk and memory. Nodes that predate it answer 404 (a *StatusError).
func (c *Client) Diagnose(ctx context.Context) (Diagnosis, error) {
	var r Diagnosis
	err := c.do(ctx, http.MethodPost, "/v1/diagnose", nil, &r, 25*time.Second)
	return r, err
}
