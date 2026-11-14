package subsrc

import (
	"context"

	"mikan/internal/panel/settings"
	submodel "mikan/internal/sub/model"
)

// LoadGroups is the subscription's group names as saved, the defaults filled in for the
// panel's language.
func LoadGroups(ctx context.Context, set *settings.Settings) (submodel.GroupNames, error) {
	var g submodel.GroupNames
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
