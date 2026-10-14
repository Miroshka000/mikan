package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"mikan/internal/nodetls"
	"mikan/internal/panel/audit"
	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/subs"
	"mikan/internal/release"
)

// nodeCmd manages the panel's nodes from the server shell. Join keys go to stdout alone,
// so a script can pass them to the node's installer; they are shown once.
func nodeCmd(ctx context.Context, st *store.Store, dataDir string, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("node needs list, add, key or set\n\n" + usage)
	}
	panelCert := func() (nodetls.Pair, error) {
		return nodetls.LoadOrCreate(filepath.Join(dataDir, "tls", "nodes"), time.Now())
	}
	// Where the node's hello goes (nodetls.Key.PanelURL).
	panelURL := func() string {
		ep, err := settings.New(st.Q).Endpoint(ctx)
		if err != nil {
			return ""
		}
		return ep.URL()
	}
	switch args[0] {
	case "list":
		nodes, err := st.Q.ListNodes(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tAPI ADDRESS\tCLIENTS CONNECT TO\tENABLED")
		for _, n := range nodes {
			addr, host := n.Address, domain.NodeHost(n)
			if addr == "" {
				addr, host = "local", "the panel's address"
			}
			on := "yes"
			if n.Enabled == 0 {
				on = "no"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", n.ID, n.Name, addr, host, on)
		}
		return tw.Flush()
	case "add":
		fs := flag.NewFlagSet("node add", flag.ContinueOnError)
		name := fs.String("name", "", "name, the node's group in subscriptions, e.g. \"🇺🇸 USA\"")
		host := fs.String("host", "", "IP or host name of the node's server")
		dom := fs.String("domain", "", "the node's domain for Hysteria2/TUIC (optional)")
		port := fs.Int("api-port", 0, "port of the node's API (random by default)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*name) == "" || strings.TrimSpace(*host) == "" {
			return errors.New("--name and --host are required")
		}
		panel, err := panelCert()
		if err != nil {
			return err
		}
		rules, err := nodeRules(ctx, st)
		if err != nil {
			return err
		}
		n, key, err := domain.AddNode(ctx, st, panel, domain.NodeInput{Name: *name, Host: *host, Domain: *dom, APIPort: *port, PanelURL: panelURL(), Rules: rules}, time.Now())
		if err != nil {
			return nodeError(err)
		}
		_ = audit.Write(ctx, st.Q, time.Now(), audit.Entry{Action: "cli.node_create", TargetType: "node", TargetID: strconv.FormatInt(n.ID, 10),
			Details: map[string]any{"name": n.Name, "address": n.Address}})
		fmt.Fprintln(stdout, key)
		fmt.Fprintf(stderr, "Node %d %q added, its API is %s. On the node's server run:\n  %s\n", n.ID, n.Name, n.Address, release.JoinCommand("<the key above>"))
		return nil
	case "key":
		id, err := nodeID(args)
		if err != nil {
			return err
		}
		panel, err := panelCert()
		if err != nil {
			return err
		}
		key, err := domain.RekeyNode(ctx, st, panel, id, panelURL(), time.Now())
		switch {
		case errors.Is(err, domain.ErrUnknownNode):
			return fmt.Errorf("no node %d", id)
		case errors.Is(err, domain.ErrLocalNode):
			return errors.New("the panel's own node needs no key")
		case err != nil:
			return err
		}
		_ = audit.Write(ctx, st.Q, time.Now(), audit.Entry{Action: "cli.node_rekey", TargetType: "node", TargetID: strconv.FormatInt(id, 10)})
		fmt.Fprintln(stdout, key)
		fmt.Fprintln(stderr, "The node's old key no longer works.")
		return nil
	case "set":
		id, err := nodeID(args)
		if err != nil {
			return err
		}
		n, err := st.Q.GetNode(ctx, id)
		if err != nil {
			return fmt.Errorf("no node %d", id)
		}
		fs := flag.NewFlagSet("node set", flag.ContinueOnError)
		name := fs.String("name", n.Name, "name, the node's group in subscriptions")
		host := fs.String("host", n.PublicHost, "IP or host name of the node's server")
		dom := fs.String("domain", n.Domain, "the node's domain")
		enabled := fs.Bool("enabled", n.Enabled != 0, "whether the node serves clients")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		// Only what the flags change is checked and written: the rules are the panel's own.
		patch := domain.NodePatch{}
		if v := strings.TrimSpace(*name); v != n.Name {
			patch.Name = &v
		}
		if v := strings.TrimSpace(*host); v != n.PublicHost {
			patch.Host = &v
		}
		if v := strings.TrimSpace(*dom); v != n.Domain {
			patch.Domain = &v
		}
		if on := *enabled; on != (n.Enabled != 0) {
			patch.Enabled = &on
		}
		if patch.Rules, err = nodeRules(ctx, st); err != nil {
			return err
		}
		n, err = domain.UpdateNode(ctx, st, n.ID, patch, time.Now())
		if err != nil {
			return nodeError(err)
		}
		_ = audit.Write(ctx, st.Q, time.Now(), audit.Entry{Action: "cli.node_update", TargetType: "node", TargetID: strconv.FormatInt(id, 10),
			Details: map[string]any{"name": n.Name, "enabled": n.Enabled != 0}})
		fmt.Fprintf(stderr, "Node %d %q saved. The panel applies the changes within 30 seconds.\n", n.ID, n.Name)
		return nil
	default:
		return fmt.Errorf("unknown node subcommand %q\n\n%s", args[0], usage)
	}
}

// nodeDNS checks a node's domain against public DNS, as the admin panel does; the tests
// put a fake resolver here.
var nodeDNS = dnscheck.New()

// nodeRules are what a node's name and domain are checked against: the same as in the
// admin panel, the subscription's groups and public DNS.
func nodeRules(ctx context.Context, st *store.Store) (domain.NodeRules, error) {
	g, err := subs.LoadGroups(ctx, settings.New(st.Q))
	if err != nil {
		return domain.NodeRules{}, err
	}
	return domain.NodeRules{Name: g.CheckName, DNS: nodeDNS}, nil
}

// nameText says in words what a node's name is refused for.
var nameText = map[string]string{
	"name_in_use":   "another node has this name",
	"name_is_group": "this is the name of a group in the subscription",
	"name_blank":    "the name is empty",
	"name_too_long": "the name is longer than 48 characters",
	"name_bad_char": "the name has a comma, a quote, a backslash or a control character",
	"name_reserved": "the name is one of Clash's own policies",
}

// nodeError says in words what the rules of a node refused.
func nodeError(err error) error {
	var name *domain.NameError
	var dom *domain.DomainError
	var out []error
	switch {
	case errors.As(err, &name):
		text := nameText[name.Code]
		if text == "" {
			text = name.Code
		}
		return errors.New("--name: " + text)
	case errors.Is(err, domain.ErrUnknownNode):
		return errors.New("no such node")
	case errors.Is(err, domain.ErrLocalNode):
		return errors.New("the panel's own node has the panel's address: change it in the panel's settings")
	case errors.Is(err, domain.ErrChannelRequired):
		return errors.New("the fair share needs the width of the node's channel")
	}
	if errors.Is(err, domain.ErrBadHost) {
		out = append(out, errors.New("--host: want an IP address or a host name like vpn.example.com"))
	}
	if errors.Is(err, domain.ErrBadDomain) {
		out = append(out, errors.New("--domain: want a host name like vpn.example.com"))
	}
	if errors.As(err, &dom) {
		out = append(out, errors.New(domainText(dom)))
	}
	if len(out) == 0 {
		return err
	}
	return errors.Join(out...)
}

// domainText says in words why a domain was refused.
func domainText(e *domain.DomainError) string {
	switch e.Code {
	case "domain_elsewhere":
		return fmt.Sprintf("--domain: %s does not lead to the node (%s)", e.Domain, e.Value)
	case "domain_not_found":
		return fmt.Sprintf("--domain: %s has no A or AAAA record in public DNS", e.Domain)
	case "server_ip_unknown":
		return "--domain: the node's address is not known, so the domain cannot be compared with it"
	}
	return fmt.Sprintf("--domain: %s could not be checked against public DNS: %v", e.Domain, e.Err)
}

func nodeID(args []string) (int64, error) {
	if len(args) < 2 {
		return 0, errors.New("name the node by its ID: mikan admin node list")
	}
	id, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("bad node ID %q", args[1])
	}
	return id, nil
}
