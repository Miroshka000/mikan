package cli

import (
	"bufio"
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"mikan/internal/hostname"
	"mikan/internal/panel/api"
	"mikan/internal/panel/app"
	"mikan/internal/panel/audit"
	"mikan/internal/panel/auth"
	"mikan/internal/panel/config"
	"mikan/internal/panel/secure"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

const usage = `mikan — VPN panel on the mihomo core

Commands:
  serve                         run the panel
  admin bootstrap [flags]       first setup: admin, secret paths, address, language
  admin url                     print the panel's link
  admin reset-password          set a new admin password (ends all sessions and revokes all API keys)
  admin reset-path              give the panel a new secret link
  admin disable-2fa             turn off the admin's 2FA
  admin backup FILE             write a consistent copy of the database while the panel runs
  admin inbound list            inbounds: node, name, preset, port
  admin inbound add PRESET [--port PORT] [--node NODE]
                                add an inbound from a preset with fresh keys
  admin inbound set NAME --port PORT [--node NODE]
                                move an inbound to another port; keys and camouflage stay
  admin cert set [--node NODE]  install an own certificate: the chain and the key (PEM) on stdin
  admin cert clear [--node NODE]
                                go back to Let's Encrypt (a node: to its self-signed one)
  admin cert show [--node NODE] what the own certificate is
  admin node list               the panel's nodes
  admin node add --name NAME --host IP [--domain DOMAIN] [--api-port PORT]
                                add a node; prints its join key
  admin node key NODE           issue a new join key (the old one stops working)
  admin node set NODE [--name] [--host] [--domain] [--enabled]
  admin targets scan [--node NODE] [--json]
                                look for REALITY camouflage sites next to a node, fastest first
  admin targets check --dest HOST:PORT [--sni NAME] [--node NODE] [--json]
                                check a site as a REALITY camouflage from the node
  admin targets apply --dest HOST:PORT [--sni NAME] (--all | --inbound NAME) [--node NODE]
                                point REALITY inbounds at a site; it is checked first (--force skips)
  health                        check that the panel answers (container healthcheck)
  openapi                       print the OpenAPI spec (for the API client generator)
  version                       print the version
`

func Run(ctx context.Context, args []string, version string, web fs.FS) error {
	if len(args) == 0 {
		fmt.Print(usage)
		return nil
	}
	switch args[0] {
	case "serve":
		cfg, err := config.FromEnv()
		if err != nil {
			return err
		}
		return app.Serve(ctx, cfg, version, web)
	case "admin":
		return adminCmd(ctx, args[1:])
	case "database":
		return databaseCmd(ctx, args[1:])
	case "openapi":
		_, humaAPI, err := api.New(api.Deps{Version: version, Now: time.Now})
		if err != nil {
			return err
		}
		out, err := json.MarshalIndent(humaAPI.OpenAPI(), "", "  ")
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(append(out, '\n'))
		return err
	case "version":
		fmt.Println(version)
		return nil
	case "health":
		return health()
	case "help", "-h", "--help":
		fmt.Print(usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
}

func adminCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("admin needs a subcommand\n\n" + usage)
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	set := settings.New(st.Q)
	switch args[0] {
	case "bootstrap":
		return bootstrap(ctx, st, set, args[1:], os.Stdin, os.Stdout)
	case "url":
		u, err := panelURL(ctx, set)
		if err != nil {
			return err
		}
		login := ""
		if a, err := findAdmin(ctx, st, ""); err == nil {
			login = a.Username
		}
		printURL(os.Stdout, os.Stderr, u, login)
		return nil
	case "reset-password":
		return resetPassword(ctx, st, args[1:], os.Stdin, os.Stdout)
	case "reset-path":
		if err := settings.Set(ctx, set, settings.KeyAdminPath, secure.Token(24)); err != nil {
			return err
		}
		_ = audit.Write(ctx, st.Q, time.Now(), audit.Entry{Action: "cli.reset_path"})
		u, err := panelURL(ctx, set)
		if err != nil {
			return err
		}
		fmt.Println("New link (the old one stops working within 5 seconds):")
		fmt.Println(u)
		return nil
	case "backup":
		if len(args) < 2 {
			return errors.New("name the file: mikan admin backup /data/backup.db")
		}
		if err := backup(ctx, st, args[1]); err != nil {
			return err
		}
		fmt.Println("Database copied to", args[1])
		return nil
	case "cert":
		return certCmd(ctx, st, set, cfg.DataDir, args[1:], os.Stdin, os.Stdout)
	case "node":
		return nodeCmd(ctx, st, cfg.DataDir, args[1:], os.Stdout, os.Stderr)
	case "inbound":
		return inboundCmd(ctx, st, args[1:], os.Stdout, os.Stderr)
	case "targets":
		return targetsCmd(ctx, st, set, cfg, args[1:], os.Stdout, os.Stderr)
	case "disable-2fa":
		fs := flag.NewFlagSet("disable-2fa", flag.ContinueOnError)
		username := fs.String("username", "", "admin login (may be left out when there is one admin)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		a, err := findAdmin(ctx, st, *username)
		if err != nil {
			return err
		}
		if err := st.Q.SetAdminTOTP(ctx, db.SetAdminTOTPParams{ID: a.ID}); err != nil {
			return err
		}
		_ = audit.Write(ctx, st.Q, time.Now(), audit.Entry{Action: "cli.disable_2fa", TargetType: "admin", TargetID: a.Username})
		fmt.Printf("2FA is off for %s.\n", a.Username)
		return nil
	default:
		return fmt.Errorf("unknown admin subcommand %q\n\n%s", args[0], usage)
	}
}

// backup writes a consistent copy of the database to path while the panel keeps running.
// The copy holds the password hashes, the bot's token and the WARP keys: the file is made
// private before anything is written to it, not after. VACUUM INTO takes an existing file
// only when it is empty.
func backup(ctx context.Context, st *store.Store, path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("backup: %w", err)
	}
	if _, err := st.DB.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("backup: %w", err)
	}
	return os.Chmod(path, 0o600)
}

func bootstrap(ctx context.Context, st *store.Store, set *settings.Settings, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	username := fs.String("username", "", "admin login (random by default)")
	host := fs.String("public-host", "", "public IP or domain of the server (required)")
	port := fs.Int("port", 0, "panel port (required)")
	domain := fs.String("domain", "", "domain for a Let's Encrypt certificate (optional)")
	email := fs.String("email", "", "email for Let's Encrypt (optional)")
	adminPath := fs.String("admin-path", "", "secret path of the panel (random by default)")
	subPath := fs.String("sub-path", "", "path of subscription links (random by default)")
	lang := fs.String("lang", "", "default language, en or ru (by default the visitor's browser decides)")
	passwordStdin := fs.Bool("password-stdin", false, "read the password from stdin instead of generating one")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *host == "" || *port <= 0 || *port > 65535 {
		return errors.New("--public-host and --port are required")
	}
	if !hostname.Valid(*host) {
		return fmt.Errorf("--public-host: want an IP address or a host name, got %q", *host)
	}
	if *domain != "" && !hostname.Valid(*domain) {
		return fmt.Errorf("--domain: want a host name like vpn.example.com, got %q", *domain)
	}
	if *lang != "" && !settings.ValidLang(*lang) {
		return fmt.Errorf("--lang: want en or ru, got %q", *lang)
	}
	if n, err := st.Q.CountAdmins(ctx); err != nil {
		return err
	} else if n > 0 {
		return errors.New("the panel is already set up; for a new password run `mikan admin reset-password`")
	}
	password, generated, err := readOrGeneratePassword(*passwordStdin, stdin)
	if err != nil {
		return err
	}
	if *adminPath == "" {
		*adminPath = secure.Token(24)
	}
	if *subPath == "" {
		*subPath = secure.Token(12)
	}
	if err := validPathSegment(*adminPath, 16); err != nil {
		return fmt.Errorf("--admin-path: %w", err)
	}
	if err := validPathSegment(*subPath, 8); err != nil {
		return fmt.Errorf("--sub-path: %w", err)
	}
	name := strings.ToLower(strings.TrimSpace(*username))
	if name == "" {
		name = secure.Login()
	}
	if err := validLogin(name); err != nil {
		return fmt.Errorf("--username: %w", err)
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	err = st.Tx(ctx, func(q *db.Queries) error {
		txSet := settings.New(q)
		if _, err := q.CreateAdmin(ctx, db.CreateAdminParams{Username: name, PasswordHash: hash, CreatedAt: time.Now().Unix()}); err != nil {
			return err
		}
		values := map[string]any{
			settings.KeyAdminPath: *adminPath, settings.KeySubPath: *subPath,
			settings.KeyPublicHost: *host, settings.KeyPanelPort: *port,
			settings.KeyDomain: *domain, settings.KeyACMEEmail: *email,
		}
		if *lang != "" {
			values[settings.KeyDefaultLang] = *lang
		}
		for k, v := range values {
			if err := settings.Set(ctx, txSet, k, v); err != nil {
				return err
			}
		}
		return audit.Write(ctx, q, time.Now(), audit.Entry{Action: "cli.bootstrap", TargetType: "admin", TargetID: name})
	})
	if err != nil {
		return err
	}
	u, err := panelURL(ctx, set)
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, "The panel is ready.")
	fmt.Fprintln(stdout, "  URL:      "+u)
	fmt.Fprintln(stdout, "  Login:    "+name)
	if generated {
		fmt.Fprintln(stdout, "  Password: "+password+"   ← shown once, save it")
	}
	return nil
}

func resetPassword(ctx context.Context, st *store.Store, args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("reset-password", flag.ContinueOnError)
	username := fs.String("username", "", "admin login (may be left out when there is one admin)")
	passwordStdin := fs.Bool("password-stdin", false, "read the password from stdin instead of generating one")
	if err := fs.Parse(args); err != nil {
		return err
	}
	a, err := findAdmin(ctx, st, *username)
	if err != nil {
		return err
	}
	password, generated, err := readOrGeneratePassword(*passwordStdin, stdin)
	if err != nil {
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	var keys int64
	err = st.Tx(ctx, func(q *db.Queries) error {
		if err := q.SetAdminPassword(ctx, db.SetAdminPasswordParams{PasswordHash: hash, ID: a.ID}); err != nil {
			return err
		}
		if err := q.DeleteAdminSessions(ctx, a.ID); err != nil {
			return err
		}
		// This is what an owner runs on the server after losing the panel or suspecting a
		// hijacked session: a key made from that session must not survive it. Scripts get
		// new keys from the admin panel.
		keys, err = q.DeleteAPIKeysOf(ctx, a.ID)
		if err != nil {
			return err
		}
		return audit.Write(ctx, q, time.Now(), audit.Entry{Action: "cli.reset_password", TargetType: "admin", TargetID: a.Username, Details: map[string]any{"keys_revoked": keys}})
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "The password of %s is changed, all sessions are ended and %d API keys are revoked.\n", a.Username, keys)
	if generated {
		fmt.Fprintln(stdout, "New password: "+password+"   ← shown once")
	}
	return nil
}

// health is the container healthcheck: the panel must answer "/" with its bare 404.
func health() error {
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	scheme := "https"
	if cfg.Dev {
		scheme = "http"
	}
	// Liveness of the local listener only; the certificate is not what is being checked.
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	resp, err := c.Get(scheme + "://" + net.JoinHostPort(host, port) + "/")
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	fmt.Println("ok")
	return nil
}

func readOrGeneratePassword(fromStdin bool, r io.Reader) (string, bool, error) {
	if !fromStdin {
		return secure.Token(32), true, nil
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", false, err
	}
	pw := strings.TrimRight(line, "\r\n")
	if len(pw) < 12 {
		return "", false, errors.New("the password must be at least 12 characters long")
	}
	return pw, false, nil
}

// printURL: stdout carries only the link, because the host `mikan` script of every
// installed version parses it (`mikan update` waits for the panel with it); the login goes
// to stderr, which still shows in a terminal.
func printURL(stdout, stderr io.Writer, url, login string) {
	fmt.Fprintln(stdout, url)
	if login != "" {
		fmt.Fprintln(stderr, "Login: "+login)
	}
}

// findAdmin resolves --username; an empty name means "the only admin", since the login
// is random since 0.1.2 and nobody should have to look it up to reset a password.
func findAdmin(ctx context.Context, st *store.Store, username string) (db.Admin, error) {
	if username != "" {
		a, err := st.Q.GetAdminByUsername(ctx, strings.ToLower(strings.TrimSpace(username)))
		if errors.Is(err, sql.ErrNoRows) {
			return a, fmt.Errorf("no admin %q", username)
		}
		return a, err
	}
	admins, err := st.Q.ListAdmins(ctx)
	if err != nil {
		return db.Admin{}, err
	}
	switch len(admins) {
	case 0:
		return db.Admin{}, errors.New("no admins: the panel is not set up")
	case 1:
		return admins[0], nil
	}
	return db.Admin{}, errors.New("there are several admins: pass --username")
}

func validLogin(s string) error {
	if len(s) < 3 || len(s) > 32 {
		return errors.New("a login is 3 to 32 characters long")
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-') {
			return errors.New("a login may have only a-z, 0-9, dots, dashes and underscores")
		}
	}
	return nil
}

func validPathSegment(s string, minLen int) error {
	if len(s) < minLen || len(s) > 64 {
		return fmt.Errorf("must be %d to 64 characters long", minLen)
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return errors.New("may have only Latin letters, digits, - and _")
		}
	}
	return nil
}

func panelURL(ctx context.Context, set *settings.Settings) (string, error) {
	ep, err := set.Endpoint(ctx)
	if err != nil {
		return "", err
	}
	p, err := set.Paths(ctx)
	if err != nil {
		return "", err
	}
	if ep.Host == "" || p.Admin == "" {
		return "", errors.New("the panel is not set up: run `mikan admin bootstrap`")
	}
	return "https://" + net.JoinHostPort(ep.Host, strconv.Itoa(ep.Port)) + "/" + p.Admin + "/", nil
}
