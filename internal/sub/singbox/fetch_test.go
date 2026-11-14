package singbox

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
)

// The panel fetches a rule list itself, so the link must not lead it inside: an address of
// this server is refused as it is dialled, whatever name or redirect led there.
func TestFetchListRefusesAnAddressInside(t *testing.T) {
	hit := false
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		_, _ = w.Write([]byte("payload:\n  - example.com\n"))
	}))
	defer srv.Close()
	if req, _ := http.NewRequest(http.MethodGet, srv.URL, nil); listClient(req) != guardedLists {
		t.Skip("the environment sets a proxy: it makes the connection, not the panel")
	}
	if _, err := fetchList(context.Background(), srv.URL+"/list.yaml"); !errors.Is(err, errListAddress) {
		t.Fatalf("a list on loopback: %v", err)
	}
	if hit {
		t.Fatal("the request reached the server inside")
	}
}

// A redirect is followed over https alone and a few times at most.
func TestListRedirect(t *testing.T) {
	at := func(raw string) *http.Request {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		return &http.Request{URL: u}
	}
	if err := listRedirect(at("https://lists.example.org/a.yaml"), make([]*http.Request, 1)); err != nil {
		t.Fatalf("an https redirect: %v", err)
	}
	if err := listRedirect(at("http://169.254.169.254/latest/meta-data"), make([]*http.Request, 1)); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("a redirect off https is followed: %v", err)
	}
	if err := listRedirect(at("https://lists.example.org/a.yaml"), make([]*http.Request, 3)); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("the fourth redirect is followed: %v", err)
	}
}

// localLists lets a test serve rule lists from loopback, where httptest listens.
func localLists(t *testing.T) {
	t.Helper()
	old := listAddressOK
	listAddressOK = func(netip.Addr) bool { return true }
	t.Cleanup(func() { listAddressOK = old })
}
