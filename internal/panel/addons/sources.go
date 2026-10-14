package addons

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"mikan/internal/panel/store/db"
	"mikan/internal/proto"
	"mikan/internal/release"
)

// Third-party addon sources. An admin adds the address of an author's file (one entry), an
// author's catalog, or a GitHub repository; its entries then install as the official ones do,
// as unofficial addons. The panel fetches what an unknown server names, so the fetch is
// narrow: https to public addresses, a short body, redirects only where they are expected.

// What one source file may be, and how long what was fetched stays fresh.
const (
	SourceLimit   = 1 << 20 // bytes of the file; the signature is one line
	SourceTimeout = 10 * time.Second
	// MaxSources and MaxSourceEntries bound what an admin (or a source) can pile up.
	MaxSources       = 16
	MaxSourceEntries = 64
	sourceFresh      = 10 * time.Minute
	sourceFailedFor  = time.Minute // after a failed fetch; doubled on each one in a row, up to the fresh time
	// A GitHub shorthand costs a request of the unauthenticated API (60 an hour from one
	// address) each time: sixteen of them stay under it only if they are read rarely.
	githubFresh = 30 * time.Minute
	// A page asking for a source never waits longer than this for a fetch it did not ask to be
	// redone: the fetch goes on, and the next ask finds it.
	sourceWait = 3 * time.Second
	// OfficialBotID is the id the panel hands the bot slot to by itself (app/botaddon.go): no
	// source can offer an addon under it.
	OfficialBotID = "telegram"
)

// Codes of what a source did wrong; the API and the stored error column carry them as they
// are, the UI turns them into words.
var (
	ErrSourceURL         = errors.New("source_url_invalid")
	ErrSourceAddress     = errors.New("source_address_refused") // not a public address
	ErrSourceRedirect    = errors.New("source_redirect_refused")
	ErrSourceTooLarge    = errors.New("source_too_large")
	ErrSourceUnreachable = errors.New("source_unreachable")
	ErrSourceNotFound    = errors.New("source_not_found")
	ErrSourceBadAnswer   = errors.New("source_bad_answer")
	ErrSourceSignature   = errors.New("source_signature_invalid")
	ErrSourceKeyNeeded   = errors.New("source_key_needed")  // signed, but neither the admin nor the file names the key
	ErrSourceKeyChanged  = errors.New("source_key_changed") // another key than the pinned one, or none
	ErrSourceEmpty       = errors.New("source_empty")       // nothing in it this panel can install
)

var sourceErrors = []error{ErrSourceURL, ErrSourceAddress, ErrSourceRedirect, ErrSourceTooLarge, ErrSourceUnreachable, ErrSourceNotFound,
	ErrSourceBadAnswer, ErrSourceSignature, ErrSourceKeyNeeded, ErrSourceKeyChanged, ErrSourceEmpty}

// SourceCode is the code of a failed fetch: one of the errors above, source_unreachable for
// anything else (the network, a timeout).
func SourceCode(err error) string {
	for _, e := range sourceErrors {
		if errors.Is(err, e) {
			return e.Error()
		}
	}
	return ErrSourceUnreachable.Error()
}

// KeyChangedError is ErrSourceKeyChanged with the key the file is signed with now (empty when
// it is not signed at all): the admin may accept a new key, never a missing one.
type KeyChangedError struct{ Key string }

func (e *KeyChangedError) Error() string { return ErrSourceKeyChanged.Error() }
func (e *KeyChangedError) Is(t error) bool {
	return t == ErrSourceKeyChanged
}

// Fingerprint of a source's public key (base64) as the admin compares it: the first 16 bytes
// of its SHA-256 in groups of four hex digits. "" for what is no key.
func Fingerprint(key string) string {
	pub, ok := ParseSourceKey(key)
	if !ok {
		return ""
	}
	sum := sha256.Sum256(pub)
	h := hex.EncodeToString(sum[:16])
	var groups []string
	for i := 0; i < len(h); i += 4 {
		groups = append(groups, h[i:i+4])
	}
	return strings.Join(groups, ":")
}

// ParseSourceKey reads a base64 ed25519 public key.
func ParseSourceKey(key string) (ed25519.PublicKey, bool) {
	pub, err := release.Key(strings.TrimSpace(key))
	return pub, err == nil
}

