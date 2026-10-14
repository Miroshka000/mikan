package subs

import (
	"database/sql"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"mikan/internal/panel/store/db"
)

// titleVars are the variables the profile title and the announcement may hold, the same way
// the bot's texts do: {brand} · {name} · until {date}. Any other {word} stays as text.
var titleVars = []string{"brand", "name", "date", "days", "used", "left", "total", "devices"}

func TestTitleValuesHoldTitleVars(t *testing.T) {
	vars := titleValues(db.User{}, 0, -1, Config{}, time.Now())
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
		if got := profileTitle(cfg, titleValues(db.User{Name: tc.name}, 0, -1, cfg, now)); got != tc.want {
			t.Errorf("title %q, name %q: %q, want %q", tc.title, tc.name, got, tc.want)
		}
	}
}

func TestFillTitle(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	u := db.User{Name: "Вася {brand}", UsedUp: 1 << 30, UsedDown: 4 << 30,
		ExpiresAt:    sql.NullInt64{Int64: now.Add(36 * time.Hour).Unix(), Valid: true},
		TrafficLimit: sql.NullInt64{Int64: 50 << 30, Valid: true}}
	vars := titleValues(u, 10<<30, -1, Config{Brand: "Mikan"}, now)
	got := fillTitle("{brand} · {name} · до {date} ({days} дн.) · {used}/{total}, осталось {left} {nope}", vars)
	want := "Mikan · Вася {brand} · до 06.10.2026 (2 дн.) · 5 ГБ/60 ГБ, осталось 55 ГБ {nope}"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	free := titleValues(db.User{Name: "a"}, 0, -1, Config{Brand: "M", Lang: "en"}, now)
	if got := fillTitle("{date} {days} {left} {total} {used}", free); got != "∞ ∞ ∞ ∞ 0 B" {
		t.Fatalf("no term, no limit: %q", got)
	}
	expired := titleValues(db.User{ExpiresAt: sql.NullInt64{Int64: now.Add(-time.Hour).Unix(), Valid: true}}, 0, -1, Config{}, now)
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
	if got, want := fillAnnounce(text, vars), "Привет, Анна!\n\nРаботы ночью.\n    Подписка до 01.11.2026.\n  Ещё строка"; got != want {
		t.Fatalf("page: %q, want %q", got, want)
	}
	if got := fillTitle(text, vars); strings.Contains(got, "\n") {
		t.Fatalf("the apps get line breaks: %q", got)
	}
	long := strings.Repeat("строка\n", AnnounceLines+5)
	if got := strings.Count(fillAnnounce(long, nil), "\n"); got >= AnnounceLines {
		t.Fatalf("%d line breaks kept", got)
	}
}

// The indent of an announcement's line survives (the page shows it as written); the space
// around the whole text and at the ends of lines does not.
func TestFillAnnounceKeepsIndent(t *testing.T) {
	got := fillAnnounce("\n\n  Заголовок  \n      вложено\n   \n"+strings.Repeat(" ", 100)+"глубоко\n\n", nil)
	want := "  Заголовок\n      вложено\n\n" + strings.Repeat(" ", indentMax) + "глубоко"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
}

func TestDevicesText(t *testing.T) {
	limit := func(n int64, mode string) db.User {
		return db.User{DeviceLimit: sql.NullInt64{Int64: n, Valid: true}, DeviceMode: mode}
	}
	for _, tc := range []struct {
		name  string
		u     db.User
		bound int64
		en    bool
		want  string
	}{
		{"bound of a limit", limit(3, "bound"), 2, false, "2/3"},
		{"bound, none yet", limit(3, "bound"), 0, false, "0/3"},
		{"bound without a limit", db.User{}, 2, false, "2"},
		{"online limit", limit(3, "online"), 2, false, "до 3 одновременно"},
		{"online limit, english", limit(3, "online"), 2, true, "up to 3 at once"},
		{"binding off with a limit", limit(3, "bound"), -1, true, "up to 3 at once"},
		{"binding off, no limit", db.User{}, -1, false, "∞"},
		{"online, no limit", db.User{DeviceMode: "online"}, 2, false, "2"},
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
