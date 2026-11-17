package support

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The support bot is the only one who gets support access. The admin takes a one-time code
// from a paid ticket in the bot; the panel asks the bot whether the code is good (Claim) and
// only then opens a grant, whose link it hands to the bot itself (Attach). The link is never
// shown to the admin, so it reaches nobody but the official support.

// DefaultBotURL is the official support bot's listener, behind a Cloudflare Tunnel on a name
// of its own, so the address leads to neither a VPN server nor a VPN domain.
const DefaultBotURL = "https://mikanpanel.dpdns.org"

// codeLen is the length of the code the bot gives.
const codeLen = 8

var codeRe = regexp.MustCompile(`^[A-Z0-9]{` + strconv.Itoa(codeLen) + `}$`)

// NormCode is a code as typed: spaces and dashes dropped, upper case. ok is false for anything
// that is not a code.
func NormCode(s string) (string, bool) {
	c := strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "\t", "").Replace(strings.TrimSpace(s)))
	return c, codeRe.MatchString(c)
}

// Errors of the bot.
var (
	// ErrCode: the bot does not know the code, it was used or it expired.
	ErrCode = errors.New("support code is not good")
	// ErrBotDown: the bot did not answer as it should.
	ErrBotDown = errors.New("support bot is unreachable")
)

// LinkError is the bot refusing the access link: it could not reach the panel by it or the
// panel is not an official build. Reason is the bot's words, for the admin.
type LinkError struct{ Reason string }

func (e *LinkError) Error() string { return "support bot refused the link: " + e.Reason }

// Bot is the support bot as the panel reaches it.
type Bot interface {
	// Claim checks the code and returns the ticket it is of; the code stays unused.
	Claim(ctx context.Context, code string) (ticket int64, err error)
	// Attach hands the access link to the bot under the code, which it uses up.
	Attach(ctx context.Context, code, link string) (ticket int64, err error)
}

// HTTPBot is the bot over HTTPS.
type HTTPBot struct {
	URL  string
	HTTP *http.Client
}

// NewHTTPBot is the bot at the address; plain http is taken for a loopback address only (tests).
func NewHTTPBot(raw string) (*HTTPBot, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("support bot address %q: want https://host[:port]", raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if ip := net.ParseIP(u.Hostname()); ip == nil || !ip.IsLoopback() {
			return nil, fmt.Errorf("support bot address %q: plain http only on a loopback address", raw)
		}
	default:
		return nil, fmt.Errorf("support bot address %q: want https://host[:port]", raw)
	}
	return &HTTPBot{URL: u.Scheme + "://" + u.Host, HTTP: &http.Client{
		Timeout: 30 * time.Second,
		// The bot answers itself; a redirect would carry the link elsewhere.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

type botReply struct {
	Ticket int64  `json:"ticket"`
	Error  string `json:"error"`
	Detail string `json:"detail"`
}

func (b *HTTPBot) call(ctx context.Context, path string, in any) (int64, error) {
	raw, err := json.Marshal(in)
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.URL+path, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrBotDown, err)
	}
	defer resp.Body.Close()
	var out botReply
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	_ = json.Unmarshal(body, &out)
	switch {
	case resp.StatusCode == http.StatusOK && out.Ticket > 0:
		return out.Ticket, nil
	case resp.StatusCode == http.StatusForbidden && out.Error == "invalid_code":
		return 0, ErrCode
	case resp.StatusCode == http.StatusUnprocessableEntity && out.Error == "link_refused":
		return 0, &LinkError{Reason: clip(out.Detail, 200)}
	case resp.StatusCode == http.StatusTooManyRequests:
		return 0, fmt.Errorf("%w: too many attempts", ErrBotDown)
	}
	return 0, fmt.Errorf("%w: status %d", ErrBotDown, resp.StatusCode)
}

func (b *HTTPBot) Claim(ctx context.Context, code string) (int64, error) {
	return b.call(ctx, "/support/claim", map[string]string{"code": code})
}

func (b *HTTPBot) Attach(ctx context.Context, code, link string) (int64, error) {
	return b.call(ctx, "/support/attach", map[string]string{"code": code, "link": link})
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
