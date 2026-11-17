package domain

import (
	"slices"
)

// Who an address for clients is shown to by group (#275): to the clients of some groups
// alone (HostGroupsOnly), to every client but theirs (HostGroupsNever), or, with no mode, to
// every client. A client is of the group it is in (GroupOf); one in no group is of none of
// them.
const (
	HostGroupsOnly  = "only"
	HostGroupsNever = "never"
)

// HostGroupModes are the modes a host may have besides none.
var HostGroupModes = []string{HostGroupsOnly, HostGroupsNever}

// HostGroupsMax bounds the groups one host names.
const HostGroupsMax = 100

// HostShowsGroup says whether a host of mode and groups goes to a client of group (0: none).
// A host "only" for groups that were all deleted goes to nobody.
func HostShowsGroup(mode string, groups []int64, group int64) bool {
	switch mode {
	case HostGroupsOnly:
		return group != 0 && slices.Contains(groups, group)
	case HostGroupsNever:
		return group == 0 || !slices.Contains(groups, group)
	}
	return true
}

// hostGroups are a host's groups as they are saved: in id order, each once, none without a
// mode.
func hostGroups(mode string, groups []int64) []int64 {
	if mode == "" {
		return []int64{}
	}
	out := slices.Clone(groups)
	slices.Sort(out)
	return slices.Compact(out)
}

// checkHostGroups refuses a mode the panel does not know, a mode with no group and too many
// groups; that each group exists is checked where the host is written (LockGroups).
func checkHostGroups(mode string, groups []int64) error {
	switch {
	case mode != "" && !slices.Contains(HostGroupModes, mode):
		return hostRefusal("group_mode", "host_group_mode")
	case mode != "" && len(groups) == 0:
		return hostRefusal("groups", "host_groups_empty")
	case len(groups) > HostGroupsMax:
		return hostRefusal("groups", "host_groups_long")
	case slices.ContainsFunc(groups, func(id int64) bool { return id <= 0 }):
		return hostRefusal("groups", "group_not_found")
	}
	return nil
}
