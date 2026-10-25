package xray

import (
	"testing"

	"mikan/internal/sub/subtest"
)

// The share links, which do not depend on the mode: one list for each user (subtest has
// the golden files' story).
func TestGoldenURIs(t *testing.T) {
	for _, v := range subtest.Variants {
		t.Run(v.Name, func(t *testing.T) {
			links, _, err := Links(v.User().Proxies())
			if err != nil {
				t.Fatal(err)
			}
			subtest.Golden(t, v.Name+".uri.txt", []byte(links+"\n"))
		})
	}
}
