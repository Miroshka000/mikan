package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"mikan/internal/panel/addons"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Third-party addon sources: the admin adds the address of an author's file, catalog or
// GitHub repository, and its addons install from the marketplace as unofficial ones. What is
// fetched, signed and checked is in internal/panel/addons/sources.go; this is the admin's side
// of it. Only a session may add or remove a source: it decides what code can be offered to
// run on the server.

type SourceSkipView struct {
	ID     string `json:"id" doc:"Идентификатор записи, как его написал источник"`
	Reason string `json:"reason" doc:"id_taken — идентификатор из подписанного каталога или другого источника; payment — платёжный адаптер из источника не ставится; invalid, image_invalid, digest_invalid, version_invalid, id_invalid — запись не по правилам; min_panel — нужна более новая панель; duplicate, too_many"`
}

type SourceView struct {
	ID             int64            `json:"id"`
	URL            string           `json:"url"`
	Title          string           `json:"title"`
	Signed         bool             `json:"signed" doc:"Ключ источника закреплён: подпись с другим ключом или без подписи панель не принимает"`
	Fingerprint    string           `json:"fingerprint,omitempty" doc:"Отпечаток закреплённого ключа, для сверки с автором"`
	NewFingerprint string           `json:"new_fingerprint,omitempty" doc:"Источник подписан уже другим ключом (error source_key_changed): отпечаток нового, который админ может принять"`
	AddedAt        time.Time        `json:"added_at"`
	FetchedAt      *time.Time       `json:"fetched_at,omitempty"`
	Error          string           `json:"error,omitempty" doc:"Код ошибки последнего чтения: source_unreachable, source_key_changed, source_signature_invalid…"`
	Entries        int              `json:"entries" doc:"Сколько записей источник предлагает: сами записи в /marketplace"`
	Skipped        []SourceSkipView `json:"skipped"`
	Installed      []string         `json:"installed" doc:"Аддоны, поставленные из этого источника: пока они есть, источник не удалить"`
}

type SourcesView struct {
	Sources      []SourceView `json:"sources"`
	CatalogError string       `json:"catalog_error,omitempty" doc:"catalog_unavailable — без подписанного каталога записи источников не сверить, их не показываем"`
	Limit        int          `json:"limit" doc:"Сколько источников можно добавить"`
}

type sourcesOutput struct{ Body SourcesView }

type addSourceInput struct {
	Body struct {
		URL string `json:"url" minLength:"1" maxLength:"512" doc:"https://…/mikan-addon.json, https://…/index.json или github.com/<владелец>/<репозиторий> (последний релиз)"`
		Key string `json:"key,omitempty" maxLength:"64" doc:"Публичный ключ автора, ed25519 в base64, если админ знает его заранее: источник принимается, только если подписан им"`
	}
}

type sourceIDInput struct {
	ID int64 `path:"id" minimum:"1"`
}

type refreshSourceInput struct {
	ID     int64  `path:"id" minimum:"1"`
	Accept string `query:"accept" maxLength:"64" pattern:"^[0-9a-f:]*$" doc:"Отпечаток нового ключа, который админ сверил с автором (после source_key_changed)"`
}

func (h *handlers) registerAddonSources() {
	op := sessionOps("addons")
	huma.Register(h.api, op("list-addon-sources", http.MethodGet, "/api/v1/addon-sources", "Источники сторонних аддонов", 0), h.listSources)
	huma.Register(h.api, op("add-addon-source", http.MethodPost, "/api/v1/addon-sources", "Добавить источник: панель читает его, проверяет и закрепляет ключ подписи", http.StatusCreated), h.addSource)
	huma.Register(h.api, op("refresh-addon-source", http.MethodPost, "/api/v1/addon-sources/{id}/refresh", "Прочитать источник заново", 0), h.refreshSource)
	huma.Register(h.api, op("delete-addon-source", http.MethodDelete, "/api/v1/addon-sources/{id}", "Убрать источник: пока из него стоит аддон, нельзя", 0), h.removeSource)
}

