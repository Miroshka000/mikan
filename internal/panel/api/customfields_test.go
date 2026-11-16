package api

import "testing"

func TestFieldValue(t *testing.T) {
	for _, c := range []struct {
		kind, in, want string
		ok             bool
	}{
		{"text", "  order 42 ", "order 42", true},
		{"text", "", "", true},
		{"text", "a\nb", "", false},
		{"number", "-2.50", "-2.5", true},
		{"number", "+007.0", "7", true},
		{"number", "-0.00", "0", true},
		{"number", "9007199254740993", "9007199254740993", true},
		{"number", "1e3", "", false},
		{"number", "NaN", "", false},
		{"number", "1.", "", false},
		{"number", "ten", "", false},
		{"bool", "true", "true", true},
		{"bool", "false", "", true},
		{"bool", "yes", "", false},
		{"date", "2026-11-16", "2026-11-16", true},
		{"date", "16.11.2026", "", false},
		{"url", "https://hoster.example/acc/1", "https://hoster.example/acc/1", true},
		{"url", "javascript:alert(1)", "", false},
		{"url", "hoster.example", "", false},
	} {
		got, ok := fieldValue(c.kind, c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("fieldValue(%s, %q) = %q, %v; want %q, %v", c.kind, c.in, got, ok, c.want, c.ok)
		}
	}
}
