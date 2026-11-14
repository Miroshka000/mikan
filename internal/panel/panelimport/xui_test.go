package panelimport

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
)

// fakeXUI answers like 3x-ui at the base path /x7k: 3.x (csrf) asks for the token of the
// session /csrf-token opened, lists its clients and takes an API token; an older one does
// none of it. The inbounds are testdata/3xui-inbounds.json, the clients 3xui-clients.json.
func fakeXUI(t *testing.T, csrf bool) *httptest.Server {
	t.Helper()
	inbounds, err := os.ReadFile("testdata/3xui-inbounds.json")
	if err != nil {
		t.Fatal(err)
	}
	clients, err := os.ReadFile("testdata/3xui-clients.json")
	if err != nil {
		t.Fatal(err)
	}
	cookie := func(r *http.Request) string {
		c, err := r.Cookie("3x-ui")
		if err != nil {
			return ""
		}
		return c.Value
	}
	return serve(t, func(w http.ResponseWriter, r *http.Request) {
		path, ok := strings.CutPrefix(r.URL.Path, "/x7k")
		if !ok {
			http.NotFound(w, r)
			return
		}
		authed := cookie(r) == "logged-in" || (csrf && r.Header.Get("Authorization") == "Bearer xui-token")
		switch {
		case path == "/csrf-token" && csrf:
			http.SetCookie(w, &http.Cookie{Name: "3x-ui", Value: "fresh", Path: "/x7k"})
			_, _ = w.Write([]byte(`{"success":true,"obj":"csrf-1"}`))
		case path == "/login" && r.Method == http.MethodPost:
			if csrf && (cookie(r) != "fresh" || r.Header.Get("X-CSRF-Token") != "csrf-1") {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			_ = r.ParseForm()
			if r.PostForm.Get("username") != "admin" || r.PostForm.Get("password") != "pw" {
				_, _ = w.Write([]byte(`{"success":false,"msg":"Wrong username or password"}`))
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "3x-ui", Value: "logged-in", Path: "/x7k"})
			_, _ = w.Write([]byte(`{"success":true,"msg":"Logged in"}`))
		case path == "/panel/api/inbounds/list" || (path == "/panel/api/clients/list" && csrf):
			if !authed {
				if r.Header.Get("X-Requested-With") == "XMLHttpRequest" {
					w.WriteHeader(http.StatusUnauthorized)
				} else {
					http.NotFound(w, r)
				}
				return
			}
			if path == "/panel/api/inbounds/list" {
				_, _ = w.Write(inbounds)
			} else {
				_, _ = w.Write(clients)
			}
		default:
			http.NotFound(w, r)
		}
	})
}

func byName(list []User) map[string]User {
	m := map[string]User{}
	for _, u := range list {
		m[u.Name] = u
	}
	return m
}

// The clients of one subscription are one user: anna is on two vless inbounds (3.x
// repeats her) and has a second client on vmess under the same subId, which adds nothing
// (mikan has no vmess); 3.x's limitHwid stands before limitIp. Settings come as objects
// (3.x) and as a string (the trojan inbound, 2.x); numbers written as strings are read; a
// client only on vmess is left out with the reason; a client 3x-ui turned off at the end
// of its term or traffic is expired or limited, not disabled; a client without a subId
// comes over without a link, a short subId with no link.
func TestFetchXUI(t *testing.T) {
	defer func(f func() time.Time) { xuiNow = f }(xuiNow)
	xuiNow = func() time.Time { return time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC) }
	srv := fakeXUI(t, true)
	got, err := Fetch(context.Background(), Client(), Source{Kind: XUI, URL: srv.URL + "/x7k/", Username: "admin", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, u := range got {
		names = append(names, u.Name)
	}
	if strings.Join(names, ",") != "anna,boris,vera,gleb,dima,egor" {
		t.Fatalf("users %v", names)
	}
	m := byName(got)
	anna := m["anna"]
	if anna.Status != StatusActive || anna.TrafficLimit != 10<<30 || anna.Used != 1000 || anna.Lifetime != 1000 ||
		!anna.Expires.Equal(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)) || anna.DeviceLimit != 5 ||
		anna.Contact != "tg:777" || anna.Note != "friend" || anna.Token != "anna0sub0long0id" || anna.Skip != "" {
		t.Errorf("anna %+v", anna)
	}
	if b := m["boris"]; b.Status != StatusDisabled || b.TrafficLimit != 0 || !b.Expires.IsZero() || b.DeviceLimit != 0 || b.Token != "boris0sub0long00" {
		t.Errorf("boris %+v", b)
	}
	if v := m["vera"]; v.Status != StatusOnHold || v.OnHold != 30*24*time.Hour || v.TrafficLimit != 5<<30 || v.DeviceLimit != 3 || !v.WeakToken || v.Token != "" {
		t.Errorf("vera %+v", v)
	}
	if g := m["gleb"]; !strings.Contains(g.Skip, "vmess") {
		t.Errorf("gleb, only on vmess, is not left out: %+v", g)
	}
	if d := m["dima"]; d.Status != StatusLimited || d.Used != 1000 || d.Lifetime != 5000 || d.Token != "" || d.WeakToken {
		t.Errorf("dima %+v", d)
	}
	if e := m["egor"]; e.Status != StatusExpired {
		t.Errorf("egor %+v", e)
	}
	if _, err := Normalize(m["gleb"]); err == nil || !strings.Contains(err.Error(), "vmess") {
		t.Errorf("Normalize took a client mikan has no protocol for: %v", err)
	}
}

