package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"mikan/internal/release"
)

// seed is the index the first release that keeps one starts from, when the "updates"
// release has none yet: the releases before it, each with the lowest version it updates
// from directly.
var seed = []release.Entry{
	{Version: "0.4.5", Channel: release.Stable, Manifest: manifestURL("v0.4.5"), From: "0.4.0"},
	{Version: "0.5.0.0", Channel: release.Stable, Manifest: manifestURL("v0.5.0.0"), From: "0.4.5"},
}

func manifestURL(tag string) string {
	return fmt.Sprintf("https://github.com/%s/releases/download/%s/manifest.json", release.Repo, tag)
}

// makeIndex adds a release to the index (or replaces its entry), signs it and checks that
// the signed index gives the release to its readers.
func makeIndex(args []string) error {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	version := fs.String("version", "", "release version, e.g. 0.5.0.1")
	channel := fs.String("channel", "", "stable or beta (default: beta for a pre-release, stable otherwise)")
	from := fs.String("from", "", "lowest installed version that updates to this release directly")
	fromFile := fs.String("from-file", "", "file with that version (# comments allowed)")
	tag := fs.String("tag", "", "release tag the manifest is downloaded from (default v<version>)")
	in := fs.String("in", "", "the current index.json (its signature next to it, .sig)")
	seeded := fs.Bool("seed", false, "start from the releases before the index instead of -in")
	out := fs.String("out", "dist", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	key, err := privateKey(os.Getenv("RELEASE_SIGNING_KEY"))
	if err != nil {
		return err
	}
	pub := key.Public().(ed25519.PublicKey)
	if *fromFile != "" {
		v, err := readVersionFile(*fromFile)
		if err != nil {
			return err
		}
		if v != "" && *from != "" && v != *from {
			return fmt.Errorf("-from %s and %s (%s) disagree", *from, *fromFile, v)
		}
		if v != "" {
			*from = v
		}
	}
	if *channel == "" {
		*channel = release.ChannelOf(*version)
	}
	if *tag == "" {
		*tag = "v" + *version
	}
	e := release.Entry{Version: *version, Channel: *channel, Manifest: manifestURL(*tag), From: *from}
	if err := checkEntry(e); err != nil {
		return err
	}

	var top map[string]json.RawMessage
	var entries []json.RawMessage
	switch {
	case *in != "" && *seeded:
		return errors.New("-in or -seed, not both")
	case *in != "":
		data, err := os.ReadFile(*in)
		if err != nil {
			return err
		}
		sig, err := os.ReadFile(*in + ".sig")
		if err != nil {
			return err
		}
		// The index carried on is the one the release key signed, as its readers check it.
		if _, err := release.ParseIndex(data, string(sig), pub); err != nil {
			return fmt.Errorf("%s: %w", *in, err)
		}
		if err := json.Unmarshal(data, &top); err != nil {
			return fmt.Errorf("%s: %w", *in, err)
		}
		if err := json.Unmarshal(top["releases"], &entries); err != nil {
			return fmt.Errorf("%s: releases: %w", *in, err)
		}
	case *seeded:
		top = map[string]json.RawMessage{}
		for _, s := range seed {
			raw, _ := json.Marshal(s)
			entries = append(entries, raw)
		}
	default:
		return errors.New("-in index.json, or -seed for the first index")
	}
	data, err := addEntry(top, entries, e, time.Now().UTC().Truncate(time.Second))
	if err != nil {
		return err
	}
	sig := release.Sign(data, key)
	// The index must give the release to the readers, by their rules.
	ix, err := release.ParseIndex(data, sig, pub)
	if err != nil {
		return err
	}
	if !slices.Contains(ix.Releases, e) {
		return fmt.Errorf("the signed index does not list %s", e.Version)
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	for name, content := range map[string][]byte{"index.json": data, "index.json.sig": []byte(sig + "\n")} {
		if err := os.WriteFile(filepath.Join(*out, name), content, 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("index: %d releases; %s on %s from %s\n", len(ix.Releases), e.Version, e.Channel, e.From)
	return nil
}

// checkEntry refuses an entry the readers would skip, or one that sends a stable server
// to a pre-release.
func checkEntry(e release.Entry) error {
	if release.ChannelOf(e.Version) == release.Beta && e.Channel != release.Beta && release.ValidChannel(e.Channel) {
		return fmt.Errorf("%s is a pre-release: its channel is beta", e.Version)
	}
	if e.From == "" {
		return errors.New("no -from version: pass -from or -from-file")
	}
	ix, err := release.DecodeIndex(mustJSON(map[string]any{"releases": []release.Entry{e}}))
	if err != nil {
		return err
	}
	if len(ix.Releases) != 1 {
		return fmt.Errorf("an entry the readers skip: version %q, channel %q, from %q (want a release version, stable or beta, from a version not newer than the release)", e.Version, e.Channel, e.From)
	}
	return nil
}

// addEntry puts e into the entries (in place of one of its version, else at the end) and
// writes the index: what this command does not know of (fields, entries) is carried as it
// was, for the readers that do.
func addEntry(top map[string]json.RawMessage, entries []json.RawMessage, e release.Entry, now time.Time) ([]byte, error) {
	raw := mustJSON(e)
	replaced := false
	for i, r := range entries {
		var fields map[string]json.RawMessage
		var v string
		if json.Unmarshal(r, &fields) == nil && json.Unmarshal(fields["version"], &v) == nil && v == e.Version {
			if replaced {
				return nil, fmt.Errorf("the index lists %s twice", v)
			}
			entries[i], replaced = raw, true
		}
	}
	if !replaced {
		entries = append(entries, raw)
	}
	return writeIndex(top, entries, now)
}

// writeIndex writes an index of the entries, with the fields of top it does not set
// carried as they were.
func writeIndex(top map[string]json.RawMessage, entries []json.RawMessage, now time.Time) ([]byte, error) {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, `{"schema":%d,"published":%s,"releases":[`, release.IndexSchema, mustJSON(now.Format(time.RFC3339)))
	for i, r := range entries {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(r)
	}
	buf.WriteByte(']')
	keys := make([]string, 0, len(top))
	for k := range top {
		if k != "schema" && k != "published" && k != "releases" {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(&buf, ",%s:%s", mustJSON(k), top[k])
	}
	buf.WriteByte('}')
	var out bytes.Buffer
	if err := json.Indent(&out, buf.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	out.WriteByte('\n')
	return out.Bytes(), nil
}

// makeGoals puts the goals of a file into the signed index in place of its goals, and
// signs it again; the releases are carried as they are.
func makeGoals(args []string) error {
	fs := flag.NewFlagSet("goals", flag.ContinueOnError)
	file := fs.String("file", ".github/goals.json", "the goals")
	in := fs.String("in", "", "the current index.json (its signature next to it, .sig)")
	out := fs.String("out", "dist", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *in == "" {
		return errors.New("-in index.json: goals go into an index a release made")
	}
	key, err := privateKey(os.Getenv("RELEASE_SIGNING_KEY"))
	if err != nil {
		return err
	}
	pub := key.Public().(ed25519.PublicKey)
	raw, err := os.ReadFile(*file)
	if err != nil {
		return err
	}
	goals, err := release.ParseGoals(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", *file, err)
	}
	goals.Sort()
	data, err := os.ReadFile(*in)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(*in + ".sig")
	if err != nil {
		return err
	}
	before, err := release.ParseIndex(data, string(sig), pub)
	if err != nil {
		return fmt.Errorf("%s: %w", *in, err)
	}
	var top map[string]json.RawMessage
	var entries []json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return fmt.Errorf("%s: %w", *in, err)
	}
	if err := json.Unmarshal(top["releases"], &entries); err != nil {
		return fmt.Errorf("%s: releases: %w", *in, err)
	}
	top["goals"] = mustJSON(goals)
	next, err := writeIndex(top, entries, time.Now().UTC().Truncate(time.Second))
	if err != nil {
		return err
	}
	nextSig := release.Sign(next, key)
	ix, err := release.ParseIndex(next, nextSig, pub)
	if err != nil {
		return err
	}
	// The readers must see every goal, and the releases exactly as before.
	if len(ix.Goals.Items) != len(goals.Items) || !slices.Equal(ix.Releases, before.Releases) {
		return errors.New("the signed index does not read back as written")
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	for name, content := range map[string][]byte{"index.json": next, "index.json.sig": []byte(nextSig + "\n")} {
		if err := os.WriteFile(filepath.Join(*out, name), content, 0o644); err != nil {
			return err
		}
	}
	fmt.Printf("index: %d goals, %d releases\n", len(ix.Goals.Items), len(ix.Releases))
	return nil
}

func mustJSON(v any) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

// legacyCheckFile checks a manifest against the legacy contract (legacy.go) with the
// release key the clients carry.
func legacyCheckFile(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: mikan-release legacy-check manifest.json")
	}
	data, err := os.ReadFile(args[0])
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(args[0] + ".sig")
	if err != nil {
		return err
	}
	// Those versions trust the first release key alone.
	pub, err := release.Key(release.ReleaseKeys[0])
	if err != nil {
		return err
	}
	if err := legacyCheck(data, string(sig), pub); err != nil {
		return fmt.Errorf("%s would not be read by mikan 0.4.5 and 0.5.0.0, which read the latest release: %w", args[0], err)
	}
	fmt.Println("ok: mikan 0.4.5 and 0.5.0.0 read this manifest")
	return nil
}
