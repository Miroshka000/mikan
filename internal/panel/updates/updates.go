// Package updates keeps the panel aware of new releases and talks to the host updater
// (the mikan command on the server) through files in the panel's data directory:
//
//	update/policy.json  the panel writes {"auto": true|false, "channel": "stable|beta"}:
//	                    the daily timer on the host updates only when auto is on, and
//	                    takes pre-releases only on beta (it accepts nothing but these two
//	                    exact words: the panel is not trusted with more)
//	update/request      the panel writes it for the Update button; a systemd path unit
//	                    runs `mikan update --requested`, which removes it first
//	update/status.json  the host writes how the last update went:
//	                    {"state": "running|ok|failed", "version", "from", "error", "at"}
//
// The panel has no Docker socket: updating is the host's job.
package updates

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mikan/internal/fsutil"
	"mikan/internal/release"
)

// Query is what a check asks for: the release a panel of version Current takes on Channel.
type Query struct {
	Current string
	Channel string
}

// Found is what a check found.
type Found struct {
	// Manifest is the release to update to now; when nothing is newer, the newest one.
	Manifest release.Manifest
	// Newest is the newest release of the channel when the update goes through Manifest on
	// the way to it, or when this version cannot reach it; "" otherwise.
	Newest string
	// Unreachable: Newest is out, but no release this version can update to leads there.
	Unreachable bool
	// Fallback says why the release index was not used, when the answer is GitHub's latest
	// release instead; "" when it came from the index.
	Fallback string
	// Goals are the index's (none when it fell back).
	Goals release.Goals
}

// Source finds the release for a query, checked.
type Source func(ctx context.Context, q Query) (Found, error)

var (
	// ErrUnavailable: the panel runs without a data directory the host watches (tests,
	// UI development).
	ErrUnavailable = errors.New("updates_unavailable")
	// ErrNoRelease: the repository has no release yet; the UI says so in its language.
	ErrNoRelease = errors.New("no_release")
)

// Fetch looks in the signed release index at indexURL (its signature at indexURL + ".sig")
// and fetches the manifest of the release it names; when the index cannot be had or
// believed, it takes the manifest at latestURL, so a broken index never stops updates.
// Every file is checked against the release key.
func Fetch(indexURL, latestURL string) Source {
	pub, err := release.Key(release.PublicKey)
	if err != nil {
		panic(err)
	}
	return fetch(indexURL, latestURL, pub, &http.Client{Timeout: 30 * time.Second})
}

func fetch(indexURL, latestURL string, pub ed25519.PublicKey, client *http.Client) Source {
	get := func(ctx context.Context, u string, limit int64) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusNotFound {
			return nil, ErrNoRelease
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET %s: %s", u, resp.Status)
		}
		return io.ReadAll(io.LimitReader(resp.Body, limit))
	}
	signed := func(ctx context.Context, u string, parse func(data []byte, sig string) error) error {
		return release.FetchSigned(ctx, get, u, 1<<20, parse)
	}
	manifest := func(ctx context.Context, u string) (m release.Manifest, err error) {
		err = signed(ctx, u, func(data []byte, sig string) (err error) {
			m, err = release.Parse(data, sig, pub)
			return err
		})
		return m, err
	}
	fromIndex := func(ctx context.Context, q Query) (Found, error) {
		var ix release.Index
		err := signed(ctx, indexURL, func(data []byte, sig string) (err error) {
			ix, err = release.ParseIndex(data, sig, pub)
			return err
		})
		if err != nil {
			return Found{}, fmt.Errorf("release index: %w", err)
		}
		c := release.Choose(ix.Releases, q.Current, q.Channel == release.Beta)
		f := Found{Goals: ix.Goals}
		e := c.Target
		switch {
		case c.Newest.Version == "":
			return f, errors.New("release index: no release of the channel")
		case e.Version == "":
			// Nothing to update to: the newest release is what the page shows.
			e = c.Newest
			if release.Newer(e.Version, q.Current) {
				f.Newest, f.Unreachable = e.Version, true
			}
		case c.Hop():
			f.Newest = c.Newest.Version
		}
		m, err := manifest(ctx, e.Manifest)
		if err != nil {
			return Found{}, fmt.Errorf("release index: %s: %w", e.Version, err)
		}
		if m.Version != e.Version {
			return Found{}, fmt.Errorf("release index: the manifest of %s is of %s", e.Version, m.Version)
		}
		f.Manifest = m
		return f, nil
	}
	return func(ctx context.Context, q Query) (Found, error) {
		f, err := fromIndex(ctx, q)
		if err == nil {
			return f, nil
		}
		m, lerr := manifest(ctx, latestURL)
		if lerr != nil {
			return Found{}, lerr
		}
		return Found{Manifest: m, Fallback: err.Error()}, nil
	}
}