// sourceBoard is what the sources hold, judged against the signed catalog and what is
// installed: whose each addon id is, and which addons came from which source.
type sourceBoard struct {
	list    []db.AddonSource
	states  map[int64]addons.SourceState
	origins map[string]int64 // addon id → source, for addons installed or asked for
	owners  map[string]int64 // addon id → source (0: the panel's own); nil without the catalog
}

// offered is the source's entries when it may offer them (see SourceState.Offered).
func (b sourceBoard) offered(id int64) (addons.SourceDoc, bool) {
	state, ok := b.states[id]
	return state.Doc, ok && state.Offered()
}

// sourceBoard reads the sources, refreshes the stale ones (refresh) in parallel, and works out
// who owns which id: an id goes to the first source to list it, and that claim is stored. Without
// the signed catalog (nil) no id can be judged: no entry is offered.
func (h *handlers) sourceBoard(ctx context.Context, st addons.State, official *addons.Catalog, refresh bool) (sourceBoard, error) {
	q := h.d.Store.Q
	b := sourceBoard{states: map[int64]addons.SourceState{}, origins: map[string]int64{}}
	var err error
	if b.list, err = q.ListAddonSources(ctx); err != nil {
		return b, err
	}
	rows, err := q.ListAddonOrigins(ctx)
	if err != nil {
		return b, err
	}
	// A row of an addon that is not there (a request the host failed) claims nothing.
	asked := map[string]bool{}
	for _, r := range h.d.Addons.Pending() {
		asked[r.ID] = r.Action == "install"
	}
	for _, r := range rows {
		if _, there := st.Adapters[r.AddonID]; there || asked[r.AddonID] {
			b.origins[r.AddonID] = r.SourceID
		}
	}
	if refresh {
		states := make([]addons.SourceState, len(b.list))
		var wg sync.WaitGroup
		for i, s := range b.list {
			wg.Add(1)
			go func() {
				defer wg.Done()
				states[i] = h.d.Addons.RefreshSource(ctx, q, s, false, "")
			}()
		}
		wg.Wait()
		for i, s := range b.list {
			// A source not read yet (its fetch is still under way) has no state to judge by.
			if !states[i].At.IsZero() {
				b.states[s.ID] = states[i]
			}
		}
	} else {
		for _, s := range b.list {
			if state, ok := h.d.Addons.Source(s.ID); ok {
				b.states[s.ID] = state
			}
		}
	}
	if official == nil {
		return b, nil
	}
	taken := map[string]bool{}
	for _, e := range official.Adapters {
		taken[e.ID] = true
	}
	for id := range st.Adapters {
		if _, fromSource := b.origins[id]; !fromSource {
			taken[id] = true
		}
	}
	var srcs []addons.SourceEntries
	for _, s := range b.list {
		if doc, ok := b.offered(s.ID); ok {
			srcs = append(srcs, addons.SourceEntries{ID: s.ID, Entries: doc.Entries})
		}
	}
	claims, err := h.claimIDs(ctx, b, taken, srcs)
	if err != nil {
		return b, err
	}
	b.owners = addons.Owners(taken, b.origins, claims)
	return b, nil
}

