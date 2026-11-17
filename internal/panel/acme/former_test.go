package acme

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/tlscert"
)

func leafName(c *x509.Certificate) string {
	if c == nil {
		return ""
	}
	if len(c.DNSNames) > 0 {
		return c.DNSNames[0]
	}
	if len(c.IPAddresses) > 0 {
		return c.IPAddresses[0].String()
	}
	return c.Subject.CommonName
}

// sniName is the name of the certificate names serves to a client that names name.
func sniName(t *testing.T, names *tlscert.BySNI, name string) string {
	t.Helper()
	c, err := names.Get(&tls.ClientHelloInfo{ServerName: name})
	if err != nil {
		t.Fatalf("%q: %v", name, err)
	}
	return leafName(c.Leaf)
}

// switchDomain writes a change of the panel's domain as the settings API does.
func switchDomain(t *testing.T, set *settings.Settings, domain string) {
	t.Helper()
	ctx := context.Background()
	prev, err := set.Address(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, set, settings.KeyDomain, domain); err != nil {
		t.Fatal(err)
	}
	next, err := set.Address(ctx)
	if err != nil {
		t.Fatal(err)
	}
	list, err := set.Formers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, set, settings.KeyFormerDomains, settings.NextFormers(list, prev, next, time.Now())); err != nil {
		t.Fatal(err)
	}
}

// A change of the domain: the certificate for the new name is got ready first while the
// old one is served; once the name is the panel's it is served at once, and the old name
// keeps its certificate for the clients that still name it, over a restart too.
func TestDomainChangeKeepsTheOldName(t *testing.T) {
	newFakeCA(t, func(string, string, string) bool { return true })
	m, set := testManager(t, "a.example.com")
	m.challenge = newTestChallenge(t)
	ctx := context.Background()
	if st, done := m.RenewNow(ctx, 30*time.Second); !done || st.Kind != "acme" {
		t.Fatalf("first: %+v", st)
	}
	names := &tlscert.BySNI{Panel: m.holder, Sub: &tlscert.Holder{}}
	formers := m.Formers(names)

	if st := m.Stage(ctx, "b.example.com", 30*time.Second); !st.OK {
		t.Fatalf("stage: %+v", st)
	}
	if got := leafName(m.Served()); got != "a.example.com" {
		t.Fatalf("before the change %s is served", got)
	}
	// Staged once: asked again, nothing is ordered.
	if st := m.Stage(ctx, "b.example.com", time.Second); !st.OK || st.Ordering {
		t.Fatalf("again: %+v", st)
	}

	switchDomain(t, set, "b.example.com")
	formers.Reconcile(ctx, false)
	m.Load(ctx)
	if st := m.Status(); st.Identifier != "b.example.com" || st.Kind != "acme" || leafName(m.Served()) != "b.example.com" {
		t.Fatalf("the new name: %+v, %s", st, leafName(m.Served()))
	}
	if got := sniName(t, names, "a.example.com"); got != "a.example.com" {
		t.Fatalf("the old name gets %s", got)
	}
	if got := sniName(t, names, "B.example.com."); got != "b.example.com" {
		t.Fatalf("the new name gets %s", got)
	}
	if fs := formers.Status(); len(fs) != 1 || fs[0].Name != "a.example.com" || fs[0].Kind != "own" || fs[0].DropAt != nil {
		t.Fatalf("status: %+v", fs)
	}
	if _, err := os.Stat(filepath.Join(m.dir, nextDir, pairCert)); !os.IsNotExist(err) {
		t.Fatalf("the staged certificate stays aside: %v", err)
	}

	// A restart: the old name has its own copy.
	again := &tlscert.BySNI{Panel: m.holder, Sub: &tlscert.Holder{}}
	m.Formers(again).Reconcile(ctx, false)
	if got := sniName(t, again, "a.example.com"); got != "a.example.com" {
		t.Fatalf("after a restart the old name gets %s", got)
	}
}

