package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mikan/internal/release"
)

// readIndex reads back what the index command signed, as the readers check it.
func (f *fixture) readIndex(dir string) release.Index {
	f.t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		f.t.Fatal(err)
	}
	sig, err := os.ReadFile(filepath.Join(dir, "index.json.sig"))
	if err != nil {
		f.t.Fatal(err)
	}
	ix, err := release.ParseIndex(data, string(sig), f.pub)
	if err != nil {
		f.t.Fatalf("the signed index does not parse: %v", err)
	}
	return ix
}

func versions(ix release.Index) string {
	var v []string
	for _, e := range ix.Releases {
		v = append(v, e.Version+"/"+e.Channel+"/"+e.From)
	}
	return strings.Join(v, " ")
}

// The first index starts from the releases before it; each next one carries the last on.
func TestIndexSeedsThenGrows(t *testing.T) {
	f := newFixture(t, changelog)
	from := f.write("upgrade-from", "# the lowest version\n0.4.5\n")
	first := filepath.Join(f.dir, "first")
	if err := makeIndex([]string{"-version", "0.5.0.1", "-from-file", from, "-seed", "-out", first}); err != nil {
		t.Fatal(err)
	}
	ix := f.readIndex(first)
	if got := versions(ix); got != "0.4.5/stable/0.4.0 0.5.0.0/stable/0.4.5 0.5.0.1/stable/0.4.5" {
		t.Fatalf("seeded: %s", got)
	}
	if ix.Schema != release.IndexSchema || ix.Releases[2].Manifest != "https://github.com/o0hhs/mikanaddons/releases/download/v0.5.0.1/manifest.json" {
		t.Fatalf("seeded: %+v", ix)
	}
	// A pre-release goes to beta, and "latest" is none of the index's business.
	second := filepath.Join(f.dir, "second")
	in := filepath.Join(first, "index.json")
	if err := makeIndex([]string{"-version", "0.5.0.2-rc.1", "-from", "0.5.0.0", "-in", in, "-out", second}); err != nil {
		t.Fatal(err)
	}
	if got := versions(f.readIndex(second)); got != "0.4.5/stable/0.4.0 0.5.0.0/stable/0.4.5 0.5.0.1/stable/0.4.5 0.5.0.2-rc.1/beta/0.5.0.0" {
		t.Fatalf("with the pre-release: %s", got)
	}
	// The same release again (a workflow run again) replaces its entry.
	if err := makeIndex([]string{"-version", "0.5.0.1", "-from", "0.5.0.0", "-in", filepath.Join(second, "index.json"), "-out", second}); err != nil {
		t.Fatal(err)
	}
	if got := versions(f.readIndex(second)); got != "0.4.5/stable/0.4.0 0.5.0.0/stable/0.4.5 0.5.0.1/stable/0.5.0.0 0.5.0.2-rc.1/beta/0.5.0.0" {
		t.Fatalf("again: %s", got)
	}
}

// What a later mikan-release wrote and this one does not know stays for its readers.
func TestIndexCarriesWhatItDoesNotKnow(t *testing.T) {
	f := newFixture(t, changelog)
	priv := f.priv
	old := []byte(`{"schema":1,"published":"2026-10-04T10:00:00Z","mirrors":["https://m"],"releases":[{"version":"0.5.0.0","channel":"stable","manifest":"https://x/0.json","from":"0.4.5","rollout":50},{"version":"0.5.0.9","channel":"canary","manifest":"https://x/9.json","from":"0.5.0.0"}]}`)
	in := f.write("index.json", string(old))
	f.write("index.json.sig", release.Sign(old, priv))
	out := filepath.Join(f.dir, "out")
	if err := makeIndex([]string{"-version", "0.5.0.1", "-from", "0.4.5", "-in", in, "-out", out}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(out, "index.json"))
	var doc struct {
		Mirrors  []string
		Releases []map[string]any
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Mirrors) != 1 || len(doc.Releases) != 3 || doc.Releases[0]["rollout"] != 50.0 || doc.Releases[1]["channel"] != "canary" {
		t.Fatalf("lost on the way: %s", data)
	}
	if got := versions(f.readIndex(out)); got != "0.5.0.0/stable/0.4.5 0.5.0.1/stable/0.4.5" {
		t.Fatalf("read: %s", got)
	}
}

