package panelapi

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"mikan/internal/panel/settings"
	"mikan/internal/panel/store/db"
)

// The addon's own store (permission kv): addons run without volumes, so what they keep
// lives in the panel's database and its backups. Values are opaque bytes; every write gets
// a new version, which is the ETag a write may be conditioned on (If-Match) and makes a
// compare-and-set; if_absent makes a lock or a dedupe mark (an event handled once).

const (
	MaxValue = 64 << 10 // one value
	MaxTTL   = 366 * 24 * 60 * 60
	maxList  = 1000
)

// validKey: printable and path-like, so keys list and read well.
var validKey = regexp.MustCompile(`^[A-Za-z0-9._:/-]{1,256}$`)

func (s *Server) handleKV() {
	s.Handle(http.MethodGet, "/kv", "kv", s.kvList)
	s.Handle(http.MethodGet, "/kv/{key...}", "kv", s.kvGet)
	s.Handle(http.MethodPut, "/kv/{key...}", "kv", s.kvPut)
	s.Handle(http.MethodDelete, "/kv/{key...}", "kv", s.kvDelete)
}

func etag(version int64) string { return `"` + strconv.FormatInt(version, 10) + `"` }

// ifMatch is the version an If-Match header names; ok is false without the header.
func ifMatch(r *http.Request) (version int64, ok, bad bool) {
	h := strings.TrimSpace(r.Header.Get("If-Match"))
	if h == "" {
		return 0, false, false
	}
	v, err := strconv.ParseInt(strings.Trim(h, `"`), 10, 64)
	return v, true, err != nil
}

func (s *Server) key(w http.ResponseWriter, r *http.Request) (string, bool) {
	k := r.PathValue("key")
	if !validKey.MatchString(k) {
		Fail(w, http.StatusBadRequest, "bad_request", "a key is 1 to 256 of A-Z a-z 0-9 . _ : / -")
		return "", false
	}
	return k, true
}

