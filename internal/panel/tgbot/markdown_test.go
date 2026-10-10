package tgbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

// The admin's Markdown becomes the bot's HTML; what is not Markdown stays as typed.
func TestMarkdownHTML(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		// Plain texts of before read the same.
		{"Hello, world", "Hello, world"},
		{"a < b & c > d", "a &lt; b &amp; c &gt; d"},
		{`<b>not a tag</b> "q" 'a'`, "&lt;b&gt;not a tag&lt;/b&gt; &#34;q&#34; &#39;a&#39;"},
		{"snake_case_name and my_bot", "snake_case_name and my_bot"},
		{"2 * 3 * 4 = 24", "2 * 3 * 4 = 24"},
		{"a ** b", "a ** b"},
		{"* item\n* item", "* item\n* item"},
		{"- one\n- two\n1. three", "- one\n- two\n1. three"},
		{"50% off_ now", "50% off_ now"},
		{"a || b", "a || b"},
		{"~ tilde ~", "~ tilde ~"},
		{"#hashtag", "#hashtag"},
		{"line one\n\nline two", "line one\n\nline two"},
		{"price: 100₽ (month)", "price: 100₽ (month)"},
		{"[not a link]", "[not a link]"},
		{"[x] (y)", "[x] (y)"},
		// The marks.
		{"**bold**", "<b>bold</b>"},
		{"*italic* and _italic_", "<i>italic</i> and <i>italic</i>"},
		{"__under__", "<u>under</u>"},
		{"~~gone~~", "<s>gone</s>"},
		{"||secret||", "<tg-spoiler>secret</tg-spoiler>"},
		{"`a < b`", "<code>a &lt; b</code>"},
		{"`` a ` b ``", "<code>a ` b</code>"},
		{"```\nx := <1>\n```", "<pre>x := &lt;1&gt;</pre>"},
		{"```go\nfmt.Println()\n```", `<pre><code class="language-go">fmt.Println()</code></pre>`},
		{"> quoted\n> more\nafter", "<blockquote>quoted\nmore</blockquote>\nafter"},
		{"# Title", "<b>Title</b>"},
		{"## Title ##", "<b>Title</b>"},
		{"# C#", "<b>C#</b>"},
		{"# **bold** title", "<b>bold title</b>"},
		{"[site](https://example.com)", `<a href="https://example.com">site</a>`},
		{"[bot](tg://resolve?domain=x)", `<a href="tg://resolve?domain=x">bot</a>`},
		{"[old](http://example.com)", `<a href="http://example.com">old</a>`},
		{`[t](https://e.com/?a=1&b="2")`, `<a href="https://e.com/?a=1&amp;b=&#34;2&#34;">t</a>`},
		{`[t](https://e.com/x "Title")`, `<a href="https://e.com/x">t</a>`},
		{"[**b** _i_](https://e.com)", `<a href="https://e.com"><b>b</b> <i>i</i></a>`},
		// Nesting.
		{"**bold _italic_ bold**", "<b>bold <i>italic</i> bold</b>"},
		{"*a **b** c*", "<i>a <b>b</b> c</i>"},
		{"**a `**` b**", "<b>a <code>**</code> b</b>"},
		{"**a\nb**", "<b>a\nb</b>"},
		{"> **x** [y](https://e.com)", `<blockquote><b>x</b> <a href="https://e.com">y</a></blockquote>`},
		// Escapes.
		{`\*not italic\*`, "*not italic*"},
		{`\_x\_ \\`, `_x_ \`},
		{`a\b`, `a\b`},
	} {
		if got := markdownHTML(c.in, func(s string) string { return fill(s, nil) }); got != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// What is broken or not allowed stays text: never a tag without its pair, never a link
// Telegram or the subscriber should not follow.
func TestMarkdownBroken(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"**open", "**open"},
		{"close**", "close**"},
		{"*a **b*", "<i>a **b</i>"},
		{"``` never closed", "``` never closed"},
		{"```\nnever closed", "<pre>never closed</pre>"},
		{"```\n```", "```\n```"},
		{"`open", "`open"},
		{"[x](javascript:alert(1))", "[x](javascript:alert(1))"},
		{"[x](data:text/html,<b>)", "[x](data:text/html,&lt;b&gt;)"},
		{"[x](//e.com)", "[x](//e.com)"},
		{"[x](https://)", "[x](https://)"},
		{"[x](https://user:pw@e.com)", "[x](https://user:pw@e.com)"},
		{"[](https://e.com)", "[](https://e.com)"},
		{"[x](https://e.com) more", `<a href="https://e.com">x</a> more`},
		{"[a\nb](https://e.com)", "[a\nb](https://e.com)"},
		{"[x](https://e.com\" onclick=\"y)", "[x](https://e.com&#34; onclick=&#34;y)"},
		{">", "&gt;"},
		{"# ", "# "},
		{"***", "***"},
		{"****", "****"},
		{"<script>alert(1)</script>", "&lt;script&gt;alert(1)&lt;/script&gt;"},
		{"**<i>x</i>**", "<b>&lt;i&gt;x&lt;/i&gt;</b>"},
	} {
		if got := markdownHTML(c.in, func(s string) string { return fill(s, nil) }); got != c.want {
			t.Errorf("%q:\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

// Every tag the converter opens it closes, in order, whatever the input: Telegram refuses
// a message with a tag out of place.
func TestMarkdownBalanced(t *testing.T) {
	marks := []string{"*", "**", "_", "__", "~~", "||", "`", "```", "[", "](https://e.com)", "> ", "# ", "\n", "\\", "a", " ", "<", "&"}
	seed := uint32(1)
	next := func() int {
		seed = seed*1664525 + 1013904223
		return int(seed >> 16)
	}
	for n := 0; n < 5000; n++ {
		var b strings.Builder
		for k := next()%12 + 1; k > 0; k-- {
			b.WriteString(marks[next()%len(marks)])
		}
		in := b.String()
		out := markdownHTML(in, func(s string) string { return fill(s, nil) })
		if err := balanced(out); err != "" {
			t.Fatalf("%q -> %q: %s", in, out, err)
		}
		if utf8.RuneCountInString(plainText(out)) > utf8.RuneCountInString(in) {
			t.Fatalf("%q -> %q: the text grew", in, out)
		}
	}
}

// balanced checks the tags of out nest; "" when they do.
func balanced(out string) string {
	var stack []string
	for {
		i := strings.IndexByte(out, '<')
		if i < 0 {
			break
		}
		j := strings.IndexByte(out[i:], '>')
		if j < 0 {
			return "a tag without its end"
		}
		tag := out[i+1 : i+j]
		out = out[i+j+1:]
		if name, ok := strings.CutPrefix(tag, "/"); ok {
			if len(stack) == 0 || stack[len(stack)-1] != name {
				return "</" + name + "> out of place"
			}
			stack = stack[:len(stack)-1]
			continue
		}
		name, _, _ := strings.Cut(tag, " ")
		for _, open := range stack {
			if open == name {
				return "<" + name + "> inside itself"
			}
		}
		stack = append(stack, name)
	}
	if len(stack) > 0 {
		return "unclosed " + strings.Join(stack, ",")
	}
	return ""
}

// The variables are filled after the Markdown: a subscriber's name with marks or tags in
// it stays text, and a variable inside a mark is formatted with it.
func TestRenderVariables(t *testing.T) {
	vars := map[string]string{"name": "**<b>Ivan</b>** _x_ [a](https://e.com)", "brand": "Mikan & Co"}
	got := render("**{name}** of {brand}: `{brand}` [{brand}](https://e.com/{brand})", vars)
	want := `<b>**&lt;b&gt;Ivan&lt;/b&gt;** _x_ [a](https://e.com)</b> of Mikan &amp; Co: <code>Mikan &amp; Co</code> <a href="https://e.com/{brand}">Mikan &amp; Co</a>`
	if got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

// A long text stays the length it was: the marks go, nothing is added to what Telegram
// counts (it counts the text, not the tags), so the 3000 letters of a text stay under 4096.
func TestMarkdownLength(t *testing.T) {
	in := strings.Repeat("**ab** _cd_ `ef` [gh](https://e.com) ", 100)
	out := render(in, nil)
	if n, m := utf8.RuneCountInString(plainText(out)), utf8.RuneCountInString(in); n >= m {
		t.Fatalf("the text grew: %d letters from %d", n, m)
	}
	long := strings.Repeat("я", maxText)
	if got := plainText(render(long, nil)); got != long {
		t.Fatal("a long plain text changed")
	}
}

// A message Telegram cannot parse goes again as plain text instead of failing; any other
// refusal stays an error.
func TestSendFallsBackToPlainText(t *testing.T) {
	var calls []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		calls = append(calls, body)
		switch {
		case body["parse_mode"] == "HTML":
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": "Bad Request: can't parse entities: Unsupported start tag \"x\" at byte offset 0"})
		case strings.HasSuffix(r.URL.Path, "/editMessageText"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": true})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": map[string]any{"message_id": 5, "chat": map[string]any{"id": 1, "type": "private"}}})
		}
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "1:x", nil)
	m, err := c.Send(context.Background(), 1, "<x>a &lt; b</x>", nil, false)
	if err != nil || m.MessageID != 5 || len(calls) != 2 || calls[1]["text"] != "a < b" || calls[1]["parse_mode"] != nil {
		t.Fatalf("send: %+v %v %v", m, err, calls)
	}
	calls = nil
	if err := c.Edit(context.Background(), 1, 5, "<x>c</x>", nil); err != nil || len(calls) != 2 || calls[1]["text"] != "c" {
		t.Fatalf("edit: %v %v", err, calls)
	}

	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 403, "description": "Forbidden: bot was blocked by the user"})
	}))
	defer other.Close()
	if _, err := NewClient(other.URL, "1:x", nil).Send(context.Background(), 1, "a", nil, false); err == nil {
		t.Fatal("a blocked bot is not a parse error")
	}
}

// plainText is what is left of the HTML when Telegram refuses it: the text, unescaped.
func TestPlainText(t *testing.T) {
	if got := plainText(`<b>a &lt; b</b> <a href="https://e.com">c &amp; d</a> &#34;e&#34;`); got != `a < b c & d "e"` {
		t.Fatalf("plain %q", got)
	}
}
