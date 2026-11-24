package domain

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"mikan/internal/panel/store/db"
)

// Clients made by a template (#295), one or a batch at once: for the admin who hands access
// out in batches rather than selling it in the bot. A template is what its clients get; the
// panel keeps templates as they are (user_templates) and makes clients of what the form sends.

// MaxBatch is how many clients one batch makes: they are one transaction, and every one of
// them takes a key slot that each node is told of.
const MaxBatch = 500

// maxTakenShown is how many of the names other clients have already a refusal names.
const maxTakenShown = 5

// ClientSpec is what a client made by a template gets: a tariff, and over it the limits and
// the term (nil: the tariff's own; 0: none, unlimited or never ending), whether the term
// waits for the first connection (nil: as the tariff says), the folder and the group (nil:
// none of its own, the tariff's group), the labels and the note.
type ClientSpec struct {
	TariffID        int64
	TrafficLimit    *int64 // bytes a period
	DeviceLimit     *int64
	SpeedLimit      *int64 // Mbit/s each way
	TermDays        *int64
	StartsOnConnect *bool
	FolderID        *int64
	GroupID         *int64
	Tags            []string
	Note            string
}

// overTariff is t with the limits of s over it: what the rows of its clients are written of.
func (s ClientSpec) overTariff(t db.Tariff) db.Tariff {
	over := func(v *int64, dst *sql.NullInt64) {
		switch {
		case v == nil:
		case *v == 0:
			*dst = sql.NullInt64{}
		default:
			*dst = sql.NullInt64{Int64: *v, Valid: true}
		}
	}
	over(s.TrafficLimit, &t.TrafficLimit)
	over(s.DeviceLimit, &t.DeviceLimit)
	over(s.SpeedLimit, &t.SpeedLimit)
	return t
}

// numberMark is where a batch's name pattern puts each client's number: {n}, or {nnn} for a
// number of at least as many digits, zeros in front (007).
var numberMark = regexp.MustCompile(`\{(n+)\}`)

