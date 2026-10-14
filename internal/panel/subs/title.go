package subs

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
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

// msk is the zone dates are written in, as the bot writes them: most users live there.
var msk = time.FixedZone("MSK", 3*60*60)

// titleValues are the variables for one user. A term or limit that is not set reads "∞".
// bound is the devices bound to the user, -1 when they were not counted (see wantsDevices).
func titleValues(u db.User, grants int64, bound int64, cfg Config, now time.Time) map[string]string {
	en := cfg.Lang == "en"
	v := map[string]string{"brand": cfg.Brand, "name": u.Name, "date": "∞", "days": "∞",
		"used": sizeText(u.UsedUp+u.UsedDown, en), "left": "∞", "total": "∞", "devices": devicesText(u, bound, en)}
	if u.ExpiresAt.Valid {
		exp := time.Unix(u.ExpiresAt.Int64, 0)
		v["date"] = exp.In(msk).Format("02.01.2006")
		days := int64(0)
		if left := exp.Sub(now); left > 0 {
			days = int64((left + 24*time.Hour - 1) / (24 * time.Hour))
		}
		v["days"] = strconv.FormatInt(days, 10)
	}
	if u.TrafficLimit.Valid && u.TrafficLimit.Int64 > 0 {
		v["left"] = sizeText(domain.TrafficLeft(u.TrafficLimit, u.UsedUp+u.UsedDown, grants), en)
		total := u.TrafficLimit.Int64
		if grants > 0 {
			total = max(total, u.UsedUp+u.UsedDown) + grants
		}
		v["total"] = sizeText(total, en)
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
func devicesText(u db.User, bound int64, en bool) string {
	switch {
	case u.DeviceLimit.Valid && (bound < 0 || !domain.BindsDevices(u.DeviceMode)):
		if en {
			return "up to " + strconv.FormatInt(u.DeviceLimit.Int64, 10) + " at once"
		}
		return "до " + strconv.FormatInt(u.DeviceLimit.Int64, 10) + " одновременно"
	case bound < 0:
		return "∞"
	case u.DeviceLimit.Valid:
		return strconv.FormatInt(bound, 10) + "/" + strconv.FormatInt(u.DeviceLimit.Int64, 10)
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
	}, fillVars(s, vars))
	return cut(line, TitleMax)
}

// fillAnnounce is the announcement as the page and the Mini App show it: filled like a
// title, its lines kept (at most AnnounceLines), blank lines between paragraphs and the
// indent of a line too (a tab is four spaces), at most AnnounceMax letters.
func fillAnnounce(s string, vars map[string]string) string {
	return fillLines(s, vars, AnnounceLines, AnnounceMax)
}

// IncyAnnounceLines: INCY shows an announcement of up to five lines (docs.incy.cc,
// Announce); it gets them, with their indents, within the TitleMax of an app's header.
const IncyAnnounceLines = 5

// fillAppAnnounce is the announcement for the app at hand: INCY's with its lines, any other
// one line, as the apps that do not say they show more would run the lines together.
func fillAppAnnounce(s string, vars map[string]string, ua string) string {
	if IsINCY(ua) {
		return fillLines(s, vars, IncyAnnounceLines, TitleMax)
	}
	return fillTitle(s, vars)
}

// fillLines fills s and keeps at most maxLines lines, with their indents, and maxRunes letters.
func fillLines(s string, vars map[string]string, maxLines, maxRunes int) string {
	lines := strings.Split(strings.ReplaceAll(fillVars(s, vars), "\r\n", "\n"), "\n")
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

// fillVars puts the user's values in place of the variables, in one pass.
func fillVars(s string, vars map[string]string) string {
	var out strings.Builder
	rest := s
	for {
		i := strings.IndexByte(rest, '{')
		if i < 0 {
			break
		}
		j := strings.IndexByte(rest[i:], '}')
		if j < 0 {
			break
		}
		if v, ok := vars[rest[i+1:i+j]]; ok {
			out.WriteString(rest[:i])
			out.WriteString(v)
			rest = rest[i+j+1:]
			continue
		}
		out.WriteString(rest[:i+1])
		rest = rest[i+1:]
	}
	out.WriteString(rest)
	return out.String()
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

// sizeText writes bytes the way the bot does: 12,5 ГБ or 12.5 GB.
func sizeText(n int64, en bool) string {
	units, sep := []string{"Б", "КБ", "МБ", "ГБ", "ТБ"}, ","
	if en {
		units, sep = []string{"B", "KB", "MB", "GB", "TB"}, "."
	}
	v, i := float64(max(n, 0)), 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	s := fmt.Sprintf("%.0f", v)
	if i > 0 && v < 100 {
		s = strings.Replace(strings.TrimSuffix(fmt.Sprintf("%.1f", v), ".0"), ".", sep, 1)
	}
	return s + " " + units[i]
}
