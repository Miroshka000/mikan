// Package checkhost asks check-host.net whether a TCP port of a server opens from Russia:
// its checkers in Russian cities connect to the port and say how it went. It sees blocks
// of an address or a port, not DPI that breaks a protocol after the connection, and not
// the whitelists of mobile networks.
//
// The contract (check-host.net/about/api): GET /nodes/hosts lists the checkers with their
// location; GET /check-tcp?host=H:P&node=N&node=M (Accept: application/json) starts a check
// and answers {"ok":1,"request_id":"…","nodes":{…}}; GET /check-result/<id> answers per
// checker null (not done yet), [{"time":0.05,"address":"…"}] or [{"error":"…"}].
package checkhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Base is the real service.
const Base = "https://check-host.net"

const (
	// maxCheckers are the Russian checkers asked at most: enough cities, a short answer.
	maxCheckers = 6
	// listFresh is how long the list of checkers is kept.
	listFresh = time.Hour
	// resultFresh is how long a host's answer is kept: a page reloaded, a button pressed
	// twice do not ask the service again.
	resultFresh = 2 * time.Minute
	// pollEvery and wait bound the wait for the checkers.
	pollEvery = 1500 * time.Millisecond
	wait      = 20 * time.Second
)

var (
	// ErrBusy: a check runs already; one at a time.
	ErrBusy = errors.New("checkhost: a check is running")
	// ErrNoCheckers: the service lists no checker in Russia, or does not answer.
	ErrNoCheckers = errors.New("checkhost: no checkers in Russia")
)

// Client talks to check-host.net.
type Client struct {
	base string
	hc   *http.Client
	now  func() time.Time
	// pollEvery is the pace of asking for results; tests make it short.
	pollEvery time.Duration

	run sync.Mutex // one check at a time

	mu       sync.Mutex
	checkers []Checker
	listedAt time.Time
	results  map[string]Result
}

// Checker is one of the service's checkers.
type Checker struct {
	ID   string `json:"id"`
	City string `json:"city"`
}

// PortResult is one port as one checker saw it.
type PortResult struct {
	Port int     `json:"port"`
	OK   bool    `json:"ok"`
	Ms   int     `json:"ms,omitempty"`
	// Error is the checker's words ("Connection timed out"); Pending: no answer in time.
	Error   string `json:"error,omitempty"`
	Pending bool   `json:"pending,omitempty"`
}

// CityResult is one checker's answer for every port.
type CityResult struct {
	Checker
	Ports []PortResult `json:"ports"`
}

// Result is a check of one host.
type Result struct {
	At     time.Time    `json:"at"`
	Cities []CityResult `json:"cities"`
	// Cached: the answer is a recent one kept, not a new check.
	Cached bool `json:"cached"`
}

// New makes a client of the service at base ("" is the real one).
func New(base string, hc *http.Client, now func() time.Time) *Client {
	if base == "" {
		base = Base
	}
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	if now == nil {
		now = time.Now
	}
	return &Client{base: strings.TrimSuffix(base, "/"), hc: hc, now: now, pollEvery: pollEvery, results: map[string]Result{}}
}

// CheckTCP checks ports of host from the Russian checkers.
func (c *Client) CheckTCP(ctx context.Context, host string, ports []int) (Result, error) {
	key := host + "|" + fmt.Sprint(ports)
	c.mu.Lock()
	if r, ok := c.results[key]; ok && c.now().Sub(r.At) < resultFresh {
		c.mu.Unlock()
		r.Cached = true
		return r, nil
	}
	c.mu.Unlock()
	if !c.run.TryLock() {
		return Result{}, ErrBusy
	}
	defer c.run.Unlock()
	ctx, cancel := context.WithTimeout(ctx, wait+15*time.Second)
	defer cancel()
	checkers, err := c.russian(ctx)
	if err != nil {
		return Result{}, err
	}
	type answer struct {
		port int
		res  map[string]PortResult
		err  error
	}
	answers := make(chan answer, len(ports))
	for _, p := range ports {
		go func() {
			res, err := c.checkPort(ctx, host, p, checkers)
			answers <- answer{p, res, err}
		}()
	}
	byPort := map[int]map[string]PortResult{}
	var firstErr error
	for range ports {
		a := <-answers
		if a.err != nil && firstErr == nil {
			firstErr = a.err
		}
		byPort[a.port] = a.res
	}
	if len(ports) > 0 && firstErr != nil && len(byPort) == 0 {
		return Result{}, firstErr
	}
	out := Result{At: c.now().UTC()}
	for _, ch := range checkers {
		cr := CityResult{Checker: ch}
		for _, p := range ports {
			pr, ok := byPort[p][ch.ID]
			if !ok {
				pr = PortResult{Port: p, Pending: true}
			}
			cr.Ports = append(cr.Ports, pr)
		}
		out.Cities = append(out.Cities, cr)
	}
	c.mu.Lock()
	for k, r := range c.results {
		if c.now().Sub(r.At) >= resultFresh {
			delete(c.results, k)
		}
	}
	c.results[key] = out
	c.mu.Unlock()
	return out, nil
}