// claimIDs keeps the claims on ids: a source that lists an id nobody holds takes it, and keeps
// it while it lists it (a source that cannot be read for now keeps what it had). The claims are
// stored, so neither a restart nor a source added later changes who holds an id.
func (h *handlers) claimIDs(ctx context.Context, b sourceBoard, taken map[string]bool, srcs []addons.SourceEntries) (map[string]int64, error) {
	q := h.d.Store.Q
	rows, err := q.ListAddonClaims(ctx)
	if err != nil {
		return nil, err
	}
	claims := map[string]int64{}
	changed := false
	for _, r := range rows {
		claims[r.AddonID] = r.SourceID
	}
	listed := map[int64]map[string]bool{}
	for _, s := range srcs {
		listed[s.ID] = map[string]bool{}
		for _, e := range s.Entries {
			listed[s.ID][e.ID] = true
		}
	}
	// A source read in full that no longer lists the id lets it go.
	for id, src := range claims {
		if state, ok := b.states[src]; ok && state.Err == nil && !listed[src][id] {
			if err := q.ReleaseAddonClaim(ctx, db.ReleaseAddonClaimParams{AddonID: id, SourceID: src}); err != nil {
				return nil, err
			}
			delete(claims, id)
			changed = true
		}
	}
	for _, s := range slices.SortedFunc(slices.Values(srcs), func(a, b addons.SourceEntries) int { return int(a.ID - b.ID) }) {
		for _, e := range s.Entries {
			if _, held := claims[e.ID]; held || taken[e.ID] {
				continue
			}
			if _, installed := b.origins[e.ID]; installed {
				continue
			}
			if err := q.ClaimAddonID(ctx, db.ClaimAddonIDParams{AddonID: e.ID, SourceID: s.ID}); err != nil {
				return nil, err
			}
			claims[e.ID] = s.ID
			changed = true
		}
	}
	if !changed {
		return claims, nil
	}
	// Another request may have claimed an id in between: what is stored decides.
	if rows, err = q.ListAddonClaims(ctx); err != nil {
		return nil, err
	}
	claims = map[string]int64{}
	for _, r := range rows {
		claims[r.AddonID] = r.SourceID
	}
	return claims, nil
}

func (b sourceBoard) source(id int64) (db.AddonSource, bool) {
	i := slices.IndexFunc(b.list, func(s db.AddonSource) bool { return s.ID == id })
	if i < 0 {
		return db.AddonSource{}, false
	}
	return b.list[i], true
}

// originOf is the source an installed addon came from.
func (b sourceBoard) originOf(addon string) (db.AddonSource, bool) {
	id, ok := b.origins[addon]
	if !ok {
		return db.AddonSource{}, false
	}
	return b.source(id)
}

func (b sourceBoard) mark(s db.AddonSource) *MarketOrigin {
	return &MarketOrigin{ID: s.ID, Title: s.Title, Signed: s.Key != ""}
}

// entriesOf is what the source offers: its entries under ids it holds.
func (b sourceBoard) entriesOf(s db.AddonSource) []addons.Entry {
	doc, ok := b.offered(s.ID)
	if !ok || b.owners == nil {
		return nil
	}
	var out []addons.Entry
	for _, e := range doc.Entries {
		if b.owners[e.ID] == s.ID {
			out = append(out, e)
		}
	}
	return out
}

// sourceErr is how a failed read of a source answers an install.
func sourceErr(err error) error {
	code := addons.SourceCode(err)
	if errors.Is(err, addons.ErrSourceKeyChanged) || errors.Is(err, addons.ErrSourceSignature) || errors.Is(err, addons.ErrSourceKeyNeeded) {
		return huma.Error409Conflict(code)
	}
	return huma.Error502BadGateway(code)
}