// SourcePin says which key a fetch must find. Key is the pinned one ("" for a source that
// was not signed); Accept is the fingerprint the admin looked at and takes as the new key.
// Unsigned is a source the admin already holds without a key: a signature that shows up on it
// is a new key like any other, taken only with Accept (a source being added has no such pin,
// and pins what it is signed with).
type SourcePin struct {
	Key      string
	Accept   string
	Unsigned bool
}

// SourceSkip is an entry of a source the panel left out, and why.
type SourceSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// SourceDoc is what one fetch of a source gave. Key is the key the file was signed with.
type SourceDoc struct {
	Title   string
	Key     string
	Entries []Entry
	Skipped []SourceSkip
}

// sourceNet is the panel's way out to sources. dev lets plain http reach 127.0.0.1 (the
// panel running for development); the github fields are where the shorthand looks, so tests
// can point them at a server of their own.
type sourceNet struct {
	dev        bool
	ghAPI      string          // https://api.github.com
	ghWeb      string          // https://github.com
	ghHosts    map[string]bool // origins that may hand over to the asset hosts
	assetHosts map[string]bool // where a release asset is served from
	client     *http.Client
}

func newSourceNet(dev bool) *sourceNet {
	n := &sourceNet{dev: dev, ghAPI: "https://api.github.com", ghWeb: "https://github.com",
		ghHosts: map[string]bool{"github.com": true, "api.github.com": true},
		assetHosts: map[string]bool{"github.com": true, "objects.githubusercontent.com": true, "release-assets.githubusercontent.com": true,
			"github-releases.githubusercontent.com": true}}
	n.client = &http.Client{
		Timeout:       SourceTimeout,
		CheckRedirect: n.checkRedirect,
		Transport: &http.Transport{
			Proxy:                 nil, // an environment proxy would be dialled instead of the source
			DialContext:           (&net.Dialer{Timeout: SourceTimeout, Control: n.control}).DialContext,
			TLSHandshakeTimeout:   SourceTimeout,
			ResponseHeaderTimeout: SourceTimeout,
			ForceAttemptHTTP2:     true,
			DisableKeepAlives:     true,
		},
	}
	return n
}

// allowed says whether the panel may connect to a: public addresses only, and in development
// 127.0.0.1 too.
func (n *sourceNet) allowed(a netip.Addr) bool {
	a = proto.Unwrap4(a)
	if n.dev && a == netip.MustParseAddr("127.0.0.1") {
		return true
	}
	return proto.PublicAddr(a)
}

// control runs on the address actually dialled, after the name was looked up, so a name
// cannot be turned into a private address between the check and the connection.
func (n *sourceNet) control(network, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	a, err := netip.ParseAddr(host)
	if err != nil || !n.allowed(a) {
		return fmt.Errorf("%w: %s", ErrSourceAddress, host)
	}
	return nil
}

// checkRedirect follows at most three redirects, never to plain http from https, and only to
// the host asked first; a GitHub address may hand over to GitHub's own asset hosts.
func (n *sourceNet) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > 3 {
		return fmt.Errorf("%w: too many", ErrSourceRedirect)
	}
	first, to := via[0].URL, req.URL
	if to.Scheme != "https" && !(n.dev && first.Scheme == "http" && to.Scheme == "http") {
		return fmt.Errorf("%w: to %s", ErrSourceRedirect, to.Scheme)
	}
	if to.Host == first.Host || n.ghHosts[first.Host] && n.assetHosts[to.Host] {
		return nil
	}
	return fmt.Errorf("%w: to %s", ErrSourceRedirect, to.Hostname())
}

// get reads one file, at most limit bytes. A 404 is no error: status says so.
func (n *sourceNet) get(ctx context.Context, rawURL string, limit int64, accept string) (body []byte, status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %v", ErrSourceURL, err)
	}
	req.Header.Set("User-Agent", "mikan-panel")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		if errors.Is(err, ErrSourceAddress) || errors.Is(err, ErrSourceRedirect) {
			return nil, 0, err
		}
		return nil, 0, fmt.Errorf("%w: %v", ErrSourceUnreachable, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, resp.StatusCode, nil
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		return nil, resp.StatusCode, fmt.Errorf("%w: HTTP %d", ErrSourceRedirect, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return nil, resp.StatusCode, fmt.Errorf("%w: HTTP %d", ErrSourceUnreachable, resp.StatusCode)
	case resp.ContentLength > limit:
		return nil, resp.StatusCode, ErrSourceTooLarge
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("%w: %v", ErrSourceUnreachable, err)
	}
	if int64(len(body)) > limit {
		return nil, resp.StatusCode, ErrSourceTooLarge
	}
	return body, resp.StatusCode, nil
}

