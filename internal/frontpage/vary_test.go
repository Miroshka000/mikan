package frontpage

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func digest(page []byte) string {
	h := sha256.Sum256(bytes.ReplaceAll(page, []byte("\r\n"), []byte("\n")))
	return hex.EncodeToString(h[:])
}

func seeded(t *testing.T, seed string) {
	t.Helper()
	SetSeed(seed)
	t.Cleanup(func() { SetSeed("") })
}

// With no seed the templates give the pages as they were written, to the byte: the
// placeholders took nothing away and added nothing.
func TestPagesAsWritten(t *testing.T) {
	for name, want := range map[string]string{
		"cats":        "4a74aa175e06adbeb5314c13bf4673dbfd5fd9c58fd9f5401916d7c38f1787e4",
		"maintenance": "24f7d38593d36d711ece52cb39873f0ac2b9e4b7562400754759023955b274b6",
	} {
		if got := digest(Page(name)); got != want {
			t.Errorf("%s as written: %s, want %s", name, got, want)
		}
	}
}

// An installation's page is its own: no placeholder left in it, the same at every call,
// another for another seed, and whole (every name the script asks for is in the markup).
func TestSeededPage(t *testing.T) {
	plain := map[string][]byte{}
	for _, name := range Templates {
		plain[name] = Page(name)
	}
	seeded(t, "installation-one")
	for _, name := range Templates {
		page := Page(name)
		if page == nil || bytes.Contains(page, []byte("[[")) || bytes.Contains(page, []byte("]]")) {
			t.Fatalf("%s: not rendered whole", name)
		}
		if bytes.Equal(page, plain[name]) {
			t.Fatalf("%s: a seeded page is the page as written", name)
		}
		if !bytes.Equal(page, Page(name)) {
			t.Fatalf("%s: two calls gave two pages", name)
		}
		title := regexp.MustCompile(`<title>([^<]+)</title>`).FindSubmatch(page)
		if title == nil || !slices.Contains(texts[name]["title"], string(title[1])) {
			t.Fatalf("%s: title %q", name, title)
		}
		if !bytes.Contains(page, []byte("<h1>"+string(title[1])+"</h1>")) {
			t.Fatalf("%s: the heading is not the title", name)
		}
	}
	cats := string(Page("cats"))
	for _, m := range regexp.MustCompile(`getElementById\("([^"]+)"\)`).FindAllStringSubmatch(cats, -1) {
		if !strings.Contains(cats, `id="`+m[1]+`"`) {
			t.Errorf("the script asks for #%s, the markup has none", m[1])
		}
	}
	if got, want := strings.Count(cats, "function "), strings.Count(string(plain["cats"]), "function "); got != want {
		t.Errorf("%d functions in the script, %d as written", got, want)
	}
	// Each of the page's names got a word of its own, and none of the script's own variables.
	l := look{kind: "cats", seed: "installation-one"}
	seen := map[string]bool{}
	for _, name := range ids["cats"] {
		w, err := l.id(name)
		if err != nil || seen[w] {
			t.Fatalf("name %s: %q %v (twice: %v)", name, w, err, seen[w])
		}
		seen[w] = true
		if slices.Contains([]string{"items", "cats", "s", "face", "count", "rate", "cat", "shop", "pop", "it", "b", "i", "a", "n", "ps", "p", "e"}, w) {
			t.Fatalf("name %s took %q, a variable of the script", name, w)
		}
	}
	first := Page("cats")
	SetSeed("installation-two")
	if bytes.Equal(first, Page("cats")) {
		t.Fatal("two installations show one page")
	}
}

// One hash must not find many installations: over many seeds the pages are all but all
// different.
func TestSeedsSpread(t *testing.T) {
	for _, name := range Templates {
		src, err := pages.ReadFile(File(name))
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for i := range 300 {
			page, err := render(name, src, "seed-"+strconv.Itoa(i))
			if err != nil {
				t.Fatal(err)
			}
			seen[digest(page)] = true
		}
		if len(seen) < 299 {
			t.Errorf("%s: %d different pages of 300 seeds", name, len(seen))
		}
	}
}

// The numbers of one group are chosen together: the game's tick and what it adds a tick.
func TestNumbersOfAGroupGoTogether(t *testing.T) {
	for kind, byKey := range numbers {
		size := map[string]int{}
		for key, v := range byKey {
			group, _, _ := strings.Cut(key, ".")
			if n, ok := size[group]; ok && n != len(v) {
				t.Errorf("%s: the group %s has lists of %d and %d values", kind, group, n, len(v))
			}
			size[group] = len(v)
		}
	}
	for i := range 50 {
		l := look{kind: "cats", seed: "s" + strconv.Itoa(i)}
		tick, _ := l.number("tick")
		per, _ := l.number("tick.per")
		a, _ := strconv.Atoi(tick)
		b, _ := strconv.Atoi(per)
		if a*b != 1000 {
			t.Fatalf("seed %d: a tick of %s ms adds a %s-th of a second's treats", i, tick, per)
		}
	}
}

func TestColor(t *testing.T) {
	plain := look{kind: "cats"}
	if c, err := plain.color("#fff7ef"); err != nil || c != "#fff7ef" {
		t.Fatalf("no seed: %q %v", c, err)
	}
	l := look{kind: "cats", seed: "x"}
	for _, grey := range []string{"#fff", "#000000", "#808080"} {
		c, err := l.color(grey)
		want := grey
		if len(grey) == 4 {
			want = "#ffffff"
		}
		if err != nil || c != want {
			t.Errorf("a grey has no hue to turn: %s gave %q %v", grey, c, err)
		}
	}
	c, err := l.color("#f08a24")
	if err != nil || c == "#f08a24" || !regexp.MustCompile(`^#[0-9a-f]{6}$`).MatchString(c) {
		t.Fatalf("the accent: %q %v", c, err)
	}
	if _, err := l.color("#zzz"); err == nil {
		t.Fatal("not a colour passed")
	}
}

// The plain 404 is the installation's own too, one of a few a small site says.
func TestNotFoundBody(t *testing.T) {
	body := func() string {
		w := httptest.NewRecorder()
		NotFound(w)
		if w.Code != 404 || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status %d, headers %v", w.Code, w.Header())
		}
		return w.Body.String()
	}
	if got := body(); got != "404 Not Found\n" {
		t.Fatalf("no seed: %q", got)
	}
	seen := map[string]bool{}
	for i := range 200 {
		SetSeed("seed-" + strconv.Itoa(i))
		got := body()
		if !slices.Contains(notFoundBodies, got) {
			t.Fatalf("seed %d: %q", i, got)
		}
		seen[got] = true
	}
	SetSeed("")
	if len(seen) != len(notFoundBodies) {
		t.Fatalf("%d of %d bodies over 200 seeds", len(seen), len(notFoundBodies))
	}
}
