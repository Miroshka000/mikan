package apps

import (
	"errors"
	"testing"
)

// More headers every answer carries are held to a rule's, and none tells a cache to keep it.
func TestCheckHeaders(t *testing.T) {
	for _, c := range []struct {
		hs   []Header
		path string
		code string
	}{
		{[]Header{{Name: "X-Group", Value: "vip"}}, "", ""},
		{[]Header{{Name: "X-Group", Value: "vip"}, {Name: "cache-control", Value: "max-age=60"}}, "headers.1.name", "resp_header_cache"},
		{[]Header{{Name: "Content-Length", Value: "1"}}, "headers.0.name", "resp_header_name"},
		{[]Header{{Name: "X-Bad", Value: "a\nb"}}, "headers.0.value", "resp_header_value"},
		{make([]Header, MaxHeaders+1), "headers", "resp_headers_many"},
	} {
		err := CheckHeaders("headers", c.hs)
		var ce *CheckError
		switch {
		case c.code == "" && err != nil:
			t.Errorf("%+v: %v", c.hs, err)
		case c.code != "" && (!errors.As(err, &ce) || ce.Path != c.path || ce.Code != c.code):
			t.Errorf("%+v: %v, want %s at %s", c.hs, err, c.code, c.path)
		}
	}
}
