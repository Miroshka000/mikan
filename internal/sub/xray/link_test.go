package xray

import (
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"testing"

	"mikan/internal/proto"
	"mikan/internal/sub/model"
	"mikan/internal/sub/subtest"
)

func links(t *testing.T, u subtest.User) []string {
	t.Helper()
	s, _, err := Links(u.Proxies())
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(s, "\n")
}

func TestURIs(t *testing.T) {
	lines := links(t, subtest.Defaults("ab12"))
	if len(lines) != 4 {
		t.Fatalf("got %d links", len(lines))
	}
	byName := map[string]*url.URL{}
	for _, l := range lines {
		u, err := url.Parse(l)
		if err != nil {
			t.Fatal(err)
		}
		byName[u.Fragment] = u
	}
	xhttp := byName["VLESS XHTTP"]
	if lines[0] != xhttp.String() || xhttp.Host != "203.0.113.7:443" ||
		xhttp.Query().Get("type") != "xhttp" || xhttp.Query().Get("mode") != "stream-one" || !strings.HasPrefix(xhttp.Query().Get("path"), "/") {
		t.Fatalf("xhttp must be the first link, on 443: %v", lines)
	}
	vision := byName["VLESS Vision"]
	q := vision.Query()
	if vision.Scheme != "vless" || vision.User.Username() != "0b4ddc4c-7c4f-4a36-9d62-6f1a44b8c4e1" || vision.Host != "203.0.113.7:8443" ||
		q.Get("flow") != "xtls-rprx-vision" || q.Get("security") != "reality" || q.Get("pbk") == "" || q.Get("sid") == "" || q.Get("sni") != "www.example.com" {
		t.Fatalf("vision link: %s", vision)
	}
	hy2 := byName["Hysteria2"]
	if hy2.Scheme != "hysteria2" || hy2.User.Username() != "S3cr3t+/=" || hy2.Query().Get("pinSHA256") != "ab12" || hy2.Query().Get("obfs") != "salamander" {
		t.Fatalf("hy2 link: %s", hy2)
	}
	tuic := byName["TUIC"]
	pw, _ := tuic.User.Password()
	if tuic.Scheme != "tuic" || pw != "S3cr3t+/=" || tuic.Query().Get("allow_insecure") != "1" {
		t.Fatalf("tuic link: %s", tuic)
	}
}

func TestURIsWithRealCertificateDoNotPin(t *testing.T) {
	all := strings.Join(links(t, subtest.Defaults("")), "\n")
	if strings.Contains(all, "insecure") || strings.Contains(all, "pinSHA256") {
		t.Fatalf("real certificate must be verified normally: %s", all)
	}
}

// A name the admin typed goes into the link as typed.
func TestCustomNames(t *testing.T) {
	u := subtest.Defaults("")
	u.Build.Inbounds[0].Name, u.Build.Inbounds[0].Named = "🇳🇱 Нидерланды", true
	first, _ := url.Parse(links(t, u)[0])
	if first.Fragment != "🇳🇱 Нидерланды" {
		t.Fatalf("link name: %q", first.Fragment)
	}
}

// The panel's default fingerprint reaches every uTLS link; an inbound's own one wins.
func TestDefaultFingerprint(t *testing.T) {
	u := subtest.Defaults("ab12")
	u.Build.Fingerprint = "firefox"
	for i, in := range u.Build.Inbounds {
		if in.Name == "VLESS Vision" {
			tpl, err := proto.Parse(in.Config)
			if err != nil || proto.SetFingerprint(tpl, "safari") != nil {
				t.Fatal(err)
			}
			u.Build.Inbounds[i].Config = proto.Marshal(tpl)
		}
	}
	want := map[string]string{"VLESS XHTTP": "firefox", "VLESS Vision": "safari"}
	for _, l := range links(t, u) {
		u, err := url.Parse(l)
		if err != nil {
			t.Fatal(err)
		}
		if fp := u.Query().Get("fp"); fp != want[u.Fragment] {
			t.Errorf("%s: fp=%q, want %q", u.Fragment, fp, want[u.Fragment])
		}
	}
}

// The mihomo-only protocols have no link: they stay out, and a subscription of nothing else
// has nothing to give.
func TestLinksOfWhatHasOne(t *testing.T) {
	ps := []model.Proxy{{Name: "tt", Type: "trusttunnel", Server: "vpn.example.com", Port: 443}, {Name: "ss", Type: "shadowsocks", Server: "vpn.example.com", Port: 6443, Cipher: "2022-blake3-aes-128-gcm", Password: "k"}}
	s, in, err := Links(ps)
	if err != nil || len(in) != 1 || in[0].Name != "ss" || strings.Count(s, "\n") != 0 || !strings.HasPrefix(s, "ss://") {
		t.Fatalf("links: %q %v %v", s, in, err)
	}
	if _, _, err := Links(ps[:1]); !errors.Is(err, model.ErrNoProxies) {
		t.Fatalf("no link at all: %v", err)
	}
	if _, _, err := Links(nil); !errors.Is(err, model.ErrNoProxies) {
		t.Fatalf("no proxies: %v", err)
	}
}

// A subscription is base64 of the lines, a stub one placeholder named after the reason.
func TestEncodeAndStub(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(string(Encode(Stub("⛔ Нет мест"))))
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(string(raw))
	if err != nil || u.Scheme != "vless" || u.Host != "127.0.0.1:1" || u.Fragment != "⛔ Нет мест" {
		t.Fatalf("stub: %s %v", raw, err)
	}
}
