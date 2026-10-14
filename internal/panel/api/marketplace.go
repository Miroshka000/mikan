package api

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/addons/panelapi"
	"mikan/internal/panel/settings"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// The marketplace of addons of protocol 2: bots, pages and the rest (the payment adapters
// have addons.go). Installing one is approving what it may do through the panel's API for
// addons: the admin sees the permissions and approves exactly them, and an update asking
// for more waits for a new approval. Only a session may do it. The entries come from the
// signed catalog and from the third-party sources the admin added (addon_sources.go); the
// latter are marked unofficial everywhere.

// MarketOrigin is the source an unofficial entry or addon comes from.
type MarketOrigin struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Signed bool   `json:"signed" doc:"Источник подписан закреплённым ключом; без подписи тот, кто управляет адресом, управляет и обновлениями"`
}

type MarketEntry struct {
	ID          string            `json:"id"`
	Name        map[string]string `json:"name" doc:"Название по языкам"`
	Description map[string]string `json:"description"`
	Version     string            `json:"version"`
	Kind        string            `json:"kind" enum:"bot,ui,generic"`
	Mode        string            `json:"mode" enum:"http,client"`
	Permissions []string          `json:"permissions" doc:"Что аддон просит: это и одобряет админ при установке"`
	Homepage    string            `json:"homepage"`
	Installed   bool              `json:"installed"`
	Update      bool              `json:"update" doc:"Установлен, а в каталоге или источнике другая сборка"`
	Wider       bool              `json:"wider" doc:"Сборка просит больше прав, чем одобрено: обновление ждёт нового одобрения"`
	Origin      *MarketOrigin     `json:"origin,omitempty" doc:"Источник неофициального аддона; нет у аддона подписанного каталога"`
	Image       string            `json:"image,omitempty" doc:"Образ записи источника: админ видит, что именно будет запущено на сервере; у записи подписанного каталога пусто"`
	Digest      string            `json:"digest,omitempty" doc:"Дайджест образа записи источника"`
}

type MarketInstalled struct {
	ID      string        `json:"id"`
	Version string        `json:"version" doc:"Одобренная версия у аддона из источника; пусто у аддона админа без источника: версию знает только каталог"`
	Kind    string        `json:"kind" enum:"bot,ui,generic"`
	Mode    string        `json:"mode" enum:"http,client"`
	Source  string        `json:"source" enum:"official,custom,external" doc:"official — из подписанного каталога; custom — образ из источника или, у старых установок, свой образ админа; external — свой процесс админа; mikan неофициальные не проверял"`
	Origin  *MarketOrigin `json:"origin,omitempty" doc:"Источник, из которого поставлен аддон (custom)"`
	Image   string        `json:"image,omitempty" doc:"Образ неофициального аддона (custom)"`
	Digest  string        `json:"digest,omitempty" doc:"Дайджест образа неофициального аддона (custom)"`
	URL     string        `json:"url,omitempty" doc:"Адрес внешнего аддона (external); пусто у клиента панели"`
	MemMB   int           `json:"mem_mb,omitempty" doc:"Память контейнера, МБ; 0 — по умолчанию 128"`
	Status  string        `json:"status" enum:"running"`
	Granted []string      `json:"granted" doc:"Одобренные права"`
}

type MarketView struct {
	Catalog      []MarketEntry     `json:"catalog" doc:"Подписанный каталог и записи источников, которые админ добавил"`
	CatalogError string            `json:"catalog_error,omitempty" doc:"catalog_unavailable — каталог не загрузился"`
	Installed    []MarketInstalled `json:"installed"`
	Queue        []AddonRequest    `json:"queue,omitempty" doc:"Все заявки, которые сервер ещё не взял, по порядку"`
	Last         *AddonResult      `json:"last,omitempty" doc:"Как сервер выполнил последнюю заявку"`
	Supported    bool              `json:"supported" doc:"Панель видит каталог данных сервера; иначе ставить аддоны нельзя"`
	HostReady    bool              `json:"host_ready" doc:"mikan на сервере умеет аддоны протокола 2; иначе сначала обновить сервер"`
}

