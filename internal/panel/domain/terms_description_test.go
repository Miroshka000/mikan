package domain

import (
	"strings"
	"testing"
)

// A tariff's description for buyers is stored trimmed with plain line breaks, and refused
// over 300 characters (not bytes: Russian text is two bytes a letter) or over 5 lines.
func TestCleanDescription(t *testing.T) {
	for name, c := range map[string]struct {
		in, want, code string
	}{
		"empty":        {"", "", ""},
		"blank":        {" \n\t ", "", ""},
		"trimmed":      {"  с белыми списками \n", "с белыми списками", ""},
		"windows":      {"раз\r\nдва\rтри", "раз\nдва\nтри", ""},
		"five lines":   {"1\n2\n3\n4\n5", "1\n2\n3\n4\n5", ""},
		"six lines":    {"1\n2\n3\n4\n5\n6", "", "description_too_many_lines"},
		"blank lines":  {"1\n\n\n\n\n6", "", "description_too_many_lines"},
		"300 letters":  {strings.Repeat("я", 300), strings.Repeat("я", 300), ""},
		"301 letters":  {strings.Repeat("я", 301), "", "description_too_long"},
		"spaces count": {"я" + strings.Repeat(" ", 300) + "я", "", "description_too_long"},
	} {
		got, err := CleanDescription(c.in)
		if c.code == "" && (err != nil || got != c.want) {
			t.Errorf("%s: %q, %v", name, got, err)
		}
		if c.code != "" && codeOf(err) != "description:"+c.code {
			t.Errorf("%s: want %s, got %v", name, c.code, err)
		}
	}
}
