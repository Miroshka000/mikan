package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"mikan/internal/nodeapi"
	"mikan/internal/nodetls"
	"mikan/internal/panel/acme"
	"mikan/internal/panel/autotune"
	"mikan/internal/panel/checkhost"
	"mikan/internal/panel/config"
	"mikan/internal/panel/dnscheck"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/nodesync"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
	"mikan/internal/panel/tlscert"
	"mikan/internal/panel/updates"
	"mikan/internal/release"
)

// workerStopTimeout is how long Serve waits for the workers before it closes the database.
const workerStopTimeout = 20 * time.Second

func Serve(ctx context.Context, cfg config.Config, version string, web fs.FS) error {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	dsn, err := store.DatabaseURL()
	if err != nil {
		return err
	}
	// Held from before the store opens (it migrates) until the panel is down: database
	// migrate and restore refuse to run under a live panel.
	unlock, err := store.LockPanel(ctx, dsn)
	if err != nil {
		return err
	}
	defer unlock()
	st, err := store.OpenPostgres(ctx, cfg.DataDir, dsn)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := domain.Seed(ctx, st, time.Now()); err != nil {
		return fmt.Errorf("seed: %w", err)
	}

	ep, err := settings.New(st.Q).Endpoint(ctx)
	if err != nil {
		return err
	}
	host := ep.Host
	if host == "" {
		host = "localhost"
	}
	// The self-signed certificate is long-lived and pinned in Hysteria2/TUIC links where
	// the local node has no public one.
	tlsDir := filepath.Join(cfg.DataDir, "tls")
	self, err := tlscert.LoadOrCreateSelfSigned(tlsDir, host, time.Now())
	if err != nil {
		return fmt.Errorf("tls: %w", err)
	}
	holder := &tlscert.Holder{}
	holder.Set(self)

	opts := Options{Version: version, Web: web, TrustProxy: cfg.TrustProxy, Log: logger, Now: time.Now,
		Autotune: autotune.DefaultOptions().Scaled(cfg.AutotuneScale), TelegramAPI: cfg.TelegramAPI,
		DataDir: cfg.DataDir, Releases: updates.Fetch(release.IndexURL, release.LatestURL), DNS: dnscheck.New(), CheckHost: checkhost.New("", nil, time.Now)}
	nodesDir := filepath.Join(tlsDir, "nodes")
	nodeCerts := tlscert.NewNodeStore(filepath.Join(tlsDir, "custom-nodes"), time.Now)
	opts.NodeCerts = nodeCerts
	set := settings.New(st.Q)
	var (
		certs     *acme.Manager
		nodesACME *acme.Nodes
	)
	if !cfg.Dev {
		certs = acme.New(cfg.DataDir, holder, self, set, logger, time.Now)
		// The certificate already on disk is served before the nodes are first synced: links
		// drop the pin once it is public, so the local node must start with it too.
		certs.Load(ctx)
		opts.Certs = certs
		opts.HSTS = certs.Trusted
		// Each remote node's public certificate, the challenge answered on its port 80.
		nodesACME = certs.Nodes(func(ctx context.Context) ([]acme.NodeTarget, error) {
			nodes, err := st.Q.ListNodes(ctx)
			if err != nil {
				return nil, err
			}
			var out []acme.NodeTarget
			for _, n := range nodes {
				if n.Address == "" {
					continue
				}
				t := acme.NodeTarget{ID: n.ID, Host: domain.NodeHost(n)}
				if ip, err := netip.ParseAddr(n.PublicHost); err == nil {
					t.Own = []netip.Addr{ip.Unmap()}
				}
				if own, _, _ := nodeCerts.Get(n.ID, t.Host); own != nil {
					t.HasOwn = true
				}
				if c, err := NodeClient(cfg, n); err == nil {
					t.Client = c
				}
				out = append(out, t)
			}
			return out, nil
		})
		opts.NodesACME = nodesACME
	}
	opts.TLS = NewNodeTLS(cfg.DataDir, nodeCerts, certs, nodesACME, set, logger, time.Now)
	opts.PanelCert = func() (nodetls.Pair, error) { return nodetls.LoadOrCreate(nodesDir, time.Now()) }
	opts.Connect = func(n db.Node) (nodesync.Target, error) {
		c, err := NodeClient(cfg, n)
		if err != nil {
			return nodesync.Target{}, err
		}
		return nodesync.Target{Node: c, TLS: opts.TLS.Source(n), Local: n.Address == "", Address: n.Address}, nil
	}
	var panelTLS *tls.Config
	if !cfg.Dev {
		panelTLS = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: holder.Get, NextProtos: []string{"h2", "http/1.1"}}
	}
	listenHost, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen %q: %w", cfg.Listen, err)
	}
	subPort := NewSubPort(listenHost, panelTLS, logger)
	defer subPort.Close()
	opts.SubPort, opts.SubPortError = subPort.Set, subPort.Error
	p, err := NewPanel(st, opts)
	if err != nil {
		return err
	}
	// A certificate issued, renewed or uploaded later goes to the nodes at once: they get it
	// only with their state, which is sent again on a change.
	if certs != nil && p.Nodes != nil {
		certs.OnChange(p.Nodes.SlotsChanged)
		nodesACME.OnChange(func(id int64) {
			if s, ok := p.Nodes.Syncer(id); ok {
				s.SlotsChanged()
			}
		})
	}
	paths, err := p.Apply(ctx)
	if err != nil {
		return err
	}
	if paths.Admin == "" {
		logger.Warn("panel is not initialized yet: run `mikan admin bootstrap`")
	}
	subPort.SetHandler(p.SubOnly())
	if port, _, err := settings.Get[int](ctx, settings.New(st.Q), settings.KeySubPort); err == nil {
		subPort.Start(port)
	}
	// The workers (node sync, billing, the bot, certificates) use the database: they are
	// stopped, and waited for, before it is closed, however Serve returns. The deferred
	// close of the store runs after this one.
	workers, stopWorkers := context.WithCancel(ctx)
	var running sync.WaitGroup
	defer func() {
		stopWorkers()
		done := make(chan struct{})
		go func() { running.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(workerStopTimeout):
			logger.Warn("workers did not stop in time; closing the database under them")
		}
	}()
	running.Go(func() { p.Run(workers) })
	if certs != nil {
		running.Go(func() { certs.Run(workers) })
		running.Go(func() { nodesACME.Run(workers) })
		// kill -HUP (an external tool's renewal hook) serves a new custom certificate now.
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		defer signal.Stop(hup)
		running.Go(func() {
			for {
				select {
				case <-workers.Done():
					return
				case <-hup:
					certs.Renew()
				}
			}
		})
	}

	httpSrv := httpServer(p.Handler)
	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return err
	}
	if panelTLS != nil {
		ln = tls.NewListener(ln, panelTLS)
	}
	logger.Info("panel started", "listen", cfg.Listen, "tls", !cfg.Dev, "version", version)

	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()
	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