type marketOutput struct{ Body MarketView }

// marketExternalInput is the developer mode: a process of the admin's own on a loopback port.
type marketExternalInput struct {
	Body struct {
		ID          string   `json:"id" maxLength:"32" doc:"Имя аддона: a-z, 0-9 и дефис, не из каталога и не из источника"`
		URL         string   `json:"url,omitempty" maxLength:"64" doc:"http://127.0.0.1:<порт>; клиенту панели (mode client) можно не указывать"`
		Kind        string   `json:"kind" enum:"bot,ui,generic"`
		Mode        string   `json:"mode" enum:"http,client" doc:"http — аддон отвечает панели на /v2/*; client — только сам ходит в панель"`
		Permissions []string `json:"permissions" maxItems:"16" doc:"Права, которые админ даёт аддону"`
		Confirm     bool     `json:"confirm" doc:"Админ подтвердил, что доверяет этому коду: mikan его не проверял"`
	}
}

type marketInstallInput struct {
	AddonIDInput
	Body struct {
		Permissions []string `json:"permissions" doc:"Права, которые админ видел и одобрил: ровно те, что просит сборка"`
	}
}

func (h *handlers) registerMarketplace() {
	op := sessionOps("addons")
	huma.Register(h.api, op("list-marketplace", http.MethodGet, "/api/v1/marketplace", "Маркетплейс аддонов: боты, страницы и прочее", 0), h.listMarketplace)
	huma.Register(h.api, op("install-marketplace", http.MethodPost, "/api/v1/marketplace/{id}/install", "Одобрить права и установить или обновить аддон каталога или источника: заявка серверу", http.StatusAccepted), h.installMarketplace)
	huma.Register(h.api, op("install-external-addon", http.MethodPost, "/api/v1/marketplace/external", "Режим разработчика: подключить свой процесс на 127.0.0.1, без проверки mikan", http.StatusAccepted), h.installExternal)
	huma.Register(h.api, op("remove-marketplace", http.MethodPost, "/api/v1/marketplace/{id}/remove", "Удалить аддон и отозвать его права: заявка серверу", http.StatusAccepted), h.removeMarketplace)
}

// granted is the addon's approved permissions; nil when it has no grant.
func (h *handlers) granted(ctx context.Context, id string) ([]string, error) {
	perms, _, err := h.grant(ctx, id)
	return perms, err
}

// grant is the addon's approved permissions and the version they were approved for; nil and
// "" when it has no grant.
func (h *handlers) grant(ctx context.Context, id string) (perms []string, version string, err error) {
	perms, version, err = addons.LoadGrant(ctx, h.d.Store.Q, id)
	if errors.Is(err, addons.ErrNoGrant) {
		return nil, "", nil
	}
	return perms, version, err
}

// marketEntry is an entry as the marketplace lists it: a running addon of this very source
// (nil for the official catalog) is installed, and one with another build is an update.
func marketEntry(e addons.Entry, origin *MarketOrigin, a addons.Installed, here bool, granted []string) MarketEntry {
	me := MarketEntry{ID: e.ID, Name: e.Name, Description: e.Description, Version: e.Version, Kind: e.Kind, Mode: e.Mode,
		Permissions: listOrEmpty(e.Permissions), Homepage: e.Homepage, Installed: here, Update: here && a.Digest != e.Digest,
		Wider: here && !addons.Covers(granted, e.Permissions), Origin: origin}
	if origin != nil {
		me.Image, me.Digest = e.Image, e.Digest
	}
	return me
}

