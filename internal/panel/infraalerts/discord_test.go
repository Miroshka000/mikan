package infraalerts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"mikan/internal/panel/acme"
	"mikan/internal/panel/settings"
	"mikan/internal/tgapi"
)

// fakeDiscord records the posts and answers with err.
type fakeDiscord struct {
	mu    sync.Mutex
	posts []string
	err   error
}

func (f *fakeDiscord) Post(_ context.Context, hook, text string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posts = append(f.posts, hook+" "+text)
	return f.err
}

func (f *fakeDiscord) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.posts)
}

const testHook = "https://discord.com/api/webhooks/1/abc"

// An admin's message and a notice are copied for the group and Discord once, when they are
// queued; an admin's message only while the admin's alerts are on, a notice always.
func TestCopyOut(t *testing.T) {
	cfg := Default()
	cfg.AdminEnabled = true
	pending := []delivery{
		{Key: "old", Target: targetAdmin, Text: "old"},
		{Key: "new", Target: targetAdmin, Text: "new", CreatedAt: 5},
		{Key: "notice/n", Target: targetNotice, Text: "n"},
		{Key: "sum", Target: targetSummary, Text: "s"},
	}
	fresh := func(k string) bool { return k != "old" }
	got := copyOut(slices.Clone(pending), fresh, cfg, true, true)
	var keys []string
	for _, d := range got[len(pending):] {
		keys = append(keys, d.Key+" "+string(d.Target)+" "+string(d.Of))
	}
	want := []string{"group|new group admin", "discord|new discord admin", "group|notice/n group notice", "discord|notice/n discord notice"}
	if !slices.Equal(keys, want) {
		t.Fatalf("copies %q, want %q", keys, want)
	}
	if got[len(pending)].CreatedAt != 5 {
		t.Fatalf("a copy is younger than its message: %+v", got[len(pending)])
	}
	cfg.AdminEnabled = false
	got = copyOut(slices.Clone(pending), fresh, cfg, false, true)
	if len(got) != len(pending)+1 || got[len(pending)].Key != "discord|notice/n" {
		t.Fatalf("with the admin's alerts off: %+v", got[len(pending):])
	}
	if got := copyOut(slices.Clone(pending), fresh, cfg, false, false); len(got) != len(pending) {
		t.Fatalf("copies with nowhere to go: %+v", got)
	}
}

// The copies go to the group's topic and to the webhook; a copy of an admin's message waits
// for the admin's switch as the message does, a notice's does not.
func TestSendPendingGroupAndDiscord(t *testing.T) {
	m := quietMonitor(time.Unix(1000, 0))
	disc := &fakeDiscord{}
	m.discord = disc
	f := &fakeSender{}
	cfg := Default()
	cfg.AdminEnabled, cfg.GroupChat, cfg.GroupThread = true, "-100123", 42
	st := persistentState{Pending: []delivery{
		{Key: "group|a", Target: targetGroup, Of: targetAdmin, Text: "<b>node</b> down"},
		{Key: "discord|a", Target: targetDiscord, Of: targetAdmin, Text: "<b>node</b> down"},
	}}
	m.sendPending(context.Background(), &st, cfg, outlets{tg: f, discord: testHook})
	if want := []string{"topic -100123/42 <b>node</b> down"}; !slices.Equal(f.calls, want) {
		t.Fatalf("telegram calls %q, want %q", f.calls, want)
	}
	if want := []string{testHook + " <b>node</b> down"}; !slices.Equal(disc.got(), want) {
		t.Fatalf("discord posts %q, want %q", disc.got(), want)
	}
	if len(st.Pending) != 0 {
		t.Fatalf("left: %+v", st.Pending)
	}

	cfg.AdminEnabled = false
	m.discord, f = &fakeDiscord{}, &fakeSender{}
	st.Pending = []delivery{
		{Key: "group|a", Target: targetGroup, Of: targetAdmin, Text: "alert"},
		{Key: "discord|a", Target: targetDiscord, Of: targetAdmin, Text: "alert"},
		{Key: "group|notice/n", Target: targetGroup, Of: targetNotice, Text: "notice"},
		{Key: "discord|notice/n", Target: targetDiscord, Of: targetNotice, Text: "notice"},
	}
	m.sendPending(context.Background(), &st, cfg, outlets{tg: f, discord: testHook})
	if want := []string{"topic -100123/42 notice"}; !slices.Equal(f.calls, want) {
		t.Fatalf("with the admin's alerts off, telegram calls %q, want %q", f.calls, want)
	}
	if want := []string{testHook + " notice"}; !slices.Equal(m.discord.(*fakeDiscord).got(), want) {
		t.Fatalf("with the admin's alerts off, discord posts %q, want %q", m.discord.(*fakeDiscord).got(), want)
	}
}

