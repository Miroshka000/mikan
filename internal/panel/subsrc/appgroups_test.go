package subsrc

import (
	"database/sql"
	"encoding/json"
	"reflect"
	"testing"

	"mikan/internal/panel/domain"
	"mikan/internal/panel/store/db"
	"mikan/internal/sub/serve"
)

const gib = int64(1) << 30

func ptr(n int64) *int64 { return &n }

// What the source tells of each group: the traffic of a group of its own apart from the
// main traffic, a group apart without a limit or on the main traffic with servers or
// without (the handler names them in the profile), nothing of a group the client cannot
// reach or that the plan leaves out.
func TestAppGroupsOf(t *testing.T) {
	pools := []db.TrafficPool{{ID: 1, Name: "Белые списки"}, {ID: 2, Name: "Игры"}, {ID: 3, Name: "Премиум +"}, {ID: 4, Name: "Закрытая"},
		{ID: 5, Name: "Европа"}, {ID: 6, Name: "Не для него"}, {ID: 7, Name: "Без лимита в тарифе"}, {ID: 8, Name: "Пустая"}}
	rows := []db.UserPool{
		{PoolID: 1, Mode: domain.PoolUnlimited, UsedDown: gib},
		{PoolID: 2, Mode: domain.PoolOwn, TrafficLimit: sql.NullInt64{Int64: 5 * gib, Valid: true}, UsedDown: 5 * gib},
		{PoolID: 3, Mode: domain.PoolOwn, TrafficLimit: sql.NullInt64{Int64: 15 * gib, Valid: true}, UsedUp: gib, UsedDown: 2 * gib},
		{PoolID: 4, Mode: domain.PoolClosed},
		{PoolID: 6, Mode: domain.PoolOwn, TrafficLimit: sql.NullInt64{Int64: gib, Valid: true}},
		{PoolID: 7, Mode: domain.PoolOwn, UsedDown: 10},
		{PoolID: 8, Mode: domain.PoolUnlimited},
	}
	left := domain.GrantsLeft{{9, 3}: 2 * gib, {9, 0}: 50 * gib}
	reach := map[int64]bool{1: true, 2: true, 3: true, 4: true, 5: true, 7: true, 8: true}

	got := appGroupsOf(pools, rows, left, 9, reach)
	want := []serve.AppGroup{
		{ID: 1, Name: "Белые списки", Mode: "unlimited", Used: gib, State: "active"},
		{ID: 2, Name: "Игры", Mode: "own", Used: 5 * gib, Limit: ptr(5 * gib), Left: ptr(0), State: "exhausted"},
		{ID: 3, Name: "Премиум +", Mode: "own", Used: 3 * gib, Limit: ptr(15 * gib), Extra: 2 * gib, Left: ptr(14 * gib), State: "active"},
		{ID: 5, Name: "Европа", Mode: "main", State: "active"},
		// A limit of its own with no number is no limit: the app shows it as unlimited.
		{ID: 7, Name: "Без лимита в тарифе", Mode: "unlimited", Used: 10, State: "active"},
		// With no servers and nothing used: the handler drops it, the source does not know.
		{ID: 8, Name: "Пустая", Mode: "unlimited", State: "active"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("groups:\n got %s\nwant %s", js(t, got), js(t, want))
	}
}

func js(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