// ghRepo is a source written as github.com/<owner>/<repo>.
type ghRepo struct{ owner, repo string }

var (
	ghOwner = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	ghName  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
)

// parseURL gives a source's address as it is kept: github.com/<owner>/<repo>, or a full https
// URL (http://127.0.0.1:<port>/… in development). Anything with credentials, a query or a
// fragment is refused, and so is a host that is no public name or address.
func (n *sourceNet) parseURL(s string) (canonical string, gh *ghRepo, err error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 512 || strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return "", nil, ErrSourceURL
	}
	if !strings.Contains(s, "://") {
		if !strings.HasPrefix(strings.ToLower(s), "github.com/") {
			return "", nil, ErrSourceURL
		}
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" || u.Hostname() == "" {
		return "", nil, ErrSourceURL
	}
	u.Host = strings.ToLower(u.Host)
	host := u.Hostname()
	switch {
	case u.Scheme == "https" && proto.PublicHost(host):
	case n.dev && u.Scheme == "http" && host == "127.0.0.1":
	default:
		return "", nil, ErrSourceURL
	}
	if u.Scheme == "https" && u.Host == "github.com" {
		// A repository, or a file of one of its releases; nothing else on github.com is a file.
		parts := strings.Split(strings.Trim(u.Path, "/"), "/")
		switch {
		case len(parts) == 2:
			owner, repo := parts[0], strings.TrimSuffix(parts[1], ".git")
			if !ghOwner.MatchString(owner) || !ghName.MatchString(repo) || repo == "." || repo == ".." {
				return "", nil, ErrSourceURL
			}
			return "github.com/" + owner + "/" + repo, &ghRepo{owner, repo}, nil
		case len(parts) < 5 || parts[2] != "releases":
			return "", nil, ErrSourceURL
		}
	}
	// A file: its signature is the same address plus .sig, so it has a name.
	if u.Path == "" || strings.HasSuffix(u.Path, "/") {
		return "", nil, ErrSourceURL
	}
	return u.String(), nil, nil
}

// ghAsset is a file of a GitHub release.
type ghAsset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
}

// githubFiles finds the latest release's file (mikan-addon.json, else index.json) and its
// signature. Only downloads from the repository's own release page are followed.
func (n *sourceNet) githubFiles(ctx context.Context, r ghRepo) (file, sig string, err error) {
	api := fmt.Sprintf("%s/repos/%s/%s/releases/latest", n.ghAPI, r.owner, r.repo)
	body, status, err := n.get(ctx, api, SourceLimit, "application/vnd.github+json")
	if err != nil {
		return "", "", err
	}
	if status == http.StatusNotFound {
		return "", "", ErrSourceNotFound
	}
	var rel struct {
		Assets []ghAsset `json:"assets"`
	}
	if err := json.Unmarshal(body, &rel); err != nil {
		return "", "", fmt.Errorf("%w: release: %v", ErrSourceBadAnswer, err)
	}
	prefix := fmt.Sprintf("%s/%s/%s/releases/download/", n.ghWeb, r.owner, r.repo)
	find := func(name string) string {
		for _, a := range rel.Assets {
			// GitHub spells the owner and the repository as they were created, whatever the admin typed.
			if a.Name == name && len(a.URL) > len(prefix) && strings.EqualFold(a.URL[:len(prefix)], prefix) && !strings.ContainsAny(a.URL, "?#") {
				return a.URL
			}
		}
		return ""
	}
	for _, name := range []string{"mikan-addon.json", "index.json"} {
		if file = find(name); file != "" {
			return file, find(name + ".sig"), nil
		}
	}
	return "", "", ErrSourceNotFound
}

