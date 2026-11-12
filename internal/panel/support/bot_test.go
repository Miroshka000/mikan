package support

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNormCode(t *testing.T) {
	for in, want := range map[string]string{"ABCD2345": "ABCD2345", " abcd-2345 ": "ABCD2345", "ab cd 23 45": "ABCD2345"} {
		if got, ok := NormCode(in); !ok || got != want {
			t.Errorf("%q: %q %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "ABC", "ABCD23456", "ABCD234!", "АБВГ2345", "ABCD_234"} {
		if _, ok := NormCode(in); ok {
			t.Errorf("%q taken", in)
		}
	}
}

func TestNewHTTPBot(t *testing.T) {
	for _, ok := range []string{DefaultBotURL, "https://desk.example.com:8790", "https://bot.example.org/", "http://127.0.0.1:9000", "http://[::1]:9000"} {
		if _, err := NewHTTPBot(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "http://bot.example.org", "ftp://x", "https://u:p@bot.example.org", "https://bot.example.org/path", "https://bot.example.org?x=1", "https://"} {
		if _, err := NewHTTPBot(bad); err == nil {
			t.Errorf("%s taken", bad)
		}
	}
}

func TestHTTPBot(t *testing.T) {
	var got map[string]string
	status, reply := http.StatusOK, `{"ticket":17}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = map[string]string{"path": r.URL.Path}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		for k, v := range body {
			got[k] = v
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	defer srv.Close()
	b, err := NewHTTPBot(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if id, err := b.Claim(ctx, "ABCD2345"); err != nil || id != 17 || got["path"] != "/support/claim" || got["code"] != "ABCD2345" {
		t.Fatalf("claim: %d %v %v", id, err, got)
	}
	if id, err := b.Attach(ctx, "ABCD2345", "https://p/x/support#ms_t"); err != nil || id != 17 || got["path"] != "/support/attach" || got["link"] != "https://p/x/support#ms_t" {
		t.Fatalf("attach: %d %v %v", id, err, got)
	}
	status, reply = http.StatusForbidden, `{"error":"invalid_code"}`
	if _, err := b.Claim(ctx, "ABCD2345"); !errors.Is(err, ErrCode) {
		t.Errorf("invalid code: %v", err)
	}
	status, reply = http.StatusUnprocessableEntity, `{"error":"link_refused","detail":"not official"}`
	var le *LinkError
	if _, err := b.Attach(ctx, "ABCD2345", "x"); !errors.As(err, &le) || le.Reason != "not official" {
		t.Errorf("refused: %v", err)
	}
	for _, c := range []struct {
		status int
		reply  string
	}{{http.StatusOK, `{}`}, {http.StatusOK, `not json`}, {http.StatusInternalServerError, ``}, {http.StatusTooManyRequests, ``}, {http.StatusFound, ``}} {
		status, reply = c.status, c.reply
		if _, err := b.Claim(ctx, "ABCD2345"); !errors.Is(err, ErrBotDown) {
			t.Errorf("%d %q: %v", c.status, c.reply, err)
		}
	}
	srv.Close()
	if _, err := b.Claim(ctx, "ABCD2345"); !errors.Is(err, ErrBotDown) {
		t.Errorf("closed: %v", err)
	}
}
