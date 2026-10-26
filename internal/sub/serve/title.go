package serve

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"mikan/internal/sub/apps"
	"mikan/internal/textutil"
)

// TitleMax bounds a filled title or announcement: apps show one line.
const TitleMax = 200

// AnnounceMax and AnnounceLines bound the announcement as the page and the Mini App show it,
// with its line breaks; the apps get at most TitleMax letters, one line but for INCY.
const (
	AnnounceMax   = 1000
	AnnounceLines = 10
	indentMax     = 40 // spaces a line of the announcement may start with
)

// msk is the zone dates are written in, the bot's too (textutil.Zone).
var msk = textutil.Zone

// Values are the variables of the title and the announcement for one user, the same way
// the bot's texts have them: {brand} · {name} · until {date}. A term or limit that is not
// set reads "∞". bound is the devices bound to the user, -1 when they were not counted
// (see wantsDevices).
func Values(u User, bound int64, cfg Config, now time.Time) map[string]string {
	en := textutil.Lang(cfg.Lang).En()
	v := map[string]string{"brand": cfg.Brand, "name": u.Name, "date": "∞", "days": "∞",
		"used": textutil.Size(u.UsedUp+u.UsedDown, cfg.Lang), "left": "∞", "total": "∞", "devices": devicesText(u, bound, en), "tariff": u.Tariff}
	if !u.Expires.IsZero() {
		v["date"] = u.Expires.In(msk).Format("02.01.2006")
		days := int64(0)
		if left := u.Expires.Sub(now); left > 0 {
			days = int64((left + 24*time.Hour - 1) / (24 * time.Hour))
		}
		v["days"] = strconv.FormatInt(days, 10)
	}
	if u.Limit != nil && *u.Limit > 0 {
		v["left"] = textutil.Size(u.Left, cfg.Lang)
		v["total"] = textutil.Size(total(u), cfg.Lang)
	}
	return v
}

// wantsDevices says whether a text holds {devices}, the one variable that costs a query.
func wantsDevices(texts ...string) bool {
	for _, s := range texts {
		if strings.Contains(s, "{devices}") {
			return true
		}
	}
	return false
}

// devicesText is {devices}: "2/3" bound devices of the limit, "2" without a limit. A limit
// that is not about places (the online mode, or no binding to count them) reads as the bot
// writes it, "up to 3 at once", and no limit at all "∞".
func devicesText(u User, bound int64, en bool) string {
	switch {
	case u.Devices != nil && (bound < 0 || !u.Places):
		if en {
			return "up to " + strconv.FormatInt(*u.Devices, 10) + " at once"
		}
		return "до " + strconv.FormatInt(*u.Devices, 10) + " одновременно"
	case bound < 0:
		return "∞"
	case u.Devices != nil:
		return strconv.FormatInt(bound, 10) + "/" + strconv.FormatInt(*u.Devices, 10)
	}
	return strconv.FormatInt(bound, 10)
}

// fillTitle puts the user's values in place of the variables, in one pass: a value that
// holds {something} itself is not filled again. The result is one line of at most
// TitleMax letters.
func fillTitle(s string, vars map[string]string) string {
	line := strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' || r == '\t' {
			return ' '
		}
		return r
	}, textutil.Fill(s, vars, nil))
	return cut(line, TitleMax)
}

// FillAnnounce is the announcement as the page and the Mini App show it: filled like a
// title, its lines kept (at most AnnounceLines), blank lines between paragraphs and the
// indent of a line too (a tab is four spaces), at most AnnounceMax letters.
func FillAnnounce(s string, vars map[string]string) string {
	return fillLines(s, vars, AnnounceLines, AnnounceMax)
}

// IncyAnnounceLines: INCY shows an announcement of up to five lines (docs.incy.cc,
// Announce); it gets them, with their indents, within the TitleMax of an app's header.
const IncyAnnounceLines = 5

// fillAppAnnounce is the announcement for the app at hand: INCY's with its lines, any other
// one line, as the apps that do not say they show more would run the lines together.
func fillAppAnnounce(s string, vars map[string]string, ua string) string {
	if apps.IsINCY(ua) {
		return fillLines(s, vars, IncyAnnounceLines, TitleMax)
	}
	return fillTitle(s, vars)
}

// fillLines fills s and keeps at most maxLines lines, with their indents, and maxRunes letters.
func fillLines(s string, vars map[string]string, maxLines, maxRunes int) string {
	lines := strings.Split(strings.ReplaceAll(textutil.Fill(s, vars, nil), "\r\n", "\n"), "\n")
	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	for i, l := range lines {
		lines[i] = indented(strings.ReplaceAll(l, "\t", "    "), maxRunes)
	}
	return strings.TrimRight(strings.TrimLeft(cutText(strings.Join(lines, "\n"), maxRunes), "\n"), " \n")
}

// indented is cut that keeps the spaces a line starts with, up to indentMax.
func indented(s string, n int) string {
	body := cut(s, n)
	if body == "" {
		return ""
	}
	return strings.Repeat(" ", min(len(s)-len(strings.TrimLeft(s, " ")), indentMax)) + body
}

// cutText keeps at most n letters of s, line breaks included.
func cutText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// profileTitle is the name the apps show for the profile: the title filled for the user,
// the brand when there is none or it fills to nothing (just {name} of a user without one).
func profileTitle(cfg Config, vars map[string]string) string {
	if cfg.Title != "" {
		if filled := fillTitle(cfg.Title, vars); strings.TrimSpace(filled) != "" {
			return filled
		}
	}
	return cfg.Brand
}

// fileName makes a profile title a file name: no path separators or characters Windows
// refuses, and never empty.
func fileName(title string) string {
	name := strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r) {
			return ' '
		}
		return r
	}, title))
	if name == "" {
		return "mikan"
	}
	return name
}