// Without a Telegram client its messages wait, and Discord's still go; a Discord that is down
// is tried again, one that lost the webhook (404) is not.
func TestSendPendingDiscordOnItsOwn(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	m := quietMonitor(now)
	disc := &fakeDiscord{err: &DiscordError{Code: 429, RetryAfter: 90 * time.Second}}
	m.discord = disc
	cfg := Default()
	cfg.AdminEnabled = true
	st := persistentState{Pending: []delivery{
		{Key: "a", Target: targetAdmin, Text: "x", CreatedAt: now.Unix()},
		{Key: "discord|a", Target: targetDiscord, Of: targetAdmin, Text: "x", CreatedAt: now.Unix()},
	}}
	m.sendPending(context.Background(), &st, cfg, outlets{discord: testHook})
	if len(disc.got()) != 1 {
		t.Fatalf("discord posts %q", disc.got())
	}
	if len(st.Pending) != 2 || st.Pending[0].Key != "a" || st.Pending[0].Attempts != 0 ||
		st.Pending[1].Attempts != 1 || st.Pending[1].NextAttemptAt != now.Unix()+90 {
		t.Fatalf("pending = %+v", st.Pending)
	}
	disc.err = &DiscordError{Code: 404, Message: "Unknown Webhook"}
	st.Pending[1].NextAttemptAt = 0
	m.sendPending(context.Background(), &st, cfg, outlets{discord: testHook})
	if len(st.Pending) != 1 || st.Pending[0].Key != "a" {
		t.Fatalf("a webhook Discord does not have is tried again: %+v", st.Pending)
	}
}

// After Discord's 429 the pass posts no more to it: its other messages wait as long as it
// asked, and no attempt of theirs is counted.
func TestSendPendingDiscordLimitHoldsTheRest(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	m := quietMonitor(now)
	disc := &fakeDiscord{err: &DiscordError{Code: 429, RetryAfter: 90 * time.Second}}
	m.discord = disc
	cfg := Default()
	cfg.AdminEnabled = true
	st := persistentState{Pending: []delivery{
		{Key: "discord|a", Target: targetDiscord, Of: targetAdmin, Text: "a", CreatedAt: now.Unix()},
		{Key: "discord|b", Target: targetDiscord, Of: targetAdmin, Text: "b", CreatedAt: now.Unix()},
		{Key: "discord|c", Target: targetDiscord, Of: targetAdmin, Text: "c", CreatedAt: now.Unix(), NextAttemptAt: now.Unix() + 600},
	}}
	m.sendPending(context.Background(), &st, cfg, outlets{discord: testHook})
	if len(disc.got()) != 1 {
		t.Fatalf("posted after the 429: %q", disc.got())
	}
	want := []struct {
		attempts int
		next     int64
	}{{1, now.Unix() + 90}, {0, now.Unix() + 90}, {0, now.Unix() + 600}}
	if len(st.Pending) != len(want) {
		t.Fatalf("pending = %+v", st.Pending)
	}
	for i, w := range want {
		if d := st.Pending[i]; d.Attempts != w.attempts || d.NextAttemptAt != w.next {
			t.Errorf("%s: attempts %d, next %d; want %d, %d", d.Key, d.Attempts, d.NextAttemptAt, w.attempts, w.next)
		}
	}
}