func (c *Client) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("checkhost: %s: status %d", path, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

// russian lists the checkers in Russia, from a list kept for an hour.
func (c *Client) russian(ctx context.Context) ([]Checker, error) {
	c.mu.Lock()
	if len(c.checkers) > 0 && c.now().Sub(c.listedAt) < listFresh {
		defer c.mu.Unlock()
		return c.checkers, nil
	}
	c.mu.Unlock()
	var list struct {
		Nodes map[string]struct {
			Location []string `json:"location"`
		} `json:"nodes"`
	}
	if err := c.get(ctx, "/nodes/hosts", &list); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoCheckers, err)
	}
	var out []Checker
	for id, n := range list.Nodes {
		if len(n.Location) == 0 || !strings.EqualFold(n.Location[0], "ru") || !validID(id) {
			continue
		}
		city := ""
		if len(n.Location) > 2 {
			city = n.Location[2]
		}
		out = append(out, Checker{ID: id, City: city})
	}
	if len(out) == 0 {
		return nil, ErrNoCheckers
	}
	slices.SortFunc(out, func(a, b Checker) int { return strings.Compare(a.ID, b.ID) })
	if len(out) > maxCheckers {
		out = out[:maxCheckers]
	}
	c.mu.Lock()
	c.checkers, c.listedAt = out, c.now()
	c.mu.Unlock()
	return out, nil
}

// validID: a checker's name goes into a URL; only a host name like ru1.node.check-host.net.
func validID(id string) bool {
	if id == "" || len(id) > 100 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '-') {
			return false
		}
	}
	return true
}

func (c *Client) checkPort(ctx context.Context, host string, port int, checkers []Checker) (map[string]PortResult, error) {
	q := url.Values{"host": {host + ":" + strconv.Itoa(port)}}
	for _, ch := range checkers {
		q.Add("node", ch.ID)
	}
	var started struct {
		OK        int    `json:"ok"`
		RequestID string `json:"request_id"`
	}
	if err := c.get(ctx, "/check-tcp?"+q.Encode(), &started); err != nil {
		return nil, err
	}
	if started.OK != 1 || !validID(started.RequestID) {
		return nil, errors.New("checkhost: the check was not started")
	}
	out := map[string]PortResult{}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	tick := time.NewTicker(c.pollEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return out, nil
		case <-deadline.C:
			return out, nil
		case <-tick.C:
		}
		var res map[string][]struct {
			Time    *float64 `json:"time"`
			Address string   `json:"address"`
			Error   string   `json:"error"`
		}
		if err := c.get(ctx, "/check-result/"+started.RequestID, &res); err != nil {
			continue
		}
		for id, rs := range res {
			if rs == nil {
				continue
			}
			pr := PortResult{Port: port}
			switch {
			case len(rs) == 0:
				pr.Error = "no answer"
			case rs[0].Error != "":
				pr.Error = clip(rs[0].Error)
			case rs[0].Time != nil:
				pr.OK, pr.Ms = true, int(math.Round(*rs[0].Time*1000))
			default:
				pr.Error = "no answer"
			}
			out[id] = pr
		}
		if len(out) >= len(checkers) {
			return out, nil
		}
	}
}

func clip(s string) string {
	if len(s) > 120 {
		return s[:120]
	}
	return s
}
