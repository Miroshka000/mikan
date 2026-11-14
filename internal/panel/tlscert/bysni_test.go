package tlscert

import (
	"crypto/tls"
	"testing"
	"time"
)

// Clients that name the subscription domain get its certificate once it has one; every
// other name, and the domain before its certificate, gets the panel's.
func TestBySNI(t *testing.T) {
	now := time.Now()
	panel, err := LoadOrCreateSelfSigned(t.TempDir(), "panel.example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	sub, err := LoadOrCreateSelfSigned(t.TempDir(), "sub.example.com", now)
	if err != nil {
		t.Fatal(err)
	}
	b := &BySNI{Panel: &Holder{}, Sub: &Holder{}}
	b.Panel.Set(panel)
	get := func(name string) *tls.Certificate {
		t.Helper()
		c, err := b.Get(&tls.ClientHelloInfo{ServerName: name})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	b.SetName("sub.example.com")
	if get("sub.example.com") != panel {
		t.Fatal("the domain without a certificate of its own gets the panel's")
	}
	b.Sub.Set(sub)
	if get("SUB.example.com.") != sub || get("panel.example.com") != panel || get("") != panel {
		t.Fatal("certificates by name")
	}
	b.SetName("")
	if get("sub.example.com") != panel {
		t.Fatal("no subscription domain: the panel's for everyone")
	}
}

// A former name gets its own certificate while it keeps one, the panel's otherwise; a
// former address answers clients that name nothing; a former subscription domain still
// reaches the subscriptions alone.
func TestBySNIFormer(t *testing.T) {
	now := time.Now()
	cert := func(name string) *tls.Certificate {
		t.Helper()
		c, err := LoadOrCreateSelfSigned(t.TempDir(), name, now)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	panel, old, ip := cert("new.example.com"), cert("old.example.com"), cert("203.0.113.7")
	b := &BySNI{Panel: &Holder{}, Sub: &Holder{}}
	b.Panel.Set(panel)
	get := func(name string) *tls.Certificate {
		t.Helper()
		c, err := b.Get(&tls.ClientHelloInfo{ServerName: name})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	oldHolder, ipHolder, empty := &Holder{}, &Holder{}, &Holder{}
	oldHolder.Set(old)
	ipHolder.Set(ip)
	b.SetFormer(&Former{Certs: map[string]*Holder{"old.example.com": oldHolder, "": ipHolder, "gone.example.com": empty}, Sub: map[string]bool{"links.example.com": true}})
	if get("OLD.example.com.") != old || get("") != ip || get("gone.example.com") != panel || get("new.example.com") != panel {
		t.Fatal("certificates of the former names")
	}
	if !b.IsSub("Links.Example.com.") || b.IsSub("old.example.com") || b.IsSub("") {
		t.Fatal("the former subscription domain")
	}
	b.SetName("sub.example.com")
	if !b.IsSub("sub.example.com") {
		t.Fatal("the subscription domain")
	}
	b.SetFormer(nil)
	if get("old.example.com") != panel || b.IsSub("links.example.com") {
		t.Fatal("no former names")
	}
}
