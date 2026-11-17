// Command mikan-sub serves subscriptions away from the panel, without its database.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"golang.org/x/crypto/acme"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/acme/autocert"
	"mikan/internal/subrelay"
)

func main() {
	if err := run(); err != nil {
		slog.Error("mikan-sub", "error", err)
		os.Exit(1)
	}
}

func run() error {
	upstream := flag.String("upstream", "", "https://panel.example/<admin-path>/api/subscription")
	prefix := flag.String("path", "", "public subscription path (same as the panel)")
	domain := flag.String("domain", "", "subscription DNS name; required for automatic HTTPS")
	listen := flag.String("listen", ":443", "listen address; behind a CDN use -http -listen 127.0.0.1:8080")
	plain := flag.Bool("http", false, "serve HTTP behind a TLS-terminating reverse proxy")
	email := flag.String("email", "", "ACME contact email")
	data := flag.String("data", "/data/acme", "persistent ACME account and certificate directory")
	offlineTTL := flag.Duration("offline-ttl", time.Hour, "maximum age of saved profiles during panel outages; 0 disables fallback")
	failureThreshold := flag.Int("failure-threshold", 3, "consecutive failed profile fetches before fast offline mode; 0 disables")
	retryDelay := flag.Duration("retry-delay", 10*time.Second, "delay before a single recovery probe")
	metricsListen := flag.String("metrics-listen", "", "optional loopback listener for /metrics, e.g. 127.0.0.1:9091")
	directory := flag.String("acme-directory", "", "HTTPS ACME directory; empty uses Let's Encrypt")
	cacheEntries := flag.Int("cache-entries", 256, "maximum saved device/profile variants")
	cacheBytes := flag.Int("cache-bytes", 64<<20, "maximum combined saved profile/header bytes")
	proxies := flag.String("trusted-proxies", "", "comma-separated trusted immediate proxy CIDRs; empty trusts none")
	ipHeader := flag.String("client-ip-header", "X-Forwarded-For", "trusted proxy client header: X-Forwarded-For or CF-Connecting-IP")
	flag.Parse()
	if *failureThreshold < 0 || *retryDelay <= 0 {
		return fmt.Errorf("invalid circuit breaker configuration")
	}
	if *metricsListen != "" {
		host, _, err := net.SplitHostPort(*metricsListen)
		if err != nil || (host != "localhost" && !net.ParseIP(host).IsLoopback()) {
			return fmt.Errorf("metrics listener must use a loopback address")
		}
	}
	if *directory != "" {
		u, err := url.Parse(*directory)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			return fmt.Errorf("ACME directory must be an HTTPS URL")
		}
	}
	if *offlineTTL < 0 {
		return fmt.Errorf("-offline-ttl must not be negative")
	}
	p, err := subrelay.New(*upstream, os.Getenv("MIKAN_SUB_TOKEN"), *prefix)
	if err != nil {
		return err
	}
	if err := p.SetCacheLimits(*cacheEntries, *cacheBytes); err != nil {
		return err
	}
	if err := p.SetTrustedProxies(*proxies, *ipHeader); err != nil {
		return err
	}
	if *plain {
		host, _, err := net.SplitHostPort(*listen)
		if err == nil && host != "localhost" && !net.ParseIP(host).IsLoopback() {
			slog.Warn("HTTP listener is public; restrict it to your TLS reverse proxy", "listen", *listen)
		}
	}
	p.SetOfflineTTL(*offlineTTL)
	p.SetCircuitBreaker(*failureThreshold, *retryDelay)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	slog.Info("mikan-sub starting", "listen", *listen, "http", *plain, "cache_entries", *cacheEntries, "offline_ttl", *offlineTTL)
	srv := &http.Server{Addr: *listen, Handler: p, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	var challenge *http.Server
	if !*plain {
		if *domain == "" {
			return fmt.Errorf("-domain is required for automatic HTTPS")
		}
		whitelist := autocert.HostWhitelist(strings.ToLower(strings.TrimSuffix(*domain, ".")))
		policy := func(ctx context.Context, host string) error {
			// HTTP-01 validators may include :80 in Host; TLS SNI never includes a port.
			if name, _, err := net.SplitHostPort(host); err == nil {
				host = name
			}
			return whitelist(ctx, strings.ToLower(strings.TrimSuffix(host, ".")))
		}
		m := &autocert.Manager{Prompt: autocert.AcceptTOS, Cache: autocert.DirCache(*data), HostPolicy: policy, Email: *email}
		if *directory != "" {
			m.Client = &acme.Client{DirectoryURL: *directory}
		}
		srv.TLSConfig = m.TLSConfig()
		srv.TLSConfig.MinVersion = tls.VersionTLS12
		// The challenge listener serves ACME only, never the subscription or panel.
		challenge = &http.Server{Addr: ":80", Handler: m.HTTPHandler(http.NotFoundHandler()), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
	}
	results := make(chan error, 3)
	var metrics *http.Server
	if *metricsListen != "" {
		metrics = &http.Server{Addr: *metricsListen, Handler: p.Metrics(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8 << 10}
		go func() { results <- metrics.ListenAndServe() }()
	}
	go func() {
		if *plain {
			results <- srv.ListenAndServe()
		} else {
			results <- srv.ListenAndServeTLS("", "")
		}
	}()
	if challenge != nil {
		go func() {
			challengeErr := challenge.ListenAndServe()
			if errors.Is(challengeErr, syscall.EADDRINUSE) {
				slog.Warn("ACME HTTP listener is busy; TLS-ALPN-01 remains available")
				return
			}
			results <- challengeErr
		}()
	}
	select {
	case <-ctx.Done():
	case err = <-results:
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdown)
	if metrics != nil {
		_ = metrics.Shutdown(shutdown)
	}
	if challenge != nil {
		_ = challenge.Shutdown(shutdown)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
