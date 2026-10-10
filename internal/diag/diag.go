// Package diag looks at what a mikan server needs from its host: names that resolve, a way
// to the internet, GitHub and GHCR for updates, a clock that is right, room on the disk
// and free memory. A node runs it for its panel (POST /v1/diagnose), the panel on its own
// server. The targets are fixed here: nothing a caller sends says where to connect.
package diag

import (
	"bufio"
	"context"
	"crypto/tls"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"mikan/internal/nodeapi"
)

// Targets are the places a check reaches.
type Targets struct {
	// Name is looked up to tell that DNS works.
	Name string
	// Internet are addresses dialled without DNS: one that connects is enough.
	Internet []string
	// GitHub and GHCR are where releases and images come from: any HTTP answer will do.
	GitHub string
	GHCR   string
}

// Default are the real targets.
var Default = Targets{
	Name:     "github.com",
	Internet: []string{"1.1.1.1:443", "8.8.8.8:443"},
	GitHub:   "https://github.com/",
	GHCR:     "https://ghcr.io/v2/",
}

// Options are what Run needs; the zero value of each field is the real thing.
type Options struct {
	Targets Targets
	// DataDir is the directory whose disk is checked; "" skips the disk.
	DataDir  string
	Resolver *net.Resolver
	Dialer   *net.Dialer
	// HTTP fetches GitHub and GHCR; nil makes a client of its own.
	HTTP *http.Client
	Now  func() time.Time
	// Disk and Memory read the host; nil reads the real one.
	Disk   func(dir string) (free, total uint64, ok bool)
	Memory func() (available, total uint64, ok bool)
}

// Limits of what is worth a word.
const (
	// SkewWarn: a clock this far off breaks TLS checks and REALITY's time window.
	SkewWarn = 30 * time.Second
	DiskFail = 500 << 20
	DiskWarn = 2 << 30
	MemWarn  = 100 << 20
	// timeout bounds each network check.
	timeout = 8 * time.Second
)

