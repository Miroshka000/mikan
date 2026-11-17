package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"mikan/internal/panel/audit"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/presets"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
)

// inboundCmd lists, adds and moves inbounds from the server shell. The running panel pushes
// the change to the node on its next reconcile, within 30 seconds.
func inboundCmd(ctx context.Context, st *store.Store, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("inbound needs list, add or set\n\n" + usage)
	}
	switch args[0] {
	case "list":
		inbounds, err := st.Q.ListInbounds(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "NODE\tNAME\tPRESET\tPORT\tENABLED")
		for _, in := range inbounds {
			on := "yes"
			if in.Enabled == 0 {
				on = "no"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s/%s\t%s\n", in.NodeID, in.Name, in.Preset, in.Port, domain.InboundNetwork(in), on)
		}
		return tw.Flush()
	case "add":
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return errors.New("name a preset: mikan admin inbound add PRESET [--port PORT] [--node NODE]\nPresets: " + presetIDs())
		}
		fs := flag.NewFlagSet("inbound add", flag.ContinueOnError)
		port := fs.String("port", "", "port or range (the preset's port by default)")
		node := fs.Int64("node", 1, "node (1 is the panel's own, see mikan admin node list)")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		// The shell has no way to pass a template: presets only.
		if args[1] == presets.Custom {
			return fmt.Errorf("no preset %q. Presets: %s", args[1], presetIDs())
		}
		row, err := domain.NewInbounds(st, nil, time.Now).Create(ctx, domain.NewInbound{NodeID: *node, Preset: args[1], Port: *port})
		var busy *domain.PortInUseError
		var pe *proto.Error
		switch {
		case errors.Is(err, domain.ErrUnknownNode):
			return fmt.Errorf("no node %d", *node)
		case errors.Is(err, domain.ErrUnknownPreset):
			return fmt.Errorf("no preset %q. Presets: %s", args[1], presetIDs())
		case errors.Is(err, domain.ErrBadPort):
			return fmt.Errorf("bad port %q", *port)
		case errors.Is(err, domain.ErrPortRangeWide):
			return portRangeWide(*port)
		case errors.As(err, &busy):
			return fmt.Errorf("%s, choose another: --port", portHolder(busy.PortHolder))
		case errors.As(err, &pe):
			return fmt.Errorf("preset %s: %w", args[1], err)
		case err != nil:
			return err
		}
		_ = audit.Write(ctx, st.Q, time.Now(), audit.Entry{Action: "cli.inbound_create", TargetType: "inbound", TargetID: row.Name,
			Details: map[string]any{"preset": row.Preset, "port": row.Port}})
		fmt.Fprintf(stderr, "Inbound %s added on %s/%s. The node gets it within 30 seconds.\n", row.Name, row.Port, domain.InboundNetwork(row))
		return openPort(ctx, st, row, stdout, stderr)
	case "set":
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			return errors.New("name an inbound: mikan admin inbound set NAME --port PORT [--node NODE]\nNames: mikan admin inbound list")
		}
		fs := flag.NewFlagSet("inbound set", flag.ContinueOnError)
		port := fs.String("port", "", "new port or range")
		node := fs.Int64("node", 1, "the inbound's node (see mikan admin inbound list)")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		if *port == "" {
			return errors.New("--port is required")
		}
		// Keys and the REALITY target stay: clients only need to refresh the subscription.
		ins := domain.NewInbounds(st, nil, time.Now)
		var prev, row db.Inbound
		in, err := ins.Find(ctx, *node, args[1])
		if err == nil {
			prev, row, err = ins.Update(ctx, in.ID, domain.InboundPatch{Port: port})
		}
		var busy *domain.PortInUseError
		switch {
		case errors.Is(err, domain.ErrUnknownNode):
			return fmt.Errorf("no node %d", *node)
		case errors.Is(err, domain.ErrUnknownInbound):
			return fmt.Errorf("node %d has no inbound %q: see mikan admin inbound list", *node, args[1])
		case errors.Is(err, domain.ErrBadPort):
			return fmt.Errorf("bad port %q", *port)
		case errors.Is(err, domain.ErrPortRangeWide):
			return portRangeWide(*port)
		case errors.As(err, &busy):
			return fmt.Errorf("%s, choose another", portHolder(busy.PortHolder))
		case err != nil:
			return err
		}
		_ = audit.Write(ctx, st.Q, time.Now(), audit.Entry{Action: "cli.inbound_update", TargetType: "inbound", TargetID: row.Name,
			Details: map[string]any{"node": row.NodeID, "port": row.Port, "old_port": prev.Port}})
		fmt.Fprintf(stderr, "Inbound %s: port %s → %s/%s. The node gets the change within 30 seconds; clients need to refresh their subscription.\n",
			row.Name, prev.Port, row.Port, domain.InboundNetwork(row))
		return openPort(ctx, st, row, stdout, stderr)
	default:
		return fmt.Errorf("unknown inbound subcommand %q\n\n%s", args[0], usage)
	}
}

// portRangeWide refuses a hopping range wider than the panel takes.
func portRangeWide(spec string) error {
	return fmt.Errorf("range %s is %d ports, at most %d: the node runs a server on each of them", spec, domain.HopPorts(spec), domain.MaxHopPorts)
}

// portHolder says what holds a port the admin asked for.
func portHolder(h domain.PortHolder) string {
	switch h.Kind {
	case domain.PortRelay:
		return "the cascade relay of the node listens on this port"
	case domain.PortSub:
		return "the panel serves subscriptions on this port"
	case domain.PortPanel:
		return "the panel itself listens on this port"
	case domain.PortNodeAPI:
		return "the node API listens on this port"
	}
	return "the port is taken by inbound " + h.Name
}

// openPort prints "port/network" on stdout for the server script, which opens it in ufw.
// A remote node's port is opened on that node's server, so only a hint goes out then.
func openPort(ctx context.Context, st *store.Store, in db.Inbound, stdout, stderr io.Writer) error {
	n, err := st.Q.GetNode(ctx, in.NodeID)
	if err != nil {
		return err
	}
	rule := in.Port + "/" + domain.InboundNetwork(in)
	if domain.ListenPinsPort(in.Listen) {
		// Behind a proxy on the server: only the proxy's port is open to clients.
		fmt.Fprintf(stderr, "The inbound listens on %s only: point the proxy in front at port %s.\n", in.Listen, rule)
		return nil
	}
	if n.Address != "" {
		fmt.Fprintf(stderr, "Open the port on the node's server %s: ufw allow %s\n", n.PublicHost, strings.Replace(rule, "-", ":", 1))
		return nil
	}
	fmt.Fprintln(stdout, rule)
	return nil
}

func presetIDs() string {
	var ids []string
	for _, p := range presets.All {
		if p.ID != presets.Custom {
			ids = append(ids, p.ID)
		}
	}
	return strings.Join(ids, ", ")
}
