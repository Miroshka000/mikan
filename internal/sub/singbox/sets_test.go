package singbox

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// A Clash rule list is YAML with a payload or a line per entry, comments and blank lines
// left out; a YAML of no payload is read as lines.
func TestParseList(t *testing.T) {
	for _, c := range []struct {
		name string
		raw  string
		want []string
	}{
		{"payload", "payload:\n  - DOMAIN-SUFFIX,example.com\n  - '+.example.org'\n", []string{"DOMAIN-SUFFIX,example.com", "+.example.org"}},
		{"lines", "# a list\n\nexample.com\r\n  # indented\n  10.0.0.0/8  \n\n", []string{"example.com", "10.0.0.0/8"}},
		{"empty", "", nil},
		{"empty payload", "payload: []\n", []string{"payload: []"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := parseList([]byte(c.raw)); !slices.Equal(got, c.want) {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}

// A list is fetched and read; a status other than 200 is an error, not an empty list.
func TestFetchList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ru.yaml" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("payload:\n  - +.gosuslugi.ru\n  - ya.ru\n"))
	}))
	defer srv.Close()

	got, err := fetchList(t.Context(), srv.URL+"/ru.yaml")
	if err != nil || !slices.Equal(got, []string{"+.gosuslugi.ru", "ya.ru"}) {
		t.Fatalf("%q %v", got, err)
	}
	if got, err := fetchList(t.Context(), srv.URL+"/gone.yaml"); err == nil {
		t.Fatalf("404 read as a list: %q", got)
	}
}