func (h *handlers) marketView(ctx context.Context) (MarketView, error) {
	v := MarketView{Catalog: []MarketEntry{}, Installed: []MarketInstalled{}}
	m := h.d.Addons
	if m == nil {
		return v, nil
	}
	st, err := m.State()
	if err != nil {
		return v, err
	}
	v.Supported, v.HostReady = m.Supported(), st.HostVersion != ""
	v.Queue, v.Last = requestSummary(m, st)
	installed := map[string]addons.Installed{}
	for id, a := range st.Adapters {
		if a.KindOf() != addons.KindPayment {
			installed[id] = a
		}
	}
	official := h.officialCatalog(ctx)
	if official == nil {
		v.CatalogError = "catalog_unavailable"
	}
	board, err := h.sourceBoard(ctx, st, official, true)
	if err != nil {
		return v, err
	}
	grants := map[string][]string{}
	for _, id := range slices.Sorted(maps.Keys(installed)) {
		a := installed[id]
		perms, version, err := h.grant(ctx, id)
		if err != nil {
			return v, err
		}
		grants[id] = perms
		mi := MarketInstalled{ID: id, Version: a.Version, Kind: a.KindOf(), Mode: a.ModeOf(), Source: a.SourceOf(), MemMB: a.MemMB, Status: a.Status, Granted: listOrEmpty(perms)}
		// Where an addon of the admin's own comes from is all there is to tell it by: nothing
		// signed names it.
		switch mi.Source {
		case addons.SourceCustom:
			mi.Image, mi.Digest = a.Image, a.Digest
			if s, ok := board.originOf(id); ok {
				mi.Origin, mi.Version = board.mark(s), version
			}
		case addons.SourceExternal:
			if a.Listen != "" {
				mi.URL = "http://" + a.Listen
			}
		}
		v.Installed = append(v.Installed, mi)
	}
	if official != nil {
		for _, e := range official.Adapters {
			if e.Kind == addons.KindPayment {
				continue
			}
			a, ok := installed[e.ID]
			// An addon of the admin's own under an id the catalog took later is not this one.
			ok = ok && a.SourceOf() == addons.SourceOfficial
			v.Catalog = append(v.Catalog, marketEntry(e, nil, a, ok, grants[e.ID]))
		}
		// Sources are judged against the catalog, so they show only while it is there.
		for _, s := range board.list {
			for _, e := range board.entriesOf(s) {
				a, ok := installed[e.ID]
				here := ok && a.SourceOf() == addons.SourceCustom && board.origins[e.ID] == s.ID
				v.Catalog = append(v.Catalog, marketEntry(e, board.mark(s), a, here, grants[e.ID]))
			}
		}
	}
	return v, nil
}

func listOrEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (h *handlers) listMarketplace(ctx context.Context, _ *struct{}) (*marketOutput, error) {
	v, err := h.marketView(ctx)
	if err != nil {
		return nil, err
	}
	return &marketOutput{Body: v}, nil
}

