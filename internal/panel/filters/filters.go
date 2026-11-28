// Package filters holds the ingress and egress filters' settings. The nodes apply them
// (see internal/node/filters.go): egress as routing rules that refuse users' traffic,
// ingress as a check of where a connection comes from.
package filters

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"

	"mikan/internal/nodeapi"
	"mikan/internal/panel/settings"
)

const KeyConfig = "traffic_filters"

// MaxItems bounds each list.
const MaxItems = 1000

// MailPorts are what the mail preset closes besides 25, which the nodes always close:
// submission and SMTPS, and 2525, the alternative submission port.
var MailPorts = []string{"465", "587", "2525"}

var ErrConfig = errors.New("filters_config")

type Config struct {
	Egress  Egress  `json:"egress"`
	Ingress Ingress `json:"ingress"`
}

type Egress struct {
	Enabled bool `json:"enabled"`
	// Mail closes MailPorts: a shared VPN address that sends mail is on blacklists within
	// hours.
	Mail     bool     `json:"mail"`
	Ports    []string `json:"ports"`
	Networks []string `json:"networks"`
	Domains  []string `json:"domains"`
	Rules    []Rule   `json:"rules,omitempty"`
	Sites    []Site   `json:"sites,omitempty"`
}

type Ingress struct {
	Enabled bool `json:"enabled"`
	// Allow: only Networks may connect; otherwise Networks may not.
	Allow    bool     `json:"allow"`
	Networks []string `json:"networks"`
}

func Default() Config {
	return Config{Egress: Egress{Mail: true, Ports: []string{}, Networks: []string{}, Domains: []string{}}, Ingress: Ingress{Networks: []string{}}}
}

func Load(ctx context.Context, s *settings.Settings) (Config, error) {
	c, _, err := settings.GetOver(ctx, s, KeyConfig, Default())
	c.fill()
	return c, err
}

func (c *Config) fill() {
	for _, l := range []*[]string{&c.Egress.Ports, &c.Egress.Networks, &c.Egress.Domains, &c.Ingress.Networks} {
		if *l == nil {
			*l = []string{}
		}
	}
}

// Problem is what is wrong with one entry of a list: the list ("egress.ports") and the
// entry's index.
type Problem struct {
	List  string
	Index int
	Value string
}

func (p *Problem) Error() string {
	return "filters: " + p.List + "[" + strconv.Itoa(p.Index) + "] " + p.Value
}

// Validate puts each list in its canonical form (trimmed, networks masked, domains in
// lower case, sorted, without repeats) and returns a *Problem for an entry that is not a
// port, a network or a domain, ErrConfig for a list too long.
func (c *Config) Validate() error {
	c.fill()
	lists := []struct {
		name  string
		l     *[]string
		canon func(string) (string, bool)
	}{
		{"egress.ports", &c.Egress.Ports, nodeapi.CanonPorts},
		{"egress.networks", &c.Egress.Networks, canonNetwork},
		{"egress.domains", &c.Egress.Domains, nodeapi.CanonDomain},
		{"ingress.networks", &c.Ingress.Networks, canonNetwork},
	}
	for _, x := range lists {
		if len(*x.l) > MaxItems {
			return ErrConfig
		}
		out := make([]string, 0, len(*x.l))
		for i, v := range *x.l {
			s, ok := x.canon(strings.TrimSpace(v))
			if !ok {
				// The answer names the entry; a pasted page must not come back whole.
				if r := []rune(v); len(r) > 64 {
					v = string(r[:64]) + "…"
				}
				return &Problem{List: x.name, Index: i, Value: v}
			}
			out = append(out, s)
		}
		slices.Sort(out)
		*x.l = slices.Compact(out)
	}
	return c.Egress.validateScoped()
}

func canonNetwork(s string) (string, bool) {
	p, ok := nodeapi.CanonNetwork(s)
	return p.String(), ok
}

// State is what the nodes get: nil when neither filter is on or has anything to do.
func (c Config) State() *nodeapi.Filters {
	f := &nodeapi.Filters{}
	if c.Egress.Enabled {
		ports := slices.Clone(c.Egress.Ports)
		if c.Egress.Mail {
			ports = append(ports, MailPorts...)
		}
		slices.Sort(ports)
		f.Egress = nodeapi.Egress{Ports: slices.Compact(ports), Networks: c.Egress.Networks, Domains: c.Egress.Domains}
	}
	if c.Ingress.Enabled && len(c.Ingress.Networks) > 0 {
		f.Ingress = nodeapi.Ingress{Allow: c.Ingress.Allow, Networks: c.Ingress.Networks}
	}
	if len(f.Egress.Ports)+len(f.Egress.Networks)+len(f.Egress.Domains)+len(f.Ingress.Networks) == 0 && !c.Egress.HasScoped() {
		return nil
	}
	return f
}