func (s *Server) kvGet(w http.ResponseWriter, r *http.Request) {
	c, _ := CallerOf(r.Context())
	k, ok := s.key(w, r)
	if !ok {
		return
	}
	row, err := s.d.Store.Q.GetAddonValue(r.Context(), db.GetAddonValueParams{AddonID: c.ID, Key: k, Now: s.d.Now().Unix()})
	if errors.Is(err, sql.ErrNoRows) {
		Fail(w, http.StatusNotFound, "not_found", "no such key")
		return
	}
	if err != nil {
		s.internal(w, "kv get", err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("ETag", etag(row.Version))
	if row.ExpiresAt.Valid {
		w.Header().Set("X-Expires-At", strconv.FormatInt(row.ExpiresAt.Int64, 10))
	}
	_, _ = w.Write(row.Value)
}

var (
	errConflict = errors.New("conflict")
	errQuota    = errors.New("quota")
)

// kvPut writes a value: ?ttl= seconds makes it expire, ?if_absent=1 writes only where no
// live value is, If-Match only over that version.
func (s *Server) kvPut(w http.ResponseWriter, r *http.Request) {
	c, _ := CallerOf(r.Context())
	k, ok := s.key(w, r)
	if !ok {
		return
	}
	value, err := io.ReadAll(io.LimitReader(r.Body, MaxValue+1))
	if err != nil {
		Fail(w, http.StatusBadRequest, "bad_request", "the body could not be read")
		return
	}
	if len(value) > MaxValue {
		Fail(w, http.StatusRequestEntityTooLarge, "too_large", "a value is at most 64 KiB")
		return
	}
	q := r.URL.Query()
	now := s.d.Now().Unix()
	var expires sql.NullInt64
	if t := q.Get("ttl"); t != "" {
		ttl, err := strconv.ParseInt(t, 10, 64)
		if err != nil || ttl < 1 || ttl > MaxTTL {
			Fail(w, http.StatusBadRequest, "bad_request", "ttl is 1 to 31622400 seconds")
			return
		}
		expires = sql.NullInt64{Int64: now + ttl, Valid: true}
	}
	absent := q.Get("if_absent") == "1" || q.Get("if_absent") == "true"
	want, conditional, bad := ifMatch(r)
	if bad {
		Fail(w, http.StatusBadRequest, "bad_request", "If-Match is a version")
		return
	}
	var version int64
	err = s.d.Store.Tx(r.Context(), func(tx *db.Queries) error {
		old, err := tx.LockAddonValue(r.Context(), db.LockAddonValueParams{AddonID: c.ID, Key: k})
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		live := exists && (!old.ExpiresAt.Valid || old.ExpiresAt.Int64 > now)
		if absent && live || conditional && (!live || old.Version != want) {
			return errConflict
		}
		if quota := s.kvQuota(r.Context()); quota > 0 {
			size, err := tx.AddonKVSize(r.Context(), db.AddonKVSizeParams{AddonID: c.ID, Key: k, Now: now})
			if err != nil {
				return err
			}
			if size+int64(len(k)+len(value)) > quota {
				return errQuota
			}
		}
		// The version goes on from an expired value too, so its old ETag never matches again.
		version = old.Version + 1
		return tx.PutAddonValue(r.Context(), db.PutAddonValueParams{AddonID: c.ID, Key: k, Value: value, Version: version, ExpiresAt: expires, UpdatedAt: now})
	})
	switch {
	case errors.Is(err, errConflict):
		Fail(w, http.StatusConflict, "conflict", "the value is not what the condition expects")
	case errors.Is(err, errQuota):
		Fail(w, http.StatusInsufficientStorage, "quota_exceeded", "the addon's store is full")
	case err != nil:
		s.internal(w, "kv put", err)
	default:
		w.Header().Set("ETag", etag(version))
		Write(w, http.StatusOK, map[string]int64{"version": version})
	}
}

// kvDelete removes a value; with If-Match only that version.
func (s *Server) kvDelete(w http.ResponseWriter, r *http.Request) {
	c, _ := CallerOf(r.Context())
	k, ok := s.key(w, r)
	if !ok {
		return
	}
	want, conditional, bad := ifMatch(r)
	if bad {
		Fail(w, http.StatusBadRequest, "bad_request", "If-Match is a version")
		return
	}
	now := s.d.Now().Unix()
	err := s.d.Store.Tx(r.Context(), func(tx *db.Queries) error {
		if conditional {
			old, err := tx.LockAddonValue(r.Context(), db.LockAddonValueParams{AddonID: c.ID, Key: k})
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			if err != nil || old.Version != want || old.ExpiresAt.Valid && old.ExpiresAt.Int64 <= now {
				return errConflict
			}
		}
		_, err := tx.DeleteAddonValue(r.Context(), db.DeleteAddonValueParams{AddonID: c.ID, Key: k})
		return err
	})
	switch {
	case errors.Is(err, errConflict):
		Fail(w, http.StatusConflict, "conflict", "the value is not what the condition expects")
	case err != nil:
		s.internal(w, "kv delete", err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type kvKey struct {
	Key       string `json:"key"`
	Version   int64  `json:"version"`
	ExpiresAt *int64 `json:"expires_at,omitempty"`
}

// kvList lists the live keys in order: ?prefix=, then pages by ?after= with ?limit= (100,
// at most 1000); next is the after of the page that follows, "" at the end.
func (s *Server) kvList(w http.ResponseWriter, r *http.Request) {
	c, _ := CallerOf(r.Context())
	q := r.URL.Query()
	limit, ok := intParam(q.Get("limit"), 100, 1, maxList)
	if !ok {
		Fail(w, http.StatusBadRequest, "bad_request", "limit is 1 to 1000")
		return
	}
	rows, err := s.d.Store.Q.ListAddonKeys(r.Context(), db.ListAddonKeysParams{AddonID: c.ID, Prefix: q.Get("prefix"), After: q.Get("after"),
		Now: s.d.Now().Unix(), Lim: int32(limit + 1)})
	if err != nil {
		s.internal(w, "kv list", err)
		return
	}
	out := struct {
		Keys []kvKey `json:"keys"`
		Next string  `json:"next"`
	}{Keys: []kvKey{}}
	for i, row := range rows {
		if int64(i) == limit {
			out.Next = out.Keys[limit-1].Key
			break
		}
		k := kvKey{Key: row.Key, Version: row.Version}
		if row.ExpiresAt.Valid {
			k.ExpiresAt = &row.ExpiresAt.Int64
		}
		out.Keys = append(out.Keys, k)
	}
	Write(w, http.StatusOK, out)
}

// pruneKV drops what has expired; reads already skip it.
func (s *Server) pruneKV(ctx context.Context) {
	if _, err := s.d.Store.Q.PruneAddonKV(ctx, s.d.Now().Unix()); err != nil && ctx.Err() == nil {
		s.d.Log.Warn("addons api: prune kv", "err", err)
	}
}

func (s *Server) internal(w http.ResponseWriter, what string, err error) {
	s.d.Log.Error("addons api: "+what, "err", err)
	Fail(w, http.StatusInternalServerError, "internal", "the panel could not do it")
}

// KVLimitKey is the setting of how many MiB one addon may keep in kv: absent is
// DefaultKVMiB, 0 no bound at all (the admin's choice, made past a warning: an addon that
// writes without end then fills the server's disk, and big stores bloat every backup).
const KVLimitKey = "addons.kv_limit_mib"

// DefaultKVMiB is far more than a bot keeps, and small enough for the backups.
const DefaultKVMiB = 256

// KVLimitMiB is how many MiB one addon may keep in kv: the admin's setting, DefaultKVMiB
// without one. unlimited is the setting at 0, and then mib is 0. On an error it answers the
// default along with it.
func KVLimitMiB(ctx context.Context, set *settings.Settings) (mib int64, unlimited bool, err error) {
	v, ok, err := settings.Get[int64](ctx, set, KVLimitKey)
	switch {
	case err != nil || !ok || v < 0:
		return DefaultKVMiB, false, err
	case v == 0:
		return 0, true, nil
	}
	return v, false, nil
}

// kvQuota is how many bytes one addon may keep; 0: no bound.
func (s *Server) kvQuota(ctx context.Context) int64 {
	mib, _, _ := KVLimitMiB(ctx, s.d.Settings) // a failed read leaves the default bound
	return mib << 20
}
