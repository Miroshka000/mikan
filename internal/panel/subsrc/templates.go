package subsrc

import (
	"context"
	"encoding/json"

	"mikan/internal/panel/store/db"
	"mikan/internal/sub/mihomo"
	submodel "mikan/internal/sub/model"
	"mikan/internal/sub/routing"
	"mikan/internal/sub/serve"
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

// TemplateRouting is a simple template's routing as the panel keeps it (sub_templates.routing)
// and the API gives it: what the settings sub_routing, sub_routes and sub_rules are to the
// built-in profile, for the template's profiles alone.
type TemplateRouting struct {
	Mode   string         `json:"mode" enum:"ru_direct,all,blocked" doc:"Режим, как sub_routing"`
	Routes routing.Routes `json:"routes" doc:"Сервисы, свои списки, приложения мимо туннеля, DNS, группы серверов и тонкая настройка, как sub_routes"`
	Rules  string         `json:"rules" maxLength:"65536" doc:"Свои правила Clash, по одному на строку, как sub_rules; строки с # и пустые пропускаются"`
}

// TemplateRoutingOf is the routing a row keeps. One that does not read (edited by hand) is the
// zero routing, the default mode and nothing else: the template's subscriptions keep working.
func TemplateRoutingOf(raw string) TemplateRouting {
	var r TemplateRouting
	if json.Unmarshal([]byte(raw), &r) != nil {
		return TemplateRouting{}
	}
	return r
}

// Served is r as the profiles are made by it: its rules checked against the groups g
// (routing.ServedRules), a line that no longer parses left out.
func (r TemplateRouting) Served(g submodel.GroupNames) serve.Routing {
	return serve.Routing{Mode: routing.ParseMode(r.Mode), Routes: r.Routes, Rules: routing.ServedRules(r.Rules, g)}
}

// LoadTemplates reads every template with its text, and the routing of each simple one by its
// id, its rules checked against the groups g.
func LoadTemplates(ctx context.Context, q *db.Queries, g submodel.GroupNames) ([]submodel.Template, map[int64]serve.Routing, error) {
	rows, err := q.ListSubTemplates(ctx)
	if err != nil {
		return nil, nil, err
	}
	out := make([]submodel.Template, 0, len(rows))
	routings := map[int64]serve.Routing{}
	for _, r := range rows {
		out = append(out, submodel.Template{ID: r.ID, Core: submodel.Core(r.Core), Body: r.Body, Simple: r.Routing.Valid, Default: r.IsDefault})
		if r.Routing.Valid {
			routings[r.ID] = TemplateRoutingOf(r.Routing.String).Served(g)
		}
	}
	return out, routings, nil
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
	prof, _, err := h.sample(ctx, nil)
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
	// Routing is a simple template's, in place of Body: the core's built-in profile made by it.
	Routing *TemplateRouting
	// Starter asks for the start of a new template in place of a profile: the built-in Clash
	// profile (of Routing, when set) as a template (mihomo.Starter), an example for the JSON
	// cores.
	Starter bool
}

// PreviewTemplate is the profile a user given every inbound gets of the template (its keys
// are placeholders), and, of a Clash one, what sing-box and xray make of it (previewNotes).
func (h *Handler) PreviewTemplate(ctx context.Context, req TemplatePreview) (Preview, error) {
	prof, cfg, err := h.sample(ctx, nil)
	if err != nil {
		return Preview{}, err
	}
	g := cfg.Groups.WithDefaults(cfg.Lang)
	t := submodel.Template{Core: req.Core, Body: req.Body}
	if req.Body == "" || req.Routing != nil {
		t = submodel.Template{}
	}
	if req.Routing != nil {
		// The simple template's routing over the settings', as Serve lays it (serve.Config.Routed).
		cfg.Config = cfg.WithRouting(req.Routing.Served(g))
		prof.Rules, prof.Routes = cfg.Rules, cfg.Routes
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
			out.Notes = previewNotes(prof, g, cfg.Mode, t)
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
