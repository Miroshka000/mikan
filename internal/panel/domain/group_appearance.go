package domain

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"mikan/internal/panel/store/db"
	"mikan/internal/sub/apps"
	submodel "mikan/internal/sub/model"
)

// GroupAppearance is how a group's subscriptions look (#290): what is set goes over the
// panel's settings for the group's clients, and a nil is the panel's. The texts and links are
// held to the rules of the settings they stand for by the API, which knows those rules.
type GroupAppearance struct {
	// Templates are the group's template of each core (submodel.Core*): the default of the
	// core for its clients. A response rule that names a template still wins.
	Templates map[submodel.Core]int64
	// Title, Announce, AnnounceURL and SupportURL stand for the subscription's settings of
	// those names; an empty Announce is no announcement for the group.
	Title, Announce, AnnounceURL, SupportURL *string
	// UpdateHours is how often the apps fetch the subscription again (Profile-Update-Interval).
	UpdateHours *int
	// Headers are more headers every answer of the group's subscriptions carries, as a
	// response rule's (apps.CheckHeaders); a rule's own go over them.
	Headers []apps.Header
	// RequireHWID stands for the setting of devices of that name: apps without a device id
	// get no servers. Unbind is how the group's subscribers unbind their devices.
	RequireHWID *bool
	Unbind      *UnbindRules
	// Page: the group has a subscription page of its own (SetGroupPage). It is read with the
	// appearance and written apart from it.
	Page bool
}

// GroupCores are the cores a group may have a template of, in the order they are shown.
var GroupCores = []submodel.Core{submodel.CoreMihomo, submodel.CoreSingBox, submodel.CoreXray}

// ErrGroupTemplate: a group's template of a core is a template of another core, or none.
var ErrGroupTemplate = errors.New("group_template")

// GroupAppearanceOf is the appearance a row holds. A document that does not read (edited by
// hand) is the panel's: the group's subscriptions keep working.
func GroupAppearanceOf(r db.ListGroupAppearanceRow) GroupAppearance {
	a := GroupAppearance{Templates: map[submodel.Core]int64{}, Headers: []apps.Header{}, Page: r.HasPage}
	for core, id := range map[submodel.Core]sql.NullInt64{submodel.CoreMihomo: r.TemplateMihomo, submodel.CoreSingBox: r.TemplateSingbox, submodel.CoreXray: r.TemplateXray} {
		if id.Valid {
			a.Templates[core] = id.Int64
		}
	}
	text := func(s sql.NullString) *string {
		if !s.Valid {
			return nil
		}
		return &s.String
	}
	a.Title, a.Announce, a.AnnounceURL, a.SupportURL = text(r.Title), text(r.Announce), text(r.AnnounceUrl), text(r.SupportUrl)
	if r.UpdateHours.Valid {
		h := int(r.UpdateHours.Int64)
		a.UpdateHours = &h
	}
	if r.RequireHwid.Valid {
		a.RequireHWID = &r.RequireHwid.Bool
	}
	if json.Unmarshal([]byte(r.Headers), &a.Headers) != nil || apps.CheckHeaders("headers", a.Headers) != nil {
		a.Headers = []apps.Header{}
	}
	if r.UnbindRules.Valid {
		var u UnbindRules
		if json.Unmarshal([]byte(r.UnbindRules.String), &u) == nil && u.Validate() == nil {
			a.Unbind = &u
		}
	}
	return a
}

// LoadGroupAppearance is every group's appearance by group id; a group without a row has the
// panel's in full and is not there.
func LoadGroupAppearance(ctx context.Context, q *db.Queries) (map[int64]GroupAppearance, error) {
	rows, err := q.ListGroupAppearance(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]GroupAppearance, len(rows))
	for _, r := range rows {
		out[r.GroupID] = GroupAppearanceOf(r)
	}
	return out, nil
}

// SetGroupAppearance writes group id's appearance but its page on q's transaction, after the
// templates it names: each must be a template of its core. The templates are held still
// meanwhile (LockSubTemplates), as a response rule holds the one it names.
func SetGroupAppearance(ctx context.Context, q *db.Queries, id int64, a GroupAppearance) error {
	if err := q.LockSubTemplates(ctx); err != nil {
		return err
	}
	p := db.SetGroupAppearanceParams{GroupID: id, Headers: "[]"}
	for _, core := range GroupCores {
		t, ok := a.Templates[core]
		if !ok {
			continue
		}
		row, err := q.GetSubTemplate(ctx, t)
		if errors.Is(err, sql.ErrNoRows) || err == nil && row.Core != string(core) {
			return &FieldError{Field: "templates." + string(core), Code: ErrGroupTemplate.Error()}
		}
		if err != nil {
			return err
		}
		v := sql.NullInt64{Int64: t, Valid: true}
		switch core {
		case submodel.CoreMihomo:
			p.TemplateMihomo = v
		case submodel.CoreSingBox:
			p.TemplateSingbox = v
		case submodel.CoreXray:
			p.TemplateXray = v
		}
	}
	text := func(s *string) sql.NullString {
		if s == nil {
			return sql.NullString{}
		}
		return sql.NullString{String: *s, Valid: true}
	}
	p.Title, p.Announce, p.AnnounceUrl, p.SupportUrl = text(a.Title), text(a.Announce), text(a.AnnounceURL), text(a.SupportURL)
	if a.UpdateHours != nil {
		p.UpdateHours = sql.NullInt64{Int64: int64(*a.UpdateHours), Valid: true}
	}
	if a.RequireHWID != nil {
		p.RequireHwid = sql.NullBool{Bool: *a.RequireHWID, Valid: true}
	}
	if len(a.Headers) > 0 {
		raw, err := json.Marshal(a.Headers)
		if err != nil {
			return err
		}
		p.Headers = string(raw)
	}
	if a.Unbind != nil {
		if err := a.Unbind.Validate(); err != nil {
			var fe *FieldError
			if errors.As(err, &fe) {
				return &FieldError{Field: "unbind." + fe.Field, Code: fe.Code}
			}
			return err
		}
		raw, err := json.Marshal(a.Unbind)
		if err != nil {
			return err
		}
		p.UnbindRules = sql.NullString{String: string(raw), Valid: true}
	}
	return q.SetGroupAppearance(ctx, p)
}

// GroupPage is group id's own subscription page as it was saved (JSON of subpage.Page); ok is
// false when the group shows the panel's.
func GroupPage(ctx context.Context, q *db.Queries, id int64) (raw string, ok bool, err error) {
	page, err := q.GetGroupPage(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !page.Valid {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return page.String, true, nil
}

// SetGroupPage gives group id a subscription page of its own (raw: subpage.Page as JSON,
// already checked), or with "" the panel's again.
func SetGroupPage(ctx context.Context, q *db.Queries, id int64, raw string) error {
	return q.SetGroupPage(ctx, db.SetGroupPageParams{GroupID: id, Page: sql.NullString{String: raw, Valid: raw != ""}})
}
