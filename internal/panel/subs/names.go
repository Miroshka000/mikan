package subs

import (
	"context"
	"errors"
	"strings"

	"mikan/internal/panel/settings"
)

// LoadGroups is the subscription's group names as saved, the defaults filled in for the
// panel's language.
func LoadGroups(ctx context.Context, set *settings.Settings) (Groups, error) {
	var g Groups
	var err error
	if g.Main, err = set.String(ctx, settings.KeyGroupMain); err != nil {
		return g, err
	}
	if g.Auto, err = set.String(ctx, settings.KeyGroupAuto); err != nil {
		return g, err
	}
	lang, err := set.Lang(ctx)
	if err != nil {
		return g, err
	}
	return g.WithDefaults(lang), nil
}

// CheckName refuses a name the admin typed for something that shows in Clash apps next to
// the groups (a node's country group, a protocol's connection): the name must pass
// ValidName and not repeat one of the groups, or the profile does not load. The error is a
// code (name_blank, name_is_group…) the UI turns into words.
func (g Groups) CheckName(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	for _, x := range []string{g.Main, g.Auto, AliasGroup} {
		if strings.EqualFold(name, x) {
			return errors.New("name_is_group")
		}
	}
	return nil
}
