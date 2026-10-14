package domain

import (
	"context"
	"errors"
	"testing"
	"time"

	"mikan/internal/panel/store/db"
)

// Where a user came from is set by the path that made the user, and nothing else decides
// it; a source the database does not know is refused.
func TestEveryCreationPathSetsTheSource(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	std := tariffs[1]

	plain, err := users.Create(ctx, CreateInput{Name: "plain", TariffID: std.ID})
	if err != nil || plain.Source != UserFromAdmin {
		t.Fatalf("a user with no source named: %q %v", plain.Source, err)
	}
	if _, err := users.Create(ctx, CreateInput{Name: "x", TariffID: std.ID, Source: "friend"}); err == nil {
		t.Fatal("an unknown source was taken")
	}
	// A purchase that makes the user is the bot's; one that renews leaves the source as it was.
	var bought, renewed db.User
	err = st.Tx(ctx, func(q *db.Queries) (err error) {
		bought, _, err = users.Purchase(ctx, q, PurchaseInput{TariffID: std.ID, Name: "buyer"})
		return err
	})
	if err != nil || bought.Source != UserFromBot {
		t.Fatalf("a purchase: %q %v", bought.Source, err)
	}
	err = st.Tx(ctx, func(q *db.Queries) (err error) {
		renewed, _, err = users.Purchase(ctx, q, PurchaseInput{UserID: plain.ID, TariffID: std.ID})
		return err
	})
	if err != nil || renewed.Source != UserFromAdmin {
		t.Fatalf("a renewal: %q %v", renewed.Source, err)
	}
	for _, from := range []string{UserFromTrial, UserFromImport} {
		var u db.User
		err = st.Tx(ctx, func(q *db.Queries) (err error) {
			u, err = users.CreateOn(ctx, q, CreateInput{Name: from, TariffID: std.ID, Source: from}, Patch{})
			return err
		})
		if err != nil || u.Source != from {
			t.Fatalf("%s: %q %v", from, u.Source, err)
		}
		if got, _ := st.Q.GetUser(ctx, u.ID); got.Source != from {
			t.Fatalf("%s is stored as %q", from, got.Source)
		}
	}
}

// A hidden user is a user: the patch and the bulk action change where the row is listed
// and nothing else, a folder that is not there is refused, and users leave a folder with it.
func TestHidingAndFoldersChangeNothingElse(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, ch := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	var ids []int64
	for _, name := range []string{"a", "b", "c"} {
		u, err := users.Create(ctx, CreateInput{Name: name, TariffID: tariffs[1].ID})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, u.ID)
	}
	folder, err := st.Q.CreateFolder(ctx, db.CreateFolderParams{Name: "f", Color: "blue", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := st.Q.GetUser(ctx, ids[0])

	yes := true
	u, err := users.Update(ctx, ids[0], Patch{Hidden: &yes, FolderID: &folder.ID})
	if err != nil || u.Hidden != 1 || u.FolderID.Int64 != folder.ID {
		t.Fatalf("patch: %+v %v", u, err)
	}
	got, _ := st.Q.GetUser(ctx, ids[0])
	if got.Hidden != 1 || got.FolderID.Int64 != folder.ID || got.Status != before.Status || got.ExpiresAt != before.ExpiresAt ||
		got.SlotID != before.SlotID || got.SubToken != before.SubToken || got.TrafficLimit != before.TrafficLimit {
		t.Fatalf("hiding changed more than the listing: %+v, was %+v", got, before)
	}
	// An ordinary patch does not bring a hidden user back or take them out of the folder.
	name := "renamed"
	if u, err := users.Update(ctx, ids[0], Patch{Name: &name}); err != nil || u.Hidden != 1 || u.FolderID.Int64 != folder.ID {
		t.Fatalf("a rename of a hidden user: %+v %v", u, err)
	}
	gone := int64(9999)
	if _, err := users.Update(ctx, ids[0], Patch{FolderID: &gone}); err == nil {
		t.Fatal("a folder that is not there was taken")
	} else {
		var fe *FieldError
		if !errors.As(err, &fe) || fe.Code != "folder_not_found" {
			t.Fatalf("a missing folder: %v", err)
		}
	}
	no := false
	if u, err := users.Update(ctx, ids[0], Patch{Hidden: &no, ClearFolder: true}); err != nil || u.Hidden != 0 || u.FolderID.Valid {
		t.Fatalf("show and take out: %+v %v", u, err)
	}

	// In bulk: all or nothing, and no word to the nodes (the policies are what they were).
	pushes := ch.policies
	if n, err := users.Bulk(ctx, ids, BulkMove, BulkOpt{Folder: &folder.ID}); err != nil || n != 3 {
		t.Fatalf("bulk move: %d %v", n, err)
	}
	if n, err := users.Bulk(ctx, ids[:2], BulkHide, BulkOpt{}); err != nil || n != 2 {
		t.Fatalf("bulk hide: %d %v", n, err)
	}
	if ch.policies != pushes {
		t.Fatal("the nodes were told about users moved and hidden")
	}
	if _, err := users.Bulk(ctx, ids, BulkMove, BulkOpt{Folder: &gone}); err == nil {
		t.Fatal("a bulk move into a folder that is not there")
	}
	count := func(where string) (n int) {
		t.Helper()
		if err := st.DB.QueryRowContext(ctx, "SELECT count(*) FROM users WHERE "+where).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count("hidden = 1") != 2 || count("folder_id IS NOT NULL") != 3 {
		t.Fatalf("hidden %d, in a folder %d", count("hidden = 1"), count("folder_id IS NOT NULL"))
	}
	if n, err := users.Bulk(ctx, ids, BulkMove, BulkOpt{}); err != nil || n != 3 || count("folder_id IS NOT NULL") != 0 {
		t.Fatalf("bulk move out of folders: %d %v", n, err)
	}
	if n, err := users.Bulk(ctx, ids, BulkUnhide, BulkOpt{}); err != nil || n != 3 || count("hidden = 1") != 0 {
		t.Fatalf("bulk unhide: %d %v", n, err)
	}
	// A folder deleted lets its users go.
	if _, err := users.Bulk(ctx, ids, BulkMove, BulkOpt{Folder: &folder.ID}); err != nil {
		t.Fatal(err)
	}
	if n, err := st.Q.DeleteFolder(ctx, folder.ID); err != nil || n != 1 {
		t.Fatalf("delete the folder: %d %v", n, err)
	}
	if count("folder_id IS NOT NULL") != 0 || count("true") != 3 {
		t.Fatalf("users after the folder went: %d in a folder, %d in all", count("folder_id IS NOT NULL"), count("true"))
	}
}
