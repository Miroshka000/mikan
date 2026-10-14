package release

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// The release index: every release an updater may go to, signed with the release key like
// a manifest. It lives at a stable address that does not depend on GitHub's "latest"
// release: the assets of a pre-release tagged "updates", which never becomes "latest".
//
//	{"schema": 1, "published": "…", "releases": [
//	  {"version": "0.5.0.1", "channel": "stable", "from": "0.4.5",
//	   "manifest": "https://github.com/o0hhs/mikanaddons/releases/download/v0.5.0.1/manifest.json"}]}
//
// An entry only points to its release's manifest, which is fetched and verified exactly
// as before. from is the lowest installed version that can update to the release directly.
//
// The format only ever grows, because every updater out there reads it:
//   - unknown fields, top level or in an entry, are ignored;
//   - an entry with an unparsable version, an unknown channel or a missing field is
//     skipped, never fatal: a later release may add entries an older reader does not
//     understand, and the rest must still work;
//   - schema stays 1 for as long as the file is called index.json: a format older
//     readers cannot read goes to a new file name, never a new meaning here;
//   - "rollout" (a percentage of servers that take a release) is reserved for later: a
//     reader may find it in an entry and ignores it for now.
//
// installer/src/release.rs reads the same file by the same rules; testdata/index.json is
// run by both.

// IndexURL is the release index; its signature is IndexURL + ".sig".
const IndexURL = "https://github.com/" + Repo + "/releases/download/updates/index.json"

// IndexSchema is the only value index.json ever has in "schema".
const IndexSchema = 1

// The channels. A server takes stable releases; one that chose beta takes the
// pre-releases (vX-rc.N tags) as well.
const (
	Stable = "stable"
	Beta   = "beta"
)

// ValidChannel says whether s is exactly a channel's name.
func ValidChannel(s string) bool { return s == Stable || s == Beta }

// ChannelOf is the channel a release of version goes to: a pre-release is beta.
func ChannelOf(version string) string {
	if p := versionParts(version); p != nil && p[5] != "" {
		return Beta
	}
	return Stable
}

type Index struct {
	Schema    int       `json:"schema"`
	Published time.Time `json:"published"`
	Releases  []Entry   `json:"releases"`
	// Goals: see goals.go; empty when the index has none.
	Goals Goals `json:"goals"`
}

type Entry struct {
	Version  string `json:"version"`
	Channel  string `json:"channel"`
	Manifest string `json:"manifest"`
	From     string `json:"from"`
}

// valid says whether a reader can use the entry; the others are skipped. A pre-release is
// never stable; a beta entry may be a release version (one tried on beta first).
func (e Entry) valid() bool {
	return versionParts(e.Version) != nil && ValidChannel(e.Channel) && (e.Channel == Beta || ChannelOf(e.Version) == Stable) &&
		strings.HasPrefix(e.Manifest, "https://") && versionParts(e.From) != nil && !Newer(e.From, e.Version)
}

// ParseIndex checks the signature over the index's exact bytes, then decodes it.
func ParseIndex(data []byte, sig string, pub ed25519.PublicKey) (Index, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(sig))
	if err != nil || len(pub) != ed25519.PublicKeySize || !ed25519.Verify(pub, data, raw) {
		return Index{}, ErrSignature
	}
	return DecodeIndex(data)
}

// DecodeIndex reads an index whose signature was checked: the entries a reader cannot use
// are left out. Keys are matched exactly and the last of a repeated key wins, as the
// installer's JSON reader does (encoding/json alone would take "Version" for "version").
func DecodeIndex(data []byte) (Index, error) {
	if !utf8.Valid(data) {
		return Index{}, errors.New("release: index: not UTF-8")
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return Index{}, fmt.Errorf("release: index: %w", err)
	}
	var releases []json.RawMessage
	if err := json.Unmarshal(doc["releases"], &releases); err != nil || releases == nil {
		return Index{}, errors.New("release: index: no list of releases")
	}
	// The schema and the date are for people: a value this reader cannot read costs nothing.
	var ix Index
	_ = json.Unmarshal(doc["schema"], &ix.Schema)
	_ = json.Unmarshal(doc["published"], &ix.Published)
	ix.Goals = decodeGoals(doc["goals"])
	for _, r := range releases {
		var fields map[string]json.RawMessage
		if json.Unmarshal(r, &fields) != nil {
			continue
		}
		text := func(key string) string {
			var s string
			if json.Unmarshal(fields[key], &s) != nil {
				return ""
			}
			return s
		}
		e := Entry{Version: text("version"), Channel: text("channel"), Manifest: text("manifest"), From: text("from")}
		if e.valid() {
			ix.Releases = append(ix.Releases, e)
		}
	}
	return ix, nil
}

// Choice is what a server of some version finds in the index.
type Choice struct {
	// Target is the release to update to now: the highest one of the channels newer than
	// the server's version that it can update to directly. Zero when there is none.
	Target Entry
	// Newest is the highest release of the channels, newer than the server or not. When it
	// is newer than Target, Target is a hop on the way to it; when there is no Target and it
	// is newer than the server, the server cannot reach it. Zero when the index lists none.
	Newest Entry
}

// Hop says whether the target is a step on the way to a newer release.
func (c Choice) Hop() bool {
	return c.Target.Version != "" && Newer(c.Newest.Version, c.Target.Version)
}

// Choose picks the release a server running current takes from entries (an index's):
// among the entries of the allowed channels (stable, and beta too when beta) with a
// version newer than current and from at or below it, the highest. A current that is not
// a release version (a fresh install, "dev") has no from to meet. The first of equal
// versions wins.
func Choose(entries []Entry, current string, beta bool) Choice {
	var c Choice
	known := versionParts(current) != nil
	for _, e := range entries {
		if !e.valid() || (e.Channel == Beta && !beta) {
			continue
		}
		if c.Newest.Version == "" || Newer(e.Version, c.Newest.Version) {
			c.Newest = e
		}
		if !Newer(e.Version, current) || (known && Newer(e.From, current)) {
			continue
		}
		if c.Target.Version == "" || Newer(e.Version, c.Target.Version) {
			c.Target = e
		}
	}
	return c
}
