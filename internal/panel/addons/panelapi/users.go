package panelapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"mikan/internal/panel/audit"
	"mikan/internal/panel/domain"
	"mikan/internal/panel/events"
	"mikan/internal/panel/store"
	"mikan/internal/panel/store/db"
)

// Subscribers outside the bot. users:read and users:write reach only the accounts the bot is
// talking to (botrpc.go); these two reach every subscriber, so an addon needs them named,
// and the admin is warned of both when approving. users:all reads (a list with search, a
// card); users:grant gives days and traffic, each grant under a key of the addon's own so
// a retry never gives twice.

const (
	maxUsersPage = 100
	defUsersPage = 50
	maxSearch    = 100
	maxReason    = 200 // runes: the grant note's bound
)

// A search reads names in batches until it has a page's worth, but never more than scanCap
// per request: past that it answers what it found with a cursor to go on from, so one
// request's work is bounded however little matches. Variables so a test can make them small.
var (
	scanBatch int32 = 500
	scanCap         = 20000
)

// validGrantKey: what an addon names a grant by, so keys stay short and plain.
var validGrantKey = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

func (s *Server) handleUsers() {
	s.Handle(http.MethodGet, "/users", "users:all", s.usersList)
	s.Handle(http.MethodGet, "/users/{id}", "users:all", s.userGet)
	s.Handle(http.MethodPost, "/users/{id}/grant", "users:grant", s.userGrant)
}

// userBrief is a subscriber in a list. It never carries the subscription link, the note or
// the contact: a link is the user's credential, the rest is the admin's.
type userBrief struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	State      string     `json:"state"` // active, expiring, limited, expired, disabled
	TariffID   *int64     `json:"tariff_id"`
	TariffName string     `json:"tariff_name"`
	ExpiresAt  *time.Time `json:"expires_at"` // null: never
	TgID       *int64     `json:"tg_id"`
}

type trafficView struct {
	Used  int64  `json:"used"`
	Limit *int64 `json:"limit"` // null: unlimited
	Left  *int64 `json:"left"`  // what is left of the limit and the packages; null: unlimited
}

// tgProfileView is the Telegram account a subscription is linked to; its id is the card's tg_id.
type tgProfileView struct {
	Username string `json:"username"`
	Name     string `json:"name"`
}

// userCard is one subscriber in full.
type userCard struct {
	userBrief
	Traffic      trafficView    `json:"traffic"`
	PackagesLeft int64          `json:"packages_left"` // bytes left of the traffic packages of the main limit
	Devices      int64          `json:"devices"`       // devices bound to the subscription
	Telegram     *tgProfileView `json:"telegram"`      // null: not linked
}

// tariffNames reads each tariff's name once per response; an archived or deleted one is "".
type tariffNames struct {
	q     *db.Queries
	names map[int64]string
}

func (t *tariffNames) of(ctx context.Context, id sql.NullInt64) (string, error) {
	if !id.Valid {
		return "", nil
	}
	if n, ok := t.names[id.Int64]; ok {
		return n, nil
	}
	tf, err := t.q.GetTariff(ctx, id.Int64)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if t.names == nil {
		t.names = map[int64]string{}
	}
	t.names[id.Int64] = tf.Name
	return tf.Name, nil
}

func (s *Server) brief(ctx context.Context, u db.User, grants domain.GrantsLeft, tariffs *tariffNames) (userBrief, error) {
	name, err := tariffs.of(ctx, u.TariffID)
	if err != nil {
		return userBrief{}, err
	}
	return userBrief{ID: u.ID, Name: u.Name, State: domain.State(u, grants.Main(u.ID), s.d.Now()), TariffID: store.Int64Ptr(u.TariffID),
		TariffName: name, ExpiresAt: store.TimePtr(u.ExpiresAt)}, nil
}

