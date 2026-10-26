package serve

import (
	"errors"
	"strconv"

	"mikan/internal/sub/model"
)

// Template is the own template the profile in format is made of (Config.Templates): the
// one the deciding response rule names (named; 0 for none) when it is written for the
// format's core, else the core's default. sing-box and xray with none of their own are made
// of the Clash template picked the same way, read as they can (mihomo.TemplateParts), as
// they were before they had templates of their own. The zero Template is the built-in
// profile, and so it is for the formats of no core (the links, the page). Which template a
// request gets is decided here and nowhere else.
func Template(ts []model.Template, format string, named int64) model.Template {
	var c model.Core
	switch format {
	case FormatClash:
		c = model.CoreMihomo
	case FormatSingBox:
		c = model.CoreSingBox
	case FormatXray:
		c = model.CoreXray
	default:
		return model.Template{}
	}
	if t, ok := pickTemplate(ts, c, named); ok || c == model.CoreMihomo {
		return t
	}
	t, _ := pickTemplate(ts, model.CoreMihomo, named)
	return t
}

// pickTemplate is the template of core c the rule named, else c's default; false for none.
func pickTemplate(ts []model.Template, c model.Core, named int64) (model.Template, bool) {
	var def model.Template
	found := false
	for _, t := range ts {
		switch {
		case t.Core != c:
		case named != 0 && t.ID == named:
			return t, true
		case t.Default && !found:
			def, found = t, true
		}
	}
	return def, found
}

// render is the profile of renders of the template t, or the built-in one (of the zero
// Template) when t does not render for this user: saved templates are checked, and one that
// went bad for some user must not leave the user's apps without a profile.
func (h *Handler) render(t model.Template, of func(model.Template) ([]byte, error)) ([]byte, error) {
	body, err := of(t)
	var te *model.TemplateError
	if errors.As(err, &te) && t.Body != "" {
		h.Warn("template/"+strconv.FormatInt(t.ID, 10)+"/"+te.Code, "subscription: an own template does not render, the built-in profile goes out",
			"template", t.ID, "core", t.Core, "err", err)
		return of(model.Template{})
	}
	return body, err
}
