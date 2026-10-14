package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"mikan/internal/panel/app"
	"mikan/internal/panel/audit"
	"mikan/internal/panel/config"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/targets"
	"mikan/internal/proto"
	"mikan/internal/scan"
)

// TargetScan is what `targets scan --json` prints; the installer reads it.
type TargetScan struct {
	Node     int64  `json:"node"`
	IP       string `json:"ip"`
	Scanned  int    `json:"scanned"`
	FromNode bool   `json:"from_node"`
	// SelfSteal is the panel's own domain on the panel's HTTPS, offered for the panel's
	// own node with a domain. It is OK once the domain has its Let's Encrypt certificate.
	SelfSteal *scan.Result  `json:"self_steal,omitempty"`
	Results   []scan.Result `json:"results"`
	Current   []Target      `json:"current"`
}

// Target is the site a REALITY inbound shows to probes now.
type Target struct {
	Inbound string `json:"inbound"`
	Enabled bool   `json:"enabled"`
	Dest    string `json:"dest"`
	SNI     string `json:"sni"`
}

// targetsCmd finds REALITY camouflage sites next to a node and points the node's REALITY
// inbounds at one. The node checks and scans from its own network when it can, so the
// round trips are the ones REALITY will see.
func targetsCmd(ctx context.Context, st *store.Store, set *settings.Settings, cfg config.Config, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("targets needs scan, check or apply\n\n" + usage)
	}
	switch args[0] {
	case "scan":
		fs := flag.NewFlagSet("targets scan", flag.ContinueOnError)
		node := fs.Int64("node", 1, "node to look around (1 is the panel's own, see mikan admin node list)")
		asJSON := fs.Bool("json", false, "print JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		n, err := st.Q.GetNode(ctx, *node)
		if err != nil {
			return fmt.Errorf("no node %d", *node)
		}
		res, err := scanTargets(ctx, st, set, cfg, n)
		if err != nil {
			return err
		}
		if *asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(res)
		}
		printScan(stdout, res)
		return nil
	case "check":
		fs := flag.NewFlagSet("targets check", flag.ContinueOnError)
		node := fs.Int64("node", 1, "the node that would dial the site (1 is the panel's own)")
		dest := fs.String("dest", "", "the site as host:port")
		sni := fs.String("sni", "", "the name clients send (the host of --dest by default)")
		asJSON := fs.Bool("json", false, "print JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*dest) == "" {
			return errors.New("usage: mikan admin targets check --dest HOST:PORT [--sni NAME] [--node NODE] [--json]")
		}
		n, err := st.Q.GetNode(ctx, *node)
		if err != nil {
			return fmt.Errorf("no node %d", *node)
		}
		r := checkTarget(ctx, set, cfg, n, strings.TrimSpace(*dest), strings.TrimSpace(*sni))
		if *asJSON {
			return json.NewEncoder(stdout).Encode(r)
		}
		if !r.OK {
			return fmt.Errorf("%s does not suit REALITY: %s", r.Dest, targetProblem(r))
		}
		fmt.Fprintf(stdout, "%s (%s) suits REALITY: TLS 1.3, HTTP/2, X25519, valid certificate, %d ms\n", r.Dest, r.SNI, r.RTTms)
		return nil
	case "apply":
		fs := flag.NewFlagSet("targets apply", flag.ContinueOnError)
		node := fs.Int64("node", 1, "the inbounds' node (1 is the panel's own)")
		dest := fs.String("dest", "", "the site as host:port: www.example.org:443, 203.0.113.20:443")
		sni := fs.String("sni", "", "the name clients send (the host of --dest by default; required for an IP)")
		all := fs.Bool("all", false, "every REALITY inbound of the node")
		inbound := fs.String("inbound", "", "one inbound, by name (see mikan admin inbound list)")
		force := fs.Bool("force", false, "apply even when the site fails the check")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		*dest, *sni, *inbound = strings.TrimSpace(*dest), strings.TrimSpace(*sni), strings.TrimSpace(*inbound)
		if *dest == "" || *all == (*inbound != "") {
			return errors.New("usage: mikan admin targets apply --dest HOST:PORT [--sni NAME] (--all | --inbound NAME) [--node NODE]")
		}
		n, err := st.Q.GetNode(ctx, *node)
		if err != nil {
			return fmt.Errorf("no node %d", *node)
		}
		names := []string{*inbound}
		if *all {
			current, err := realityTargets(ctx, st, n.ID)
			if err != nil {
				return err
			}
			if len(current) == 0 {
				return fmt.Errorf("node %d has no REALITY inbounds", n.ID)
			}
			names = names[:0]
			for _, t := range current {
				names = append(names, t.Inbound)
			}
		}
		if !*force {
			r := checkTarget(ctx, set, cfg, n, *dest, *sni)
			if !r.OK {
				return fmt.Errorf("%s does not suit REALITY: %s; --force applies it anyway", *dest, targetProblem(r))
			}
			fmt.Fprintf(stderr, "%s (%s): TLS 1.3, HTTP/2, X25519, valid certificate, %d ms.\n", *dest, r.SNI, r.RTTms)
		}
		ins := domain.NewInbounds(st, nil, time.Now)
		for _, name := range names {
			// The keys stay: clients keep working once they refresh the subscription.
			var prev, next db.Inbound
			in, err := ins.Find(ctx, n.ID, name)
			if err == nil {
				prev, next, err = ins.Update(ctx, in.ID, domain.InboundPatch{Dest: dest, ServerName: sni})
			}
			var pe *proto.Error
			switch {
			case errors.Is(err, domain.ErrUnknownInbound):
				return fmt.Errorf("node %d has no inbound %q: see mikan admin inbound list", n.ID, name)
			case errors.As(err, &pe) && pe.Code == "dest_no_reality":
				return fmt.Errorf("inbound %s has no REALITY camouflage", name)
			case errors.As(err, &pe):
				return fmt.Errorf("%s: %s", name, targetError(pe))
			case err != nil:
				return err
			}
			before, after := targetOf(prev), targetOf(next)
			_ = audit.Write(ctx, st.Q, time.Now(), audit.Entry{Action: "cli.inbound_target", TargetType: "inbound", TargetID: next.Name,
				Details: map[string]any{"node": n.ID, "old": before.Dest, "new": after.Dest, "sni": after.SNI}})
			fmt.Fprintf(stderr, "Inbound %s: %s → %s (%s)\n", name, before.Dest, after.Dest, after.SNI)
		}
		fmt.Fprintln(stderr, "The node gets the change within 30 seconds; clients need to refresh their subscription.")
		return nil
	default:
		return fmt.Errorf("unknown targets subcommand %q\n\n%s", args[0], usage)
	}
}