// usersList pages by id, oldest first, from ?after= (exclusive), so a page never moves
// when subscribers come and go; "next" is the cursor of the following page, null at the end.
// ?q= matches a name (a part of it, any case), an id, or a Telegram id the subscription
// is linked to.
func (s *Server) usersList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	query := r.URL.Query()
	limit, ok := intParam(query.Get("limit"), defUsersPage, 1, maxUsersPage)
	if !ok {
		Fail(w, http.StatusBadRequest, "bad_request", "limit is 1 to 100")
		return
	}
	after, ok := intParam(query.Get("after"), 0, 0, math.MaxInt64)
	if !ok {
		Fail(w, http.StatusBadRequest, "bad_request", "after is a user id")
		return
	}
	needle := strings.TrimSpace(query.Get("q"))
	if utf8.RuneCountInString(needle) > maxSearch {
		Fail(w, http.StatusBadRequest, "bad_request", "q is at most 100 characters")
		return
	}
	match, err := s.userMatcher(ctx, needle)
	if err != nil {
		s.internal(w, "users search", err)
		return
	}
	// Names are read, not rows, and filtered here (the search lowercases as Go does, which
	// PostgreSQL's lower() does not for every locale); only the page's own rows are read in
	// full. Without a search a page is one small read.
	batch := int32(limit + 1)
	if needle != "" {
		batch = scanBatch
	}
	var found []int64
	cursor, scanned, end := after, 0, false
	for int64(len(found)) <= limit && scanned < scanCap && !end {
		rows, err := s.d.Store.Q.ListUserNamesAfter(ctx, db.ListUserNamesAfterParams{After: cursor, Lim: batch})
		if err != nil {
			s.internal(w, "users list", err)
			return
		}
		for _, u := range rows {
			cursor = u.ID
			scanned++
			if match(u.ID, u.Name) {
				found = append(found, u.ID)
				if int64(len(found)) > limit {
					break
				}
			}
		}
		end = len(rows) < int(batch)
	}
	out := struct {
		Users []userBrief `json:"users"`
		Next  *int64      `json:"next"`
	}{Users: make([]userBrief, 0, pageSize(limit, maxUsersPage))}
	switch {
	case int64(len(found)) > limit:
		found = found[:limit]
		out.Next = &found[len(found)-1]
	case !end:
		// The scan was cut short: what matched is here, and the cursor goes on from where it stopped.
		out.Next = &cursor
	}
	var page []db.User
	if len(found) > 0 {
		if page, err = s.d.Store.Q.ListUsersByIDs(ctx, found); err != nil {
			s.internal(w, "users list", err)
			return
		}
	}
	ids := make([]int64, len(page))
	for i, u := range page {
		ids[i] = u.ID
	}
	grants, err := domain.GrantsLeftOf(ctx, s.d.Store.Q, ids, s.d.Now())
	if err != nil {
		s.internal(w, "users grants", err)
		return
	}
	links, err := s.d.Store.Q.ListTgLinksOfUsers(ctx, ids)
	if err != nil {
		s.internal(w, "users links", err)
		return
	}
	tg := make(map[int64]int64, len(links))
	for _, l := range links {
		tg[l.UserID] = l.TgID
	}
	tariffs := &tariffNames{q: s.d.Store.Q}
	for _, u := range page {
		b, err := s.brief(ctx, u, grants, tariffs)
		if err != nil {
			s.internal(w, "users tariff", err)
			return
		}
		if id, ok := tg[u.ID]; ok {
			b.TgID = &id
		}
		out.Users = append(out.Users, b)
	}
	Write(w, http.StatusOK, out)
}

// userMatcher is the filter of a search: everything for an empty one.
func (s *Server) userMatcher(ctx context.Context, needle string) (func(id int64, name string) bool, error) {
	if needle == "" {
		return func(int64, string) bool { return true }, nil
	}
	lower := strings.ToLower(needle)
	n, numeric := int64(0), false
	linked := map[int64]bool{}
	if v, err := strconv.ParseInt(needle, 10, 64); err == nil && v > 0 {
		n, numeric = v, true
		owned, err := s.d.Store.Q.ListTgLinksOf(ctx, v)
		if err != nil {
			return nil, err
		}
		for _, u := range owned {
			linked[u.ID] = true
		}
	}
	return func(id int64, name string) bool {
		return strings.Contains(strings.ToLower(name), lower) || numeric && (id == n || linked[id])
	}, nil
}

func userID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		Fail(w, http.StatusBadRequest, "bad_request", "the id is a user id")
		return 0, false
	}
	return id, true
}

