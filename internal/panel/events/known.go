package events

import "slices"

// Types lists every event type the panel emits, in the order of Docs: what a page that lets
// the admin pick events offers.
func Types() []string {
	out := make([]string, len(Docs))
	for i, d := range Docs {
		out[i] = d.Type
	}
	return out
}

// Known says whether typ is an event type the panel emits. A name that is not one matches no
// event, so whoever accepts a choice of types checks it here.
func Known(typ string) bool {
	return slices.ContainsFunc(Docs, func(d Doc) bool { return d.Type == typ })
}