// sourceFile is what a source's document has beside its entries: a title, and the public key
// its signature must verify with. A catalog lists its entries under adapters (as the official
// one does, whose own "version" is a number); a file without that is one entry.
type sourceFile struct {
	Title    string          `json:"title"`
	Key      string          `json:"key"`
	Adapters json.RawMessage `json:"adapters"`
}

var validVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)

// cleanText keeps what is readable and short: a source's words are shown to the admin, and
// the page is never handed more than it can show.
func cleanText(t Text, maxRunes int) Text {
	out := Text{}
	// The same languages survive every time: the ones the page shows first, then by name.
	langs := slices.Sorted(maps.Keys(t))
	slices.SortStableFunc(langs, func(a, b string) int { return langRank(a) - langRank(b) })
	for _, lang := range langs {
		if len(out) >= 8 {
			break
		}
		if len(lang) == 0 || len(lang) > 8 {
			continue
		}
		s := strings.Map(func(r rune) rune {
			if r == '\n' || r == '\t' || !unicode.IsControl(r) && r != utf8.RuneError {
				return r
			}
			return -1
		}, t[lang])
		if r := []rune(s); len(r) > maxRunes {
			s = string(r[:maxRunes])
		}
		if s = strings.TrimSpace(s); s != "" {
			out[lang] = s
		}
	}
	return out
}

func langRank(l string) int {
	switch l {
	case "en":
		return 0
	case "ru":
		return 1
	}
	return 2
}

// publicRegistry: the registry an image names (Docker Hub when it names none) is a public
// host. An unofficial addon's image is pulled by the root host on the source's word, and that
// must not reach a loopback or a LAN endpoint.
func publicRegistry(image string) bool {
	parts := strings.Split(image, "/")
	if len(parts) < 2 || !(strings.ContainsAny(parts[0], ".:") || parts[0] == "localhost") {
		return true
	}
	host := parts[0]
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return proto.PublicHost(host)
}

// checkSourceEntry says why a source's entry cannot be installed, "" when it can; on success
// it leaves the entry clean. A source offers protocol 2 addons only (no payment adapter: it
// would decide where money goes), pinned by digest: no tag a source could move.
func checkSourceEntry(e *Entry, panelVersion string) string {
	switch {
	case !ValidID(e.ID):
		return "id_invalid"
	case e.ID == OfficialBotID:
		return "id_taken"
	case e.Kind == KindPayment || e.Protocol == Protocol:
		return "payment"
	case e.Protocol != Protocol2 || !e.normalize():
		return "invalid"
	case !ValidImage(e.Image) || !publicRegistry(e.Image):
		return "image_invalid"
	case !release.ValidDigest(e.Digest):
		return "digest_invalid"
	case !validVersion.MatchString(e.Version):
		return "version_invalid"
	case e.MinPanel != "" && panelVersion != "dev" && release.Newer(e.MinPanel, panelVersion):
		return "min_panel"
	}
	e.Name, e.Description = cleanText(e.Name, 80), cleanText(e.Description, 500)
	// The page links it: a scheme other than https must not reach the link.
	if u, err := url.Parse(e.Homepage); err != nil || u.Scheme != "https" || u.Host == "" || len(e.Homepage) > 300 {
		e.Homepage = ""
	}
	return ""
}

