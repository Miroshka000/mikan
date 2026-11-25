package domain

import (
	"database/sql"
	"slices"
	"strings"
	"testing"

	"mikan/internal/panel/store/db"
)

// A batch's names are its pattern with each client's number in place of every mark: {n} as it
// is, {nnn} with zeros in front up to three digits. A pattern without a mark, a count out of
// bounds, or a name the panel would refuse is refused at the pattern.
func TestBatchNames(t *testing.T) {
	for _, c := range []struct {
		pattern string
		start   int64
		count   int
		want    []string
		code    string
	}{
		{"client-{n}", 1, 3, []string{"client-1", "client-2", "client-3"}, ""},
		{"school-{nnn}", 9, 2, []string{"school-009", "school-010"}, ""},
		{"{nn} · {n}", 5, 1, []string{"05 · 5"}, ""},
		{"  клиент {n}  ", 0, 1, []string{"клиент 0"}, ""},
		{"{nn}", 99, 2, []string{"99", "100"}, ""},
		{"client", 1, 2, nil, "name_pattern:name_pattern_number"},
		{"client-{x}", 1, 2, nil, "name_pattern:name_pattern_number"},
		{"client-{n}", 1, 0, nil, "count:batch_count"},
		{"client-{n}", 1, MaxBatch + 1, nil, "count:batch_count"},
		{strings.Repeat("я", 99) + "{n}", 9, 2, nil, "name_pattern:" + CodeUserNameLong},
		{"a\n{n}", 1, 1, nil, "name_pattern:" + CodeNameChars},
	} {
		got, err := BatchNames(c.pattern, c.start, c.count)
		if codeOf(err) != c.code || !slices.Equal(got, c.want) {
			t.Errorf("BatchNames(%q, %d, %d) = %q, %q; want %q, %q", c.pattern, c.start, c.count, got, codeOf(err), c.want, c.code)
		}
	}
}

// A template's limit over its tariff: nil keeps the tariff's, 0 takes it away, a number sets
// it; the rest of the tariff stays as it is.
func TestClientSpecOverTariff(t *testing.T) {
	n := func(v int64) *int64 { return &v }
	tariff := db.Tariff{ID: 4, TrafficLimit: sql.NullInt64{Int64: 150 << 30, Valid: true}, DeviceLimit: sql.NullInt64{Int64: 3, Valid: true}, DurationDays: 30}
	got := ClientSpec{TrafficLimit: n(50 << 30), DeviceLimit: n(0)}.overTariff(tariff)
	want := tariff
	want.TrafficLimit, want.DeviceLimit = sql.NullInt64{Int64: 50 << 30, Valid: true}, sql.NullInt64{}
	if got != want {
		t.Errorf("over the tariff: %+v, want %+v", got, want)
	}
	if got := (ClientSpec{}).overTariff(tariff); got != tariff {
		t.Errorf("as the tariff: %+v", got)
	}
}

// A refusal names the first taken names in the batch's order, five at most.
func TestFirstTaken(t *testing.T) {
	names := []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7"}
	if got := firstTaken(names, []string{"a7", "a2"}); !slices.Equal(got, []string{"a2", "a7"}) {
		t.Errorf("two: %q", got)
	}
	if got := firstTaken(names, names); !slices.Equal(got, names[:maxTakenShown]) {
		t.Errorf("all: %q", got)
	}
}
