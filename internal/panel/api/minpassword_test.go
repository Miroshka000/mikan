package api

import (
	"reflect"
	"strconv"
	"testing"

	"mikan/internal/panel/auth"
)

// The password form's minLength is a literal in a struct tag; the shell holds to the same
// auth.MinPassword, so the two must not drift apart.
func TestPasswordTagIsMinPassword(t *testing.T) {
	body := reflect.TypeOf(passwordInput{}).Field(0).Type
	f, ok := body.FieldByName("New")
	if !ok {
		t.Fatal("no New field in the password form")
	}
	if got := f.Tag.Get("minLength"); got != strconv.Itoa(auth.MinPassword) {
		t.Fatalf("minLength:%q, auth.MinPassword is %d", got, auth.MinPassword)
	}
}
