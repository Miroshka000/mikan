// mikan-node runs the VPN data plane: mihomo embedded as a library, controlled by the
// panel over a unix socket, or over pinned TLS when the node runs on another server.
// It links mihomo and is therefore distributed under GPL-3.0.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"mikan/internal/acmechallenge"
	"mikan/internal/node"
	"mikan/internal/nodetls"
)

var version = "dev"

func main() {
	// The installer checks a join key before it sets the node up and learns the API port
	// from it; the key comes in the environment so it does not show in the process list.
	if len(os.Args) > 1 && os.Args[1] == "key-port" {
		key, err := nodetls.DecodeKey(os.Getenv("MIKAN_NODE_JOIN"))
		if err != nil {
			fmt.Fprintln(os.Stderr, "mikan-node:", err)
			os.Exit(1)
		}
		fmt.Println(key.Port)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "hello" {
		helloOnce()
		return
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "mikan-node:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	dataDir := envOr("MIKAN_DATA_DIR", "/data")
	sock := envOr("MIKAN_NODE_SOCKET", "/run/mikan/node.sock")

	release, err := time.ParseDuration(envOr("MIKAN_DEVICE_RELEASE", "60s"))
	if err != nil {
		return fmt.Errorf("MIKAN_DEVICE_RELEASE: %w", err)
	}
	allowPrivate, err := strconv.ParseBool(envOr("MIKAN_ALLOW_PRIVATE", "false"))
	if err != nil {
		return fmt.Errorf("MIKAN_ALLOW_PRIVATE: %w", err)
	}
	// The panel's HTTP-01 challenges for this node's certificate: port 80, unless a web
	// server of the admin's holds it and passes /.well-known/acme-challenge/ on.
	acmeListen, err := acmechallenge.ParseListen(os.Getenv("MIKAN_ACME_LISTEN"))
	if err != nil {
		return fmt.Errorf("MIKAN_ACME_LISTEN: %w", err)
	}
	eng, err := node.Start(node.Options{DataDir: dataDir, Version: version, Log: log, DeviceRelease: release, AllowPrivate: allowPrivate, ACMEListen: acmeListen})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(sock), 0o700); err != nil {
		return err
	}
	if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		return err
	}
	srv := &http.Server{Handler: node.Handler(eng, log), ReadHeaderTimeout: 10 * time.Second}
	serve := func(ln net.Listener) {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("node api", "err", err)
			stop()
		}
	}
	go serve(ln)
	log.Info("node started", "version", version, "socket", sock)
	// A remote node also serves the Node API over TCP to its panel (see nodetls).
	if raw := os.Getenv("MIKAN_NODE_JOIN"); raw != "" {
		key, err := nodetls.DecodeKey(raw)
		if err != nil {
			return fmt.Errorf("MIKAN_NODE_JOIN: %w", err)
		}
		cfg, err := key.ServerConfig()
		if err != nil {
			return fmt.Errorf("MIKAN_NODE_JOIN: %w", err)
		}
		tcp, err := net.Listen("tcp", net.JoinHostPort(envOr("MIKAN_NODE_API_LISTEN", ""), strconv.Itoa(key.Port)))
		if err != nil {
			return err
		}
		go serve(tls.NewListener(tcp, cfg))
		log.Info("node api for the panel", "port", key.Port)
		go helloLoop(ctx, key, dataDir, log)
	}

	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := eng.PersistCounters(); err != nil {
				log.Error("persist counters", "err", err)
			}
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = srv.Shutdown(shutdownCtx)
			return eng.PersistCounters()
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
