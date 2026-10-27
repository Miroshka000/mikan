package singbox

import (
	"slices"
	"strings"
	"testing"

	"mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/subtest"
)

// The preview of an own Clash profile is told what sing-box makes of its groups: a
// load-balance becomes an urltest, the others stay as they are.
func TestNotes(t *testing.T) {
	src := strings.Replace(subtest.OwnTemplate, "  - name: 🚫 Без VPN\n",
		"  - name: ⚖️ Баланс\n    type: load-balance\n    include-all-proxies: true\n    strategy: round-robin\n  - name: 🚫 Без VPN\n", 1)
	if src == subtest.OwnTemplate {
		t.Fatal("the load-balance group is not in the profile")
	}
	prof := profileOf(subtest.Multi())
	want := []model.Note{{Core: "singbox", Group: "⚖️ Баланс", Code: "balance_fastest"}}
	if got := Notes(prof, model.GroupNames{}, routing.All, subtest.Clash(src)); !slices.Equal(got, want) {
		t.Fatalf("notes: %+v", got)
	}
	if got := Notes(prof, model.GroupNames{}, routing.All, subtest.Clash(subtest.OwnTemplate)); len(got) != 0 {
		t.Fatalf("notes of groups sing-box has: %+v", got)
	}
}