// httpServer is the panel's HTTP server, on its own port and on the subscription port.
func httpServer(h http.Handler) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		ErrorLog:          log.New(dropHandshakeNoise{}, "", log.LstdFlags),
	}
}

// NodeClient reaches a node's API as the running panel does: the local node over its
// socket, a remote one over TLS with the panel's client certificate and the node's pin.
// The CLI uses it too, from inside the panel's container.
func NodeClient(cfg config.Config, n db.Node) (*nodeapi.Client, error) {
	if n.Address == "" {
		if cfg.NodeSocket == "" {
			return nil, nodesync.ErrNoNode
		}
		return nodeapi.NewUnixClient(cfg.NodeSocket), nil
	}
	panel, err := nodetls.LoadOrCreate(filepath.Join(cfg.DataDir, "tls", "nodes"), time.Now())
	if err != nil {
		return nil, err
	}
	tc, err := nodetls.ClientConfig(panel, n.CertSha256)
	if err != nil {
		return nil, err
	}
	return nodeapi.NewTLSClient(n.Address, tc), nil
}

// Internet scanners produce a constant stream of failed TLS handshakes; they are not actionable.
type dropHandshakeNoise struct{}

func (dropHandshakeNoise) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("TLS handshake error")) {
		return len(p), nil
	}
	return os.Stderr.Write(p)
}