// Run makes every check at once and returns them in a fixed order.
func Run(ctx context.Context, o Options) nodeapi.Diagnosis {
	if o.Targets.Name == "" {
		o.Targets = Default
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Resolver == nil {
		o.Resolver = net.DefaultResolver
	}
	if o.Dialer == nil {
		o.Dialer = &net.Dialer{Timeout: timeout}
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{Timeout: timeout, Transport: &http.Transport{
			Proxy: nil, DialContext: o.Dialer.DialContext, TLSHandshakeTimeout: timeout,
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if o.Disk == nil {
		o.Disk = diskSpace
	}
	if o.Memory == nil {
		o.Memory = memory
	}
	var (
		wg                      sync.WaitGroup
		dns, inet, github, ghcr nodeapi.DiagItem
		githubSkew, ghcrSkew    *time.Duration
	)
	wg.Go(func() { dns = lookup(ctx, o) })
	wg.Go(func() { inet = internet(ctx, o) })
	wg.Go(func() { github, githubSkew = fetch(ctx, o, "github", o.Targets.GitHub) })
	wg.Go(func() { ghcr, ghcrSkew = fetch(ctx, o, "ghcr", o.Targets.GHCR) })
	wg.Wait()
	skew := githubSkew
	if skew == nil {
		skew = ghcrSkew
	}
	return nodeapi.Diagnosis{At: o.Now().UTC(), Items: []nodeapi.DiagItem{dns, inet, github, ghcr, Clock(skew), disk(o), mem(o)}}
}

func fail(id, code string, err error) nodeapi.DiagItem {
	it := nodeapi.DiagItem{ID: id, Status: nodeapi.CheckFail, Code: code}
	if err != nil {
		it.Detail = clip(err.Error())
	}
	return it
}

func clip(s string) string {
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

func lookup(ctx context.Context, o Options) nodeapi.DiagItem {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	addrs, err := o.Resolver.LookupNetIP(ctx, "ip", o.Targets.Name)
	if err != nil || len(addrs) == 0 {
		code := nodeapi.Classify(err)
		if code == nodeapi.LinkUnknown || code == "" {
			code = nodeapi.LinkDNS
		}
		it := fail("dns", code, err)
		it.Params = map[string]string{"name": o.Targets.Name}
		return it
	}
	return nodeapi.DiagItem{ID: "dns", Status: nodeapi.CheckOK, Params: map[string]string{"name": o.Targets.Name}}
}

func internet(ctx context.Context, o Options) nodeapi.DiagItem {
	var last error
	for _, addr := range o.Targets.Internet {
		c, cancel := context.WithTimeout(ctx, timeout)
		conn, err := o.Dialer.DialContext(c, "tcp", addr)
		cancel()
		if err == nil {
			conn.Close()
			return nodeapi.DiagItem{ID: "internet", Status: nodeapi.CheckOK}
		}
		last = err
	}
	return fail("internet", nodeapi.Classify(last), last)
}

// fetch asks a place for any answer, and reads the time it says it is.
func fetch(ctx context.Context, o Options, id, url string) (nodeapi.DiagItem, *time.Duration) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fail(id, nodeapi.LinkUnknown, err), nil
	}
	req.Header.Set("User-Agent", "mikan-diag")
	sent := o.Now()
	resp, err := o.HTTP.Do(req)
	if err != nil {
		return fail(id, nodeapi.Classify(err), err), nil
	}
	got := o.Now()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	it := nodeapi.DiagItem{ID: id, Status: nodeapi.CheckOK, Params: map[string]string{"status": strconv.Itoa(resp.StatusCode)}}
	if at, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
		// The answer was made between sending and receiving: the middle is the best guess.
		mid := sent.Add(got.Sub(sent) / 2)
		d := mid.Sub(at)
		return it, &d
	}
	return it, nil
}

// Clock words a skew (this server's clock minus the right time); nil: not known.
func Clock(skew *time.Duration) nodeapi.DiagItem {
	if skew == nil {
		return nodeapi.DiagItem{ID: "clock", Status: nodeapi.CheckSkip, Code: "unknown"}
	}
	secs := int64(math.Round(skew.Seconds()))
	it := nodeapi.DiagItem{ID: "clock", Status: nodeapi.CheckOK, Params: map[string]string{"skew": strconv.FormatInt(secs, 10)}}
	if *skew >= SkewWarn || *skew <= -SkewWarn {
		it.Status, it.Code = nodeapi.CheckWarn, "skew"
	}
	return it
}

func disk(o Options) nodeapi.DiagItem {
	if o.DataDir == "" {
		return nodeapi.DiagItem{ID: "disk", Status: nodeapi.CheckSkip, Code: "unknown"}
	}
	free, total, ok := o.Disk(o.DataDir)
	if !ok {
		return nodeapi.DiagItem{ID: "disk", Status: nodeapi.CheckSkip, Code: "unknown"}
	}
	it := nodeapi.DiagItem{ID: "disk", Status: nodeapi.CheckOK, Params: map[string]string{"free": strconv.FormatUint(free, 10), "total": strconv.FormatUint(total, 10)}}
	switch {
	case free < DiskFail:
		it.Status, it.Code = nodeapi.CheckFail, "low"
	case free < DiskWarn:
		it.Status, it.Code = nodeapi.CheckWarn, "low"
	}
	return it
}

func mem(o Options) nodeapi.DiagItem {
	avail, total, ok := o.Memory()
	if !ok {
		return nodeapi.DiagItem{ID: "memory", Status: nodeapi.CheckSkip, Code: "unknown"}
	}
	it := nodeapi.DiagItem{ID: "memory", Status: nodeapi.CheckOK, Params: map[string]string{"available": strconv.FormatUint(avail, 10), "total": strconv.FormatUint(total, 10)}}
	if avail < MemWarn {
		it.Status, it.Code = nodeapi.CheckWarn, "low"
	}
	return it
}

// ParseMeminfo reads MemAvailable and MemTotal from /proc/meminfo, in bytes.
func ParseMeminfo(r io.Reader) (available, total uint64, ok bool) {
	var haveA, haveT bool
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		key, rest, found := strings.Cut(sc.Text(), ":")
		if !found {
			continue
		}
		f := strings.Fields(rest)
		if len(f) == 0 {
			continue
		}
		v, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			continue
		}
		if len(f) > 1 && f[1] == "kB" {
			v <<= 10
		}
		switch key {
		case "MemAvailable":
			available, haveA = v, true
		case "MemTotal":
			total, haveT = v, true
		}
	}
	return available, total, haveA && haveT
}