// With the bot off, what a round finds still reaches Discord.
func TestRoundWithoutTheBotReachesDiscord(t *testing.T) {
	st, ctx := testMonitorStore(t)
	set := settings.New(st.Q)
	cfg := Default()
	cfg.AdminEnabled = true
	if err := settings.Set(ctx, set, KeyConfig, cfg); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, set, KeyDiscord, Discord{URL: testHook}); err != nil {
		t.Fatal(err)
	}
	now := time.Now() // a round's messages are dated by the clock (appendPending)
	m := New(st, set, nil, nil, func() acme.Status { return acme.Status{Error: "broken", CheckedAt: now} }, nil, nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now)
	disc := &fakeDiscord{}
	m.discord = disc
	m.round(ctx)
	m.delivering.Wait()
	if got := disc.got(); len(got) != 1 || !strings.Contains(got[0], "TLS") {
		t.Fatalf("discord posts %q", got)
	}
	if left, err := m.load(ctx); err != nil || len(left.Pending) != 0 {
		t.Fatalf("the queue after: %+v %v", left.Pending, err)
	}
}

// A notice goes to the admin's chat, to the group's topic and to Discord.
func TestNotifyReachesTheGroupAndDiscord(t *testing.T) {
	st, ctx := testMonitorStore(t)
	set := settings.New(st.Q)
	cfg := Default()
	cfg.GroupChat, cfg.GroupThread = "-100123", 42
	if err := settings.Set(ctx, set, KeyConfig, cfg); err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, set, KeyDiscord, Discord{URL: testHook}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var sent []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var msg struct {
			ChatID any    `json:"chat_id"`
			Thread int64  `json:"message_thread_id"`
			Text   string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&msg)
		chat, _ := json.Marshal(msg.ChatID)
		mu.Lock()
		sent = append(sent, string(chat)+"/"+strconv.FormatInt(msg.Thread, 10)+" "+msg.Text)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true,"result":{"message_id":1}}`)
	}))
	t.Cleanup(api.Close)
	now := time.Unix(1_800_000_000, 0)
	m := New(st, set, nil, nil, nil, nil, &noticeBot{tgapi.NewClient(api.URL, "1:x", nil)},
		slog.New(slog.NewTextHandler(io.Discard, nil)), func() time.Time { return now })
	disc := &fakeDiscord{}
	m.discord = disc
	m.Notify(ctx, "k", "came in")
	m.delivering.Wait()
	mu.Lock()
	got := slices.Clone(sent)
	mu.Unlock()
	slices.Sort(got)
	if want := []string{`"-100123"/42 came in`, "777/0 came in"}; !slices.Equal(got, want) {
		t.Fatalf("telegram got %q, want %q", got, want)
	}
	if want := []string{testHook + " came in"}; !slices.Equal(disc.got(), want) {
		t.Fatalf("discord got %q, want %q", disc.got(), want)
	}

	// The admin's chat was told already (a top-up's screenshot): the others alone.
	mu.Lock()
	sent = nil
	mu.Unlock()
	m.NotifyElsewhere(ctx, "topup/1", "a top-up")
	m.delivering.Wait()
	mu.Lock()
	got = slices.Clone(sent)
	mu.Unlock()
	if want := []string{`"-100123"/42 a top-up`}; !slices.Equal(got, want) {
		t.Fatalf("elsewhere, telegram got %q, want %q", got, want)
	}
	if want := []string{testHook + " came in", testHook + " a top-up"}; !slices.Equal(disc.got(), want) {
		t.Fatalf("elsewhere, discord got %q, want %q", disc.got(), want)
	}
}

func TestParseDiscordWebhook(t *testing.T) {
	for _, c := range []struct {
		in string
		ok bool
	}{
		{"https://discord.com/api/webhooks/123456/AbC-d_e", true},
		{" https://discordapp.com/api/v10/webhooks/1/x ", true},
		{"https://canary.discord.com/api/webhooks/1/x?thread_id=99", true},
		{"https://Discord.COM/api/webhooks/1/x", true},
		{"https://DISCORD.com.evil.example/api/webhooks/1/x", false},
		{"http://discord.com/api/webhooks/1/x", false},
		{"https://discord.com.evil.example/api/webhooks/1/x", false},
		{"https://evil.example/api/webhooks/1/x", false},
		{"https://user@discord.com/api/webhooks/1/x", false},
		{"https://discord.com:8443/api/webhooks/1/x", false},
		{"https://discord.com/api/webhooks/1/x/slack", false},
		{"https://discord.com/api/webhooks/1/x?wait=true", false},
		{"https://discord.com/api/webhooks/1/x?thread_id=a", false},
		{"https://discord.com/api/webhooks/1/x#f", false},
		{"", false},
	} {
		got, err := ParseDiscordWebhook(c.in)
		want := strings.Replace(strings.TrimSpace(c.in), "Discord.COM", "discord.com", 1) // the host in lower case
		if (err == nil) != c.ok || c.ok && got != want {
			t.Errorf("ParseDiscordWebhook(%q) = %q, %v; want ok=%v", c.in, got, err, c.ok)
		}
	}
}

func TestDiscordText(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"🔴 <b>n&lt;1&gt; &amp; m</b> — down", "🔴 **n<1> & m** — down"},
		{"<b>a*b_c</b> ~x~ `y` |z|", `**a\*b\_c** \~x\~ ` + "\\`y\\`" + ` \|z\|`},
		{"<i>i</i> <code>a*b</code> <a href=\"https://x.example/?a=1&amp;b=2\">link</a>", "*i* `a*b` [link](<https://x.example/?a=1&b=2>)"},
		{"<tg-spoiler>s</tg-spoiler> 1 < 2", "s 1 < 2"},
		{"@everyone", "@everyone"},
	} {
		if got := DiscordText(c.in); got != c.want {
			t.Errorf("DiscordText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := []rune(DiscordText(strings.Repeat("я", 3000))); len(got) != discordMax {
		t.Errorf("a long text is %d characters", len(got))
	}
}

// Post sends the text as Discord's Markdown with no pings; a 429 says how long to wait, and
// an error never carries the webhook's address.
func TestDiscordClientPost(t *testing.T) {
	var body map[string]any
	status := http.StatusNoContent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, `{"name":"alerts","token":"secret"}`)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if status == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "2.5")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"message":"You are being rate limited.","retry_after":2.5}`)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	c := &DiscordClient{HTTP: srv.Client()}
	hook := srv.URL + "/api/webhooks/1/tok3n"
	if err := c.Post(context.Background(), hook, "<b>x</b> @everyone"); err != nil {
		t.Fatal(err)
	}
	if body["content"] != "**x** @everyone" {
		t.Fatalf("content %v", body["content"])
	}
	if am, _ := body["allowed_mentions"].(map[string]any); am == nil || len(am["parse"].([]any)) != 0 {
		t.Fatalf("allowed_mentions %v", body["allowed_mentions"])
	}
	status = http.StatusTooManyRequests
	err := c.Post(context.Background(), hook, "x")
	var de *DiscordError
	if !errors.As(err, &de) || de.Code != 429 || de.RetryAfter != 2500*time.Millisecond {
		t.Fatalf("429: %v", err)
	}
	if name, err := c.Check(context.Background(), hook+"?thread_id=5"); err != nil || name != "alerts" {
		t.Fatalf("Check = %q, %v", name, err)
	}
	srv.Close()
	err = c.Post(context.Background(), hook, "x")
	if err == nil || strings.Contains(err.Error(), "tok3n") {
		t.Fatalf("an error with the address in it: %v", err)
	}
}