// scanNeighbors is the panel's own scan of a /24, when the node cannot do it. The tests put
// a fake here: the real one opens ~250 connections to public addresses.
var scanNeighbors = scan.Neighbors

// targetDeps is what the shared checks (targets) need: the node n does them from its own
// network when it can be reached, otherwise the panel does.
func targetDeps(set *settings.Settings, cfg config.Config, n db.Node) targets.Deps {
	d := targets.Deps{Settings: set, Neighbors: scanNeighbors}
	if c, err := app.NodeClient(cfg, n); err == nil {
		d.CheckTarget, d.ScanTargets = c.CheckTarget, c.ScanTargets
	}
	return d
}

func scanTargets(ctx context.Context, st *store.Store, set *settings.Settings, cfg config.Config, n db.Node) (TargetScan, error) {
	res := TargetScan{Node: n.ID, Results: []scan.Result{}}
	var err error
	if res.Current, err = realityTargets(ctx, st, n.ID); err != nil {
		return res, err
	}
	// The panel's own domain on its HTTPS is offered for the panel's own node with a domain.
	found, err := targetDeps(set, cfg, n).Scan(ctx, targets.ScanRequest{Node: n, SelfSteal: true})
	if err != nil {
		return res, err
	}
	res.IP, res.Scanned, res.FromNode, res.SelfSteal, res.Results = found.IP, found.Scanned, found.FromNode, found.SelfSteal, found.Results
	return res, nil
}