// A change with nothing ready (the CLI): the panel orders the new name's certificate after
// the change, and the old name takes its certificate over from where the order put it.
func TestDomainChangeWithoutStage(t *testing.T) {
	newFakeCA(t, func(string, string, string) bool { return true })
	m, set := testManager(t, "a.example.com")
	m.challenge = newTestChallenge(t)
	ctx := context.Background()
	if st, done := m.RenewNow(ctx, 30*time.Second); !done || st.Kind != "acme" {
		t.Fatalf("first: %+v", st)
	}
	names := &tlscert.BySNI{Panel: m.holder, Sub: &tlscert.Holder{}}
	switchDomain(t, set, "b.example.com")
	if st, done := m.RenewNow(ctx, 30*time.Second); !done || st.Kind != "acme" || leafName(m.Served()) != "b.example.com" {
		t.Fatalf("the new name: %+v", st)
	}
	if c, _, err := loadPair(filepath.Join(m.dir, prevDir)); err != nil || leafName(c.Leaf) != "a.example.com" {
		t.Fatalf("the old pair is kept aside: %v", err)
	}
	m.Formers(names).Reconcile(ctx, false)
	if got := sniName(t, names, "a.example.com"); got != "a.example.com" {
		t.Fatalf("the old name gets %s", got)
	}
}

// When the new name's certificate cannot be had, the change is told why and the panel goes
// on serving what it has; the admin's own certificate that does not cover the new name is
// said so before anything is ordered.
func TestStageFails(t *testing.T) {
	var valid atomic.Bool
	valid.Store(true)
	newFakeCA(t, func(string, string, string) bool { return valid.Load() })
	m, _ := testManager(t, "a.example.com")
	m.challenge = newTestChallenge(t)
	ctx := context.Background()
	if st, done := m.RenewNow(ctx, 30*time.Second); !done || st.Kind != "acme" {
		t.Fatalf("first: %+v", st)
	}
	valid.Store(false)
	st := m.Stage(ctx, "b.example.com", 30*time.Second)
	if st.OK || st.Ordering || st.Code == "" {
		t.Fatalf("a refused order: %+v", st)
	}
	if leafName(m.Served()) != "a.example.com" || m.Status().Kind != "acme" {
		t.Fatalf("the panel's certificate moved: %+v", m.Status())
	}
	if at := m.Status().Attempts; len(at) == 0 || at[0].Identifier != "b.example.com" || at[0].Error == "" {
		t.Fatalf("the attempt is not listed: %+v", at)
	}

	certPEM, keyPEM := ownCert(t, "a.example.com", time.Now().Add(60*24*time.Hour))
	if err := m.SetCustom(ctx, certPEM, keyPEM); err != nil {
		t.Fatal(err)
	}
	if st := m.Stage(ctx, "b.example.com", time.Second); st.OK || st.Code != "custom_wrong_host" {
		t.Fatalf("own certificate for another name: %+v", st)
	}
	if st := m.Stage(ctx, "10.0.0.5", time.Second); !st.OK {
		t.Fatalf("an address no CA certifies needs nothing: %+v", st)
	}
}

// writePair puts a certificate for name valid until into dir as an ordered pair.
func writePair(t *testing.T, dir, name string, until time.Time) {
	t.Helper()
	certPEM, keyPEM := ownCert(t, name, until)
	if err := savePair(dir, certPEM, keyPEM, CALetsEncrypt); err != nil {
		t.Fatal(err)
	}
}

