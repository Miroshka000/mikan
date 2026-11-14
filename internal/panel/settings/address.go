package settings

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"time"

	"mikan/internal/hostname"
)

const (
	// KeyFormerDomains are the names the panel answered at before its domain, or the
	// subscription domain, changed (a list of Former): the links handed out with them, the
	// webhook addresses entered at payment providers and the nodes' join keys keep working
	// while the names lead here (acme.Formers).
	KeyFormerDomains = "former_domains"
	// KeyAddressApplied is the panel's Address as the panel last followed it: a change made
	// by the CLI, or while the panel was down, is followed once it is seen.
	KeyAddressApplied = "address_applied"
)

// Address is where the panel is reached: what its links, its certificates and its nodes
// follow.
type Address struct {
	Domain     string `json:"domain"`
	SubDomain  string `json:"sub_domain"`
	PublicHost string `json:"public_host"`
}

// Address is the panel's address now.
func (s *Settings) Address(ctx context.Context) (Address, error) {
	var a Address
	for key, dst := range map[string]*string{KeyDomain: &a.Domain, KeySubDomain: &a.SubDomain, KeyPublicHost: &a.PublicHost} {
		v, err := s.String(ctx, key)
		if err != nil {
			return Address{}, err
		}
		*dst = strings.TrimSpace(v)
	}
	return a, nil
}

// Identifier is the name the panel's certificate is for: its domain, else its address.
func (a Address) Identifier() string {
	if a.Domain != "" {
		return a.Domain
	}
	return a.PublicHost
}

// Names are the names the panel answers at now, as FormerName writes them.
func (a Address) Names() []string {
	var out []string
	for _, n := range []string{a.Domain, a.SubDomain, a.Identifier()} {
		if n = FormerName(n); n != "" && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// Same says whether b is the same address.
func (a Address) Same(b Address) bool {
	return strings.EqualFold(a.Domain, b.Domain) && strings.EqualFold(a.SubDomain, b.SubDomain) && strings.EqualFold(a.PublicHost, b.PublicHost)
}

// Former is a name the panel answered at before (KeyFormerDomains): its domain, or its
// subscription domain, before a change, or the address of a panel that had no domain until
// it got one.
type Former struct {
	Name string `json:"name"`
	// Sub: the subscription domain's. Its requests reach the subscriptions alone, as the
	// subscription domain's do.
	Sub bool `json:"sub,omitempty"`
	// Since is when it stopped being the panel's (unix seconds).
	Since int64 `json:"since"`
}

// MaxFormers bounds the former names: each keeps a certificate of its own.
const MaxFormers = 8

// FormerName is a name as the former names keep it: lower-case, without the final dot.
func FormerName(s string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
}

// Formers are the former names, as saved.
func (s *Settings) Formers(ctx context.Context) ([]Former, error) {
	v, _, err := Get[[]Former](ctx, s, KeyFormerDomains)
	return v, err
}

// NextFormers are the former names once the panel's address went from prev to next at now.
// A domain or subscription domain left behind joins them, and so does the address of a panel
// that gets its first domain: its links named that address. A name the panel answers at
// again leaves them. Only names a CA certifies are kept, the newest MaxFormers. A change of
// the address alone adds nothing: the server moved, and the address it left leads nowhere.
func NextFormers(list []Former, prev, next Address, now time.Time) []Former {
	out := slices.Clone(list)
	add := func(name string, sub bool) {
		name = FormerName(name)
		if name == "" || !hostname.Public(name) {
			return
		}
		out = slices.DeleteFunc(out, func(f Former) bool { return FormerName(f.Name) == name })
		out = append(out, Former{Name: name, Sub: sub, Since: now.Unix()})
	}
	if !strings.EqualFold(prev.Domain, next.Domain) {
		add(prev.Identifier(), false)
	}
	if !strings.EqualFold(prev.SubDomain, next.SubDomain) {
		add(prev.SubDomain, true)
	}
	current := next.Names()
	out = slices.DeleteFunc(out, func(f Former) bool { return slices.Contains(current, FormerName(f.Name)) })
	if len(out) > MaxFormers {
		slices.SortStableFunc(out, func(a, b Former) int { return cmp.Compare(b.Since, a.Since) })
		out = out[:MaxFormers]
	}
	return out
}

// UpdateFormers adds to the former names what the change of the address from prev to the
// one saved in s now leaves behind. Call it in the transaction that changed the address.
func UpdateFormers(ctx context.Context, s *Settings, prev Address, now time.Time) error {
	next, err := s.Address(ctx)
	if err != nil || prev.Same(next) {
		return err
	}
	list, err := s.Formers(ctx)
	if err != nil {
		return err
	}
	out := NextFormers(list, prev, next, now)
	if slices.Equal(out, list) {
		return nil
	}
	return Set(ctx, s, KeyFormerDomains, out)
}

// SubHost is where subscription links point: the subscription domain, else the panel's
// name (SubEndpoint).
func (a Address) SubHost() string {
	if a.SubDomain != "" {
		return a.SubDomain
	}
	return a.Identifier()
}

// WithoutFormer is list without name.
func WithoutFormer(list []Former, name string) []Former {
	name = FormerName(name)
	return slices.DeleteFunc(slices.Clone(list), func(f Former) bool { return FormerName(f.Name) == name })
}
