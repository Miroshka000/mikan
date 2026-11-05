package release

import (
	"slices"
	"testing"
)

// The goal closest to its sum comes first and the done ones last, whatever the order of the
// file; goals with the same share keep it, and so do the done ones among themselves.
func TestGoalsSort(t *testing.T) {
	g := Goals{Items: []Goal{
		{ID: "done-a", Target: 100, Raised: 100, Status: GoalDone},
		{ID: "empty-small", Target: 50, Status: GoalOpen},
		{ID: "a-tenth", Target: 100, Raised: 10, Status: GoalOpen},
		{ID: "more-money-less-share", Target: 1000, Raised: 50, Status: GoalOpen},
		{ID: "also-a-tenth", Target: 200, Raised: 20, Status: GoalOpen},
		{ID: "done-b", Target: 50, Status: GoalDone},
		{ID: "empty-big", Target: 300, Status: GoalOpen},
		{ID: "past-its-sum", Target: 50, Raised: 60, Status: GoalWorking},
	}}
	g.Sort()
	got := make([]string, 0, len(g.Items))
	for _, it := range g.Items {
		got = append(got, it.ID)
	}
	want := []string{"past-its-sum", "a-tenth", "also-a-tenth", "more-money-less-share", "empty-small", "empty-big", "done-a", "done-b"}
	if !slices.Equal(got, want) {
		t.Fatalf("order: %v, want %v", got, want)
	}
}

// A reader shows the goals it can and leaves the rest out; an index without goals, or
// with goals it cannot read at all, still gives its releases.
func TestIndexGoalsLenient(t *testing.T) {
	ix, err := DecodeIndex([]byte(`{"schema":1,"releases":[{"version":"0.5.0.1","channel":"stable","manifest":"https://x/m.json","from":"0.4.5"}],
		"goals":{"donate":"https://web.tribute.tg/d/REA","items":[
			{"id":"ok","title":{"ru":"Цель"},"target":100,"raised":10,"currency":"USD","status":"open"},
			{"id":"ok","title":{"ru":"Дубль"},"target":100,"currency":"USD","status":"open"},
			{"id":"bad status","title":{"ru":"x"},"target":100,"currency":"USD","status":"maybe"},
			{"id":"future","title":{"ru":"x"},"target":100,"currency":"USD","status":"open","new_field":true},
			{"id":"evil","title":{"ru":"x"},"target":100,"currency":"USD","status":"open","url":"javascript:alert(1)"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.Releases) != 1 {
		t.Fatalf("releases: %+v", ix.Releases)
	}
	if len(ix.Goals.Items) != 2 || ix.Goals.Items[0].ID != "ok" || ix.Goals.Items[1].ID != "future" {
		t.Fatalf("goals: %+v", ix.Goals.Items)
	}
	for _, goals := range []string{`"goals":{"donate":"javascript:x","items":[]}`, `"goals":[1,2]`, `"goals":"x"`} {
		ix, err := DecodeIndex([]byte(`{"releases":[{"version":"0.5.0.1","channel":"stable","manifest":"https://x/m.json","from":"0.4.5"}],` + goals + `}`))
		if err != nil || len(ix.Releases) != 1 || len(ix.Goals.Items) != 0 {
			t.Errorf("%s: %v %+v", goals, err, ix)
		}
	}
}
