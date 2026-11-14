package serve

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// titleVars are the variables the profile title and the announcement may hold, the same way
// the bot's texts do: {brand} · {name} · until {date}. Any other {word} stays as text.
var titleVars = []string{"brand", "name", "tariff", "date", "days", "used", "left", "total", "devices"}

func ptr(n int64) *int64 { return &n }

func TestValuesHoldTitleVars(t *testing.T) {
	vars := Values(User{}, -1, Config{}, time.Now())
	if len(vars) != len(titleVars) {
		t.Fatalf("%d values, %d variables", len(vars), len(titleVars))
	}
	for _, name := range titleVars {
		if _, ok := vars[name]; !ok {
			t.Errorf("no value for {%s}", name)
		}
	}
}

func TestProfileTitle(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	cfg := Config{Brand: "Mikan"}
	for _, tc := range []struct{ title, name, want string }{
		{"", "Вася", "Mikan"},
		{"{name}", "Вася", "Вася"},
		{"{name}", "", "Mikan"},
		{"{name}", "   ", "Mikan"},
		{" {name} ", "", "Mikan"},
		{"{brand} {name}", "", "Mikan"},
		{"{nope}", "Вася", "{nope}"},
	} {
		cfg.Title = tc.title
		if got := profileTitle(cfg, Values(User{Name: tc.name}, -1, cfg, now)); got != tc.want {
			t.Errorf("title %q, name %q: %q, want %q", tc.title, tc.name, got, tc.want)
		}
	}
}

func TestFillTitle(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	// 50 GB of which 5 are used, and 10 more in packages: 55 left of 60.
	u := User{Name: "Вася {brand}", Tariff: "Стандарт", UsedUp: 1 << 30, UsedDown: 4 << 30, Expires: now.Add(36 * time.Hour),
		Limit: ptr(50 << 30), Grants: 10 << 30, Left: 55 << 30}
	vars := Values(u, -1, Config{Brand: "Mikan"}, now)
	got := fillTitle("{brand} · {name} · {tariff} до {date} ({days} дн.) · {used}/{total}, осталось {left} {nope}", vars)
	want := "Mikan · Вася {brand} · Стандарт до 06.10.2026 (2 дн.) · 5 ГБ/60 ГБ, осталось 55 ГБ {nope}"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	free := Values(User{Name: "a", Left: -1}, -1, Config{Brand: "M", Lang: "en"}, now)
	if got := fillTitle("{date} {days} {left} {total} {used}", free); got != "∞ ∞ ∞ ∞ 0 B" {
		t.Fatalf("no term, no limit: %q", got)
	}
	expired := Values(User{Expires: now.Add(-time.Hour)}, -1, Config{}, now)
	if expired["days"] != "0" {
		t.Fatalf("an ended term has 0 days left: %q", expired["days"])
	}
	long := fillTitle(strings.Repeat("я", 150)+"{name}", map[string]string{"name": strings.Repeat("ю", 100) + "\n"})
	if n := len([]rune(long)); n != TitleMax || strings.ContainsAny(long, "\n") {
		t.Fatalf("one line of %d letters at most: %d", TitleMax, n)
	}
}

func TestFileName(t *testing.T) {
	for in, want := range map[string]string{
		"Mikan · Вася":   "Mikan · Вася",
		"VPN/RU: 30 дн.": "VPN RU  30 дн.",
		`a\b*c?"d"<e>|`:  "a b c  d  e",
		" \t\n":          "mikan",
		"":               "mikan",
	} {
		if got := fileName(in); got != want {
			t.Errorf("fileName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The page's announcement keeps its lines and the blank line between paragraphs; the apps
// get the same text as one line.
func TestFillAnnounceKeepsLines(t *testing.T) {
	vars := map[string]string{"name": "Анна", "date": "01.11.2026"}
	text := "Привет, {name}!\r\n\nРаботы ночью.\n\tПодписка до {date}.  \n  Ещё строка"
	if got, want := FillAnnounce(text, vars), "Привет, Анна!\n\nРаботы ночью.\n    Подписка до 01.11.2026.\n  Ещё строка"; got != want {
		t.Fatalf("page: %q, want %q", got, want)
	}
	if got := fillTitle(text, vars); strings.Contains(got, "\n") {
		t.Fatalf("the apps get line breaks: %q", got)
	}
	long := strings.Repeat("строка\n", AnnounceLines+5)
	if got := strings.Count(FillAnnounce(long, nil), "\n"); got >= AnnounceLines {
		t.Fatalf("%d line breaks kept", got)
	}
}

// The indent of an announcement's line survives (the page shows it as written); the space
// around the whole text and at the ends of lines does not.
func TestFillAnnounceKeepsIndent(t *testing.T) {
	got := FillAnnounce("\n\n  Заголовок  \n      вложено\n   \n"+strings.Repeat(" ", 100)+"глубоко\n\n", nil)
	want := "  Заголовок\n      вложено\n\n" + strings.Repeat(" ", indentMax) + "глубоко"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestDevicesText(t *testing.T) {
	limit := func(n int64, places bool) User { return User{Devices: ptr(n), Places: places} }
	for _, tc := range []struct {
		name  string
		u     User
		bound int64
		en    bool
		want  string
	}{
		{"bound of a limit", limit(3, true), 2, false, "2/3"},
		{"bound, none yet", limit(3, true), 0, false, "0/3"},
		{"bound without a limit", User{Places: true}, 2, false, "2"},
		{"online limit", limit(3, false), 2, false, "до 3 одновременно"},
		{"online limit, english", limit(3, false), 2, true, "up to 3 at once"},
		{"binding off with a limit", limit(3, true), -1, true, "up to 3 at once"},
		{"binding off, no limit", User{Places: true}, -1, false, "∞"},
		{"online, no limit", User{}, 2, false, "2"},
	} {
		if got := devicesText(tc.u, tc.bound, tc.en); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	if !wantsDevices("a", "x {devices}") || wantsDevices("{days}", "") {
		t.Error("wantsDevices does not tell {devices}")
	}
}

// INCY shows up to five lines of an announcement, so it gets them with their indents; the
// other apps still get one line.
func TestFillAppAnnounce(t *testing.T) {
	text := "Работы ночью\n  с 2 до 4\nстрока 3\nстрока 4\nстрока 5\nстрока 6"
	if got, want := fillAppAnnounce(text, nil, "INCY/2.0.8/iOS"), "Работы ночью\n  с 2 до 4\nстрока 3\nстрока 4\nстрока 5"; got != want {
		t.Errorf("INCY: %q, want %q", got, want)
	}
	if got := fillAppAnnounce(text, nil, "Happ/3.2.1"); strings.Contains(got, "\n") {
		t.Errorf("Happ got lines: %q", got)
	}
	long := strings.Repeat("а", TitleMax+50)
	if got := fillAppAnnounce(long, nil, "INCY/2.0.8"); utf8.RuneCountInString(got) > TitleMax {
		t.Errorf("INCY over TitleMax: %d", utf8.RuneCountInString(got))
	}
}
