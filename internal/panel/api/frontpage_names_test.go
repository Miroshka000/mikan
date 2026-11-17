package api

import (
	"os"
	"regexp"
	"slices"
	"testing"
	"time"

	"mikan/internal/frontpage"
)

// The names of the front pages are written in more than one place: frontpage.Templates and
// frontpage.Custom, the enum of each field of the API that carries one (a node's front_page
// in its view and in its body, the panel's site_template in the settings and in their body),
// and the CHECK of the nodes' column (migration 0068, which took over from 0046). Nothing
// else ties them together, so a new page would be refused by one and taken by another.
func TestFrontPageNamesAreOneList(t *testing.T) {
	want := append([]string{"", frontpage.Custom}, frontpage.Templates...) // "" is no page
	slices.Sort(want)

	_, api, err := New(Deps{Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	var found int
	for name, schema := range api.OpenAPI().Components.Schemas.Map() {
		for _, field := range []string{"front_page", "site_template"} {
			prop := schema.Properties[field]
			if prop == nil {
				continue
			}
			found++
			got := make([]string, len(prop.Enum))
			for i, v := range prop.Enum {
				got[i], _ = v.(string)
			}
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Errorf("%s.%s: the enum is %q, the pages are %q", name, field, got, want)
			}
		}
	}
	if found < 4 {
		t.Errorf("%d fields found, want the node's front_page twice and site_template twice: the scan lost its way", found)
	}

	raw, err := os.ReadFile("../store/postgres/0068_node_front_page_custom.sql")
	if err != nil {
		t.Fatal(err)
	}
	// The first CHECK of the file is the one the migration leaves in place.
	check := regexp.MustCompile(`front_page IN \(([^)]*)\)`).FindSubmatch(raw)
	if check == nil {
		t.Fatal("0068 has no CHECK of front_page")
	}
	var inSQL []string
	for _, m := range regexp.MustCompile(`'([^']*)'`).FindAllSubmatch(check[1], -1) {
		inSQL = append(inSQL, string(m[1]))
	}
	slices.Sort(inSQL)
	if !slices.Equal(inSQL, want) {
		t.Errorf("the CHECK of 0068 takes %q, the pages are %q", inSQL, want)
	}
}