// checkTarget tests a site from the node that would dial it, or from here when the node
// cannot tell. A destination the panel must not dial is a result like any other, with the
// check's own error code.
func checkTarget(ctx context.Context, set *settings.Settings, cfg config.Config, n db.Node, dest, sni string) scan.Result {
	r, err := targetDeps(set, cfg, n).Check(ctx, dest, sni)
	switch {
	case errors.Is(err, targets.ErrBadDest):
		return scan.Result{Dest: dest, SNI: sni, Error: "bad_dest"}
	case errors.Is(err, targets.ErrPrivateDest):
		return scan.Result{Dest: dest, SNI: sni, Error: "private"}
	}
	return r
}

// realityTargets lists the node's inbounds that have a REALITY camouflage.
func realityTargets(ctx context.Context, st *store.Store, nodeID int64) ([]Target, error) {
	inbounds, err := st.Q.ListNodeInbounds(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	out := []Target{}
	for _, in := range inbounds {
		if t := targetOf(in); t.Dest != "" {
			out = append(out, t)
		}
	}
	return out, nil
}

func targetOf(in db.Inbound) Target {
	t := Target{Inbound: in.Name, Enabled: in.Enabled != 0}
	if tpl, err := proto.Parse(in.Config); err == nil {
		var names []string
		t.Dest, names = presets.Dest(tpl)
		if len(names) > 0 {
			t.SNI = names[0]
		}
	}
	return t
}

// problemText says in words what scan.Problem names with a code.
var problemText = map[string]string{
	"timeout": "no answer", "refused": "connection refused", "no_tls13": "no TLS 1.3", "dns": "the name does not resolve",
	"sni_required": "an IP needs --sni", "bad_dest": "want host:port", "handshake": "the TLS handshake failed",
	"no_x25519": "no X25519", "no_h2": "no HTTP/2", "private": "an internal address: only public sites suit",
}

// targetProblem says why a site does not suit REALITY.
func targetProblem(r scan.Result) string {
	code := scan.Problem(r)
	switch {
	case problemText[code] != "":
		return problemText[code]
	case code == "cert":
		return "the certificate is not valid for " + r.SNI
	}
	return code
}

func targetError(e *proto.Error) string {
	switch e.Code {
	case "dest_format":
		return "--dest must be host:port"
	case "reality_sni":
		return "--dest is an IP: pass the site's name with --sni"
	}
	if e.Detail != "" {
		return e.Code + ": " + e.Detail
	}
	return e.Code
}

func printScan(w io.Writer, r TargetScan) {
	from := "from the node"
	if !r.FromNode {
		from = "from the panel"
	}
	fmt.Fprintf(w, "Sites next to %s (%d addresses scanned %s), fastest first:\n", r.IP, r.Scanned, from)
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  #\tSNI\tDEST\tRTT")
	for i, x := range r.Results {
		fmt.Fprintf(tw, "  %d\t%s\t%s\t%d ms\n", i+1, x.SNI, x.Dest, x.RTTms)
	}
	_ = tw.Flush()
	if len(r.Results) == 0 {
		fmt.Fprintln(w, "  none found: keep the current sites or pick one with `mikan admin targets apply --dest`")
	}
	if s := r.SelfSteal; s != nil {
		state := "ready"
		if !s.OK {
			state = "not ready: " + targetProblem(*s)
		}
		fmt.Fprintf(w, "The panel's own domain %s on %s: %s\n", s.SNI, s.Dest, state)
	}
	if len(r.Current) > 0 {
		fmt.Fprintln(w, "Now:")
		tw = tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, t := range r.Current {
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", t.Inbound, t.Dest, t.SNI)
		}
		_ = tw.Flush()
	}
}
