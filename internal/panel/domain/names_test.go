package domain

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func codeOf(err error) string {
	var fe *FieldError
	if errors.As(err, &fe) {
		return fe.Field + ":" + fe.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

// A user's name is one line of printable text, trimmed, 1–100 letters.
func TestCleanUserName(t *testing.T) {
	for _, c := range []struct{ in, want, code string }{
		{"  Иван  ", "Иван", ""},
		{"Мама 👩‍👧", "Мама 👩‍👧", ""}, // the emoji's zero-width joiner stays
		{"<b>Ivan</b> & co", "<b>Ivan</b> & co", ""},
		{strings.Repeat("я", 100), strings.Repeat("я", 100), ""},
		{"", "", "name:" + CodeUserNameEmpty},
		{" \t ", "", "name:" + CodeUserNameEmpty},
		{strings.Repeat("я", 101), "", "name:" + CodeUserNameLong},
		{"a\nb", "", "name:" + CodeNameChars},
		{"a\x00b", "", "name:" + CodeNameChars},
		{"a b", "", "name:" + CodeNameChars},
		{"abc‮dcba", "", "name:" + CodeNameChars},
		{"a\xffb", "", "name:" + CodeNameChars},
	} {
		got, err := CleanUserName(c.in)
		if codeOf(err) != c.code || got != c.want {
			t.Errorf("%q: %q %v, want %q %s", c.in, got, err, c.want, c.code)
		}
	}
}

// A device's name may be empty (the app's name again) and is at most 40 letters.
func TestCleanDeviceName(t *testing.T) {
	for _, c := range []struct{ in, want, code string }{
		{"", "", ""},
		{"  ", "", ""},
		{" Телефон мамы ", "Телефон мамы", ""},
		{strings.Repeat("x", 40), strings.Repeat("x", 40), ""},
		{strings.Repeat("x", 41), "", "name:" + CodeDeviceNameLong},
		{"a\tb", "", "name:" + CodeNameChars},
		{"⁦x", "", "name:" + CodeNameChars},
	} {
		got, err := CleanDeviceName(c.in)
		if codeOf(err) != c.code || got != c.want {
			t.Errorf("%q: %q %v, want %q %s", c.in, got, err, c.want, c.code)
		}
	}
}

// Renaming a user changes the name only: the link, the slot and the keys stay. A bad name
// changes nothing.
func TestRenameUser(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st, users, _ := setup(t, &now)
	ctx := context.Background()
	tariffs, _ := st.Q.ListTariffs(ctx)
	if _, err := users.Create(ctx, CreateInput{Name: "  ", TariffID: tariffs[1].ID}); codeOf(err) != "name:"+CodeUserNameEmpty {
		t.Fatalf("a blank name at creation: %v", err)
	}
	u, err := users.Create(ctx, CreateInput{Name: " old ", TariffID: tariffs[1].ID})
	if err != nil || u.Name != "old" {
		t.Fatalf("create: %q %v", u.Name, err)
	}
	name := "  Новое имя  "
	r, err := users.Update(ctx, u.ID, Patch{Name: &name})
	if err != nil || r.Name != "Новое имя" || r.SubToken != u.SubToken || r.SlotID != u.SlotID || r.ExpiresAt != u.ExpiresAt {
		t.Fatalf("rename: %+v %v", r, err)
	}
	for _, bad := range []string{"", "a\nb", strings.Repeat("я", 101)} {
		if _, err := users.Update(ctx, u.ID, Patch{Name: &bad}); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
	if got, _ := users.Get(ctx, u.ID); got.Name != "Новое имя" {
		t.Fatalf("a refused name changed the user: %q", got.Name)
	}
}