func (s *Server) userGet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := userID(w, r)
	if !ok {
		return
	}
	u, err := s.d.Users.Get(ctx, id)
	if errors.Is(err, domain.ErrNotFound) {
		Fail(w, http.StatusNotFound, "not_found", "no such user")
		return
	}
	if err != nil {
		s.internal(w, "user", err)
		return
	}
	q := s.d.Store.Q
	grants, err := domain.UserGrantsLeft(ctx, q, id, s.d.Now())
	if err != nil {
		s.internal(w, "user grants", err)
		return
	}
	bound, err := q.CountBoundDevices(ctx, id)
	if err != nil {
		s.internal(w, "user devices", err)
		return
	}
	b, err := s.brief(ctx, u, grants, &tariffNames{q: q})
	if err != nil {
		s.internal(w, "user tariff", err)
		return
	}
	card := userCard{userBrief: b, PackagesLeft: grants.Main(id), Devices: bound}
	card.Traffic = trafficView{Used: u.UsedUp + u.UsedDown, Limit: store.Int64Ptr(u.TrafficLimit)}
	if left := domain.TrafficLeft(u.TrafficLimit, card.Traffic.Used, grants.Main(id)); left >= 0 {
		card.Traffic.Left = &left
	}
	switch l, err := q.TgLinkOfUser(ctx, id); {
	case err == nil:
		card.TgID = &l.TgID
		card.Telegram = &tgProfileView{Username: l.Username, Name: l.FirstName}
	case !errors.Is(err, sql.ErrNoRows):
		s.internal(w, "user link", err)
		return
	}
	Write(w, http.StatusOK, card)
}

// grantRequest is the body of a grant. Days and bytes are pointers: an explicit 0 is a
// mistake, not "none". Days is only the extension of the term; how long traffic lasts is
// TrafficDays, and only for lifetime "days", so each can be said on its own.
type grantRequest struct {
	Days        *int64 `json:"days"`
	Bytes       *int64 `json:"bytes"`
	PoolID      *int64 `json:"pool_id"`
	Lifetime    string `json:"lifetime"`
	TrafficDays *int64 `json:"traffic_days"`
	Reason      string `json:"reason"`
	Key         string `json:"key"`
}

// grantResult is what a grant answers, kept under its key: a repeat answers the same,
// with repeat set.
type grantResult struct {
	UserID         int64      `json:"user_id"`
	Days           int64      `json:"days,omitempty"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"` // the end of the term after the days
	Bytes          int64      `json:"bytes,omitempty"`
	PoolID         *int64     `json:"pool_id,omitempty"`
	Lifetime       string     `json:"lifetime,omitempty"`
	GrantID        int64      `json:"grant_id,omitempty"`
	TrafficExpires *time.Time `json:"traffic_expires_at,omitempty"`
	Repeat         bool       `json:"repeat"`
}

// Why a grant stopped inside its transaction, told apart from a failure of the database.
var (
	errKeyOtherUser = errors.New("the key was used for another user")
	errUserDisabled = errors.New("the user is disabled")
)

