package subsrc

import (
	"context"

	"mikan/internal/panel/store/db"
	"mikan/internal/sub/mihomo"
	submodel "mikan/internal/sub/model"
	"mikan/internal/sub/singbox"
	"mikan/internal/sub/xray"
)

// The subscription templates as the panel keeps them (sub_templates): what the profiles are
// made of (serve.Template picks one for each request), and the check and the preview of one
// for the admin panel, against a user given every inbound.

// A core has at most MaxTemplates templates, each of at most MaxTemplateSize bytes: the
// subscriptions' config holds them all. A full profile with rule lists is a few dozen
// kilobytes.
const (
	MaxTemplates    = 20
	MaxTemplateSize = 512 << 10
)

// LoadTemplates reads every template with its text.
func LoadTemplates(ctx context.Context, q *db.Queries) ([]submodel.Template, error) {
	rows, err := q.ListSubTemplates(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]submodel.Template, 0, len(rows))
	for _, r := range rows {
		out = append(out, submodel.Template{ID: r.ID, Core: submodel.Core(r.Core), Body: r.Body, Default: r.IsDefault})
	}
	return out, nil
}

// TemplateIDs are the ids of the templates there are, for the response rules to name.
func TemplateIDs(ctx context.Context, q *db.Queries) ([]int64, error) {
	rows, err := q.ListSubTemplateHeads(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]int64, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out, nil
}

// CheckTemplate checks a template of core against a user given every inbound: a
// *submodel.TemplateError for what would break the profiles, submodel.ErrNoProxies when
// the panel has no inbound to check it with.
func (h *Handler) CheckTemplate(ctx context.Context, core submodel.Core, src string) error {
	prof, _, err := h.sample(ctx)
	if err != nil {
		return err
	}
	switch core {
	case submodel.CoreMihomo:
		return mihomo.CheckTemplate(prof, src)
	case submodel.CoreSingBox:
		return singbox.CheckTemplate(prof, src)
	case submodel.CoreXray:
		return xray.CheckTemplate(prof, src)
	}
	return submodel.TemplateErr("template_core", string(core))
}

// TemplatePreview is a template the admin panel shows the profile of before it is saved.
type TemplatePreview struct {
	Core submodel.Core
	Body string // "": the core's built-in profile
	// Starter asks for the start of a new template in place of a profile: the built-in Clash
	// profile as a template (mihomo.Starter), an example for the JSON cores.
	Starter bool
}

// PreviewTemplate is the profile a user given every inbound gets of the template (its keys
// are placeholders), and, of a Clash one, what sing-box and xray make of its groups.
func (h *Handler) PreviewTemplate(ctx context.Context, req TemplatePreview) (Preview, error) {
	prof, cfg, err := h.sample(ctx)
	if err != nil {
		return Preview{}, err
	}
	g := cfg.Groups.WithDefaults(cfg.Lang)
	t := submodel.Template{Core: req.Core, Body: req.Body}
	if req.Body == "" {
		t = submodel.Template{}
	}
	var out Preview
	switch req.Core {
	case submodel.CoreMihomo:
		switch {
		case req.Starter:
			out.Profile, err = mihomo.Starter(prof, g, cfg.Mode)
			return out, err
		case t.Body != "":
			out.Profile, _, err = mihomo.Template(prof, t.Body)
		default:
			out.Profile, err = mihomo.Render(prof, g, cfg.Mode)
		}
		if err == nil {
			out.Notes = append(singbox.Notes(prof, g, cfg.Mode, t), xray.Notes(prof, g, cfg.Mode, t)...)
		}
	case submodel.CoreSingBox:
		if req.Starter {
			return Preview{Profile: singbox.Example(g)}, nil
		}
		out.Profile, err = singbox.Render(ctx, prof, g, cfg.Mode, t)
	case submodel.CoreXray:
		if req.Starter {
			return Preview{Profile: xray.Example(g)}, nil
		}
		out.Profile, err = xray.Render(prof, g, cfg.Mode, t)
	default:
		err = submodel.TemplateErr("template_core", string(req.Core))
	}
	if err != nil {
		return Preview{}, err
	}
	return out, nil
}
