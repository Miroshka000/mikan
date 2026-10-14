package textutil

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClipKeepsWholeCharacters(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"", 5, ""},
		{"abc", 5, "abc"},
		{"abc", 3, "abc"},
		{"abcdef", 3, "abc"},
		{"abc", 0, ""},
		{"abc", -1, ""},
		{"привет", 6, "при"},
		{"привет", 5, "пр"}, // the cut falls inside "и": it goes whole
		{"привет", 1, ""},
		{"a€b", 3, "a"}, // "€" is three bytes
		{"a€b", 4, "a€"},
		{"😀😀", 7, "😀"},
		{"ab\xffcd", 10, "abcd"}, // a byte that is not UTF-8 is dropped even when nothing is cut
		{"ab\xffcd", 3, "ab"},
	} {
		got := Clip(c.in, c.n)
		if got != c.want {
			t.Errorf("Clip(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
		if !utf8.ValidString(got) || len(got) > max(c.n, 0) {
			t.Errorf("Clip(%q, %d) = %q is invalid or too long", c.in, c.n, got)
		}
	}
}

func TestClipEveryCutIsValid(t *testing.T) {
	s := strings.Repeat("aя€😀", 20)
	for n := 0; n <= len(s)+2; n++ {
		got := Clip(s, n)
		if !utf8.ValidString(got) || len(got) > n || !strings.HasPrefix(s, got) {
			t.Fatalf("Clip(s, %d) = %q", n, got)
		}
	}
}

func TestClipRunes(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"", 3, ""},
		{"abc", 3, "abc"},
		{"abcdef", 3, "abc"},
		{"привет", 3, "при"},
		{"привет", 6, "привет"},
		{"привет", 0, ""},
		{"😀😀😀", 2, "😀😀"},
		{"ab\xffcd", 10, "abcd"},
	} {
		if got := ClipRunes(c.in, c.n); got != c.want {
			t.Errorf("ClipRunes(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}