// parseSourceDoc reads a source file and checks the signature, when there is one, against
// the key the file names. It returns the key the file is signed with ("" unsigned).
func (m *Manager) parseSourceDoc(data []byte, sig []byte, fallbackTitle, pinKey string) (SourceDoc, error) {
	var f sourceFile
	if err := json.Unmarshal(data, &f); err != nil {
		return SourceDoc{}, fmt.Errorf("%w: %v", ErrSourceBadAnswer, err)
	}
	doc := SourceDoc{Title: cleanLine(f.Title, 60)}
	if doc.Title == "" {
		doc.Title = fallbackTitle
	}
	if sig != nil {
		// A signature names no key: it is checked against the one the admin holds (given on
		// add, or pinned), else the one the file declares. A file signed by another key than
		// the pinned one comes out with that key, for FetchSource to call it a change.
		var pub ed25519.PublicKey
		for _, k := range []string{pinKey, f.Key} {
			if p, ok := ParseSourceKey(k); ok && release.Verify(data, string(sig), p) == nil {
				pub = p
				break
			}
		}
		switch {
		case pub == nil && pinKey == "" && f.Key == "":
			return SourceDoc{}, ErrSourceKeyNeeded
		case pub == nil:
			return SourceDoc{}, ErrSourceSignature
		}
		doc.Key = base64.StdEncoding.EncodeToString(pub)
	}
	var entries []Entry
	if f.Adapters != nil {
		if err := json.Unmarshal(f.Adapters, &entries); err != nil {
			return SourceDoc{}, fmt.Errorf("%w: adapters: %v", ErrSourceBadAnswer, err)
		}
	} else {
		var e Entry
		if err := json.Unmarshal(data, &e); err != nil {
			return SourceDoc{}, fmt.Errorf("%w: %v", ErrSourceBadAnswer, err)
		}
		entries = []Entry{e}
	}
	seen := map[string]bool{}
	for _, e := range entries {
		if len(doc.Entries) >= MaxSourceEntries {
			doc.Skipped = append(doc.Skipped, SourceSkip{ID: skipID(e.ID), Reason: "too_many"})
			continue
		}
		if reason := checkSourceEntry(&e, m.version); reason != "" {
			doc.Skipped = append(doc.Skipped, SourceSkip{ID: skipID(e.ID), Reason: reason})
			continue
		}
		if seen[e.ID] {
			doc.Skipped = append(doc.Skipped, SourceSkip{ID: e.ID, Reason: "duplicate"})
			continue
		}
		seen[e.ID] = true
		doc.Entries = append(doc.Entries, e)
	}
	return doc, nil
}

// skipID is an id as shown in a skipped list: short and printable, whatever the source wrote.
func skipID(id string) string { return cleanLine(id, 40) }

func cleanLine(s string, maxRunes int) string {
	s = strings.Join(strings.FieldsFunc(s, unicode.IsControl), " ")
	if r := []rune(s); len(r) > maxRunes {
		s = string(r[:maxRunes])
	}
	return strings.TrimSpace(s)
}