// installSourceEntry is the entry of the source that holds id, read again now: what the admin
// approves is what the source says at this moment, not what a page showed a while ago.
func (h *handlers) installSourceEntry(ctx context.Context, st addons.State, c addons.Catalog, id string) (addons.Entry, *db.AddonSource, error) {
	board, err := h.sourceBoard(ctx, st, &c, true)
	if err != nil {
		return addons.Entry{}, nil, err
	}
	owner := board.owners[id]
	src, ok := board.source(owner)
	if owner == 0 || !ok {
		return addons.Entry{}, nil, huma.Error404NotFound("addon_unknown")
	}
	state := h.d.Addons.RefreshSource(ctx, h.d.Store.Q, src, true, "")
	if state.Err != nil {
		return addons.Entry{}, nil, sourceErr(state.Err)
	}
	i := slices.IndexFunc(state.Doc.Entries, func(e addons.Entry) bool { return e.ID == id })
	if i < 0 {
		return addons.Entry{}, nil, huma.Error404NotFound("addon_unknown")
	}
	e := state.Doc.Entries[i]
	// An addon already under this id is this source's own, or it is not ours to replace.
	if a, have := st.Adapters[id]; have {
		if a.SourceOf() != addons.SourceCustom || board.origins[id] != src.ID {
			return addons.Entry{}, nil, huma.Error409Conflict("addon_id_taken")
		}
		if a.Status == "running" && a.Digest == e.Digest {
			return addons.Entry{}, nil, huma.Error409Conflict("addon_current")
		}
	}
	return e, &src, nil
}

func (h *handlers) sourcesView(ctx context.Context, st addons.State, c *addons.Catalog, refresh bool) (SourcesView, error) {
	v := SourcesView{Sources: []SourceView{}, Limit: addons.MaxSources}
	if c == nil {
		v.CatalogError = "catalog_unavailable"
	}
	board, err := h.sourceBoard(ctx, st, c, refresh)
	if err != nil {
		return v, err
	}
	for _, s := range board.list {
		v.Sources = append(v.Sources, sourceView(board, s))
	}
	return v, nil
}

func sourceView(b sourceBoard, s db.AddonSource) SourceView {
	sv := SourceView{ID: s.ID, URL: s.Url, Title: s.Title, Signed: s.Key != "", Fingerprint: addons.Fingerprint(s.Key), AddedAt: time.Unix(s.AddedAt, 0).UTC(),
		Error: s.Error, Skipped: []SourceSkipView{}, Installed: []string{}}
	if s.FetchedAt > 0 {
		t := time.Unix(s.FetchedAt, 0).UTC()
		sv.FetchedAt = &t
	}
	state, fetched := b.states[s.ID]
	if fetched {
		// What this process just saw is fresher than the row.
		sv.Error = ""
		if state.Err != nil {
			sv.Error = addons.SourceCode(state.Err)
			if errors.Is(state.Err, addons.ErrSourceKeyChanged) && state.Doc.Key != "" {
				sv.NewFingerprint = addons.Fingerprint(state.Doc.Key)
			}
		}
		for _, sk := range state.Doc.Skipped {
			sv.Skipped = append(sv.Skipped, SourceSkipView{ID: sk.ID, Reason: sk.Reason})
		}
	}
	for id, src := range b.origins {
		if src == s.ID {
			sv.Installed = append(sv.Installed, id)
		}
	}
	slices.Sort(sv.Installed)
	entries := b.entriesOf(s)
	sv.Entries = len(entries)
	listed := map[string]bool{}
	for _, e := range entries {
		listed[e.ID] = true
	}
	// What the source lists under an id another holds is not offered, and says so.
	if fetched && state.Offered() && b.owners != nil {
		for _, e := range state.Doc.Entries {
			if !listed[e.ID] {
				sv.Skipped = append(sv.Skipped, SourceSkipView{ID: e.ID, Reason: "id_taken"})
			}
		}
	}
	return sv
}

func (h *handlers) listSources(ctx context.Context, _ *struct{}) (*sourcesOutput, error) {
	st, err := h.addonState()
	if err != nil {
		return nil, err
	}
	v, err := h.sourcesView(ctx, st, h.officialCatalog(ctx), true)
	if err != nil {
		return nil, err
	}
	return &sourcesOutput{Body: v}, nil
}