// BatchNames are the names of count clients numbered from start on, made of pattern: each
// mark of the number replaced, the name then checked as CleanUserName checks it. A pattern
// with no mark would give every client the same name.
func BatchNames(pattern string, start int64, count int) ([]string, error) {
	if !numberMark.MatchString(pattern) {
		return nil, fieldErr("name_pattern", "name_pattern_number")
	}
	if count < 1 || count > MaxBatch {
		return nil, fieldErr("count", "batch_count")
	}
	out := make([]string, 0, count)
	for i := range count {
		n := start + int64(i)
		name, err := CleanUserName(numberMark.ReplaceAllStringFunc(pattern, func(m string) string {
			return fmt.Sprintf("%0*d", len(m)-2, n)
		}))
		var fe *FieldError
		if errors.As(err, &fe) {
			// The names are the pattern's: it is what the admin changes.
			return nil, fieldErr("name_pattern", fe.Code)
		}
		if err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, nil
}

// NamesTakenError refuses a batch whose names other clients have already: Names are the
// first of them, in order.
type NamesTakenError struct{ Names []string }

func (e *NamesTakenError) Error() string { return "names taken: " + strings.Join(e.Names, ", ") }

// firstTaken are the first maxTakenShown of names that taken holds, in the batch's order.
func firstTaken(names, taken []string) []string {
	set := make(map[string]bool, len(taken))
	for _, n := range taken {
		set[n] = true
	}
	var out []string
	for _, n := range names {
		if set[n] {
			if out = append(out, n); len(out) == maxTakenShown {
				break
			}
		}
	}
	return out
}

// CreateClient makes a client of spec named name, with contact: the panel's form and POST
// /users. Its name may be another client's, as it always could.
func (s *Users) CreateClient(ctx context.Context, name, contact string, spec ClientSpec) (db.User, error) {
	name, err := CleanUserName(name)
	if err != nil {
		return db.User{}, err
	}
	var u db.User
	err = s.makeUsers(ctx, func(q *db.Queries) error {
		made, err := s.createClients(ctx, q, []string{name}, contact, spec)
		if err == nil {
			u = made[0]
		}
		return err
	})
	return u, err
}

// CreateMany makes a client of spec for each of names (BatchNames), in one transaction: all
// of them or none. None is made under a name another client has (NamesTakenError): a client
// the admin names alone may share one, a batch would only hide its own among the others.
// The slots are taken for all of them at once.
func (s *Users) CreateMany(ctx context.Context, names []string, spec ClientSpec) ([]db.User, error) {
	if err := s.RefillFor(ctx, len(names)); err != nil {
		return nil, err
	}
	var out []db.User
	err := s.makeUsers(ctx, func(q *db.Queries) error {
		// Two batches at once (or a batch and an import) cannot both find a name free.
		if err := q.LockUserNames(ctx, names); err != nil {
			return err
		}
		taken, err := q.TakenUserNames(ctx, names)
		if err != nil {
			return err
		}
		if len(taken) > 0 {
			return &NamesTakenError{Names: firstTaken(names, taken)}
		}
		out, err = s.createClients(ctx, q, names, "", spec)
		return err
	})
	return out, err
}

// createClients makes a client of spec for each of names on q's transaction. The tariff, its
// word on the wait, the folder and the group are read once for all of them; the limits over
// the tariff go into the rows as they are written.
func (s *Users) createClients(ctx context.Context, q *db.Queries, names []string, contact string, spec ClientSpec) ([]db.User, error) {
	if v := spec.SpeedLimit; v != nil && *v != 0 && !ValidSpeedLimit(*v) {
		return nil, ErrBadSpeedLimit
	}
	t, err := liveTariff(ctx, q, spec.TariffID, false)
	if err != nil {
		return nil, err
	}
	if err := lockPlace(ctx, q, spec); err != nil {
		return nil, err
	}
	in, err := clientInput(ctx, q, t, contact, spec)
	if err != nil {
		return nil, err
	}
	own := spec.overTariff(t)
	out := make([]db.User, 0, len(names))
	for _, name := range names {
		in.Name = name
		u, err := s.createOfTariff(ctx, q, in, own)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := s.place(ctx, q, out, spec); err != nil {
		return nil, err
	}
	return out, nil
}

// lockPlace keeps the folder and the group of spec from being deleted until the transaction
// ends, locked before the users' rows as updateOn locks them; one that is not there is refused.
func lockPlace(ctx context.Context, q *db.Queries, spec ClientSpec) error {
	if spec.FolderID != nil {
		if err := LockFolder(ctx, q, *spec.FolderID); err != nil {
			return err
		}
	}
	if spec.GroupID != nil {
		return LockGroups(ctx, q, "group_id", *spec.GroupID)
	}
	return nil
}

// clientInput is what every client of spec on the tariff t is created with but its name: the
// tariff's word on the wait is read here, once for the whole batch.
func clientInput(ctx context.Context, q *db.Queries, t db.Tariff, contact string, spec ClientSpec) (CreateInput, error) {
	in := CreateInput{Contact: contact, Note: spec.Note, Tags: spec.Tags, TariffID: t.ID, StartsOnConnect: spec.StartsOnConnect}
	if spec.TermDays != nil {
		in.TermDays = sql.NullInt64{Int64: *spec.TermDays, Valid: true}
	}
	if in.StartsOnConnect == nil {
		tl, err := LoadTariffLifecycle(ctx, q, t.ID)
		if err != nil {
			return in, err
		}
		in.StartsOnConnect = &tl.StartOnConnect
	}
	return in, nil
}

// place puts the clients made, out, into the folder and the group of spec: one statement for
// all of them each.
func (s *Users) place(ctx context.Context, q *db.Queries, out []db.User, spec ClientSpec) error {
	ids := make([]int64, len(out))
	for i, u := range out {
		ids[i] = u.ID
	}
	if spec.FolderID != nil {
		folder := NullInt(spec.FolderID)
		if err := q.SetUsersFolder(ctx, db.SetUsersFolderParams{FolderID: folder, UpdatedAt: s.now().Unix(), Ids: ids}); err != nil {
			return err
		}
		for i := range out {
			out[i].FolderID = folder
		}
	}
	if spec.GroupID != nil {
		return q.SetUsersGroup(ctx, db.SetUsersGroupParams{Ids: ids, GroupID: *spec.GroupID})
	}
	return nil
}
