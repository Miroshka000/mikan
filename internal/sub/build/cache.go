package build

import (
	"sync"

	"mikan/internal/proto"
)

// A subscription is fetched by every app every hour or so, and each fetch reads every
// inbound's template: a YAML parse that gives the same answer until the admin edits it.
// The parsed templates are kept by their source text, so an edit simply is a new key.
var parsed struct {
	mu sync.Mutex
	m  map[string]proto.Template
}

// parsedMax bounds the cache: a panel has a few dozen inbounds, and stale texts of edited
// ones fall out when it is cleared.
const parsedMax = 512

// parseTemplate is proto.Parse with the answer kept. The template is a copy, free to be
// changed by the caller.
func parseTemplate(src string) (proto.Template, error) {
	parsed.mu.Lock()
	t, ok := parsed.m[src]
	parsed.mu.Unlock()
	if !ok {
		var err error
		if t, err = proto.Parse(src); err != nil {
			return nil, err
		}
		parsed.mu.Lock()
		if parsed.m == nil || len(parsed.m) >= parsedMax {
			parsed.m = map[string]proto.Template{}
		}
		parsed.m[src] = t
		parsed.mu.Unlock()
	}
	return t.Clone(), nil
}