type Checker struct {
	dir     string
	version string
	source  Source
	log     *slog.Logger
	now     func() time.Time

	mu      sync.Mutex
	policy  Policy
	found   *Found
	checked time.Time
	err     string
}

// Policy is what the host updater takes from the panel's settings.
type Policy struct {
	Auto    bool   `json:"auto"`
	Channel string `json:"channel"`
}

// New makes a checker for the panel of version; dataDir "" or source nil turn parts off.
func New(dataDir, version string, source Source, log *slog.Logger, now func() time.Time) *Checker {
	c := &Checker{version: version, source: source, log: log, now: now, policy: Policy{Channel: release.Stable}}
	if dataDir != "" {
		c.dir = filepath.Join(dataDir, "update")
	}
	return c
}

// How often the release is looked for, and how soon again after a check that failed: a
// GitHub that was down at the moment must not leave "error" on the page for a day.
var (
	firstCheck = time.Minute
	checkEvery = 24 * time.Hour
	retryAfter = time.Hour
)

// Run checks a minute after the start, then once a day.
func (c *Checker) Run(ctx context.Context) {
	if c.source == nil {
		return
	}
	t := time.NewTimer(firstCheck)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			next := checkEvery
			// No release yet is an answer, not a failure.
			if err := c.check(ctx); err != nil && !errors.Is(err, ErrNoRelease) {
				next = retryAfter
			}
			t.Reset(next)
		}
	}
}

// Check asks for the newest release now.
func (c *Checker) Check(ctx context.Context) { _ = c.check(ctx) }

func (c *Checker) check(ctx context.Context) error {
	if c.source == nil {
		return nil
	}
	c.mu.Lock()
	q := Query{Current: c.version, Channel: c.policy.Channel}
	c.mu.Unlock()
	f, err := c.source(ctx, q)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checked = c.now()
	if err != nil {
		c.err = err.Error()
		if c.log != nil {
			c.log.Warn("update check", "err", err)
		}
		return err
	}
	if c.log != nil {
		if f.Fallback != "" {
			c.log.Warn("update check: the latest release instead of the index", "release", f.Manifest.Version, "why", f.Fallback)
		} else {
			c.log.Info("update check", "source", "index", "channel", q.Channel, "release", f.Manifest.Version, "newest", f.Newest)
		}
	}
	c.err = ""
	c.found = &f
	return nil
}

type State struct {
	Current   string
	Channel   string
	Latest    *release.Manifest
	Found     Found
	CheckedAt time.Time
	Error     string
}

func (c *Checker) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := State{Current: c.version, Channel: c.policy.Channel, CheckedAt: c.checked, Error: c.err}
	if c.found != nil {
		s.Found = *c.found
		s.Latest = &s.Found.Manifest
	}
	return s
}

// Available says whether a newer release is out that this panel can update to.
func (s State) Available() bool {
	return s.Latest != nil && !s.Found.Unreachable && release.Newer(s.Latest.Version, s.Current)
}

// SetPolicy tells the host whether its daily check may update and which channel it takes.
// A channel other than the known ones is stable. A change of channel forgets what the
// last check found on the other one.
func (c *Checker) SetPolicy(p Policy) error {
	if !release.ValidChannel(p.Channel) {
		p.Channel = release.Stable
	}
	c.mu.Lock()
	if c.policy.Channel != p.Channel {
		c.found = nil
	}
	c.policy = p
	c.mu.Unlock()
	data, _ := json.Marshal(p)
	return c.write("policy.json", data)
}

// Request asks the host to update now.
func (c *Checker) Request() error {
	data, _ := json.Marshal(map[string]string{"at": c.now().UTC().Format(time.RFC3339)})
	return c.write("request", data)
}

// Requested returns when the Update button was pressed, while the host has not taken
// the request yet.
func (c *Checker) Requested() (time.Time, bool) {
	if c.dir == "" {
		return time.Time{}, false
	}
	fi, err := os.Stat(filepath.Join(c.dir, "request"))
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// HostStatus is how the host's last update went.
type HostStatus struct {
	State   string `json:"state" enum:"running,ok,failed"`
	Version string `json:"version"`
	From    string `json:"from"`
	Error   string `json:"error"`
	At      string `json:"at" doc:"RFC 3339"`
}

func (c *Checker) Host() (HostStatus, bool) {
	var s HostStatus
	if c.dir == "" {
		return s, false
	}
	data, err := os.ReadFile(filepath.Join(c.dir, "status.json"))
	if err != nil || json.Unmarshal(data, &s) != nil || s.State == "" {
		return s, false
	}
	return s, true
}

func (c *Checker) write(name string, data []byte) error {
	if c.dir == "" {
		return ErrUnavailable
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(c.dir, name), data, 0o644)
}
