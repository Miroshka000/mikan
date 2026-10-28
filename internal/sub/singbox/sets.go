package singbox

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
)

// ruleSets collects the rule sets the rules use: the geodata as MetaCubeX's .srs
// (routing.MetaCubeXRuleSet), and the profile's Clash lists converted (list).
type ruleSets struct {
	ctx    context.Context
	detour string // the lists download through the tunnel: GitHub is slow or cut off where it is needed
	lists  map[string]routing.Provider
	used   map[string]map[string]any
	order  []string
	// preview is the admin panel's (Dropped): no list is downloaded, one sing-box can read is
	// taken as read, and dropped tells what is left out.
	preview bool
	dropped []model.Note
}

// drop tells the preview what sing-box leaves out, once.
func (s *ruleSets) drop(code, subject string) {
	if n := note(subject, code); s.preview && !slices.Contains(s.dropped, n) {
		s.dropped = append(s.dropped, n)
	}
}

func (s *ruleSets) geo(kind, name string) string {
	name = strings.ToLower(name)
	tag := kind + "-" + name
	if _, ok := s.used[tag]; !ok {
		s.add(tag, map[string]any{"type": "remote", "tag": tag, "format": "binary", "url": routing.MetaCubeXRuleSet(kind, name), "download_detour": s.detour})
	}
	return tag
}

func (s *ruleSets) add(tag string, set map[string]any) {
	s.used[tag] = set
	s.order = append(s.order, tag)
}

// sets are the rule sets in the order the rules first use them.
func (s *ruleSets) sets() []map[string]any {
	out := make([]map[string]any, 0, len(s.order))
	for _, tag := range s.order {
		out = append(out, s.used[tag])
	}
	return out
}

// list is a Clash rule list as an inline rule set: the classical, domain and IP lists in
// YAML or text a sing-box rule can hold; false for the rest (.mrs) and for a list of
// nothing sing-box reads. An entry sing-box would refuse is left out: one fails the config.
func (s *ruleSets) list(name string) (string, bool) {
	tag := "list-" + name
	if _, ok := s.used[tag]; ok {
		return tag, true
	}
	p, ok := s.lists[name]
	if !ok || p.Format == "mrs" || p.URL == "" {
		return "", false
	}
	if s.preview {
		return tag, true
	}
	payload, err := singboxList(s.ctx, p.URL, listTTLOf(s.lists, p.URL))
	if err != nil {
		return "", false
	}
	var rules []map[string]any
	switch p.Behavior {
	case "domain":
		var plain, suffix []string
		for _, d := range payload {
			switch rest, ok := strings.CutPrefix(d, "+."); {
			case ok:
				suffix = append(suffix, rest)
			case strings.HasPrefix(d, "."):
				suffix = append(suffix, d)
			case !strings.ContainsAny(d, "*?, "):
				plain = append(plain, d)
			}
		}
		rules = headless(map[string][]any{"domain": anys(plain), "domain_suffix": anys(suffix)})
	case "ipcidr":
		var cidrs []string
		for _, c := range payload {
			if _, err := netip.ParsePrefix(c); err == nil {
				cidrs = append(cidrs, c)
			}
		}
		rules = headless(map[string][]any{"ip_cidr": anys(cidrs)})
	default:
		byField := map[string][]any{}
		for _, line := range payload {
			m, ok := routing.ReadMatch(line)
			if !ok || len(m) != 1 {
				continue
			}
			c, ok := s.condition(m[0])
			if !ok {
				continue
			}
			for k, v := range c {
				switch l := v.(type) {
				case []string:
					byField[k] = append(byField[k], anys(l)...)
				case []int:
					for _, n := range l {
						byField[k] = append(byField[k], n)
					}
				}
			}
		}
		// Fields of one kind are OR'ed in a rule; kinds are AND'ed, so each kind is a
		// rule of its own.
		for _, kinds := range [][]string{{"domain", "domain_suffix", "domain_keyword", "domain_regex"}, {"ip_cidr"}, {"source_ip_cidr"},
			{"process_name"}, {"package_name"}, {"process_path"}, {"process_path_regex"}, {"port", "port_range"}, {"source_port", "source_port_range"}} {
			r := map[string][]any{}
			for _, k := range kinds {
				r[k] = byField[k]
			}
			rules = append(rules, headless(r)...)
		}
	}
	if len(rules) == 0 {
		return "", false
	}
	s.add(tag, map[string]any{"type": "inline", "tag": tag, "rules": rules})
	return tag, true
}