// userGrant gives days (an extension of the term) and/or traffic. Days turn a disabled
// user on, as the admin's extension does; that is the admin's call, so an addon is refused
// for a disabled user (409 user_disabled). Everything is one transaction: the key, the
// days, the traffic, the audit lines and the event are all there or none is.
func (s *Server) userGrant(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, _ := CallerOf(ctx)
	id, ok := userID(w, r)
	if !ok {
		return
	}
	var in grantRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		Fail(w, http.StatusBadRequest, "bad_request", "the body is JSON: days, bytes, pool_id, lifetime, traffic_days, reason, key")
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	switch {
	case !validGrantKey.MatchString(in.Key):
		Fail(w, http.StatusBadRequest, "bad_request", "key is 1 to 64 of A-Z a-z 0-9 . _ : -")
		return
	case in.Reason == "" || utf8.RuneCountInString(in.Reason) > maxReason:
		Fail(w, http.StatusBadRequest, "bad_request", "reason is 1 to 200 characters")
		return
	case in.Days == nil && in.Bytes == nil:
		Fail(w, http.StatusBadRequest, "bad_request", "days or bytes is needed")
		return
	case in.Bytes == nil && (in.PoolID != nil || in.Lifetime != "" || in.TrafficDays != nil):
		Fail(w, http.StatusBadRequest, "bad_request", "pool_id, lifetime and traffic_days go with bytes")
		return
	case in.PoolID != nil && *in.PoolID < 1:
		Fail(w, http.StatusBadRequest, "bad_request", "pool_id is a pool id")
		return
	case (in.Lifetime == domain.LifetimeDays) != (in.TrafficDays != nil):
		Fail(w, http.StatusBadRequest, "bad_request", "traffic_days goes with lifetime days, and lifetime days needs it; days is only the extension of the term")
		return
	case in.TrafficDays != nil && (*in.TrafficDays < 1 || *in.TrafficDays > domain.MaxGrantDays):
		Fail(w, http.StatusBadRequest, "bad_request", "traffic_days is 1 to 3650")
		return
	}
	// Traffic that does not say how long it lasts lasts until it is used up.
	if in.Bytes != nil && in.Lifetime == "" {
		in.Lifetime = domain.LifetimeUsed
	}
	var days, trafficDays, bytes, pool int64
	if in.Days != nil {
		days = *in.Days
	}
	if in.TrafficDays != nil {
		trafficDays = *in.TrafficDays
	}
	if in.Bytes != nil {
		bytes = *in.Bytes
	}
	if in.PoolID != nil {
		pool = *in.PoolID
	}

	actor := audit.AddonActor(c.ID)
	var res grantResult
	var repeat bool
	err := s.d.Store.Tx(ctx, func(q *db.Queries) error {
		// A retried transaction starts clean.
		res, repeat = grantResult{UserID: id}, false
		u, err := q.GetUser(ctx, id)
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrNotFound
		}
		if err != nil {
			return err
		}
		now := s.d.Now()
		// The key first: a repeat changes nothing, and two requests with one key never both
		// get past it (the second waits for the first's commit, then finds the key).
		n, err := q.ClaimAddonGrantKey(ctx, db.ClaimAddonGrantKeyParams{AddonID: c.ID, Key: in.Key, UserID: id, CreatedAt: now.Unix()})
		if err != nil {
			return err
		}
		if n == 0 {
			first, err := q.GetAddonGrantKey(ctx, db.GetAddonGrantKeyParams{AddonID: c.ID, Key: in.Key})
			if err != nil {
				return err
			}
			if first.UserID != id {
				return errKeyOtherUser
			}
			repeat = true
			return json.Unmarshal([]byte(first.Result), &res)
		}
		if in.Days != nil {
			if u.Status == "disabled" {
				return errUserDisabled
			}
			ext, err := s.d.Users.ExtendOn(ctx, q, id, days)
			if err != nil {
				return err
			}
			res.Days, res.ExpiresAt = days, store.TimePtr(ext.ExpiresAt)
			err = audit.Write(ctx, q, now, audit.Entry{Action: "user.extend", TargetType: "user", TargetID: strconv.FormatInt(id, 10),
				Details: map[string]any{"actor": actor, "days": days, "reason": in.Reason, "key": in.Key}})
			if err != nil {
				return err
			}
		}
		if in.Bytes != nil {
			g, err := s.d.Users.GrantOn(ctx, q, id, domain.GrantInput{PoolID: pool, Bytes: bytes, Lifetime: in.Lifetime, Days: trafficDays, Note: in.Reason, By: actor})
			if err != nil {
				return err
			}
			res.Bytes, res.PoolID, res.Lifetime, res.GrantID, res.TrafficExpires = g.Bytes, in.PoolID, g.Lifetime, g.ID, store.TimePtr(g.ExpiresAt)
			err = audit.Write(ctx, q, now, audit.Entry{Action: "user.grant", TargetType: "user", TargetID: strconv.FormatInt(id, 10),
				Details: map[string]any{"actor": actor, "bytes": g.Bytes, "pool_id": in.PoolID, "lifetime": g.Lifetime, "traffic_days": trafficDays, "reason": in.Reason, "key": in.Key}})
			if err != nil {
				return err
			}
		}
		stored, err := json.Marshal(res)
		if err != nil {
			return err
		}
		if err := q.SetAddonGrantResult(ctx, db.SetAddonGrantResultParams{AddonID: c.ID, Key: in.Key, Result: string(stored)}); err != nil {
			return err
		}
		var tg int64
		switch l, err := q.GetTgLink(ctx, id); {
		case err == nil:
			tg = l.TgID
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		// The reason is the admin's: the feed is read by every addon that holds events, so it carries
		// what was given and to whom (as payments do), not why.
		return events.Emit(ctx, q, now, events.UserGranted, id, tg, events.Granted{Days: days, Bytes: bytes, PoolID: pool, Addon: c.ID}, "user.granted:"+c.ID+":"+in.Key)
	})
	var fe *domain.FieldError
	switch {
	case errors.Is(err, domain.ErrNotFound):
		Fail(w, http.StatusNotFound, "not_found", "no such user")
		return
	case errors.Is(err, errKeyOtherUser):
		Fail(w, http.StatusConflict, "conflict", "the key was used for another user")
		return
	case errors.Is(err, errUserDisabled):
		Fail(w, http.StatusConflict, "user_disabled", "days are not given to a disabled user")
		return
	case errors.As(err, &fe):
		Fail(w, http.StatusBadRequest, "bad_request", fe.Field+": "+fe.Code)
		return
	case err != nil:
		s.internal(w, "grant", err)
		return
	}
	if !repeat {
		s.d.Users.Changed()
		s.d.Hub.Kick()
	}
	res.Repeat = repeat
	Write(w, http.StatusOK, res)
}