// installMarketplace records the approval first and then asks the host: an addon never runs
// with permissions the admin has not seen. The entry is the signed catalog's, or the one a
// source holds under that id; a source's addon runs as an image by its digest on the bridge
// network, as every unofficial addon does.
func (h *handlers) installMarketplace(ctx context.Context, in *marketInstallInput) (*marketOutput, error) {
	st, err := h.addonHost()
	if err != nil {
		return nil, err
	}
	c, err := h.catalog(ctx)
	if err != nil {
		return nil, huma.Error502BadGateway("catalog_unavailable")
	}
	var (
		e   addons.Entry
		src *db.AddonSource
	)
	if slices.ContainsFunc(c.Adapters, func(x addons.Entry) bool { return x.ID == in.ID }) {
		e, err = h.installEntry(ctx, st, in.ID, false)
	} else {
		e, src, err = h.installSourceEntry(ctx, st, c, in.ID)
	}
	if err != nil {
		return nil, err
	}
	// The admin approves what they were shown; an entry that changed in between is shown
	// again rather than approved blindly.
	if !addons.SamePermissions(in.Body.Permissions, e.Permissions) {
		return nil, huma.Error409Conflict("permissions_changed")
	}
	// A permission the host does not know yet would make it drop the addon: mikan update first.
	if !addons.HostTakes(st.HostVersion, e.Permissions) {
		return nil, huma.Error409Conflict("host_outdated")
	}
	if queued(h.d.Addons, e.ID) {
		return nil, huma.Error409Conflict("addon_busy")
	}
	admin := sessionOf(ctx).AdminID
	req := addons.Request{Action: "install", ID: e.ID}
	details := map[string]any{"version": e.Version, "digest": e.Digest, "permissions": e.Permissions}
	if src != nil {
		req = addons.Request{Action: "install", ID: e.ID, Source: addons.SourceCustom, Image: e.Image, Digest: e.Digest,
			Kind: e.Kind, Mode: e.Mode, Permissions: e.Permissions, MemMB: e.Resources.MemMB}
		details["source"], details["source_id"], details["source_url"], details["image"] = addons.SourceCustom, src.ID, src.Url, e.Image
	}
	if err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.UpsertAddonGrant(ctx, db.UpsertAddonGrantParams{AddonID: e.ID, Version: e.Version,
			Permissions: addons.EncodePermissions(e.Permissions), ApprovedBy: sql.NullInt64{Int64: admin, Valid: true}, ApprovedAt: h.d.Now().Unix()}); err != nil {
			return err
		}
		if src != nil {
			return q.UpsertAddonOrigin(ctx, db.UpsertAddonOriginParams{AddonID: e.ID, SourceID: src.ID})
		}
		// An addon of the catalog has no source: a row left by an install that never
		// happened must not make this one a source's.
		return q.DeleteAddonOrigin(ctx, e.ID)
	}); err != nil {
		// The source was removed while the install was on its way.
		if store.IsForeignKey(err) {
			return nil, huma.Error404NotFound("source_unknown")
		}
		return nil, err
	}
	if err := h.ask(ctx, req, details); err != nil {
		return nil, err
	}
	// A bot from the catalog is the admin asking for a bot again: the official one is handed
	// the bot once it runs, as on a fresh panel. A bot from a source is not the official one.
	if e.Kind == addons.KindBot && src == nil {
		if err := settings.Set(ctx, h.d.Settings, panelapi.AutoBotKey, true); err != nil {
			return nil, err
		}
	}
	return h.listMarketplace(ctx, nil)
}

// externalGrantVersion is the grant's version of a process of the admin's own: no build
// stands behind it.
const externalGrantVersion = "external"

// installExternal is the developer mode: a process of the admin's own on a loopback port, no
// container. Only a session may, and only with the admin's word that they trust the code.
// Its id may not be one the catalog or a source has, so it never passes for another addon
// (the switch to the bot addon looks for "telegram").
func (h *handlers) installExternal(ctx context.Context, in *marketExternalInput) (*marketOutput, error) {
	// The operation is session only; this is the same refusal again, should its mark be lost.
	if err := requireSession(ctx, "source"); err != nil {
		return nil, err
	}
	b := in.Body
	r := addons.Request{Action: "install", ID: b.ID, Source: addons.SourceExternal, URL: b.URL, Kind: b.Kind, Mode: b.Mode, Permissions: b.Permissions}
	var bad []error
	if !addons.ValidID(b.ID) {
		bad = append(bad, fieldDetail("body.id", "addon_id_invalid", nil))
	}
	if !addons.ValidURL(b.URL) && (b.URL != "" || b.Mode != addons.ModeClient) {
		bad = append(bad, fieldDetail("body.url", "addon_url_invalid", nil))
	}
	seen := map[string]bool{}
	for _, p := range b.Permissions {
		if seen[p] || !slices.Contains(addons.Permissions, p) {
			bad = append(bad, fieldDetail("body.permissions", "addon_permissions_invalid", p))
			break
		}
		seen[p] = true
	}
	if !b.Confirm {
		bad = append(bad, fieldDetail("body.confirm", "addon_not_confirmed", nil))
	}
	if len(bad) > 0 {
		return nil, fieldErrs(bad...)
	}
	if !r.Valid() {
		// What the fields above let through and the host would still refuse.
		return nil, huma.Error422UnprocessableEntity("addon_request_invalid")
	}
	st, err := h.addonHost()
	if err != nil {
		return nil, err
	}
	if !addons.HostTakes(st.HostVersion, b.Permissions) {
		return nil, huma.Error409Conflict("host_outdated")
	}
	// Without the catalog the id cannot be checked against it: refused rather than guessed.
	c, err := h.catalog(ctx)
	if err != nil {
		return nil, huma.Error502BadGateway("catalog_unavailable")
	}
	if slices.ContainsFunc(c.Adapters, func(e addons.Entry) bool { return e.ID == b.ID }) {
		return nil, fieldErr("body.id", "addon_id_taken")
	}
	// Another program under this id (a payment adapter, an addon of a source) goes first: an
	// install over it would take over its tokens.
	if a, ok := st.Adapters[b.ID]; ok && a.SourceOf() != addons.SourceExternal {
		return nil, fieldErr("body.id", "addon_id_taken")
	}
	board, err := h.sourceBoard(ctx, st, &c, false)
	if err != nil {
		return nil, err
	}
	if _, held := board.owners[b.ID]; held {
		return nil, fieldErr("body.id", "addon_id_taken")
	}
	if queued(h.d.Addons, b.ID) {
		return nil, huma.Error409Conflict("addon_busy")
	}
	admin := sessionOf(ctx).AdminID
	if err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.UpsertAddonGrant(ctx, db.UpsertAddonGrantParams{AddonID: b.ID, Version: externalGrantVersion,
			Permissions: addons.EncodePermissions(b.Permissions), ApprovedBy: sql.NullInt64{Int64: admin, Valid: true}, ApprovedAt: h.d.Now().Unix()}); err != nil {
			return err
		}
		// A row of an install from a source that never happened must not claim this one.
		return q.DeleteAddonOrigin(ctx, b.ID)
	}); err != nil {
		return nil, err
	}
	// Where the code comes from and what it may do; nothing here is a secret.
	if err := h.ask(ctx, r, map[string]any{"source": addons.SourceExternal, "url": r.URL, "kind": b.Kind, "mode": b.Mode, "permissions": listOrEmpty(b.Permissions)}); err != nil {
		return nil, err
	}
	return h.listMarketplace(ctx, nil)
}