// A former name's certificate is renewed while the name leads here; one left without a
// valid certificate for a week is let go and said so, one within the week is kept.
func TestFormerRenewedAndLetGo(t *testing.T) {
	var valid atomic.Bool
	valid.Store(true)
	newFakeCA(t, func(string, string, string) bool { return valid.Load() })
	m, set := testManager(t, "b.example.com")
	m.challenge = newTestChallenge(t)
	ctx := context.Background()
	names := &tlscert.BySNI{Panel: m.holder, Sub: &tlscert.Holder{}}
	formers := m.Formers(names)
	now := time.Now()
	list := []settings.Former{{Name: "a.example.com", Since: now.Add(-24 * time.Hour).Unix()}}
	if err := settings.Set(ctx, set, settings.KeyFormerDomains, list); err != nil {
		t.Fatal(err)
	}
	writePair(t, formers.dir("a.example.com"), "a.example.com", now.Add(10*time.Minute))
	formers.Reconcile(ctx, true)
	fs := formers.Status()
	if len(fs) != 1 || fs[0].Kind != "own" || fs[0].NotAfter == nil || fs[0].NotAfter.Before(now.Add(80*24*time.Hour)) {
		t.Fatalf("renewed: %+v", fs)
	}

	valid.Store(false) // the names lead elsewhere now
	var dropped atomic.Value
	formers.OnDrop(func(_ context.Context, name string) { dropped.Store(name) })
	list = append(list, settings.Former{Name: "gone.example.com", Since: now.Add(-8 * 24 * time.Hour).Unix()},
		settings.Former{Name: "new.example.com", Since: now.Add(-time.Hour).Unix()})
	if err := settings.Set(ctx, set, settings.KeyFormerDomains, list); err != nil {
		t.Fatal(err)
	}
	formers.Reconcile(ctx, true)
	if got, _ := dropped.Load().(string); got != "gone.example.com" {
		t.Fatalf("let go: %q", got)
	}
	saved, err := set.Formers(ctx)
	if err != nil || len(saved) != 2 || saved[0].Name != "a.example.com" || saved[1].Name != "new.example.com" {
		t.Fatalf("saved: %+v %v", saved, err)
	}
	fs = formers.Status()
	if len(fs) != 2 || fs[0].Name != "new.example.com" || fs[0].Kind != "none" || fs[0].DropAt == nil || fs[0].Error == "" {
		t.Fatalf("kept within the week: %+v", fs)
	}
	if got := sniName(t, names, "new.example.com"); got != "b.example.com" {
		t.Fatalf("a name without a certificate gets the panel's: %s", got)
	}
}

// The former address of a panel that got a domain answers the clients that name nothing,
// and only while the panel has a domain.
func TestFormerAddress(t *testing.T) {
	t.Setenv("MIKAN_ACME_DIRECTORY", "http://127.0.0.1:1/directory")
	m, set := testManager(t, "b.example.com")
	ctx := context.Background()
	names := &tlscert.BySNI{Panel: m.holder, Sub: &tlscert.Holder{}}
	formers := m.Formers(names)
	if err := settings.Set(ctx, set, settings.KeyFormerDomains, []settings.Former{{Name: "203.0.113.7", Since: time.Now().Unix()}}); err != nil {
		t.Fatal(err)
	}
	self, err := tlscert.LoadOrCreateSelfSigned(t.TempDir(), "203.0.113.7", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	certPEM, keyPEM, err := tlscert.CustomPEM(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := savePair(formers.dir("203.0.113.7"), []byte(certPEM), []byte(keyPEM), CALetsEncrypt); err != nil {
		t.Fatal(err)
	}
	formers.Reconcile(ctx, false)
	if got := sniName(t, names, ""); got != "203.0.113.7" {
		t.Fatalf("no name: %s", got)
	}
	// No domain any more: the panel answers at its address itself.
	if err := settings.Set(ctx, set, settings.KeyDomain, ""); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, set, settings.KeyPublicHost, "198.51.100.9"); err != nil {
		t.Fatal(err)
	}
	formers.Reconcile(ctx, false)
	if got := sniName(t, names, ""); got == "203.0.113.7" {
		t.Fatal("a panel without a domain gave its former address to the clients that name nothing")
	}
}
