package api

// setOrder is setOrderAt for a list sent as body.ids.
func setOrder(ids, have []int64, code string, set func(id, place int64) error) error {
	return setOrderAt("body.ids", ids, have, code, set)
}

// setOrderAt numbers the rows in the order of ids, from 1. The list must name every row of have
// once, so that a stale page of the admin cannot silently drop or duplicate one; code is what
// the UI is told otherwise, of the field at location. set gives one row its place.
func setOrderAt(location string, ids, have []int64, code string, set func(id, place int64) error) error {
	known := make(map[int64]bool, len(have))
	for _, id := range have {
		known[id] = true
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if !known[id] || seen[id] {
			return fieldErrValue(location, code, id)
		}
		seen[id] = true
	}
	if len(ids) != len(have) {
		return fieldErr(location, code)
	}
	for i, id := range ids {
		if err := set(id, int64(i+1)); err != nil {
			return err
		}
	}
	return nil
}