// removeMarketplace takes the approval back at once: the addon loses the panel's API before
// the host has even stopped it.
func (h *handlers) removeMarketplace(ctx context.Context, in *AddonIDInput) (*marketOutput, error) {
	st, err := h.addonState()
	if err != nil {
		return nil, err
	}
	a, ok := st.Adapters[in.ID]
	if !ok || a.KindOf() == addons.KindPayment {
		return nil, huma.Error404NotFound("addon_not_installed")
	}
	if queued(h.d.Addons, in.ID) {
		return nil, huma.Error409Conflict("addon_busy")
	}
	// The grant goes first, and the addon's store, its place in the feed and its grant keys
	// with it: another addon installed later under the same id starts with nothing of this
	// one's (a grant key of the old one would answer the new one with the old result).
	if err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.DeleteAddonGrant(ctx, in.ID); err != nil {
			return err
		}
		if err := q.DeleteAddonOrigin(ctx, in.ID); err != nil {
			return err
		}
		if err := q.DeleteAddonData(ctx, in.ID); err != nil {
			return err
		}
		if err := q.DeleteAddonCursor(ctx, in.ID); err != nil {
			return err
		}
		if err := q.DeleteAddonGrantKeys(ctx, in.ID); err != nil {
			return err
		}
		if a.KindOf() != addons.KindBot {
			return nil
		}
		// The admin took a bot away: the slot is free, and the panel does not put the
		// official bot back by itself (app/botaddon.go) until the admin asks for a bot again.
		set := settings.New(q)
		if slot, _, err := settings.Get[string](ctx, set, panelapi.SlotKey); err != nil {
			return err
		} else if slot == in.ID {
			if err := settings.Set(ctx, set, panelapi.SlotKey, ""); err != nil {
				return err
			}
		}
		return settings.Set(ctx, set, panelapi.AutoBotKey, false)
	}); err != nil {
		return nil, err
	}
	if err := h.ask(ctx, addons.Request{Action: "remove", ID: in.ID}, nil); err != nil {
		return nil, err
	}
	return h.listMarketplace(ctx, nil)
}

// queued: a request for the addon still waits for the host; the grant is not touched under
// it, so the approval and the request the host takes never disagree.
func queued(m *addons.Manager, id string) bool {
	return slices.ContainsFunc(m.Pending(), func(r addons.Request) bool { return r.ID == id })
}