// An index the release key did not sign is not carried on, and an entry its readers would
// skip is never written.
func TestIndexRefuses(t *testing.T) {
	f := newFixture(t, changelog)
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	old := []byte(`{"schema":1,"releases":[]}`)
	in := f.write("index.json", string(old))
	f.write("index.json.sig", release.Sign(old, otherPriv))
	out := filepath.Join(f.dir, "out")
	if err := makeIndex([]string{"-version", "0.5.0.1", "-from", "0.4.5", "-in", in, "-out", out}); err == nil {
		t.Fatal("an index of another key is carried on")
	}
	f.write("index.json.sig", release.Sign(old, f.priv))
	for name, args := range map[string][]string{
		"no from":                {"-version", "0.5.0.1"},
		"from newer":             {"-version", "0.5.0.1", "-from", "0.5.0.2"},
		"bad version":            {"-version", "v0.5.0.1", "-from", "0.4.5"},
		"bad from":               {"-version", "0.5.0.1", "-from", "latest"},
		"unknown channel":        {"-version", "0.5.0.1", "-from", "0.4.5", "-channel", "nightly"},
		"a stable pre-release":   {"-version", "0.5.0.1-rc.1", "-from", "0.4.5", "-channel", "stable"},
		"neither in nor seed":    {"-version", "0.5.0.1", "-from", "0.4.5", "-in", ""},
		"both in and seed":       {"-version", "0.5.0.1", "-from", "0.4.5", "-seed"},
		"a from file that lies":  {"-version", "0.5.0.1", "-from", "0.4.4", "-from-file", f.write("upgrade-from", "0.4.5\n")},
		"a missing current file": {"-version", "0.5.0.1", "-from", "0.4.5", "-in", filepath.Join(f.dir, "none.json")},
	} {
		full := append(append([]string{}, args...), "-out", out)
		if !strings.Contains(strings.Join(args, " "), "-in") {
			full = append(full, "-in", in)
		}
		if err := makeIndex(full); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A beta release version (tried on beta first) is fine.
	if err := makeIndex([]string{"-version", "0.5.0.1", "-from", "0.4.5", "-channel", "beta", "-in", in, "-out", out}); err != nil {
		t.Fatal(err)
	}
	if got := versions(f.readIndex(out)); got != "0.5.0.1/beta/0.4.5" {
		t.Fatalf("beta: %s", got)
	}
}

// The repository's file names a version the index takes.
func TestUpgradeFromFile(t *testing.T) {
	v, err := readVersionFile("../../.github/upgrade-from")
	if err != nil || v == "" {
		t.Fatalf("%q %v", v, err)
	}
	if err := checkEntry(release.Entry{Version: "99.0.0", Channel: release.Stable, Manifest: manifestURL("v99.0.0"), From: v}); err != nil {
		t.Fatal(err)
	}
	for _, e := range seed {
		if err := checkEntry(e); err != nil {
			t.Errorf("seed %s: %v", e.Version, err)
		}
	}
}

// Goals go into the signed index in place of the old ones, the releases stay as they were,
// and the next release carries them on.
func TestGoalsIntoIndex(t *testing.T) {
	f := newFixture(t, changelog)
	first := filepath.Join(f.dir, "first")
	if err := makeIndex([]string{"-version", "0.5.0.1", "-from", "0.4.5", "-seed", "-out", first}); err != nil {
		t.Fatal(err)
	}
	goals := f.write("goals.json", `{"donate": "https://web.tribute.tg/d/REA", "items": [
		{"id": "device-addon", "title": {"ru": "Продажа +1 устройства", "en": "Selling +1 device"}, "target": 150, "raised": 40, "currency": "USD", "status": "open"},
		{"id": "singbox", "title": {"en": "sing-box profiles"}, "target": 100, "raised": 100, "currency": "USD", "status": "done", "version": "0.5.0.3"}]}`)
	withGoals := filepath.Join(f.dir, "goals")
	if err := makeGoals([]string{"-file", goals, "-in", filepath.Join(first, "index.json"), "-out", withGoals}); err != nil {
		t.Fatal(err)
	}
	ix := f.readIndex(withGoals)
	if len(ix.Goals.Items) != 2 || ix.Goals.Items[0].Raised != 40 || ix.Goals.Items[1].Version != "0.5.0.3" || ix.Goals.Donate != "https://web.tribute.tg/d/REA" {
		t.Fatalf("goals: %+v", ix.Goals)
	}
	if got := versions(ix); got != "0.4.5/stable/0.4.0 0.5.0.0/stable/0.4.5 0.5.0.1/stable/0.4.5" {
		t.Fatalf("releases changed: %s", got)
	}
	next := filepath.Join(f.dir, "next")
	if err := makeIndex([]string{"-version", "0.5.0.2", "-from", "0.4.5", "-in", filepath.Join(withGoals, "index.json"), "-out", next}); err != nil {
		t.Fatal(err)
	}
	if ix := f.readIndex(next); len(ix.Goals.Items) != 2 {
		t.Fatalf("a release lost the goals: %+v", ix.Goals)
	}
	// A file the readers would not show whole is refused before anything is signed.
	for name, body := range map[string]string{
		"no donate link":       `{"items": []}`,
		"http donate link":     `{"donate": "http://x.example", "items": []}`,
		"unknown field":        `{"donate": "https://x.example", "items": [], "extra": 1}`,
		"bad id":               `{"donate": "https://x.example", "items": [{"id": "Device", "title": {"ru": "a"}, "target": 1, "currency": "USD", "status": "open"}]}`,
		"twice":                `{"donate": "https://x.example", "items": [{"id": "a", "title": {"ru": "a"}, "target": 1, "currency": "USD", "status": "open"}, {"id": "a", "title": {"ru": "b"}, "target": 1, "currency": "USD", "status": "open"}]}`,
		"done without release": `{"donate": "https://x.example", "items": [{"id": "a", "title": {"ru": "a"}, "target": 1, "currency": "USD", "status": "done"}]}`,
		"markup":               `{"donate": "https://x.example", "items": [{"id": "a", "title": {"ru": "<script>"}, "target": 1, "currency": "USD", "status": "open"}]}`,
	} {
		if err := makeGoals([]string{"-file", f.write("bad.json", body), "-in", filepath.Join(first, "index.json"), "-out", filepath.Join(f.dir, "bad")}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The repository's goals file is one the workflow signs.
func TestGoalsFile(t *testing.T) {
	data, err := os.ReadFile("../../.github/goals.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := release.ParseGoals(data); err != nil {
		t.Fatal(err)
	}
}