// An API token instead of the password (3.x), and a version with neither a CSRF token nor
// a list of clients: the device limit is then the limit of addresses.
func TestFetchXUIAccess(t *testing.T) {
	srv := fakeXUI(t, true)
	got, err := Fetch(context.Background(), Client(), Source{Kind: XUI, URL: srv.URL + "/x7k", Token: "xui-token"})
	if err != nil || len(got) != 6 {
		t.Fatalf("with a token: %d users, %v", len(got), err)
	}
	if _, err := Fetch(context.Background(), Client(), Source{Kind: XUI, URL: srv.URL + "/x7k", Token: "wrong"}); !errors.Is(err, ErrAuth) {
		t.Fatalf("a wrong token: %v", err)
	}
	if _, err := Fetch(context.Background(), Client(), Source{Kind: XUI, URL: srv.URL + "/x7k", Username: "admin", Password: "no"}); !errors.Is(err, ErrAuth) {
		t.Fatalf("a wrong password: %v", err)
	}
	old := fakeXUI(t, false)
	got, err = Fetch(context.Background(), Client(), Source{Kind: XUI, URL: old.URL + "/x7k", Username: "admin", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	if anna := byName(got)["anna"]; anna.DeviceLimit != 2 {
		t.Fatalf("anna without the clients' list: %+v", anna)
	}
	// Without the base path the panel is not there.
	if _, err := Fetch(context.Background(), Client(), Source{Kind: XUI, URL: old.URL, Username: "admin", Password: "pw"}); !errors.Is(err, ErrAnswer) {
		t.Fatalf("without the base path: %v", err)
	}
}

// The base path is the panel's secret: an error, which goes to the log, does not carry it.
func TestFetchXUIHidesThePath(t *testing.T) {
	srv := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	_, err := Fetch(context.Background(), Client(), Source{Kind: XUI, URL: srv.URL + "/s3cretPath", Username: "admin", Password: "pw"})
	if !errors.Is(err, ErrAnswer) || strings.Contains(err.Error(), "s3cretPath") || !strings.Contains(err.Error(), "/…/login") {
		t.Fatalf("%v", err)
	}
	if got := hidePath(errors.New("GET /p/panel/api and /p"), "http://h/p").Error(); got != "GET /…/panel/api and /…" {
		t.Fatalf("whole segments: %q", got)
	}
}

// Broken settings of an inbound fail the read instead of leaving its clients out unseen;
// a huge negative term is refused, not wrapped into a short one.
func TestFetchXUIBroken(t *testing.T) {
	for _, raw := range []string{`"{"`, `{"clients": 1}`} {
		if _, err := xuiUsers([]xuiInbound{{ID: 1, Protocol: "vless", Settings: json.RawMessage(raw)}}, nil, time.Now()); !errors.Is(err, ErrAnswer) {
			t.Fatalf("broken settings %s: %v", raw, err)
		}
	}
	list, err := xuiUsers([]xuiInbound{{ID: 1, Protocol: "vless", Settings: json.RawMessage(`{"clients":[{"email":"x","expiryTime":-9000000000000000000}]}`)}}, nil, time.Now())
	if err != nil || len(list) != 1 {
		t.Fatal(list, err)
	}
	if _, err := Normalize(list[0]); err == nil {
		t.Fatalf("a huge on-hold term was taken: %+v", list[0])
	}
}

// The subId is filed as the old link, as Remnawave's short UUID; a client mikan has no
// protocol for is reported with the reason and not made.
func TestApplyXUI(t *testing.T) {
	st, tariff := seeded(t)
	ctx := context.Background()
	users := domain.NewUsers(st, domain.NewPool(st, time.Now), noChanges{}, time.Now)
	list := []User{
		{Name: "anna", Status: StatusActive, Token: "anna0sub0long0id"},
		{Name: "gleb", Status: StatusActive, Token: "gleb0sub0long000", Skip: "only on vmess inbounds, which mikan has not"},
	}
	p, err := Check(ctx, st.Q, list)
	if err != nil || p.New != 1 || p.Invalid.Count != 1 || !strings.Contains(p.Invalid.Names[0], "gleb: only on vmess") {
		t.Fatalf("preview %+v %v", p, err)
	}
	r, err := Apply(ctx, st, users, time.Now(), XUI, tariff, list, nil)
	if err != nil || r.Created != 1 || r.Links != 1 || r.Failed.Count != 1 {
		t.Fatalf("report %+v %v", r, err)
	}
	row, err := st.Q.LegacySubTokenUser(ctx, "anna0sub0long0id")
	if err != nil || row.User.Name != "anna" {
		t.Fatalf("anna's old link: %+v %v", row, err)
	}
}
