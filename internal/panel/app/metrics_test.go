package app

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"mikan/internal/panel/domain"
)

// Prometheus scrapes with a read key; the text is the exposition format, line by line.
func TestMetricsForPrometheus(t *testing.T) {
	k := newKeyHarness(t)
	ctx := t.Context()
	clock := func() time.Time { return k.now }
	tariffs, _ := k.st.Q.ListTariffs(ctx)
	if _, err := domain.NewUsers(k.st, domain.NewPool(k.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID}); err != nil {
		t.Fatal(err)
	}
	url := k.api + "/metrics"
	if resp, _ := k.do(http.MethodGet, url, nil, map[string]string{"Authorization": "Bearer mk_wrong"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("a wrong key: %d", resp.StatusCode)
	}
	resp, body := k.do(http.MethodGet, url, nil, map[string]string{"Authorization": "Bearer " + k.read})
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain; version=0.0.4") {
		t.Fatalf("read key: %d %s %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
	text := string(body)
	for _, want := range []string{
		`mikan_info{version="`,
		`mikan_users{state="active"} 1`,
		`mikan_users{state="disabled"} 0`,
		`mikan_traffic_bytes_total{direction="up"} 0`,
		"# TYPE mikan_traffic_bytes_total counter",
		"# TYPE mikan_node_up gauge",
		"go_goroutines ",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
	sample := regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*(\{[a-zA-Z_][a-zA-Z0-9_]*="(?:[^"\\]|\\.)*"(,[a-zA-Z_][a-zA-Z0-9_]*="(?:[^"\\]|\\.)*")*\})? -?[0-9.eE+-]+$`)
	for _, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if !strings.HasPrefix(line, "# HELP ") && !strings.HasPrefix(line, "# TYPE ") && !sample.MatchString(line) {
			t.Errorf("not the exposition format: %q", line)
		}
	}
}

func TestPromLabelsAreEscaped(t *testing.T) {
	k := newKeyHarness(t)
	if _, err := k.st.DB.ExecContext(t.Context(), `UPDATE nodes SET name = $1`, "ноде \"A\"\\1"); err != nil {
		t.Fatal(err)
	}
	_, body := k.do(http.MethodGet, k.api+"/metrics", nil, map[string]string{"Authorization": "Bearer " + k.read})
	if !strings.Contains(string(body), `node="ноде \"A\"\\1"`) {
		t.Fatalf("escaping:\n%s", body)
	}
}

// Scrapes closer than a few seconds get the same text: a scrape reads every user.
func TestMetricsAreKeptAFewSeconds(t *testing.T) {
	k := newKeyHarness(t)
	ctx := t.Context()
	scrape := func() string {
		t.Helper()
		_, body := k.do(http.MethodGet, k.api+"/metrics", nil, map[string]string{"Authorization": "Bearer " + k.read})
		return string(body)
	}
	if !strings.Contains(scrape(), `mikan_users{state="active"} 0`) {
		t.Fatal("no users yet")
	}
	clock := func() time.Time { return k.now }
	tariffs, _ := k.st.Q.ListTariffs(ctx)
	if _, err := domain.NewUsers(k.st, domain.NewPool(k.st, clock), noChanges{}, clock).Create(ctx, domain.CreateInput{Name: "a", TariffID: tariffs[1].ID}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(scrape(), `mikan_users{state="active"} 0`) {
		t.Fatal("a scrape right after another is made again")
	}
	k.advance(6 * time.Second)
	if !strings.Contains(scrape(), `mikan_users{state="active"} 1`) {
		t.Fatal("an old text is served past its time")
	}
}