// FetchSource reads a source and checks it against pin. The file and its signature are two
// requests: a release published between them leaves a pair that does not match, asked for once
// more.
func (m *Manager) FetchSource(ctx context.Context, rawURL string, pin SourcePin) (SourceDoc, error) {
	n := m.srcNet()
	canonical, gh, err := n.parseURL(rawURL)
	if err != nil {
		return SourceDoc{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 3*SourceTimeout)
	defer cancel()
	var doc SourceDoc
	for attempt := 0; attempt < 2; attempt++ {
		doc, err = m.fetchSourceOnce(ctx, n, canonical, gh, pin)
		if !errors.Is(err, ErrSourceSignature) {
			break
		}
	}
	if err != nil {
		return SourceDoc{}, err
	}
	// The pinned key is the trust anchor. A source held without one that begins to sign is
	// not pinned behind the admin's back either: nobody compared that key with the author, and
	// the warning of an unsigned source would be gone for it.
	accepted := doc.Key != "" && pin.Accept != "" && pin.Accept == Fingerprint(doc.Key)
	if (pin.Key != "" || pin.Unsigned) && doc.Key != pin.Key && !accepted {
		return SourceDoc{}, &KeyChangedError{Key: doc.Key}
	}
	if len(doc.Entries) == 0 {
		// The doc comes with the error: what was left out says why.
		return doc, fmt.Errorf("%w: %d left out", ErrSourceEmpty, len(doc.Skipped))
	}
	return doc, nil
}

func (m *Manager) fetchSourceOnce(ctx context.Context, n *sourceNet, canonical string, gh *ghRepo, pin SourcePin) (SourceDoc, error) {
	file, sigURL, title := canonical, canonical+".sig", hostTitle(canonical)
	if gh != nil {
		var err error
		if file, sigURL, err = n.githubFiles(ctx, *gh); err != nil {
			return SourceDoc{}, err
		}
		title = gh.owner + "/" + gh.repo
	}
	data, status, err := n.get(ctx, file, SourceLimit, "")
	if err != nil {
		return SourceDoc{}, err
	}
	if status == http.StatusNotFound {
		return SourceDoc{}, ErrSourceNotFound
	}
	var sig []byte
	if sigURL != "" {
		sig, status, err = n.get(ctx, sigURL, 4096, "")
		switch {
		// Object stores answer 403 for a key that is not there. For a pinned source no
		// signature is source_key_changed all the same.
		case err != nil && (status == http.StatusForbidden || status == http.StatusGone), err == nil && status == http.StatusNotFound:
			sig = nil
		case err != nil:
			return SourceDoc{}, err
		case sig == nil:
			sig = []byte{}
		case pin.Key == "" && looksLikePage(sig):
			// A static host's fallback page, taken for the signature of a file that has none.
			sig = nil
		}
	}
	return m.parseSourceDoc(data, sig, title, pin.Key)
}

// looksLikePage: what came for a signature is a web page (HTML), not a signature.
func looksLikePage(b []byte) bool {
	return strings.HasPrefix(strings.TrimSpace(string(b)), "<")
}

// hostTitle is the name of a source that gave none: its host.
func hostTitle(canonical string) string {
	u, err := url.Parse(canonical)
	if err != nil {
		return canonical
	}
	return u.Hostname()
}

// CanonicalSourceURL is the address a source is kept under, or ErrSourceURL.
func (m *Manager) CanonicalSourceURL(raw string) (string, error) {
	c, _, err := m.srcNet().parseURL(raw)
	return c, err
}

func (m *Manager) srcNet() *sourceNet {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.src
}

// SetDev lets sources be fetched over plain http from 127.0.0.1: the panel running for
// development, where an addon author tries a source on their own machine.
func (m *Manager) SetDev(dev bool) {
	m.mu.Lock()
	m.src = newSourceNet(dev)
	m.mu.Unlock()
}

// SourceState is the last fetch of a source: what it gave, or why it did not. After a failure
// that says nothing about the source itself (the network, a 5xx, a rate limit) Doc is what the
// last good fetch gave, still offered; after any other it is empty.
type SourceState struct {
	Doc   SourceDoc
	Err   error
	At    time.Time
	Fails int // fetches in a row that failed: the retry waits longer each time
}

// Offered: the entries of Doc may be offered and installed from.
func (s SourceState) Offered() bool {
	return s.Err == nil || errors.Is(s.Err, ErrSourceUnreachable) && len(s.Doc.Entries) > 0
}

// SourceStore is what recording a fetch needs; *db.Queries has it.
type SourceStore interface {
	SetAddonSourceFetch(ctx context.Context, arg db.SetAddonSourceFetchParams) error
	SetAddonSourceKey(ctx context.Context, arg db.SetAddonSourceKeyParams) error
}

// Source is the last fetch of the source, from memory; false before the first one.
func (m *Manager) Source(id int64) (SourceState, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sources[id]
	return s, ok
}

// SetSource keeps a fetch the caller made itself (a source just added).
func (m *Manager) SetSource(id int64, doc SourceDoc) {
	m.mu.Lock()
	m.sources[id] = SourceState{Doc: doc, At: m.now()}
	m.mu.Unlock()
}

// ForgetSource drops what is kept of a removed source.
func (m *Manager) ForgetSource(id int64) {
	m.mu.Lock()
	delete(m.sources, id)
	m.mu.Unlock()
}

// sourceTTL is how long the last fetch of a source holds: a good one for the fresh time, a
// failed one for a minute and twice as long after each failure in a row.
func sourceTTL(rawURL string, st SourceState) time.Duration {
	fresh := sourceFresh
	if strings.HasPrefix(rawURL, "github.com/") {
		fresh = githubFresh
	}
	if st.Err == nil {
		return fresh
	}
	return min(sourceFailedFor<<min(max(st.Fails-1, 0), 8), fresh)
}

// sourceFlight is a fetch of one source under way: whoever asks for the source meanwhile joins
// it instead of starting another.
type sourceFlight struct {
	done chan struct{}
	st   SourceState
}

// RefreshSource gives the state of the source, fetching it when the last fetch is not fresh
// (force: always) and recording how it went: the time and the code of the error in the table,
// the key when a signature newly pins one.
//
// A fetch is one per source at a time and does not belong to the request that started it: a
// request that is cancelled leaves it running, and it never records the cancellation as the
// source's fault. A page (not force) is not made to wait for a source it already knows: it
// gets the old state at once, and the fetch brings the new one for the next ask; for a source
// it does not know yet it waits a few seconds at most. An install or a refresh (force) waits
// for the answer.
func (m *Manager) RefreshSource(ctx context.Context, q SourceStore, s db.AddonSource, force bool, accept string) SourceState {
	cached, have := m.Source(s.ID)
	if have && !force && m.now().Sub(cached.At) < sourceTTL(s.Url, cached) {
		return cached
	}
	f := m.fetchFlight(ctx, q, s, accept)
	if have && !force {
		return cached
	}
	var late <-chan time.Time
	if !force {
		t := time.NewTimer(sourceWait)
		defer t.Stop()
		late = t.C
	}
	select {
	case <-f.done:
		return f.st
	case <-ctx.Done():
	case <-late:
	}
	// Nothing is known yet: no entries, and no fault of the source to show.
	return cached
}

func (m *Manager) fetchFlight(ctx context.Context, q SourceStore, s db.AddonSource, accept string) *sourceFlight {
	m.mu.Lock()
	// An accept is the admin's own ask and is not folded into a fetch that does not carry it.
	if f, ok := m.flights[s.ID]; ok && accept == "" {
		m.mu.Unlock()
		return f
	}
	f := &sourceFlight{done: make(chan struct{})}
	m.flights[s.ID] = f
	m.mu.Unlock()
	ctx = context.WithoutCancel(ctx)
	go func() {
		f.st = m.fetchAndRecord(ctx, q, s, accept)
		m.mu.Lock()
		if m.flights[s.ID] == f {
			delete(m.flights, s.ID)
		}
		m.mu.Unlock()
		close(f.done)
	}()
	return f
}

// fetchAndRecord fetches the source and keeps the outcome. A key that changed leaves no entries
// to install; a failure of the network keeps the last good ones.
func (m *Manager) fetchAndRecord(ctx context.Context, q SourceStore, s db.AddonSource, accept string) SourceState {
	prev, _ := m.Source(s.ID)
	doc, err := m.FetchSource(ctx, s.Url, SourcePin{Key: s.Key, Accept: accept, Unsigned: s.Key == ""})
	st := SourceState{Doc: doc, Err: err, At: m.now()}
	if err != nil {
		st.Fails = prev.Fails + 1
		st.Doc = SourceDoc{Skipped: doc.Skipped}
		var kc *KeyChangedError
		switch {
		case errors.As(err, &kc):
			st.Doc.Key = kc.Key // for the fingerprint the admin may accept
		case errors.Is(err, ErrSourceUnreachable) && prev.Offered():
			st.Doc = prev.Doc
		}
		m.log.Warn("addons: source", "id", s.ID, "url", s.Url, "err", SourceCode(err))
	}
	m.mu.Lock()
	m.sources[s.ID] = st
	m.mu.Unlock()
	code := ""
	if err != nil {
		code = SourceCode(err)
	}
	if err := q.SetAddonSourceFetch(ctx, db.SetAddonSourceFetchParams{ID: s.ID, FetchedAt: st.At.Unix(), Error: code}); err != nil {
		m.log.Warn("addons: record source fetch", "id", s.ID, "err", err)
	}
	if st.Err == nil && st.Doc.Key != s.Key {
		if err := q.SetAddonSourceKey(ctx, db.SetAddonSourceKeyParams{ID: s.ID, Key: st.Doc.Key}); err != nil {
			m.log.Warn("addons: record source key", "id", s.ID, "err", err)
		}
	}
	return st
}

// SourceEntries is the entries a source lists now.
type SourceEntries struct {
	ID      int64
	Entries []Entry
}

// Owners says who holds each addon id: 0 is the panel's own (the official catalog, and what
// is installed with no source), a number is a source. An id an installed addon came from is
// that source's, then an id a source claimed (the first to list it keeps it; the claims are
// stored). An entry of a source is offered only under an id its source holds.
func Owners(taken map[string]bool, origins, claims map[string]int64) map[string]int64 {
	owner := map[string]int64{}
	for id := range taken {
		owner[id] = 0
	}
	for _, held := range []map[string]int64{origins, claims} {
		for id, src := range held {
			if _, ok := owner[id]; !ok {
				owner[id] = src
			}
		}
	}
	return owner
}
