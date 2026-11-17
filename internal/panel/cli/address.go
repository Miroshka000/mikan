package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"mikan/internal/hostname"
	"mikan/internal/panel/audit"
	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// The panel's domain and address from the server, for a panel whose admin page is out of
// reach (the address in every link is the one that changed). The running panel follows a
// change within seconds (its address follower): the certificate for the new name is ordered,
// the name left behind keeps working for the links handed out with it, and the nodes,
// the cascades and the bot take the change.

// domainCmd is `admin domain [NAME | --clear]`: without arguments it says what the address
// is and which former names still work.
func domainCmd(ctx context.Context, st *store.Store, set *settings.Settings, args []string, dns *dnscheck.Checker, stdout io.Writer) error {
	fs := flag.NewFlagSet("domain", flag.ContinueOnError)
	clear := fs.Bool("clear", false, "no domain any more: the links go to the server's address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	prev, err := set.Address(ctx)
	if err != nil {
		return err
	}
	if fs.NArg() == 0 && !*clear {
		return showAddress(ctx, set, prev, stdout)
	}
	if fs.NArg() > 1 || *clear && fs.NArg() > 0 {
		return errors.New("give one domain, or --clear")
	}
	next := prev
	next.Domain = ""
	if !*clear {
		next.Domain = settings.FormerName(fs.Arg(0))
		if !hostname.Name(next.Domain) {
			return fmt.Errorf("%q is not a domain name like vpn.example.com", fs.Arg(0))
		}
		if err := domainHere(ctx, dns, next.Domain, prev.PublicHost); err != nil {
			return err
		}
	}
	if next.Identifier() == "" {
		return errors.New("the panel needs an address without a domain: set it first with `mikan address IP`")
	}
	return saveAddress(ctx, st, set, prev, next, "cli.domain", stdout)
}

// addressCmd is `admin address IP`: the server's address, which links use without a domain.
func addressCmd(ctx context.Context, st *store.Store, set *settings.Settings, args []string, dns *dnscheck.Checker, stdout io.Writer) error {
	prev, err := set.Address(ctx)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		return showAddress(ctx, set, prev, stdout)
	}
	if len(args) > 1 {
		return errors.New("give one address")
	}
	next := prev
	next.PublicHost = strings.Trim(strings.TrimSpace(args[0]), "[]")
	if !hostname.Valid(next.PublicHost) {
		return fmt.Errorf("%q is not an IP address or a host name", args[0])
	}
	if next.Domain != "" {
		if err := domainHere(ctx, dns, next.Domain, next.PublicHost); err != nil {
			return fmt.Errorf("the panel's domain does not lead to %s: %w", next.PublicHost, err)
		}
	}
	return saveAddress(ctx, st, set, prev, next, "cli.address", stdout)
}

// domainHere says why the domain does not lead to this server, as the settings page does.
func domainHere(ctx context.Context, dns *dnscheck.Checker, name, publicHost string) error {
	var e *domain.DomainError
	if errors.As(domain.DomainHere(ctx, dns, name, dnscheck.Own(publicHost)), &e) {
		switch e.Code {
		case "domain_elsewhere":
			return fmt.Errorf("%s points to another server (%s): point its A record at this server and wait for DNS", name, e.Value)
		case "domain_not_found":
			return fmt.Errorf("%s has no A or AAAA record: point it at this server and wait for DNS", name)
		}
		return fmt.Errorf("%s cannot be checked in public DNS (%s): try again in a minute", name, e.Code)
	}
	return nil
}

func saveAddress(ctx context.Context, st *store.Store, set *settings.Settings, prev, next settings.Address, action string, stdout io.Writer) error {
	if prev.Same(next) {
		fmt.Fprintln(stdout, "Nothing to change.")
		return nil
	}
	now := time.Now()
	err := st.Tx(ctx, func(q *db.Queries) error {
		txSet := settings.New(q)
		for key, v := range map[string]string{settings.KeyDomain: next.Domain, settings.KeyPublicHost: next.PublicHost} {
			if err := settings.Set(ctx, txSet, key, v); err != nil {
				return err
			}
		}
		if err := settings.UpdateFormers(ctx, txSet, prev, now); err != nil {
			return err
		}
		return audit.Write(ctx, q, now, audit.Entry{Action: action, TargetType: "settings", Details: map[string]any{
			"domain": map[string]string{"from": prev.Domain, "to": next.Domain}, "public_host": map[string]string{"from": prev.PublicHost, "to": next.PublicHost}}})
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "The panel's address is now %s.\n", next.Identifier())
	if !strings.EqualFold(prev.Identifier(), next.Identifier()) {
		fmt.Fprintln(stdout, "The running panel orders the certificate for it within a minute.")
		if prev.Identifier() != "" && !strings.EqualFold(prev.Domain, next.Domain) {
			fmt.Fprintf(stdout, "%s keeps working for the links handed out with it while it leads to this server.\n", prev.Identifier())
		}
	}
	if u, err := panelURL(ctx, set); err == nil {
		fmt.Fprintln(stdout, "  URL: "+u)
	}
	return nil
}

func showAddress(ctx context.Context, set *settings.Settings, a settings.Address, stdout io.Writer) error {
	fmt.Fprintf(stdout, "Domain:          %s\n", orNone(a.Domain))
	fmt.Fprintf(stdout, "Server address:  %s\n", orNone(a.PublicHost))
	if a.SubDomain != "" {
		fmt.Fprintf(stdout, "Subscriptions:   %s\n", a.SubDomain)
	}
	list, err := set.Formers(ctx)
	if err != nil {
		return err
	}
	for _, f := range list {
		kind := "the panel's"
		if f.Sub {
			kind = "the subscriptions'"
		}
		fmt.Fprintf(stdout, "Former name:     %s (%s, until %s)\n", f.Name, kind, time.Unix(f.Since, 0).UTC().Format("2006-01-02"))
	}
	return nil
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
