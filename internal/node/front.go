package node

import (
	"bytes"
	"crypto/tls"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"mikan/internal/frontpage"
	"mikan/internal/nodeapi"
)

// The front page: what a stranger who opens the node's own domain, or its address with no
// name at all, over https gets: a page of the panel's templates on the node's real
// certificate, instead of the REALITY target's certificate for a name it was never made
// for. The SNI router hands such connections over inside the process (frontPage.take):
// there is no port of its own to find, and the router's address filter applies first.
type frontPage struct {
	ln   *handover
	srv  *http.Server
	page atomic.Pointer[[]byte]
	// site: the page is the admin's own, which may have files for more addresses than "/".
	site atomic.Bool
	cert atomic.Pointer[tls.Certificate]
}

func newFrontPage() *frontPage {
	f := &frontPage{ln: newHandover()}
	f.srv = &http.Server{
		Handler:           http.HandlerFunc(f.serve),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0), // strangers' broken handshakes are no news
	}
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"h2", "http/1.1"},
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return f.cert.Load(), nil
		},
	}
	go func() { _ = f.srv.Serve(tls.NewListener(f.ln, cfg)) }()
	return f
}

func (f *frontPage) serve(w http.ResponseWriter, r *http.Request) {
	page := f.page.Load()
	if page == nil {
		frontpage.NotFound(w)
		return
	}
	// Browsers come back over https only, as to any site that has it for good.
	w.Header().Set("Strict-Transport-Security", "max-age=31536000")
	// The admin's own site answers each of its addresses; any other page is one page.
	if f.site.Load() && frontpage.ServeSite(w, r) {
		return
	}
	frontpage.Serve(w, r, *page)
}

// set makes kind the page (frontpage.Templates) over tlsState, the node's certificate;
// either missing, or a certificate that does not load, turns the page off (on says which).
func (f *frontPage) set(kind string, tlsState *nodeapi.TLSFiles) {
	page := frontpage.Page(kind)
	f.site.Store(kind == frontpage.Custom)
	if page == nil || tlsState == nil || tlsState.CertPEM == "" {
		f.page.Store(nil)
		return
	}
	cert, err := tls.X509KeyPair([]byte(tlsState.CertPEM), []byte(tlsState.KeyPEM))
	if err != nil {
		f.page.Store(nil)
		return
	}
	f.cert.Store(&cert)
	f.page.Store(&page)
}

// on says whether the page is on.
func (f *frontPage) on() bool { return f != nil && f.page.Load() != nil }

// take serves c, which sent hello already, and returns when c is done with.
func (f *frontPage) take(c net.Conn, hello []byte) {
	rc := &replayConn{Conn: c, r: io.MultiReader(bytes.NewReader(hello), c), done: make(chan struct{})}
	if f.ln.push(rc) {
		<-rc.done
	}
}

// handover is a listener whose connections come from inside the process.
type handover struct {
	ch   chan net.Conn
	done chan struct{}
	once sync.Once
}

func newHandover() *handover { return &handover{ch: make(chan net.Conn), done: make(chan struct{})} }

func (h *handover) Accept() (net.Conn, error) {
	select {
	case c := <-h.ch:
		return c, nil
	case <-h.done:
		return nil, net.ErrClosed
	}
}

func (h *handover) push(c net.Conn) bool {
	select {
	case h.ch <- c:
		return true
	case <-h.done:
		return false
	}
}

func (h *handover) Close() error {
	h.once.Do(func() { close(h.done) })
	return nil
}

func (h *handover) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

// replayConn reads what was read of it already first, and says when it is closed.
type replayConn struct {
	net.Conn
	r    io.Reader
	done chan struct{}
	once sync.Once
}

func (c *replayConn) Read(p []byte) (int, error) { return c.r.Read(p) }

func (c *replayConn) Close() error {
	c.once.Do(func() { close(c.done) })
	return c.Conn.Close()
}
