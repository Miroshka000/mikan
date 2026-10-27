package infraalerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"mikan/internal/textutil"
)

// KeyDiscord keeps the Discord webhook the admin's alerts and notices also go to (Discord).
// The address is the webhook's secret: whoever has it posts to the channel, so it stays out of
// the alerts' settings the panel shows and is told only as set or not, with its name.
const KeyDiscord = "infrastructure_alerts_discord"

// Discord is KeyDiscord's value.
type Discord struct {
	URL  string `json:"url"`
	Name string `json:"name,omitempty"` // the webhook's name in Discord, shown to the admin
}

// ErrDiscordWebhook: the address is not a Discord webhook.
var ErrDiscordWebhook = errors.New("discord_webhook_format")

// discordMax is how long a message Discord takes, in characters.
const discordMax = 2000

var discordPath = regexp.MustCompile(`^/api(/v\d{1,2})?/webhooks/\d{1,25}/[A-Za-z0-9_-]{1,100}$`)

// ParseDiscordWebhook checks that s is a Discord webhook: https, a host of Discord, the
// webhook's path and, for a forum channel, a thread_id. Nothing else is posted to: the
// address is the admin's, but it is the panel that makes the request.
func ParseDiscordWebhook(s string) (string, error) {
	s = strings.TrimSpace(s)
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" || !discordPath.MatchString(u.Path) {
		return "", ErrDiscordWebhook
	}
	switch u.Hostname() {
	case "discord.com", "discordapp.com", "canary.discord.com", "ptb.discord.com":
	default:
		return "", ErrDiscordWebhook
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", ErrDiscordWebhook
	}
	for k, v := range q {
		if k != "thread_id" || len(v) != 1 {
			return "", ErrDiscordWebhook
		}
		if _, err := strconv.ParseUint(v[0], 10, 64); err != nil {
			return "", ErrDiscordWebhook
		}
	}
	return u.String(), nil
}

// DiscordError is Discord's answer that was not a success: a 4xx but 429 is for good (the
// webhook was deleted), and RetryAfter is how long a 429 asks to wait.
type DiscordError struct {
	Code       int
	RetryAfter time.Duration
	Message    string
}

func (e *DiscordError) Error() string {
	if e.Message == "" {
		return "discord: HTTP " + strconv.Itoa(e.Code)
	}
	return "discord: HTTP " + strconv.Itoa(e.Code) + ": " + e.Message
}

// discordPoster posts a message to a webhook (*DiscordClient); a test hands the monitor a
// fake.
type discordPoster interface {
	Post(ctx context.Context, hook, text string) error
}

// DiscordClient talks to Discord webhooks.
type DiscordClient struct{ HTTP *http.Client }

// NewDiscordClient is a client that does not follow redirects: an answer of Discord that
// sends elsewhere is an error, not a request to somewhere else.
func NewDiscordClient() *DiscordClient {
	return &DiscordClient{HTTP: &http.Client{Timeout: deliveryTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}}
}

// Post sends text, Telegram HTML as the alerts are written, as one Discord message. Nobody is
// pinged: a name with @everyone in it stays text.
func (c *DiscordClient) Post(ctx context.Context, hook, text string) error {
	body, err := json.Marshal(map[string]any{
		"content":          DiscordText(text),
		"allowed_mentions": map[string]any{"parse": []string{}},
	})
	if err != nil {
		return err
	}
	_, err = c.do(ctx, http.MethodPost, hook, body)
	return err
}

// Check reads the webhook: Discord answers with its name when the address works, 401 or 404
// when there is no such webhook.
func (c *DiscordClient) Check(ctx context.Context, hook string) (string, error) {
	u, err := url.Parse(hook)
	if err != nil {
		return "", ErrDiscordWebhook
	}
	u.RawQuery = "" // a thread is not part of the webhook GET reads
	raw, err := c.do(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	var w struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(raw, &w)
	return textutil.ClipRunes(w.Name, 80), nil
}

func (c *DiscordClient) do(ctx context.Context, method, hook string, body []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, deliveryTimeout)
	defer cancel()
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, hook, rd)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		// The address is the secret: the error says what went wrong, not where to.
		if inner := errors.Unwrap(err); inner != nil {
			err = inner
		}
		return nil, fmt.Errorf("discord: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return raw, nil
	}
	de := &DiscordError{Code: resp.StatusCode}
	var answer struct {
		Message    string  `json:"message"`
		RetryAfter float64 `json:"retry_after"`
	}
	_ = json.Unmarshal(raw, &answer)
	de.Message = textutil.ClipRunes(answer.Message, 200)
	if s, err := strconv.ParseFloat(resp.Header.Get("Retry-After"), 64); err == nil && s > 0 {
		de.RetryAfter = time.Duration(s * float64(time.Second))
	} else if answer.RetryAfter > 0 {
		de.RetryAfter = time.Duration(answer.RetryAfter * float64(time.Second))
	}
	return nil, de
}

// DiscordText turns a message in Telegram HTML into Discord's Markdown: bold, italic and code
// keep their marks, a link becomes [text](address), the rest of the tags go, and the text
// between them is unescaped and has Discord's own marks escaped, so a node called a*b stays
// a*b. Longer than Discord takes, it is cut.
func DiscordText(s string) string {
	var out strings.Builder
	href, code := "", false
	text := func(s string) {
		s = html.UnescapeString(s)
		if !code { // inside code Discord shows a backslash as it is
			s = discordMarks.Replace(s)
		}
		out.WriteString(s)
	}
	for s != "" {
		i := strings.IndexByte(s, '<')
		if i < 0 {
			i = len(s)
		}
		text(s[:i])
		s = s[i:]
		if s == "" {
			break
		}
		j := strings.IndexByte(s, '>')
		if j < 0 {
			text(s)
			break
		}
		name, attrs, _ := strings.Cut(strings.TrimSpace(s[1:j]), " ")
		name = strings.ToLower(name)
		s = s[j+1:]
		switch name {
		case "b", "/b", "strong", "/strong":
			out.WriteString("**")
		case "i", "/i", "em", "/em":
			out.WriteString("*")
		case "u", "/u", "ins", "/ins":
			out.WriteString("__")
		case "s", "/s", "del", "/del", "strike", "/strike":
			out.WriteString("~~")
		case "code", "/code":
			out.WriteString("`")
			code = name == "code"
		case "pre", "/pre":
			out.WriteString("\n```\n")
			code = name == "pre"
		case "a":
			if _, v, ok := strings.Cut(attrs, "href="); ok {
				href = html.UnescapeString(strings.Trim(v, `"'`))
			}
			out.WriteString("[")
		case "/a":
			out.WriteString("](" + href + ")")
			href = ""
		}
	}
	msg := strings.TrimSpace(out.String())
	if n := []rune(msg); len(n) > discordMax {
		msg = string(n[:discordMax-1]) + "…"
	}
	return msg
}

// discordMarks escapes what Discord's Markdown reads as formatting in the middle of a line.
var discordMarks = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "~", `\~`, "`", "\\`", "|", `\|`)
