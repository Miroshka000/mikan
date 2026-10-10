package nodeapi

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// Why the panel could not talk to a node: the codes the Nodes page explains, each with its
// own fix. The raw error stays beside them for the admin who wants the details.
const (
	// LinkTimeout: nothing answered on the port in time. A firewall (the hoster's or ufw)
	// that drops packets looks exactly like this, and so does a server that is off.
	LinkTimeout = "timeout"
	// LinkRefused: the server is there, but nothing listens on the port: the node is not
	// installed yet, stopped, or crashed.
	LinkRefused = "refused"
	// LinkUnreachable: no route to the address: a wrong IP, or the network says it is down.
	LinkUnreachable = "unreachable"
	// LinkDNS: the node's name does not resolve.
	LinkDNS = "dns"
	// LinkPinMismatch: a node answered, but with another certificate than the panel pins,
	// or it refused the panel's: its key is outdated or belongs to another node or panel.
	LinkPinMismatch = "pin_mismatch"
	// LinkTLS: something that is not a mikan node answers on the port.
	LinkTLS = "tls"
	// LinkHTTPStatus: the node answered the health check with an error status.
	LinkHTTPStatus = "http_status"
	// LinkUnknown: none of the above; the raw error says more.
	LinkUnknown = "unknown"
)

// Classify names why a call to a node failed (one of the Link codes); "" for no error.
func Classify(err error) string {
	if err == nil {
		return ""
	}
	var se *StatusError
	var ne *Error
	if errors.As(err, &se) || errors.As(err, &ne) {
		return LinkHTTPStatus
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		if dnsErr.IsTimeout {
			return LinkTimeout
		}
		return LinkDNS
	}
	msg := err.Error()
	// The pin check runs inside the handshake: its words come back in the error. The node's
	// own refusal of the panel's certificate arrives as a TLS alert.
	if strings.Contains(msg, "does not match the pin") || strings.Contains(msg, "no certificate to pin") ||
		strings.Contains(msg, "remote error: tls: bad certificate") || strings.Contains(msg, "remote error: tls: certificate required") ||
		strings.Contains(msg, "remote error: tls: unknown certificate") {
		return LinkPinMismatch
	}
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return LinkRefused
	case errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return LinkUnreachable
	}
	// The transport's handshake timer: the port took the connection and then said nothing a
	// node would. A firewall drops the connection itself, before this.
	if strings.Contains(msg, "TLS handshake timeout") || strings.Contains(msg, "HTTP response to HTTPS client") {
		return LinkTLS
	}
	var hdr tls.RecordHeaderError
	var alert tls.AlertError
	if errors.As(err, &hdr) || errors.As(err, &alert) {
		return LinkTLS
	}
	// A handshake the other side cut: something answered on the port, and it was no node.
	// The TLS layer only says so in words.
	if strings.Contains(msg, "tls:") || (strings.Contains(msg, "handshake") && !isTimeout(err)) {
		return LinkTLS
	}
	if isTimeout(err) {
		return LinkTimeout
	}
	if errors.Is(err, syscall.ECONNRESET) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return LinkTLS
	}
	// Errors that lost their type on the way (a wrapped string) still read the same.
	switch {
	case strings.Contains(msg, "connection refused"):
		return LinkRefused
	case strings.Contains(msg, "no route to host"), strings.Contains(msg, "network is unreachable"):
		return LinkUnreachable
	case strings.Contains(msg, "no such host"):
		return LinkDNS
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "deadline exceeded"):
		return LinkTimeout
	}
	return LinkUnknown
}

func isTimeout(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// LinkParams are the facts the text of a code names: the port of the node's address, and
// the status of an http_status.
func LinkParams(code, address string, err error) map[string]string {
	p := map[string]string{}
	if host, port, e := net.SplitHostPort(address); e == nil {
		p["host"], p["port"] = host, port
	}
	var se *StatusError
	if code == LinkHTTPStatus && errors.As(err, &se) {
		p["status"] = strconv.Itoa(se.Status)
	}
	return p
}