// addSource reads the source before it keeps it: what is not a source, is not signed as the
// admin expects, or lists an id the catalog or another source holds, is refused with nothing
// stored. A signed source pins its key now; the admin sees the fingerprint to compare.
func (h *handlers) addSource(ctx context.Context, in *addSourceInput) (*sourcesOutput, error) {
	// The operation is session only; this is the same refusal again, should its mark be lost.
	if err := requireSession(ctx, "url"); err != nil {
		return nil, err
	}
	st, err := h.addonState()
	if err != nil {
		return nil, err
	}
	m := h.d.Addons
	canonical, err := m.CanonicalSourceURL(in.Body.URL)
	if err != nil {
		return nil, fieldErr("body.url", addons.SourceCode(err))
	}
	pin := ""
	if k := strings.TrimSpace(in.Body.Key); k != "" {
		pub, ok := addons.ParseSourceKey(k)
		if !ok {
			return nil, fieldErr("body.key", "source_key_invalid")
		}
		pin = base64.StdEncoding.EncodeToString(pub)
	}
	q := h.d.Store.Q
	if n, err := q.CountAddonSources(ctx); err != nil {
		return nil, err
	} else if n >= addons.MaxSources {
		return nil, huma.Error409Conflict("source_limit")
	}
	c, err := h.catalog(ctx)
	if err != nil {
		return nil, huma.Error502BadGateway("catalog_unavailable")
	}
	// The sources held already are read first (a panel just started knows none of them): an id
	// one of them holds is not for the new one.
	board, err := h.sourceBoard(ctx, st, &c, true)
	if err != nil {
		return nil, err
	}
	if slices.ContainsFunc(board.list, func(s db.AddonSource) bool { return s.Url == canonical }) {
		return nil, fieldErr("body.url", "source_exists")
	}
	doc, err := m.FetchSource(ctx, canonical, addons.SourcePin{Key: pin})
	switch {
	case errors.Is(err, addons.ErrSourceKeyChanged):
		return nil, fieldErr("body.key", addons.SourceCode(err))
	case errors.Is(err, addons.ErrSourceEmpty):
		return nil, fieldErrValue("body.url", addons.SourceCode(err), doc.Skipped)
	case err != nil:
		return nil, fieldErr("body.url", addons.SourceCode(err))
	}
	// An id already taken is refused before anything is kept: the admin names the source
	// that has the better claim to take it up with its author.
	var taken []string
	for _, e := range doc.Entries {
		if _, held := board.owners[e.ID]; held {
			taken = append(taken, e.ID)
		}
	}
	if len(taken) > 0 {
		return nil, fieldErrValue("body.url", "addon_id_taken", taken)
	}
	now := h.d.Now().Unix()
	ids := make([]string, 0, len(doc.Entries))
	for _, e := range doc.Entries {
		ids = append(ids, e.ID)
	}
	var row db.AddonSource
	// One add at a time: the limit and the claims on ids are judged on what the other adds left.
	err = h.d.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.LockAddonSources(ctx); err != nil {
			return err
		}
		if n, err := q.CountAddonSources(ctx); err != nil {
			return err
		} else if n >= addons.MaxSources {
			return huma.Error409Conflict("source_limit")
		}
		// The same address added at the same moment: the one that got here first stays.
		if rows, err := q.ListAddonSources(ctx); err != nil {
			return err
		} else if slices.ContainsFunc(rows, func(s db.AddonSource) bool { return s.Url == canonical }) {
			return fieldErr("body.url", "source_exists")
		}
		claims, err := q.ListAddonClaims(ctx)
		if err != nil {
			return err
		}
		var held []string
		for _, c := range claims {
			if slices.Contains(ids, c.AddonID) {
				held = append(held, c.AddonID)
			}
		}
		if len(held) > 0 {
			return fieldErrValue("body.url", "addon_id_taken", held)
		}
		if row, err = q.AddAddonSource(ctx, db.AddAddonSourceParams{Url: canonical, Key: doc.Key, Title: doc.Title,
			AddedBy: sql.NullInt64{Int64: sessionOf(ctx).AdminID, Valid: true}, AddedAt: now, FetchedAt: now}); err != nil {
			return err
		}
		for _, id := range ids {
			if err := q.ClaimAddonID(ctx, db.ClaimAddonIDParams{AddonID: id, SourceID: row.ID}); err != nil {
				return err
			}
		}
		return nil
	})
	if store.IsUnique(err) {
		return nil, fieldErr("body.url", "source_exists")
	}
	if err != nil {
		return nil, err
	}
	m.SetSource(row.ID, doc)
	h.audit(ctx, sessionOf(ctx).AdminID, "addon.source.add", "addon_source", strconv.FormatInt(row.ID, 10),
		map[string]any{"url": canonical, "title": doc.Title, "signed": doc.Key != "", "fingerprint": addons.Fingerprint(doc.Key), "entries": ids})
	v, err := h.sourcesView(ctx, st, &c, false)
	if err != nil {
		return nil, err
	}
	return &sourcesOutput{Body: v}, nil
}

