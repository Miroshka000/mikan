// target is the far end for node e2e tests. Its wire format makes the expected byte
// counts exact: a request is 1 op byte + 8-byte big-endian length.
//
//	'D' n: server sends n bytes, closes        → client up 9, down n
//	'U' n: client sends n bytes, server "K"    → client up 9+n, down 1
//	'S' r: server streams r bytes/s until the client goes away
//	'W' 0: server sends the address the connection came from, as text
//	'N' 0: server sends its host name, to tell two targets apart
//
// UDP on :9001 echoes every datagram. With -tls it serves more addresses, over TLS when
// a connection opens with a TLS handshake and in the clear otherwise, so a test sees what
// got through to the port. The certificate is its own, for the -tls-name names, written
// into -tls-dir as <host name>.crt for the clients to trust.
package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"flag"
	"io"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var (
	tlsAddrs = flag.String("tls", "", "comma-separated addresses that take TLS as well as the clear")
	tlsNames = flag.String("tls-name", "", "comma-separated names the TLS certificate is for")
	tlsDir   = flag.String("tls-dir", "", "the directory the TLS certificate is written to (PEM)")
)

// tlsHandshake is the first byte of a TLS record that carries a handshake.
const tlsHandshake = 0x16

func main() {
	flag.Parse()
	go udpEcho()
	if *tlsAddrs != "" {
		if *tlsNames == "" || *tlsDir == "" {
			log.Fatal("-tls needs -tls-name and -tls-dir")
		}
		cfg := ownCertificate(strings.Split(*tlsNames, ","), *tlsDir)
		for _, addr := range strings.Split(*tlsAddrs, ",") {
			ln, err := net.Listen("tcp", addr)
			if err != nil {
				log.Fatal(err)
			}
			go accept(ln, func(c net.Conn) net.Conn { return sniffed(c, cfg) })
		}
	}
	ln, err := net.Listen("tcp", ":9000")
	if err != nil {
		log.Fatal(err)
	}
	accept(ln, func(c net.Conn) net.Conn { return c })
}

// accept serves every connection of ln as wrap makes it; wrap may wait for the client's
// first bytes, so it runs on the connection's own goroutine.
func accept(ln net.Listener, wrap func(net.Conn) net.Conn) {
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go func() { serve(wrap(c)) }()
	}
}

// sniffed is c over TLS when it opens with a TLS handshake, and c in the clear otherwise.
func sniffed(c net.Conn, cfg *tls.Config) net.Conn {
	pc := &peeked{Conn: c, r: bufio.NewReader(c)}
	if first, err := pc.r.Peek(1); err == nil && first[0] == tlsHandshake {
		return tls.Server(pc, cfg)
	}
	return pc
}

// peeked is a connection whose first bytes were read ahead into r.
type peeked struct {
	net.Conn
	r *bufio.Reader
}

func (p *peeked) Read(b []byte) (int, error) { return p.r.Read(b) }

// ownCertificate makes a self-signed certificate for names and writes it into dir as
// <host name>.crt.
func ownCertificate(names []string, dir string) *tls.Config {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}
	host, err := os.Hostname()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, host+".crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		log.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
}

func serve(c net.Conn) {
	defer c.Close()
	hdr := make([]byte, 9)
	if _, err := io.ReadFull(c, hdr); err != nil {
		return
	}
	n := int64(binary.BigEndian.Uint64(hdr[1:]))
	buf := make([]byte, 32<<10)
	for i := range buf {
		buf[i] = byte(i)
	}
	switch hdr[0] {
	case 'W':
		host, _, _ := net.SplitHostPort(c.RemoteAddr().String())
		_, _ = c.Write([]byte(host))
	case 'N':
		name, _ := os.Hostname()
		_, _ = c.Write([]byte(name))
	case 'D':
		for n > 0 {
			k := min(n, int64(len(buf)))
			if _, err := c.Write(buf[:k]); err != nil {
				return
			}
			n -= k
		}
	case 'U':
		if _, err := io.CopyN(io.Discard, c, n); err != nil {
			return
		}
		_, _ = c.Write([]byte{'K'})
	case 'S':
		chunk := max(n/20, 1024)
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for range t.C {
			if _, err := c.Write(buf[:min(chunk, int64(len(buf)))]); err != nil {
				return
			}
		}
	}
}

func udpEcho() {
	pc, err := net.ListenPacket("udp", ":9001")
	if err != nil {
		log.Fatal(err)
	}
	buf := make([]byte, 65535)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			continue
		}
		_, _ = pc.WriteTo(buf[:n], addr)
	}
}
