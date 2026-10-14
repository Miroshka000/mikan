package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"mikan/internal/hostname"
	"mikan/internal/panel/app"
	"mikan/internal/panel/audit"
	"mikan/internal/panel/config"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// migrateCmd finishes moving the panel to this server after `mikan restore`:
// it points the panel and subscription links at the new address while keeping
// everything clients already hold — sub tokens, the subscription path, tariffs,
// nodes and their identities (data/panel/tls migrates with the backup, so remote
// nodes trust the moved panel without rejoining). It then checks the remote nodes
// are reachable and prints what to open in the firewall.
func migrateCmd(ctx context.Context, st *store.Store, set *settings.Settings, cfg config.Config, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	host := fs.String("public-host", "", "public IP or domain of THIS server (required)")
	port := fs.Int("port", 0, "panel port (by default the one from the backup)")
	domain := fs.String("domain", "", "domain for a Let's Encrypt certificate (by default the one from the backup)")
	noDomain := fs.Bool("no-domain", false, "drop the migrated domain and serve by IP")
	noCheck := fs.Bool("no-check-nodes", false, "do not reach the remote nodes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	*host = strings.TrimSpace(*host)
	if *host == "" || !hostname.Valid(*host) {
		return fmt.Errorf("--public-host: want an IP address or a host name, got %q", *host)
	}
	if *port < 0 || *port > 65535 {
		return fmt.Errorf("--port: want 1 to 65535, got %d", *port)
	}
	*domain = strings.TrimSpace(strings.ToLower(*domain))
	if *noDomain {
		*domain = ""
	} else if *domain != "" && !hostname.Valid(*domain) {
		return fmt.Errorf("--domain: want a host name like vpn.example.com, got %q", *domain)
	}
	oldHost, _ := set.String(ctx, settings.KeyPublicHost)
	oldDomain, _ := set.String(ctx, settings.KeyDomain)
	oldPort, _, _ := settings.Get[int](ctx, set, settings.KeyPanelPort)
	newPort := oldPort
	if *port != 0 {
		newPort = *port
	}
	if newPort <= 0 || newPort > 65535 {
		return fmt.Errorf("panel port is %d: pass a working one with --port", newPort)
	}
	newDomain := oldDomain
	if *noDomain || *domain != "" {
		newDomain = *domain
	}
	err := st.Tx(ctx, func(q *db.Queries) error {
		txSet := settings.New(q)
		if err := settings.Set(ctx, txSet, settings.KeyPublicHost, *host); err != nil {
			return err
		}
		if err := settings.Set(ctx, txSet, settings.KeyDomain, newDomain); err != nil {
			return err
		}
		if err := settings.Set(ctx, txSet, settings.KeyPanelPort, newPort); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	_ = audit.Write(ctx, st.Q, time.Now(), audit.Entry{Action: "cli.migrate", Details: map[string]any{
		"old_host": oldHost, "old_domain": oldDomain, "old_port": oldPort,
		"host": *host, "domain": newDomain, "port": newPort,
	}})
	paths, err := set.Paths(ctx)
	if err != nil {
		return err
	}
	if paths.Sub == "" {
		return errors.New("the subscription path is not set: the restored database looks broken")
	}
	subHost := newDomain
	if subHost == "" {
		subHost = *host
	}
	subPort := newPort
	if p, _, err := settings.Get[int](ctx, set, settings.KeySubPort); err == nil && p > 0 {
		subPort = p
	}
	fmt.Fprintf(stderr, "Address updated: %s -> %s (domain %q -> %q, port %d -> %d).\n",
		oldHost, *host, oldDomain, newDomain, oldPort, newPort)
	fmt.Fprintf(stderr, "Subscription tokens and the path %q are unchanged: clients keep their links, only the host moves.\n", paths.Sub)
	fmt.Fprintln(stdout, "Subscriptions: https://"+net.JoinHostPort(subHost, strconv.Itoa(subPort))+"/"+paths.Sub+"/<token>")
	if u, err := panelURL(ctx, set); err == nil {
		fmt.Fprintln(stdout, "Panel: "+u)
	}
	if *noCheck {
		fmt.Fprintln(stderr, "Node check skipped (--no-check-nodes).")
	} else if err := checkMigratedNodes(ctx, st, cfg, stderr); err != nil {
		return err
	}
	printFirewallHint(ctx, st, newPort, subPort, stderr)
	return nil
}

// checkMigratedNodes reaches every remote node with the migrated panel identity.
// The panel's client certificate moved with the backup, so a reachable node needs
// no rejoin; an unreachable one is usually the node's firewall still allowing the
// old panel IP only (join --panel-ip) — re-run the join on the node's server.
func checkMigratedNodes(ctx context.Context, st *store.Store, cfg config.Config, stderr io.Writer) error {
	nodes, err := st.Q.ListNodes(ctx)
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		fmt.Fprintln(stderr, "No nodes in the database.")
		return nil
	}
	failed := 0
	for _, n := range nodes {
		if n.Address == "" {
			fmt.Fprintf(stderr, "Node %d %q: local, runs on this server.\n", n.ID, n.Name)
			continue
		}
		c, err := app.NodeClient(cfg, n)
		if err != nil {
			if errors.Is(err, nodesync.ErrNoNode) {
				fmt.Fprintf(stderr, "Node %d %q: local, but the panel runs without a node socket.\n", n.ID, n.Name)
				continue
			}
			fmt.Fprintf(stderr, "Node %d %q: cannot set up a client: %v.\n", n.ID, n.Name, err)
			failed++
			continue
		}
		hctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, err = c.Health(hctx)
		cancel()
		if err != nil {
			fmt.Fprintf(stderr, "Node %d %q at %s: UNREACHABLE (%v). If its firewall allows only the old panel IP, re-run the join on the node's server.\n",
				n.ID, n.Name, n.Address, err)
			failed++
			continue
		}
		fmt.Fprintf(stderr, "Node %d %q at %s: ok.\n", n.ID, n.Name, n.Address)
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d remote node(s) unreachable: subscriptions served by this panel keep working, fix the nodes' firewall or rejoin them", failed, len(nodes))
	}
	return nil
}

// printFirewallHint lists the ports of this server clients connect to, so the admin
// can open them after the move (a fresh server only has the install-time panel port).
func printFirewallHint(ctx context.Context, st *store.Store, panelPort, subPort int, stderr io.Writer) {
	all, err := st.Q.ListInbounds(ctx)
	if err != nil {
		return
	}
	local := map[int]bool{}
	for _, in := range all {
		if in.NodeID == nodesync.LocalNode {
			if p, err := strconv.Atoi(in.Port); err == nil {
				local[p] = true
			}
		}
	}
	ports := map[int]bool{panelPort: true}
	if subPort != panelPort {
		ports[subPort] = true
	}
	for p := range local {
		ports[p] = true
	}
	list := make([]int, 0, len(ports))
	for p := range ports {
		list = append(list, p)
	}
	sort.Ints(list)
	str := make([]string, 0, len(list))
	for _, p := range list {
		str = append(str, strconv.Itoa(p))
	}
	fmt.Fprintf(stderr, "Open in ufw on this server (TCP/UDP): %s.\n", strings.Join(str, ", "))
}