// refreshSource reads the source again now. A failed read is not an error of this request: it
// is the source's state (the view carries its code, as the table does). A new key is taken
// only with accept, the fingerprint the admin looked at.
func (h *handlers) refreshSource(ctx context.Context, in *refreshSourceInput) (*sourcesOutput, error) {
	if err := requireSession(ctx, "id"); err != nil {
		return nil, err
	}
	st, err := h.addonState()
	if err != nil {
		return nil, err
	}
	q := h.d.Store.Q
	src, err := q.GetAddonSource(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("source_unknown")
	}
	if err != nil {
		return nil, err
	}
	state := h.d.Addons.RefreshSource(ctx, q, src, true, in.Accept)
	if in.Accept != "" && state.Err == nil && state.Doc.Key != src.Key {
		h.audit(ctx, sessionOf(ctx).AdminID, "addon.source.key", "addon_source", strconv.FormatInt(src.ID, 10),
			map[string]any{"url": src.Url, "from": addons.Fingerprint(src.Key), "to": addons.Fingerprint(state.Doc.Key)})
	}
	v, err := h.sourcesView(ctx, st, h.officialCatalog(ctx), false)
	if err != nil {
		return nil, err
	}
	return &sourcesOutput{Body: v}, nil
}

// removeSource: the addons that came from the source keep working only while it stays, so a
// source with one installed (or asked for) is not removed: the admin removes the addon first.
func (h *handlers) removeSource(ctx context.Context, in *sourceIDInput) (*sourcesOutput, error) {
	if err := requireSession(ctx, "id"); err != nil {
		return nil, err
	}
	st, err := h.addonState()
	if err != nil {
		return nil, err
	}
	q := h.d.Store.Q
	src, err := q.GetAddonSource(ctx, in.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, huma.Error404NotFound("source_unknown")
	}
	if err != nil {
		return nil, err
	}
	board, err := h.sourceBoard(ctx, st, nil, false)
	if err != nil {
		return nil, err
	}
	for id, s := range board.origins {
		if s == src.ID {
			return nil, huma.Error409Conflict("source_in_use", &huma.ErrorDetail{Location: "path.id", Message: "source_in_use", Value: id})
		}
	}
	// Rows of addons that never got there go with it.
	if err := h.d.Store.Tx(ctx, func(q *db.Queries) error {
		if err := q.DeleteAddonOriginsOf(ctx, src.ID); err != nil {
			return err
		}
		return q.DeleteAddonSource(ctx, src.ID)
	}); err != nil {
		return nil, err
	}
	h.d.Addons.ForgetSource(src.ID)
	h.audit(ctx, sessionOf(ctx).AdminID, "addon.source.remove", "addon_source", strconv.FormatInt(src.ID, 10), map[string]any{"url": src.Url, "title": src.Title})
	v, err := h.sourcesView(ctx, st, h.officialCatalog(ctx), false)
	if err != nil {
		return nil, err
	}
	return &sourcesOutput{Body: v}, nil
}
