package addons

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"mikan/internal/panel/store/db"
	"mikan/internal/release"
)

// The admin approves the permissions of an addon of protocol 2 as a set: the table
// addon_grants keeps them sorted, as JSON.

// ErrNoGrant: the admin approved nothing for the addon.
var ErrNoGrant = errors.New("addon_no_grant")

// GrantReader is the query LoadGrant needs; *db.Queries has it.
type GrantReader interface {
	GetAddonGrant(ctx context.Context, addonID string) (db.AddonGrant, error)
}

// LoadGrant reads what the admin approved for an addon, and for which version of it. An
// addon with no grant is ErrNoGrant; a grant that cannot be read is an error too, never a
// grant of nothing.
func LoadGrant(ctx context.Context, q GrantReader, id string) (perms []string, version string, err error) {
	g, err := q.GetAddonGrant(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNoGrant
	}
	if err != nil {
		return nil, "", err
	}
	if perms, err = DecodePermissions(g.Permissions); err != nil {
		return nil, "", err
	}
	return perms, g.Version, nil
}

// HasPerm: the permission was approved.
func HasPerm(approved []string, perm string) bool { return slices.Contains(approved, perm) }

// EncodePermissions is the stored form: sorted, without repeats.
func EncodePermissions(perms []string) string {
	s := slices.Clone(perms)
	slices.Sort(s)
	data, _ := json.Marshal(slices.Compact(s))
	return string(data)
}

// DecodePermissions reads what EncodePermissions stored.
func DecodePermissions(stored string) ([]string, error) {
	var perms []string
	err := json.Unmarshal([]byte(stored), &perms)
	return perms, err
}

// Covers: every permission asked for was approved.
func Covers(approved, asked []string) bool {
	for _, p := range asked {
		if !HasPerm(approved, p) {
			return false
		}
	}
	return true
}

// SamePermissions: the two hold the same set.
func SamePermissions(a, b []string) bool {
	return Covers(a, b) && Covers(b, a)
}

// permsSince is the host version that first knows a permission added after protocol 2 shipped:
// an older host drops an entry that asks for one as unsound, so its addon would not run.
var permsSince = map[string]string{"users:all": "0.5.1.0-rc.11", "users:grant": "0.5.1.0-rc.11"}

// HostTakes says whether a host of this version runs an addon with these permissions. A host
// that reports no release version (a development build) is taken to know them all.
func HostTakes(host string, perms []string) bool {
	for _, p := range perms {
		if since, ok := permsSince[p]; ok && release.Valid(host) && !release.AtLeast(host, since) {
			return false
		}
	}
	return true
}
