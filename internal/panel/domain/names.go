package domain

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Names people give: a user's (the admin's), a bound device's (the admin's or the
// subscriber's). They go into Telegram messages, the apps' headers and the profile title,
// so they are one line of printable text; every place that shows one escapes it.
const (
	UserNameMax   = 100 // letters, as POST /users takes them
	DeviceNameMax = 40
)

// Validation codes of names (the UI translates errors.api.<code>).
const (
	CodeUserNameEmpty  = "user_name_empty"
	CodeUserNameLong   = "user_name_long"
	CodeNameChars      = "name_chars"
	CodeDeviceNameLong = "device_name_long"
)

// CleanUserName trims a user's name and checks it: not empty, at most UserNameMax
// letters, one line without control characters. The error is a *FieldError on "name".
func CleanUserName(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", fieldErr("name", CodeUserNameEmpty)
	case !utf8.ValidString(s) || !printableLine(s):
		return "", fieldErr("name", CodeNameChars)
	case utf8.RuneCountInString(s) > UserNameMax:
		return "", fieldErr("name", CodeUserNameLong)
	}
	return s, nil
}

// CleanDeviceName trims a device's own name and checks it like a user's; empty is allowed
// and takes the device back to the name its app reports.
func CleanDeviceName(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case !utf8.ValidString(s) || !printableLine(s):
		return "", fieldErr("name", CodeNameChars)
	case utf8.RuneCountInString(s) > DeviceNameMax:
		return "", fieldErr("name", CodeDeviceNameLong)
	}
	return s, nil
}

// printableLine: no control characters, no line or paragraph separators, and none of the
// marks that turn the direction of the text around (a name that reads backwards could pass
// for another). The zero-width joiner of emoji stays.
func printableLine(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool {
		switch {
		case unicode.IsControl(r), r == ' ', r == ' ':
			return true
		case r >= '‪' && r <= '‮', r >= '⁦' && r <= '⁩', r == '‎', r == '‏', r == '؜':
			return true
		}
		return false
	})
}