// headless is a rule of the fields that hold something; none when none does.
func headless(fields map[string][]any) []map[string]any {
	r := map[string]any{}
	for k, l := range fields {
		if len(l) > 0 {
			r[k] = l
		}
	}
	if len(r) == 0 {
		return nil
	}
	return []map[string]any{r}
}

func anys(l []string) []any {
	out := make([]any, len(l))
	for i, s := range l {
		out[i] = s
	}
	return out
}

// singboxList is the payload of a Clash rule list, fetched again ttl later (listTTLOf): the
// panel's lists are pinned to a commit, but an own profile's list may follow a branch. A
// failure is tried again a few minutes later, and meanwhile the payload fetched last stays.
var singboxList = cachedList

const (
	listTTL    = 24 * time.Hour // a list of no interval
	listMinTTL = time.Hour
	listMaxTTL = 7 * 24 * time.Hour
	listRetry  = 5 * time.Minute
)

// listTTLOf is how long the list at src lives: the interval its rule-provider gives, within
// an hour and a week (a shorter one fetches for every profile; a longer one keeps a list
// that follows a branch stale), and a day for one of none. Of providers that share the
// address the shortest wins: each asks for the list to be that fresh.
func listTTLOf(lists map[string]routing.Provider, src string) time.Duration {
	ttl := time.Duration(0)
	for _, p := range lists {
		if p.URL != src {
			continue
		}
		d := listTTL
		if p.Interval > 0 {
			d = min(max(time.Duration(p.Interval)*time.Second, listMinTTL), listMaxTTL)
		}
		if ttl == 0 || d < ttl {
			ttl = d
		}
	}
	if ttl == 0 {
		return listTTL
	}
	return ttl
}

func cachedList(ctx context.Context, src string, ttl time.Duration) ([]string, error) {
	listCache.Lock()
	e, ok := listCache.m[src]
	listCache.Unlock()
	if !ok || time.Since(e.at) >= e.ttl(ttl) {
		seen := e.at
		payload, err := fetchList(ctx, src)
		listCache.Lock()
		cur, ok := listCache.m[src]
		if err != nil && ok && !cur.at.Equal(seen) {
			e = cur // a fetch that ended while ours ran stands: a failure does not undo it
		} else {
			if err != nil && ok {
				payload = cur.payload
			}
			e = listEntry{payload: payload, err: err, at: time.Now()}
			listCache.m[src] = e
		}
		listCache.Unlock()
	}
	if e.payload != nil {
		return e.payload, nil
	}
	return nil, e.err
}

// listEntry is the payload fetched last and the error of the last fetch, if it failed.
type listEntry struct {
	payload []string
	err     error
	at      time.Time
}

// ttl is how long e lives: listRetry after a failure, else fresh.
func (e listEntry) ttl(fresh time.Duration) time.Duration {
	if e.err != nil {
		return listRetry
	}
	return fresh
}

var listCache = struct {
	sync.Mutex
	m map[string]listEntry
}{m: map[string]listEntry{}}

func fetchList(ctx context.Context, src string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", src, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return parseList(raw), nil
}

// parseList reads a Clash rule list: YAML with a payload, or a line per entry. A payload of
// nothing (`payload: []`, `payload:`) is an empty list, not a line "payload: []".
func parseList(raw []byte) []string {
	var doc struct {
		Payload yaml.Node `yaml:"payload"`
	}
	if yaml.Unmarshal(raw, &doc) == nil && doc.Payload.Kind != 0 {
		var payload []string
		if doc.Payload.Decode(&payload) == nil {
			return payload
		}
	}
	var out []string
	for _, l := range strings.Split(string(raw), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}
